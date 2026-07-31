package main

import (
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/eastchain/east-validator/internal/api"
	"github.com/eastchain/east-validator/internal/config"
	"github.com/eastchain/east-validator/internal/genesis"
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
		Msg("starting east-validator (sealer mode)")

	store, err := state.Open(cfg.DataDir)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open state")
	}
	defer store.Close()

	// Load or create default genesis (max supply + buckets)
	var g *genesis.Genesis
	if _, err := os.Stat(cfg.GenesisPath); err == nil {
		g, err = genesis.LoadFile(cfg.GenesisPath)
		if err != nil {
			log.Fatal().Err(err).Msg("invalid genesis file")
		}
		log.Info().Str("path", cfg.GenesisPath).Msg("loaded genesis file")
	} else {
		def := genesis.Default()
		// Optional: inject public chain signing address from env
		if addr := os.Getenv("CHAIN_SIGNING_ADDRESS"); addr != "" {
			def.ChainSigningAddress = addr
		}
		g = &def
		log.Info().Msg("using default genesis (1B max supply + tokenomics buckets)")
	}

	if err := store.InitGenesis(g); err != nil {
		log.Fatal().Err(err).Msg("genesis init failed")
	}

	srv := api.New(cfg, store)

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Info().Msg("shutting down...")
		_ = srv.Shutdown()
		_ = store.Close()
		os.Exit(0)
	}()

	if err := srv.Start(); err != nil && err != http.ErrServerClosed {
		log.Fatal().Err(err).Msg("http server failed")
	}
}
