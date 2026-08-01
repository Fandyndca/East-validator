package main

import (
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/eastchain/east-validator/internal/api"
	"github.com/eastchain/east-validator/internal/config"
	"github.com/eastchain/east-validator/internal/consensus"
	"github.com/eastchain/east-validator/internal/genesis"
	"github.com/eastchain/east-validator/internal/mempool"
	"github.com/eastchain/east-validator/internal/p2p"
	"github.com/eastchain/east-validator/internal/state"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	cfg := config.Load()
	log.Info().
		Str("node_id", cfg.NodeID).
		Str("data_dir", cfg.DataDir).
		Str("http", cfg.HTTPAddr).
		Dur("block_interval", cfg.BlockInterval).
		Bool("auto_produce", cfg.AutoProduce).
		Msg("starting east-validator (sealer mode)")

	store, err := state.Open(cfg.DataDir)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open state")
	}
	defer store.Close()

	var g *genesis.Genesis
	if _, err := os.Stat(cfg.GenesisPath); err == nil {
		g, err = genesis.LoadFile(cfg.GenesisPath)
		if err != nil {
			log.Fatal().Err(err).Msg("invalid genesis file")
		}
		log.Info().
			Str("path", cfg.GenesisPath).
			Int64("numeric_chain_id", g.NumericChainID).
			Msg("loaded genesis file")
	} else {
		def := genesis.Default()
		if addr := os.Getenv("CHAIN_SIGNING_ADDRESS"); addr != "" {
			def.ChainSigningAddress = addr
		}
		g = &def
		log.Info().
			Int64("numeric_chain_id", g.NumericChainID).
			Msg("using default genesis (1B max supply + tokenomics buckets)")
	}

	if err := store.InitGenesis(g); err != nil {
		log.Fatal().Err(err).Msg("genesis init failed")
	}

	// Local proposer identity
	localAddr := os.Getenv("CHAIN_SIGNING_ADDRESS")
	if localAddr == "" && os.Getenv("CHAIN_SIGNING_PRIVATE_KEY") != "" {
		// derived later in producer if needed
	}

	// Leader schedule (CometBFT-style round-robin when 2+ validators)
	leader := consensus.NewLeaderSchedule(localAddr)
	if raw := os.Getenv("VALIDATORS"); raw != "" {
		parts := strings.Split(raw, ",")
		var addrs []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				addrs = append(addrs, p)
			}
		}
		leader.SetValidators(addrs)
		log.Info().Int("count", len(addrs)).Msg("validator set loaded from VALIDATORS env")
	} else if localAddr != "" {
		// Solo: register self so stats are consistent
		leader.SetValidators([]string{localAddr})
	}

	// Mempool
	pool := mempool.New(store, mempool.DefaultConfig())

	// libp2p
	p2pCfg := p2p.LoadConfigFromEnv(cfg.NodeID)
	p2pNode, err := p2p.New(p2pCfg)
	if err != nil {
		log.Fatal().Err(err).Msg("p2p start failed")
	}
	defer p2pNode.Close()

	p2pNode.OnHeartbeat(func(m p2p.HeartbeatMsg) {
		tier := state.TierLight
		if m.Tier == "full" {
			tier = state.TierFull
		}
		if m.Address == "" {
			return
		}
		if _, err := store.RecordHeartbeat(m.Address, m.NodeID, tier, cfg.EpochSeconds); err != nil {
			log.Debug().Err(err).Msg("record remote heartbeat")
		}
	})

	var producer *consensus.Producer
	if cfg.AutoProduce {
		producer = consensus.NewProducer(store, cfg.BlockInterval)
		producer.SetP2P(p2pNode)
		producer.SetMempool(pool)
		producer.SetLeader(leader)
		producer.Start()
		defer producer.Stop()
	}

	p2pNode.OnBlock(func(a p2p.BlockAnnounce) {
		if err := consensus.VerifyAndSaveGossipedBlock(store, a.Height, a.Hash, a.PrevHash, a.Timestamp, a.Proposer, a.Txs); err != nil {
			log.Debug().Err(err).Uint64("height", a.Height).Str("from", a.FromPeer).Msg("gossiped block rejected")
			return
		}
		if producer != nil {
			producer.NotifyExternalProposal()
		}
		log.Info().
			Uint64("height", a.Height).
			Str("hash", a.Hash).
			Str("from", a.FromPeer).
			Int("txs", len(a.Txs)).
			Msg("p2p block received and applied")
	})

	srv := api.New(cfg, store, producer, p2pNode, pool, leader)

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Info().Msg("shutting down...")
		if producer != nil {
			producer.Stop()
		}
		_ = p2pNode.Close()
		_ = srv.Shutdown()
		_ = store.Close()
		os.Exit(0)
	}()

	if err := srv.Start(); err != nil && err != http.ErrServerClosed {
		log.Fatal().Err(err).Msg("http server failed")
	}
}
