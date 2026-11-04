package monitor

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// Options configures the HTTP surface.
type Options struct {
	// Token, when non-empty, is required as "Authorization: Bearer <token>"
	// on every endpoint. The reports contain device names, IPs and channel
	// names, so a non-loopback listener must always use one.
	Token string
}

// Handler serves /healthz, /metrics and /api/reports from a Monitor with no
// authentication (the default 127.0.0.1 bind makes that safe).
func Handler(m *Monitor) http.Handler {
	return HandlerWith(m, Options{})
}

// HandlerWith is Handler with explicit options.
func HandlerWith(m *Monitor, opt Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", guard(opt.Token, func(w http.ResponseWriter, r *http.Request) {
		s := m.Snapshot()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		code := http.StatusOK
		status := "ok"
		if !s.Healthy() {
			code = http.StatusServiceUnavailable
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
	}))
	mux.HandleFunc("/metrics", guard(opt.Token, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(m.Snapshot().Metrics()))
	}))
	mux.HandleFunc("/api/reports", guard(opt.Token, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(m.Snapshot())
	}))
	return mux
}

// guard wraps next with a bearer-token check. An empty token disables auth.
func guard(token string, next http.HandlerFunc) http.HandlerFunc {
	if token == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !tokenMatches(r, token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nvr-agent"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func tokenMatches(r *http.Request, want string) bool {
	const prefix = "bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return false
	}
	got := strings.TrimSpace(h[len(prefix):])
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
