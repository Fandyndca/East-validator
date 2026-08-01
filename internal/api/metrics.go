package api

import (
	"fmt"
	"net/http"
	"runtime"
	"time"
)

var processStart = time.Now()

// handleMetrics exposes Prometheus-text metrics for scraping.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	h, _ := s.store.GetLatestHeight()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	peers := 0
	if s.p2p != nil {
		peers = s.p2p.PeerCount()
	}
	mpSize := 0
	if s.pool != nil {
		// Mempool may expose Len — best-effort via Stats map
		if st := s.mempoolStats(); st != nil {
			if n, ok := st["size"].(int); ok {
				mpSize = n
			}
		}
	}
	bftStep := ""
	bftRound := 0
	if s.bft != nil {
		st := s.bft.Stats()
		if v, ok := st["step"].(string); ok {
			bftStep = v
		}
		if v, ok := st["round"].(int32); ok {
			bftRound = int(v)
		}
		if v, ok := st["round"].(int); ok {
			bftRound = v
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = fmt.Fprintf(w, "# HELP east_block_height Latest sealed block height\n")
	_, _ = fmt.Fprintf(w, "# TYPE east_block_height gauge\n")
	_, _ = fmt.Fprintf(w, "east_block_height %d\n", h)

	_, _ = fmt.Fprintf(w, "# HELP east_peers Connected libp2p peers\n")
	_, _ = fmt.Fprintf(w, "# TYPE east_peers gauge\n")
	_, _ = fmt.Fprintf(w, "east_peers %d\n", peers)

	_, _ = fmt.Fprintf(w, "# HELP east_mempool_size Mempool transaction count\n")
	_, _ = fmt.Fprintf(w, "# TYPE east_mempool_size gauge\n")
	_, _ = fmt.Fprintf(w, "east_mempool_size %d\n", mpSize)

	_, _ = fmt.Fprintf(w, "# HELP east_uptime_seconds Process uptime\n")
	_, _ = fmt.Fprintf(w, "# TYPE east_uptime_seconds gauge\n")
	_, _ = fmt.Fprintf(w, "east_uptime_seconds %.0f\n", time.Since(processStart).Seconds())

	_, _ = fmt.Fprintf(w, "# HELP east_go_heap_bytes Heap alloc\n")
	_, _ = fmt.Fprintf(w, "# TYPE east_go_heap_bytes gauge\n")
	_, _ = fmt.Fprintf(w, "east_go_heap_bytes %d\n", mem.HeapAlloc)

	_, _ = fmt.Fprintf(w, "# HELP east_bft_round Current BFT round\n")
	_, _ = fmt.Fprintf(w, "# TYPE east_bft_round gauge\n")
	_, _ = fmt.Fprintf(w, "east_bft_round %d\n", bftRound)

	_, _ = fmt.Fprintf(w, "# HELP east_bft_step_info BFT step name (see logs/status)\n")
	_, _ = fmt.Fprintf(w, "# TYPE east_bft_step_info gauge\n")
	_, _ = fmt.Fprintf(w, "east_bft_step_info{step=%q} 1\n", bftStep)
}
