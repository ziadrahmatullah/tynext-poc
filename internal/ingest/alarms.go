package ingest

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// raiseAlarm inserts an open alarm. The partial unique index on
// (device_id, code) WHERE cleared_at IS NULL makes it idempotent, so a
// redelivered 'raised' or a second sweep of the stale detector adds nothing.
func raiseAlarm(ctx context.Context, pool *pgxpool.Pool, deviceID, source, code, severity, message string, value *float64, ts time.Time) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO alarms (source, code, device_id, severity, message, value, raised_at)
		VALUES ($1, $2, $3::uuid, $4, $5, $6, $7)
		ON CONFLICT (device_id, code) WHERE cleared_at IS NULL AND code IS NOT NULL
		DO NOTHING`,
		source, code, deviceID, severity, message, value, ts)
	if err != nil {
		return fmt.Errorf("raise %s untuk device %s: %w", code, deviceID, err)
	}
	return nil
}

// clearAlarm closes whatever is open for (device, code). Clearing something
// that is not open is a no-op, not an error.
func clearAlarm(ctx context.Context, pool *pgxpool.Pool, deviceID, code string, ts time.Time) error {
	_, err := pool.Exec(ctx, `
		UPDATE alarms SET cleared_at = $3
		WHERE device_id = $1::uuid AND code = $2 AND cleared_at IS NULL`,
		deviceID, code, ts)
	if err != nil {
		return fmt.Errorf("clear %s untuk device %s: %w", code, deviceID, err)
	}
	return nil
}

// openAlarmDevices returns the device ids that currently have an open alarm
// with this code. Ingest calls it at startup so a restart does not leave a
// stale alarm open forever with nobody tracking it.
func openAlarmDevices(ctx context.Context, pool *pgxpool.Pool, code string) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `
		SELECT device_id::text FROM alarms
		WHERE code = $1 AND cleared_at IS NULL AND device_id IS NOT NULL`, code)
	if err != nil {
		return nil, fmt.Errorf("baca alarm %s yang terbuka: %w", code, err)
	}
	defer rows.Close()

	open := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		open[id] = true
	}
	return open, rows.Err()
}
