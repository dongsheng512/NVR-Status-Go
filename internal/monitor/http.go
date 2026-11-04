package monitor

import (
	"encoding/json"
	"net/http"
)

// Handler serves /healthz and /metrics from a Monitor.
func Handler(m *Monitor) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		s := m.Snapshot()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		code := http.StatusOK
		if !s.Healthy() {
			code = http.StatusServiceUnavailable
		}
		status := "ok"
		if !s.Healthy() {
			status = "degraded"
		}
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":       status,
			"scanned_at":   s.ScannedAt,
			"duration":     s.Duration,
			"device_count": len(s.Reports),
			"reports":      s.Reports,
		})
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(m.Snapshot().Metrics()))
	})
	mux.HandleFunc("/api/reports", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(m.Snapshot())
	})
	return mux
}
