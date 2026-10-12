// Command ingest subscribes to the RAIN contract topics and writes them to
// PostgreSQL. It validates every payload against the embedded JSON Schemas, so
// what reaches the database is exactly what the contract says it is.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/tynext/tynext-poc/internal/ingest"
)

func main() {
	log := newLogger()

	cfg := ingest.ConfigFromEnv()
	if err := cfg.MQTT.Validate(); err != nil {
		log.Fatal().Err(err).Msg("konfigurasi MQTT tidak lengkap")
	}
	if cfg.DSN == "" {
		log.Fatal().Msg("RAIN_PG_DSN kosong")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	log.Info().
		Str("broker", cfg.MQTT.URL).
		Str("clientId", cfg.MQTT.ClientID).
		Str("http", cfg.HTTPAddr).
		Dur("batchInterval", cfg.BatchInterval).
		Int("batchMaxRows", cfg.BatchMaxRows).
		Dur("staleAfter", cfg.StaleAfter).
		Msg("ingest mulai")

	err := ingest.New(cfg, log).Run(ctx)
	stop()
	if err != nil {
		log.Error().Err(err).Msg("ingest berhenti dengan error")
		os.Exit(1)
	}
}

func newLogger() zerolog.Logger {
	level := zerolog.InfoLevel
	if lv, err := zerolog.ParseLevel(os.Getenv("RAIN_LOG_LEVEL")); err == nil && lv != zerolog.NoLevel {
		level = lv
	}

	// Human-readable on a terminal, JSON when piped into a log collector.
	if term := os.Getenv("RAIN_LOG_FORMAT"); term == "console" || (term == "" && isTerminal()) {
		return zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}).
			Level(level).With().Timestamp().Str("svc", "ingest").Logger()
	}
	return zerolog.New(os.Stderr).Level(level).With().Timestamp().Str("svc", "ingest").Logger()
}

func isTerminal() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
