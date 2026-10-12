// Package store holds the PostgreSQL access shared by cmd/migrate and the
// ingest service: the connection pool and the device inventory.
package store

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tynext/tynext-poc/internal/contract"
)

// EnvDSN is the connection string variable.
const EnvDSN = "RAIN_PG_DSN"

// DSNFromEnv reads RAIN_PG_DSN.
func DSNFromEnv() string { return os.Getenv(EnvDSN) }

// Open builds a pool and waits until the database answers. The retry loop is
// what lets ingest start before PostgreSQL is ready and recover after a restart.
func Open(ctx context.Context, dsn string, wait time.Duration) (*pgxpool.Pool, error) {
	if dsn == "" {
		return nil, fmt.Errorf("%s is empty", EnvDSN)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", EnvDSN, err)
	}
	cfg.MaxConns = 8
	cfg.MaxConnLifetime = time.Hour
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	deadline := time.Now().Add(wait)
	var lastErr error
	for {
		if err := pool.Ping(ctx); err == nil {
			return pool, nil
		} else {
			lastErr = err
		}
		if ctx.Err() != nil {
			pool.Close()
			return nil, ctx.Err()
		}
		if time.Now().After(deadline) {
			pool.Close()
			return nil, fmt.Errorf("database did not answer within %s: %w", wait, lastErr)
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// SeedDevices upserts contracts/devices.json into the devices table. It is
// idempotent: codes already present are updated in place, so device ids survive
// and the telemetry that points at them stays valid.
func SeedDevices(ctx context.Context, pool *pgxpool.Pool, devices []contract.Device) (inserted, updated int, err error) {
	for _, d := range devices {
		source, err := d.SourceJSON()
		if err != nil {
			return inserted, updated, fmt.Errorf("device %s: %w", d.Code, err)
		}
		var isInsert bool
		err = pool.QueryRow(ctx, `
			INSERT INTO devices (code, class, name, breaker, section, source, enabled)
			VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6::jsonb, true)
			ON CONFLICT (code) DO UPDATE SET
				class   = EXCLUDED.class,
				name    = EXCLUDED.name,
				breaker = EXCLUDED.breaker,
				section = EXCLUDED.section,
				source  = EXCLUDED.source
			RETURNING (xmax = 0) AS inserted`,
			d.Code, string(d.Class), d.Name, d.Breaker, d.Section, string(source),
		).Scan(&isInsert)
		if err != nil {
			return inserted, updated, fmt.Errorf("seed device %s: %w", d.Code, err)
		}
		if isInsert {
			inserted++
		} else {
			updated++
		}
	}
	return inserted, updated, nil
}

// DeviceIDs maps device code to the uuid primary key, which is what every other
// table references.
func DeviceIDs(ctx context.Context, pool *pgxpool.Pool) (map[string]string, error) {
	rows, err := pool.Query(ctx, `SELECT code, id::text FROM devices WHERE enabled`)
	if err != nil {
		return nil, fmt.Errorf("load device ids: %w", err)
	}
	defer rows.Close()

	ids := map[string]string{}
	for rows.Next() {
		var code, id string
		if err := rows.Scan(&code, &id); err != nil {
			return nil, err
		}
		ids[code] = id
	}
	return ids, rows.Err()
}
