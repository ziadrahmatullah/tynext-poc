package ingest

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/tynext/tynext-poc/internal/store"
)

// EnvTestDSN opts into the tests that need a real PostgreSQL. Without it they
// skip, so `make test` stays hermetic; `make test-integration` sets it.
const EnvTestDSN = "RAIN_TEST_PG_DSN"

// testDB connects and creates a throwaway device. Everything the test writes
// hangs off that device, so dropping it cleans up through ON DELETE CASCADE.
func testDB(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv(EnvTestDSN)
	if dsn == "" {
		t.Skipf("set %s to run the tests that need PostgreSQL", EnvTestDSN)
	}

	ctx := context.Background()
	pool, err := store.Open(ctx, dsn, 10*time.Second)
	if err != nil {
		t.Fatalf("connect to %s: %v", EnvTestDSN, err)
	}

	code := fmt.Sprintf("zz-test-%d", time.Now().UnixNano())
	var id string
	err = pool.QueryRow(ctx, `
		INSERT INTO devices (code, class, name) VALUES ($1, 'meter', 'device uji')
		RETURNING id::text`, code).Scan(&id)
	if err != nil {
		pool.Close()
		t.Fatalf("create the throwaway device: %v", err)
	}

	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM devices WHERE id = $1::uuid`, id); err != nil {
			t.Errorf("clean up device %s: %v", code, err)
		}
		pool.Close()
	})
	return pool, id
}

func testWriter(t *testing.T, pool *pgxpool.Pool) *writer {
	t.Helper()
	cfg := ConfigFromEnv()
	cfg.BatchMaxRows = DefaultBatchMaxRows
	return newWriter(cfg, pool, zerolog.Nop(), NewMetrics())
}

func countRows(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// This is the mechanism that makes a QoS 1 redelivery and a replayed buffer
// land exactly once.
func TestTelemetryIsDeduplicated(t *testing.T) {
	pool, deviceID := testDB(t)
	w := testWriter(t, pool)
	defer w.release()
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Millisecond)
	rows := make([]telemetryRow, 0, 10)
	for i := 0; i < 5; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		rows = append(rows,
			telemetryRow{deviceID: deviceID, metric: "p_kw", ts: ts, value: float64(1000 + i)},
			telemetryRow{deviceID: deviceID, metric: "f_hz", ts: ts, value: 50.01},
		)
	}

	inserted, err := w.copyTelemetry(ctx, rows)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if inserted != 10 {
		t.Fatalf("first write inserted %d rows, want 10", inserted)
	}

	// The same batch again: every row is a duplicate.
	inserted, err = w.copyTelemetry(ctx, rows)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if inserted != 0 {
		t.Errorf("replay inserted %d rows, want 0", inserted)
	}

	// A batch that overlaps: half old, half new.
	overlap := append([]telemetryRow{}, rows[6:]...)
	for i := 5; i < 8; i++ {
		overlap = append(overlap, telemetryRow{
			deviceID: deviceID, metric: "p_kw",
			ts: base.Add(time.Duration(i) * time.Second), value: float64(1000 + i),
		})
	}
	inserted, err = w.copyTelemetry(ctx, overlap)
	if err != nil {
		t.Fatalf("overlapping write: %v", err)
	}
	if inserted != 3 {
		t.Errorf("overlapping write inserted %d rows, want 3", inserted)
	}

	if total := countRows(t, pool, `SELECT count(*) FROM telemetry WHERE device_id = $1::uuid`, deviceID); total != 13 {
		t.Errorf("telemetry has %d rows, want 13", total)
	}
}

// Duplicates inside one batch must not make the insert fail.
func TestTelemetryBatchWithInternalDuplicates(t *testing.T) {
	pool, deviceID := testDB(t)
	w := testWriter(t, pool)
	defer w.release()

	ts := time.Now().UTC().Truncate(time.Millisecond)
	rows := []telemetryRow{
		{deviceID: deviceID, metric: "p_kw", ts: ts, value: 1},
		{deviceID: deviceID, metric: "p_kw", ts: ts, value: 2},
		{deviceID: deviceID, metric: "p_kw", ts: ts, value: 3},
	}

	inserted, err := w.copyTelemetry(context.Background(), rows)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if inserted != 1 {
		t.Errorf("inserted %d rows, want 1", inserted)
	}
}

func TestStateLogsOnlyTransitions(t *testing.T) {
	pool, deviceID := testDB(t)
	w := testWriter(t, pool)
	defer w.release()
	ctx := context.Background()

	ts := time.Now().UTC().Truncate(time.Millisecond)

	w.applyState(ctx, deviceID, "closed", "true", ts)
	w.applyState(ctx, deviceID, "closed", "true", ts.Add(time.Second))
	w.applyState(ctx, deviceID, "closed", "true", ts.Add(2*time.Second))

	if n := countRows(t, pool, `SELECT count(*) FROM state_log WHERE device_id = $1::uuid`, deviceID); n != 1 {
		t.Errorf("state_log has %d rows after three identical values, want 1", n)
	}

	w.applyState(ctx, deviceID, "closed", "false", ts.Add(3*time.Second))
	if n := countRows(t, pool, `SELECT count(*) FROM state_log WHERE device_id = $1::uuid`, deviceID); n != 2 {
		t.Errorf("state_log has %d rows after a change, want 2", n)
	}

	var old, current string
	err := pool.QueryRow(ctx, `
		SELECT old_value, new_value FROM state_log
		WHERE device_id = $1::uuid ORDER BY id DESC LIMIT 1`, deviceID).Scan(&old, &current)
	if err != nil {
		t.Fatalf("read state_log: %v", err)
	}
	if old != "true" || current != "false" {
		t.Errorf("transition recorded as %q → %q, want \"true\" → \"false\"", old, current)
	}

	var value string
	if err := pool.QueryRow(ctx, `
		SELECT value FROM device_state WHERE device_id = $1::uuid AND key = 'closed'`, deviceID).Scan(&value); err != nil {
		t.Fatalf("read device_state: %v", err)
	}
	if value != "false" {
		t.Errorf("device_state.value = %q, want the latest value", value)
	}

	// A fresh writer has an empty cache but must not re-log an unchanged value.
	w2 := testWriter(t, pool)
	defer w2.release()
	w2.applyState(ctx, deviceID, "closed", "false", ts.Add(4*time.Second))
	if n := countRows(t, pool, `SELECT count(*) FROM state_log WHERE device_id = $1::uuid`, deviceID); n != 2 {
		t.Errorf("state_log has %d rows after a restart with no change, want 2", n)
	}
}

func TestAlarmRaiseIsIdempotentAndClearCloses(t *testing.T) {
	pool, deviceID := testDB(t)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond)
	value := -62.0

	for i := 0; i < 3; i++ {
		if err := raiseAlarm(ctx, pool, deviceID, SourcePLC, "A-12", "warning", "dp -62 kW", &value, ts); err != nil {
			t.Fatalf("raise %d: %v", i, err)
		}
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM alarms WHERE device_id = $1::uuid AND cleared_at IS NULL`, deviceID); n != 1 {
		t.Errorf("three identical raises produced %d open alarms, want 1", n)
	}

	open, err := openAlarmDevices(ctx, pool, "A-12")
	if err != nil {
		t.Fatalf("openAlarmDevices: %v", err)
	}
	if !open[deviceID] {
		t.Error("the open alarm should be reported by openAlarmDevices")
	}

	cleared := ts.Add(time.Minute)
	if err := clearAlarm(ctx, pool, deviceID, "A-12", cleared); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if n := countRows(t, pool,
		`SELECT count(*) FROM alarms WHERE device_id = $1::uuid AND cleared_at IS NULL`, deviceID); n != 0 {
		t.Errorf("%d alarms still open after clearing, want 0", n)
	}

	// Clearing again changes nothing and is not an error.
	if err := clearAlarm(ctx, pool, deviceID, "A-12", cleared); err != nil {
		t.Fatalf("clear twice: %v", err)
	}

	// The same code can be raised again later, as a new row.
	if err := raiseAlarm(ctx, pool, deviceID, SourcePLC, "A-12", "warning", "muncul lagi", &value, cleared.Add(time.Minute)); err != nil {
		t.Fatalf("raise again: %v", err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM alarms WHERE device_id = $1::uuid`, deviceID); n != 2 {
		t.Errorf("alarms has %d rows, want 2 (one closed, one open)", n)
	}
}

func TestRejectIsStoredWithTheOriginalBytes(t *testing.T) {
	pool, _ := testDB(t)
	w := testWriter(t, pool)
	defer w.release()
	ctx := context.Background()

	topic := fmt.Sprintf("rain/pts/meter/zz-test-%d/telemetry", time.Now().UnixNano())
	payload := []byte(`{this is not json`)
	w.writeReject(ctx, reject{
		topic: topic, payload: payload, kind: "telemetry",
		reason: reasonSchema, detail: "invalid JSON", received: time.Now().UTC(),
	})
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM ingest_rejects WHERE topic = $1`, topic); err != nil {
			t.Errorf("clean up rejects: %v", err)
		}
	})

	var stored []byte
	var reason string
	err := pool.QueryRow(ctx, `
		SELECT payload, reason FROM ingest_rejects WHERE topic = $1`, topic).Scan(&stored, &reason)
	if err != nil {
		t.Fatalf("read ingest_rejects: %v", err)
	}
	if string(stored) != string(payload) {
		t.Errorf("stored payload = %q, want the original bytes", stored)
	}
	if reason == "" {
		t.Error("reason should say why the payload was refused")
	}
}
