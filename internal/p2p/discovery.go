package p2p

import (
	"context"
	"fmt"
	"sync"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/rs/zerolog/log"
)

// Rendezvous namespace used for DHT provider advertisements so EAST nodes
// find each other without a shared static bootstrap list (after the first
// few seeds).
const DiscoveryNamespace = "eastchain-validators-v1"

// mdnsServiceTag is the LAN multicast service name.
const mdnsServiceTag = "eastchain._udp"

// discoveryService owns DHT + mDNS lifecycle and continuous peer finding.
type discoveryService struct {
	host   host.Host
	kdht   *dht.IpfsDHT
	ctx    context.Context
	cancel context.CancelFunc

	mu          sync.Mutex
	knownPeers  map[peer.ID]time.Time
	bootstrapN  int
}

func startDiscovery(parent context.Context, h host.Host, bootstrapPeers []peer.AddrInfo) (*discoveryService, error) {
	ctx, cancel := context.WithCancel(parent)

	// ModeServer: this validator participates fully in the DHT (stores records,
	// answers FIND_NODE). ModeClient would only query — fine for light clients
	// later, but validators must be servers so the network can form without a
	// fixed bootstrap after the first connection.
	kdht, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("kad-dht: %w", err)
	}

	// Bootstrap the DHT routing table (connects to well-known + our seeds).
	if err := kdht.Bootstrap(ctx); err != nil {
		log.Warn().Err(err).Msg("dht bootstrap returned error (continuing)")
	}

	ds := &discoveryService{
		host:       h,
		kdht:       kdht,
		ctx:        ctx,
		cancel:     cancel,
		knownPeers: make(map[peer.ID]time.Time),
		bootstrapN: len(bootstrapPeers),
	}

	// Dial configured bootstrap multiaddrs first so DHT has at least one peer.
	for _, info := range bootstrapPeers {
		if info.ID == h.ID() {
			continue
		}
		cctx, ccancel := context.WithTimeout(ctx, 15*time.Second)
		err := h.Connect(cctx, info)
		ccancel()
		if err != nil {
			log.Warn().Str("peer", info.ID.String()).Err(err).Msg("bootstrap dial failed")
			continue
		}
		ds.remember(info.ID)
		log.Info().Str("peer", info.ID.String()).Msg("connected to bootstrap peer")
	}

	// mDNS for same-LAN / docker-compose / local multi-validator setups.
	notifee := &mdnsNotifee{h: h, ds: ds}
	svc := mdns.NewMdnsService(h, mdnsServiceTag, notifee)
	if err := svc.Start(); err != nil {
		log.Warn().Err(err).Msg("mDNS service start failed (LAN discovery disabled)")
	} else {
		log.Info().Str("tag", mdnsServiceTag).Msg("mDNS discovery enabled")
	}

	// Continuous DHT rendezvous: advertise ourselves + find other EAST nodes.
	go ds.advertiseLoop()
	go ds.findPeersLoop()

	log.Info().
		Str("namespace", DiscoveryNamespace).
		Int("bootstrap", len(bootstrapPeers)).
		Msg("peer discovery started (DHT + mDNS)")

	return ds, nil
}

func (ds *discoveryService) Close() {
	ds.cancel()
	if ds.kdht != nil {
		_ = ds.kdht.Close()
	}
}

func (ds *discoveryService) remember(id peer.ID) {
	ds.mu.Lock()
	ds.knownPeers[id] = time.Now()
	ds.mu.Unlock()
}

func (ds *discoveryService) KnownPeerCount() int {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	return len(ds.knownPeers)
}

func (ds *discoveryService) advertiseLoop() {
	routingDiscovery := routing.NewRoutingDiscovery(ds.kdht)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Advertise immediately, then refresh TTL periodically.
	for {
		ttl, err := routingDiscovery.Advertise(ds.ctx, DiscoveryNamespace)
		if err != nil {
			log.Debug().Err(err).Msg("DHT advertise failed (will retry)")
		} else {
			log.Debug().Dur("ttl", ttl).Msg("DHT advertised under eastchain namespace")
		}
		select {
		case <-ds.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (ds *discoveryService) findPeersLoop() {
	routingDiscovery := routing.NewRoutingDiscovery(ds.kdht)
	// More aggressive early on so a fresh node joins the mesh quickly,
	// then settle to a slower cadence.
	intervals := []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute}
	idx := 0

	for {
		select {
		case <-ds.ctx.Done():
			return
		case <-time.After(intervals[idx]):
		}
		if idx < len(intervals)-1 {
			idx++
		}

		peerCh, err := routingDiscovery.FindPeers(ds.ctx, DiscoveryNamespace)
		if err != nil {
			log.Debug().Err(err).Msg("DHT FindPeers failed")
			continue
		}
		for pi := range peerCh {
			if pi.ID == ds.host.ID() || len(pi.Addrs) == 0 {
				continue
			}
			// Already connected?
			if ds.host.Network().Connectedness(pi.ID) == network.Connected {
				ds.remember(pi.ID)
				continue
			}
			cctx, cancel := context.WithTimeout(ds.ctx, 12*time.Second)
			err := ds.host.Connect(cctx, pi)
			cancel()
			if err != nil {
				log.Debug().Str("peer", pi.ID.String()).Err(err).Msg("DHT peer dial failed")
				continue
			}
			ds.remember(pi.ID)
			log.Info().Str("peer", pi.ID.String()).Msg("discovered + connected peer via DHT")
		}
	}
}

// mdnsNotifee implements mdns.Notifee.
type mdnsNotifee struct {
	h  host.Host
	ds *discoveryService
}

func (n *mdnsNotifee) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == n.h.ID() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := n.h.Connect(ctx, pi); err != nil {
		log.Debug().Str("peer", pi.ID.String()).Err(err).Msg("mDNS peer dial failed")
		return
	}
	n.ds.remember(pi.ID)
	log.Info().Str("peer", pi.ID.String()).Msg("discovered + connected peer via mDNS")
}


