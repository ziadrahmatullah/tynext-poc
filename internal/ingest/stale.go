package ingest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/tynext/tynext-poc/internal/contract"
)

// staleDetector raises the one alarm the server produces on its own: a device
// that stopped sending telemetry. Everything else in the alarms table comes
// from the PLC.
//
// The baseline starts when ingest starts, so a device that never reports is
// flagged five seconds later — which is the truth, we are not hearing from it.
type staleDetector struct {
	cfg  Config
	pool *pgxpool.Pool
	log  zerolog.Logger
	m    *Metrics

	devices map[string]deviceRef

	mu       sync.Mutex
	lastSeen map[string]time.Time
	open     map[string]bool
}

func newStaleDetector(cfg Config, pool *pgxpool.Pool, log zerolog.Logger, m *Metrics, devices map[string]deviceRef, start time.Time) *staleDetector {
	last := make(map[string]time.Time, len(devices))
	for code := range devices {
		last[code] = start
	}
	return &staleDetector{
		cfg:      cfg,
		pool:     pool,
		log:      log,
		m:        m,
		devices:  devices,
		lastSeen: last,
		open:     map[string]bool{},
	}
}

// seen records that telemetry arrived. Called from the MQTT callback.
func (s *staleDetector) seen(code string, at time.Time) {
	s.mu.Lock()
	s.lastSeen[code] = at
	s.mu.Unlock()
}

// reconcile loads the STALE alarms that are already open, so a restart does not
// orphan them: without this the detector would never clear an alarm it did not
// raise itself.
func (s *staleDetector) reconcile(ctx context.Context) error {
	openByID, err := openAlarmDevices(ctx, s.pool, StaleCode)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for code, dev := range s.devices {
		if openByID[dev.id] {
			s.open[code] = true
		}
	}
	s.m.OpenAlarms.Set(float64(len(s.open)))
	if len(s.open) > 0 {
		s.log.Info().Int("count", len(s.open)).Msg("alarm STALE terbuka dari sesi sebelumnya diambil alih")
	}
	return nil
}

func (s *staleDetector) run(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.StaleCheckEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.sweep(ctx, now)
		}
	}
}

// staleAction is one decision the sweep made: raise or clear STALE for a device.
type staleAction struct {
	code     string
	deviceID string
	raise    bool
	lastSeen time.Time
	severity string
}

// decide compares every device against the deadline and flips the in-memory
// flag. It is separated from the database writes so the rule can be tested on
// its own.
func (s *staleDetector) decide(now time.Time) (actions []staleAction, open int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for code, dev := range s.devices {
		last := s.lastSeen[code]
		isStale := now.Sub(last) > s.cfg.StaleAfter
		switch {
		case isStale && !s.open[code]:
			s.open[code] = true
			actions = append(actions, staleAction{
				code: code, deviceID: dev.id, raise: true,
				lastSeen: last, severity: staleSeverity(code),
			})
		case !isStale && s.open[code]:
			delete(s.open, code)
			actions = append(actions, staleAction{
				code: code, deviceID: dev.id, raise: false, lastSeen: last,
			})
		}
	}
	return actions, len(s.open)
}

// staleSeverity: the EMS-PV controller is the single publisher, so if it is
// quiet nothing from the plant is arriving at all.
func staleSeverity(code string) string {
	if code == contract.PublisherID {
		return contract.SeverityCritical
	}
	return contract.SeverityWarning
}

func (s *staleDetector) sweep(ctx context.Context, now time.Time) {
	actions, open := s.decide(now)
	s.m.OpenAlarms.Set(float64(open))

	for _, a := range actions {
		if a.raise {
			msg := fmt.Sprintf("Tidak ada telemetry selama lebih dari %s (terakhir %s)",
				s.cfg.StaleAfter, a.lastSeen.UTC().Format(time.RFC3339))
			if err := raiseAlarm(ctx, s.pool, a.deviceID, SourceServer, StaleCode, a.severity, msg, nil, now); err != nil {
				s.failed(err, a.code, false)
				continue
			}
			s.log.Warn().Str("device", a.code).Str("severity", a.severity).Msg("alarm STALE")
			continue
		}

		if err := clearAlarm(ctx, s.pool, a.deviceID, StaleCode, now); err != nil {
			s.failed(err, a.code, true)
			continue
		}
		s.log.Info().Str("device", a.code).Msg("alarm STALE dibersihkan, data kembali")
	}
}

func (s *staleDetector) failed(err error, code string, restoreOpen bool) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	s.m.DBErrors.Inc()
	s.log.Error().Err(err).Str("device", code).Msg("gagal menulis alarm STALE")

	s.mu.Lock()
	if restoreOpen {
		s.open[code] = true
	} else {
		delete(s.open, code)
	}
	s.mu.Unlock()
}
