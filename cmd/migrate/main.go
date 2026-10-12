// Command migrate applies the embedded SQL migrations and seeds the devices
// table from contracts/devices.json.
//
// The device list is read from the contract at run time rather than copied into
// a seed .sql file, so there is exactly one place that says which devices exist.
//
//	go run ./cmd/migrate up       apply migrations, then seed
//	go run ./cmd/migrate down     roll everything back
//	go run ./cmd/migrate seed     re-seed devices only
//	go run ./cmd/migrate version  print the current schema version
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/tynext/tynext-poc/internal/contract"
	"github.com/tynext/tynext-poc/internal/store"
	"github.com/tynext/tynext-poc/migrations"
)

func main() {
	action := "up"
	if len(os.Args) > 1 {
		action = os.Args[1]
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, action)
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate %s: %v\n", action, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, action string) error {
	dsn := store.DSNFromEnv()
	if dsn == "" {
		return fmt.Errorf("%s is empty", store.EnvDSN)
	}

	switch action {
	case "up":
		if err := migrateDB(dsn, "up"); err != nil {
			return err
		}
		return seed(ctx, dsn)
	case "down":
		return migrateDB(dsn, "down")
	case "version":
		return printVersion(dsn)
	case "seed":
		return seed(ctx, dsn)
	default:
		return fmt.Errorf("unknown action %q (want up, down, seed or version)", action)
	}
}

func newMigrate(dsn string) (*migrate.Migrate, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, pgxDSN(dsn))
	if err != nil {
		return nil, fmt.Errorf("open migrator: %w", err)
	}
	return m, nil
}

// pgxDSN rewrites the scheme so golang-migrate picks its pgx/v5 driver instead
// of the lib/pq one.
func pgxDSN(dsn string) string {
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if strings.HasPrefix(dsn, prefix) {
			return "pgx5://" + strings.TrimPrefix(dsn, prefix)
		}
	}
	return dsn
}

func migrateDB(dsn, direction string) error {
	m, err := newMigrate(dsn)
	if err != nil {
		return err
	}
	defer closeMigrate(m)

	if direction == "up" {
		err = m.Up()
	} else {
		err = m.Down()
	}
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	if errors.Is(err, migrate.ErrNoChange) {
		fmt.Println("• skema sudah mutakhir, tidak ada migrasi yang dijalankan")
	} else {
		fmt.Printf("• migrasi %s selesai\n", direction)
	}
	return printVersionOf(m)
}

func printVersion(dsn string) error {
	m, err := newMigrate(dsn)
	if err != nil {
		return err
	}
	defer closeMigrate(m)
	return printVersionOf(m)
}

func printVersionOf(m *migrate.Migrate) error {
	version, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		fmt.Println("• versi skema: kosong (belum ada migrasi)")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("• versi skema: %d (dirty=%t)\n", version, dirty)
	if dirty {
		return fmt.Errorf("skema dirty di versi %d — perbaiki manual sebelum lanjut", version)
	}
	return nil
}

func closeMigrate(m *migrate.Migrate) {
	srcErr, dbErr := m.Close()
	if srcErr != nil {
		fmt.Fprintf(os.Stderr, "tutup source migrasi: %v\n", srcErr)
	}
	if dbErr != nil {
		fmt.Fprintf(os.Stderr, "tutup koneksi migrasi: %v\n", dbErr)
	}
}

func seed(ctx context.Context, dsn string) error {
	devices, err := contract.LoadDevices()
	if err != nil {
		return err
	}

	pool, err := store.Open(ctx, dsn, 30*time.Second)
	if err != nil {
		return err
	}
	defer pool.Close()

	inserted, updated, err := store.SeedDevices(ctx, pool, devices)
	if err != nil {
		return err
	}

	var total int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM devices`).Scan(&total); err != nil {
		return err
	}
	fmt.Printf("• seed devices: %d baru, %d diperbarui, %d baris di tabel\n", inserted, updated, total)
	if total != len(devices) {
		fmt.Fprintf(os.Stderr,
			"  peringatan: tabel berisi %d device, contracts/devices.json berisi %d — ada baris lama yang tidak lagi ada di kontrak\n",
			total, len(devices))
	}
	return nil
}
