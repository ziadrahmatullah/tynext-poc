package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
)

// health reports what /healthz answers.
type health struct {
	brokerConnected atomic.Bool
	dbReachable     atomic.Bool
	startedAt       time.Time
}

type healthBody struct {
	Status          string `json:"status"`
	BrokerConnected bool   `json:"brokerConnected"`
	DatabaseUp      bool   `json:"databaseUp"`
	UptimeSeconds   int64  `json:"uptimeSeconds"`
}

// serveHTTP runs /healthz and /metrics until ctx is cancelled.
func serveHTTP(ctx context.Context, addr string, h *health, m *Metrics, log zerolog.Logger) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		body := healthBody{
			BrokerConnected: h.brokerConnected.Load(),
			DatabaseUp:      h.dbReachable.Load(),
			UptimeSeconds:   int64(time.Since(h.startedAt).Seconds()),
		}
		// The database is the part that loses data when it is down; a broker
		// reconnect is expected and recoverable, so it does not fail the probe.
		body.Status = "ok"
		code := http.StatusOK
		if !body.DatabaseUp {
			body.Status = "degraded"
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(body)
	})

	mux.Handle("/metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info().Str("addr", addr).Msg("http: /healthz dan /metrics")
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
