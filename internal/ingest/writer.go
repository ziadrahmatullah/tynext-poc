package ingest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// maxPendingRows caps how much telemetry is held in memory while the database
// is unreachable. Past this the oldest rows are dropped and logged, because an
// ingest that grows without bound is a second outage on top of the first.
const maxPendingRows = 100_000

// telemetryRow is one (device, metric, ts, value) tuple.
type telemetryRow struct {
	deviceID string
	metric   string
	ts       time.Time
	value    float64
}

// writer owns every database write. A single goroutine runs it, which is what
// lets the state cache below be a plain map.
type writer struct {
	cfg  Config
	pool *pgxpool.Pool
	log  zerolog.Logger
	m    *Metrics

	// conn is pinned so the staging temp table survives between flushes.
	conn *pgxpool.Conn

	batch []telemetryRow

	// stateCache remembers the last value written per (device, key) so an
	// unchanged state does not cost a round trip every second. Correctness does
	// not depend on it: the SQL compares against the stored value anyway.
	stateCache map[string]string
}

func newWriter(cfg Config, pool *pgxpool.Pool, log zerolog.Logger, m *Metrics) *writer {
	return &writer{
		cfg:        cfg,
		pool:       pool,
		log:        log,
		m:          m,
		batch:      make([]telemetryRow, 0, cfg.BatchMaxRows),
		stateCache: map[string]string{},
	}
}

// run consumes decoded messages and rejects until the channels close, then
// flushes what is left. Draining on the way out is what makes shutdown lossless.
func (w *writer) run(ctx context.Context, msgs <-chan message, rejects <-chan reject) {
	ticker := time.NewTicker(w.cfg.BatchInterval)
	defer ticker.Stop()
	defer w.release()

	for {
		select {
		case msg, ok := <-msgs:
			if !ok {
				w.drain(rejects)
				w.flush(context.WithoutCancel(ctx))
				return
			}
			w.handle(ctx, msg)

		case r, ok := <-rejects:
			if !ok {
				rejects = nil
				continue
			}
			w.writeReject(ctx, r)

		case <-ticker.C:
			w.flush(ctx)
		}
	}
}

// drain empties the reject channel after the message channel closed. It reads
// until the channel is closed rather than until it is momentarily empty, so a
// reject still in flight is not lost on the way out. The deadline is there in
// case the channel is never closed.
func (w *writer) drain(rejects <-chan reject) {
	if rejects == nil {
		return
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case r, ok := <-rejects:
			if !ok {
				return
			}
			w.writeReject(context.Background(), r)
		case <-deadline:
			w.log.Warn().Msg("menguras antrean reject melewati batas waktu")
			return
		}
	}
}

func (w *writer) handle(ctx context.Context, msg message) {
	switch {
	case msg.telemetry != nil:
		w.handleTelemetry(ctx, msg)
	case msg.status != nil:
		w.handleStatus(ctx, msg)
	case msg.alarm != nil:
		w.handleAlarm(ctx, msg)
	}
}

func (w *writer) handleTelemetry(ctx context.Context, msg message) {
	tel := msg.telemetry
	w.m.LagMS.Observe(float64(msg.received.Sub(tel.TS.Time).Milliseconds()))

	for metric, value := range tel.Metrics {
		w.batch = append(w.batch, telemetryRow{
			deviceID: msg.deviceID,
			metric:   metric,
			ts:       tel.TS.Time,
			value:    value,
		})
	}

	for key, sv := range tel.State {
		w.applyState(ctx, msg.deviceID, key, sv.String(), tel.TS.Time)
	}

	if len(w.batch) >= w.cfg.BatchMaxRows {
		w.flush(ctx)
	}
}

