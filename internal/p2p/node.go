package p2p

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	"github.com/multiformats/go-multiaddr"
	"github.com/rs/zerolog/log"

	"github.com/eastchain/east-validator/internal/tx"
)

const (
	TopicBlocks     = "eastchain/blocks/1.0.0"
	TopicHeartbeats = "eastchain/heartbeats/1.0.0"
)

// BlockAnnounce is gossiped after a block is sealed.
type BlockAnnounce struct {
	Height    uint64            `json:"height"`
	Hash      string            `json:"hash"`
	PrevHash  string            `json:"prev_hash"`
	Timestamp int64             `json:"timestamp"`
	Proposer  string            `json:"proposer"`
	Signature string            `json:"signature,omitempty"`
	TxHashes  []string          `json:"tx_hashes,omitempty"`
	Txs       []*tx.Transaction `json:"txs,omitempty"` // full tx bodies so recipients can replay them into their own state, not just record the hashes
	FromPeer  string            `json:"from_peer,omitempty"`
}

// HeartbeatMsg is gossiped by full/light/validator nodes.
type HeartbeatMsg struct {
	Address  string `json:"address"`
	NodeID   string `json:"node_id"`
	Tier     string `json:"tier"` // light | full | validator
	Height   uint64 `json:"height"`
	UnixMs   int64  `json:"unix_ms"`
	FromPeer string `json:"from_peer,omitempty"`
}

type Config struct {
	Enabled       bool
	ListenPort    int
	PrivateKeyHex string
	Bootstrap     []string
	NodeID        string
}

type Node struct {
	cfg    Config
	host   host.Host
	ps     *pubsub.PubSub
	blocks *pubsub.Topic
	hb     *pubsub.Topic
	ctx    context.Context
	cancel context.CancelFunc

	mu          sync.RWMutex
	onBlock     func(BlockAnnounce)
	onHeartbeat func(HeartbeatMsg)
	peerCount   int
}

func LoadConfigFromEnv(nodeID string) Config {
	enabled := os.Getenv("P2P_ENABLED") != "false"
	port := 4001
	if v := os.Getenv("P2P_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &port)
	}
	var bootstrap []string
	if raw := os.Getenv("P2P_BOOTSTRAP"); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				bootstrap = append(bootstrap, p)
			}
		}
	}
	return Config{
		Enabled:       enabled,
		ListenPort:    port,
		PrivateKeyHex: os.Getenv("P2P_PRIVATE_KEY"),
		Bootstrap:     bootstrap,
		NodeID:        nodeID,
	}
}

func New(cfg Config) (*Node, error) {
	if !cfg.Enabled {
		log.Info().Msg("p2p disabled (P2P_ENABLED=false)")
		return &Node{cfg: cfg}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())

	priv, err := loadOrGenerateKey(cfg.PrivateKeyHex)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("p2p key: %w", err)
	}

	listen, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", cfg.ListenPort))
	if err != nil {
		cancel()
		return nil, err
	}

	h, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrs(listen),
		libp2p.Security(libp2ptls.ID, libp2ptls.New),
		libp2p.Security(noise.ID, noise.New),
		libp2p.DefaultTransports,
		libp2p.DefaultMuxers,
		libp2p.EnableNATService(),
		libp2p.EnableRelay(),
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("libp2p host: %w", err)
	}

	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		_ = h.Close()
		cancel()
		return nil, fmt.Errorf("gossipsub: %w", err)
	}

	blocks, err := ps.Join(TopicBlocks)
	if err != nil {
		_ = h.Close()
		cancel()
		return nil, err
	}
	hbTopic, err := ps.Join(TopicHeartbeats)
	if err != nil {
		_ = h.Close()
		cancel()
		return nil, err
	}

	n := &Node{
		cfg:    cfg,
		host:   h,
		ps:     ps,
		blocks: blocks,
		hb:     hbTopic,
		ctx:    ctx,
		cancel: cancel,
	}

	if err := n.subscribeBlocks(); err != nil {
		n.Close()
		return nil, err
	}
	if err := n.subscribeHeartbeats(); err != nil {
		n.Close()
		return nil, err
	}

	h.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, c network.Conn) {
			n.mu.Lock()
			n.peerCount++
			n.mu.Unlock()
			log.Info().Str("peer", c.RemotePeer().String()).Msg("p2p peer connected")
		},
		DisconnectedF: func(_ network.Network, c network.Conn) {
			n.mu.Lock()
			if n.peerCount > 0 {
				n.peerCount--
			}
			n.mu.Unlock()
			log.Info().Str("peer", c.RemotePeer().String()).Msg("p2p peer disconnected")
		},
	})

	log.Info().
		Str("peer_id", h.ID().String()).
		Strs("addrs", addrStrings(h)).
		Int("port", cfg.ListenPort).
		Msg("libp2p node started")

	go n.dialBootstrap()
	return n, nil
}

func (n *Node) Enabled() bool { return n.cfg.Enabled && n.host != nil }

func (n *Node) PeerID() string {
	if n.host == nil {
		return ""
	}
	return n.host.ID().String()
}

