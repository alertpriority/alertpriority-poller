package health

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

// Server provides health and readiness endpoints.
type Server struct {
	port      int
	startedAt time.Time
	ready     atomic.Bool

	// Metrics exposed via /metrics
	ChecksExecuted     atomic.Int64
	ChecksPerMinute    atomic.Int64
	Errors             atomic.Int64
	QueueDepth         atomic.Int64
	AvgCheckDurationMs atomic.Int64
}

// NewServer creates a new health server.
func NewServer(port int) *Server {
	s := &Server{
		port:      port,
		startedAt: time.Now().UTC(),
	}
	return s
}

// SetReady marks the poller as ready to serve.
func (s *Server) SetReady(ready bool) {
	s.ready.Store(ready)
}

// UptimeSeconds returns the poller uptime in seconds.
func (s *Server) UptimeSeconds() int64 {
	return int64(time.Since(s.startedAt).Seconds())
}

// Start starts the health HTTP server in a goroutine.
func (s *Server) Start() {
	mux := http.NewServeMux()
	mux.HandleFunc("/ready", s.handleReady)
	mux.HandleFunc("/metrics", s.handleMetrics)

	addr := fmt.Sprintf(":%d", s.port)
	log.Printf("[health] listening on %s", addr)

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("[health] server error: %v", err)
		}
	}()
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.ready.Load() {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "not ready")
	}
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"uptime_seconds":       s.UptimeSeconds(),
		"ready":                s.ready.Load(),
		"checks_executed":      s.ChecksExecuted.Load(),
		"checks_per_minute":    s.ChecksPerMinute.Load(),
		"errors":               s.Errors.Load(),
		"queue_depth":          s.QueueDepth.Load(),
		"avg_check_duration_ms": s.AvgCheckDurationMs.Load(),
	})
}