func (w *writer) handleStatus(ctx context.Context, msg message) {
	st := msg.status
	// The will message carries no ts, so the receive time stands in for it.
	ts := st.Timestamp(msg.received)

	err := w.exec(ctx, `
		INSERT INTO device_status (device_id, online, last_seen, fw_version)
		VALUES ($1::uuid, $2, $3, NULLIF($4, ''))
		ON CONFLICT (device_id) DO UPDATE SET
			online     = EXCLUDED.online,
			last_seen  = EXCLUDED.last_seen,
			fw_version = COALESCE(EXCLUDED.fw_version, device_status.fw_version)`,
		msg.deviceID, st.Online, ts, st.FW)
	if err != nil {
		w.dbError(err, "tulis device_status", msg.topic.String())
		return
	}

	w.m.Written.WithLabelValues("device_status").Inc()
	w.log.Info().
		Str("device", msg.topic.DeviceID).
		Bool("online", st.Online).
		Time("ts", ts).
		Msg("status")
}

func (w *writer) handleAlarm(ctx context.Context, msg message) {
	al := msg.alarm
	if al.State == "cleared" {
		w.clearAlarm(ctx, msg.deviceID, al.Code, al.TS.Time, msg.topic.String())
		return
	}
	w.raiseAlarm(ctx, msg.deviceID, SourcePLC, al.Code, al.Severity, al.Message, al.Value, al.TS.Time, msg.topic.String())
}

func (w *writer) raiseAlarm(ctx context.Context, deviceID, source, code, severity, message string, value *float64, ts time.Time, topic string) {
	if err := raiseAlarm(ctx, w.pool, deviceID, source, code, severity, message, value, ts); err != nil {
		w.dbError(err, "tulis alarm raised", topic)
		return
	}
	w.m.Written.WithLabelValues("alarms").Inc()
}

func (w *writer) clearAlarm(ctx context.Context, deviceID, code string, ts time.Time, topic string) {
	if err := clearAlarm(ctx, w.pool, deviceID, code, ts); err != nil {
		w.dbError(err, "tulis alarm cleared", topic)
		return
	}
	w.m.Written.WithLabelValues("alarms").Inc()
}

// applyState upserts device_state and appends to state_log only when the value
// actually changed. Both happen in one statement so the comparison sees a
// consistent snapshot.
func (w *writer) applyState(ctx context.Context, deviceID, key, value string, ts time.Time) {
	cacheKey := deviceID + "\x00" + key
	if prev, ok := w.stateCache[cacheKey]; ok && prev == value {
		return
	}

	err := w.exec(ctx, `
		WITH prev AS (
			SELECT value AS old_value FROM device_state WHERE device_id = $1::uuid AND key = $2
		), upserted AS (
			INSERT INTO device_state (device_id, key, value, ts)
			VALUES ($1::uuid, $2, $3, $4)
			ON CONFLICT (device_id, key) DO UPDATE SET value = EXCLUDED.value, ts = EXCLUDED.ts
			RETURNING 1
		)
		INSERT INTO state_log (device_id, key, old_value, new_value, ts)
		SELECT $1::uuid, $2, prev.old_value, $3, $4 FROM prev
		WHERE prev.old_value IS DISTINCT FROM $3
		UNION ALL
		SELECT $1::uuid, $2, NULL, $3, $4
		WHERE NOT EXISTS (SELECT 1 FROM prev)`,
		deviceID, key, value, ts)
	if err != nil {
		w.dbError(err, "tulis device_state", key)
		return
	}

	w.stateCache[cacheKey] = value
	w.m.Written.WithLabelValues("device_state").Inc()
}

func (w *writer) writeReject(ctx context.Context, r reject) {
	w.m.Rejected.WithLabelValues(r.kind, r.reason).Inc()
	w.log.Warn().
		Str("topic", r.topic).
		Str("kind", r.kind).
		Str("reason", r.reason).
		Str("detail", r.detail).
		Int("bytes", len(r.payload)).
		Msg("payload ditolak")

	if err := w.exec(ctx, `
		INSERT INTO ingest_rejects (topic, payload, reason, received_at)
		VALUES ($1, $2, $3, $4)`,
		r.topic, r.payload, r.error(), r.received); err != nil {
		w.dbError(err, "tulis ingest_rejects", r.topic)
		return
	}
	w.m.Written.WithLabelValues("ingest_rejects").Inc()
}

