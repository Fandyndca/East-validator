package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/rs/zerolog/log"

	"github.com/eastchain/east-validator/internal/config"
	"github.com/eastchain/east-validator/internal/consensus"
	"github.com/eastchain/east-validator/internal/state"
	"github.com/eastchain/east-validator/internal/tx"
)

type Server struct {
	cfg   config.Config
	store *state.Store
	http  *http.Server
}

func New(cfg config.Config, store *state.Store) *Server {
	s := &Server{cfg: cfg, store: store}
	r := mux.NewRouter()

	r.HandleFunc("/health", s.handleHealth).Methods("GET")
	r.HandleFunc("/stats", s.handleStats).Methods("GET")
	r.HandleFunc("/account/{address}", s.handleGetAccount).Methods("GET")
	r.HandleFunc("/block/latest", s.handleLatestBlock).Methods("GET")
	r.HandleFunc("/block/{height}", s.handleGetBlock).Methods("GET")
	r.HandleFunc("/supply", s.handleSupply).Methods("GET")
	r.HandleFunc("/supply/{bucket}", s.handleBucket).Methods("GET")

	// Write / consensus
	r.HandleFunc("/tx", s.auth(s.handleSubmitTx)).Methods("POST")
	r.HandleFunc("/consensus/propose", s.auth(s.handlePropose)).Methods("POST")
	r.HandleFunc("/admin/seed", s.auth(s.handleSeed)).Methods("POST")
	r.HandleFunc("/admin/prune", s.auth(s.handlePrune)).Methods("POST")

	s.http = &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	return s
}

func (s *Server) Start() error {
	log.Info().Str("addr", s.cfg.HTTPAddr).Msg("HTTP API listening")
	return s.http.ListenAndServe()
}

func (s *Server) Shutdown() error { return s.http.Close() }

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APISecret == "" {
			next(w, r)
			return
		}
		if r.Header.Get("X-API-Secret") != s.cfg.APISecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"node_id": s.cfg.NodeID,
		"name":    s.cfg.NodeName,
		"role":    "sealer",
	})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Stats())
}

func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	addr := mux.Vars(r)["address"]
	acc, err := s.store.GetAccount(addr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, acc)
}

func (s *Server) handleLatestBlock(w http.ResponseWriter, r *http.Request) {
	height, err := s.store.GetLatestHeight()
	if err != nil || height == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"height": 0})
		return
	}
	h, err := s.store.GetBlock(height)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleGetBlock(w http.ResponseWriter, r *http.Request) {
	height, err := strconv.ParseUint(mux.Vars(r)["height"], 10, 64)
	if err != nil {
		http.Error(w, "invalid height", http.StatusBadRequest)
		return
	}
	h, err := s.store.GetBlock(height)
	if err != nil {
		http.Error(w, "block not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleSupply(w http.ResponseWriter, r *http.Request) {
	buckets, err := s.store.ListBuckets()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total_max_supply": s.store.TotalMaxSupply(),
		"buckets":          buckets,
	})
}

func (s *Server) handleBucket(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["bucket"]
	b, err := s.store.GetBucket(name)
	if err != nil {
		http.Error(w, "bucket not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleSubmitTx(w http.ResponseWriter, r *http.Request) {
	var t tx.Transaction
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := t.ValidateBasic(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.ApplyTx(&t); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"txHash": t.Hash(),
		"type":   t.Type,
	})
}

// handlePropose — Fullnode Browser submits a block proposal; Railway seals it.
func (s *Server) handlePropose(w http.ResponseWriter, r *http.Request) {
	var p consensus.Proposal
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if p.ProposalID == "" || p.Proposer == "" || p.Signature == "" {
		http.Error(w, "proposal_id, proposer, signature required", http.StatusBadRequest)
		return
	}

	sealerKey := os.Getenv("CHAIN_SIGNING_PRIVATE_KEY")
	result, err := consensus.VerifyAndSeal(s.store, p, sealerKey)
	if err != nil {
		log.Warn().Err(err).Str("proposer", p.Proposer).Msg("proposal rejected")
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}

	log.Info().
		Uint64("height", result.Header.Height).
		Str("hash", result.Header.Hash).
		Str("proposer", p.Proposer).
		Msg("block sealed")

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"height":           result.Header.Height,
		"hash":             result.Header.Hash,
		"sealer_signature": result.SealerSig,
		"header":           result.Header,
	})
}

type seedRequest struct {
	Address string `json:"address"`
	Balance int64  `json:"balance"`
}

func (s *Server) handleSeed(w http.ResponseWriter, r *http.Request) {
	var req seedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if req.Address == "" {
		http.Error(w, "address required", http.StatusBadRequest)
		return
	}
	if err := s.store.SeedBalance(req.Address, req.Balance); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePrune(w http.ResponseWriter, r *http.Request) {
	if err := s.store.PruneOldBlocks(s.cfg.KeepRecentBlocks); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "keep": s.cfg.KeepRecentBlocks})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