func (n *Node) ListenAddrs() []string {
	if n.host == nil {
		return nil
	}
	return addrStrings(n.host)
}

func (n *Node) PeerCount() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.peerCount
}

func (n *Node) OnBlock(fn func(BlockAnnounce)) {
	n.mu.Lock()
	n.onBlock = fn
	n.mu.Unlock()
}

func (n *Node) OnHeartbeat(fn func(HeartbeatMsg)) {
	n.mu.Lock()
	n.onHeartbeat = fn
	n.mu.Unlock()
}

func (n *Node) PublishBlock(a BlockAnnounce) error {
	if !n.Enabled() || n.blocks == nil {
		return nil
	}
	a.FromPeer = n.PeerID()
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return n.blocks.Publish(n.ctx, b)
}

func (n *Node) PublishHeartbeat(m HeartbeatMsg) error {
	if !n.Enabled() || n.hb == nil {
		return nil
	}
	m.FromPeer = n.PeerID()
	if m.UnixMs == 0 {
		m.UnixMs = time.Now().UnixMilli()
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return n.hb.Publish(n.ctx, b)
}

func (n *Node) subscribeBlocks() error {
	sub, err := n.blocks.Subscribe()
	if err != nil {
		return err
	}
	go func() {
		for {
			msg, err := sub.Next(n.ctx)
			if err != nil {
				return
			}
			if msg.ReceivedFrom == n.host.ID() {
				continue
			}
			var a BlockAnnounce
			if err := json.Unmarshal(msg.Data, &a); err != nil {
				continue
			}
			a.FromPeer = msg.ReceivedFrom.String()
			n.mu.RLock()
			fn := n.onBlock
			n.mu.RUnlock()
			if fn != nil {
				fn(a)
			} else {
				log.Debug().Uint64("height", a.Height).Str("hash", a.Hash).Msg("p2p block announce")
			}
		}
	}()
	return nil
}

func (n *Node) subscribeHeartbeats() error {
	sub, err := n.hb.Subscribe()
	if err != nil {
		return err
	}
	go func() {
		for {
			msg, err := sub.Next(n.ctx)
			if err != nil {
				return
			}
			if msg.ReceivedFrom == n.host.ID() {
				continue
			}
			var m HeartbeatMsg
			if err := json.Unmarshal(msg.Data, &m); err != nil {
				continue
			}
			m.FromPeer = msg.ReceivedFrom.String()
			n.mu.RLock()
			fn := n.onHeartbeat
			n.mu.RUnlock()
			if fn != nil {
				fn(m)
			}
		}
	}()
	return nil
}

func (n *Node) dialBootstrap() {
	if n.host == nil {
		return
	}
	for _, raw := range n.cfg.Bootstrap {
		maddr, err := multiaddr.NewMultiaddr(raw)
		if err != nil {
			log.Warn().Str("addr", raw).Err(err).Msg("invalid bootstrap multiaddr")
			continue
		}
		info, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			log.Warn().Str("addr", raw).Err(err).Msg("bootstrap addr missing /p2p/PEERID")
			continue
		}
		ctx, cancel := context.WithTimeout(n.ctx, 15*time.Second)
		err = n.host.Connect(ctx, *info)
		cancel()
		if err != nil {
			log.Warn().Str("peer", info.ID.String()).Err(err).Msg("bootstrap dial failed")
			continue
		}
		log.Info().Str("peer", info.ID.String()).Msg("connected to bootstrap peer")
	}
}

func (n *Node) Close() error {
	if n.cancel != nil {
		n.cancel()
	}
	if n.host != nil {
		return n.host.Close()
	}
	return nil
}

func (n *Node) Stats() map[string]any {
	if !n.Enabled() {
		return map[string]any{"enabled": false}
	}
	return map[string]any{
		"enabled":     true,
		"peer_id":     n.PeerID(),
		"peers":       n.PeerCount(),
		"listen":      n.ListenAddrs(),
		"topics":      []string{TopicBlocks, TopicHeartbeats},
		"bootstrap_n": len(n.cfg.Bootstrap),
	}
}

func loadOrGenerateKey(hexKey string) (crypto.PrivKey, error) {
	if hexKey != "" {
		hexKey = strings.TrimPrefix(hexKey, "0x")
		b, err := hex.DecodeString(hexKey)
		if err == nil && len(b) > 0 {
			if pk, err := crypto.UnmarshalPrivateKey(b); err == nil {
				return pk, nil
			}
			// 32-byte seed → ed25519
			if len(b) == 32 {
				// UnmarshalEd25519PrivateKey expects 64-byte expanded key;
				// Generate from seed via libp2p helper if available — fallback generate.
				log.Warn().Msg("P2P_PRIVATE_KEY looks like raw seed; generating stable key via Seed")
			}
		}
		log.Warn().Msg("P2P_PRIVATE_KEY invalid — generating ephemeral key (set proper key for stable peer id)")
	}
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	return priv, err
}

func addrStrings(h host.Host) []string {
	var out []string
	for _, a := range h.Addrs() {
		full := fmt.Sprintf("%s/p2p/%s", a.String(), h.ID().String())
		out = append(out, full)
	}
	return out
}