// flush writes the accumulated telemetry. On failure the rows stay in the batch
// and are retried on the next tick, so a PostgreSQL restart costs latency and
// not data.
func (w *writer) flush(ctx context.Context) {
	if len(w.batch) == 0 {
		return
	}
	rows := w.batch
	w.m.BatchSize.Observe(float64(len(rows)))

	inserted, err := w.copyTelemetry(ctx, rows)
	if err != nil {
		w.dbError(err, "tulis telemetry", "")
		w.keepForRetry(rows)
		return
	}

	w.batch = w.batch[:0]
	duplicates := len(rows) - int(inserted)
	w.m.Written.WithLabelValues("telemetry").Add(float64(inserted))
	if duplicates > 0 {
		w.m.Duplicates.Add(float64(duplicates))
	}
	w.log.Debug().
		Int("rows", len(rows)).
		Int64("inserted", inserted).
		Int("duplicates", duplicates).
		Msg("flush telemetry")
}

func (w *writer) keepForRetry(rows []telemetryRow) {
	if len(rows) <= maxPendingRows {
		w.batch = rows
		return
	}
	dropped := len(rows) - maxPendingRows
	w.batch = append(w.batch[:0], rows[dropped:]...)
	w.log.Error().
		Int("dropped", dropped).
		Int("kept", len(w.batch)).
		Msg("buffer telemetry penuh, baris tertua dibuang")
}

// copyTelemetry stages the batch in a temp table and inserts it with
// ON CONFLICT DO NOTHING. That is what makes a QoS 1 redelivery or a replayed
// buffer land exactly once, and it tells us how many rows were duplicates.
func (w *writer) copyTelemetry(ctx context.Context, rows []telemetryRow) (int64, error) {
	conn, err := w.acquire(ctx)
	if err != nil {
		return 0, err
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		w.release()
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// device_id is staged as text and cast on insert: the staging table is not
	// the schema, and the cast keeps the copy encoding boring.
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE IF NOT EXISTS telemetry_in (
			device_id text, metric text, ts timestamptz, value double precision
		) ON COMMIT DELETE ROWS`); err != nil {
		w.release()
		return 0, fmt.Errorf("siapkan temp table: %w", err)
	}

	src := pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
		r := rows[i]
		return []any{r.deviceID, r.metric, r.ts, r.value}, nil
	})
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"telemetry_in"},
		[]string{"device_id", "metric", "ts", "value"}, src); err != nil {
		w.release()
		return 0, fmt.Errorf("copy ke temp table: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO telemetry (device_id, metric, ts, value)
		SELECT device_id::uuid, metric, ts, value FROM telemetry_in
		ON CONFLICT (device_id, metric, ts) DO NOTHING`)
	if err != nil {
		w.release()
		return 0, fmt.Errorf("insert telemetry: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		w.release()
		return 0, fmt.Errorf("commit: %w", err)
	}
	return tag.RowsAffected(), nil
}

// acquire pins a pooled connection so the temp table persists across flushes.
func (w *writer) acquire(ctx context.Context) (*pgxpool.Conn, error) {
	if w.conn != nil && !w.conn.Conn().IsClosed() {
		return w.conn, nil
	}
	w.release()
	conn, err := w.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("ambil koneksi: %w", err)
	}
	w.conn = conn
	return conn, nil
}

func (w *writer) release() {
	if w.conn != nil {
		w.conn.Release()
		w.conn = nil
	}
}

// exec runs a one-off statement on the pool. Only the telemetry copy needs the
// pinned connection.
func (w *writer) exec(ctx context.Context, sql string, args ...any) error {
	_, err := w.pool.Exec(ctx, sql, args...)
	return err
}

func (w *writer) dbError(err error, what, where string) {
	if errors.Is(err, context.Canceled) {
		return
	}
	w.m.DBErrors.Inc()
	w.log.Error().Err(err).Str("where", where).Msg("gagal " + what)
}
