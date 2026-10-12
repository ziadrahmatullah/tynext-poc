// Command simulator stands in for the CX5240 while the PLC is not available.
// It publishes the exact payloads contracts/ defines, from one MQTT connection,
// so on the test day the only change is swapping the source.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/tynext/tynext-poc/internal/mqttx"
	"github.com/tynext/tynext-poc/internal/sim"
)

func main() {
	plant := sim.DefaultPlantConfig()
	opts := sim.Options{Plant: plant}

	flag.DurationVar(&opts.Interval, "interval", time.Second, "jarak antar siklus publish")
	flag.DurationVar(&opts.Duration, "duration", 0, "berhenti setelah durasi ini (0 = sampai Ctrl-C)")
	flag.Float64Var(&opts.InvalidRate, "invalid-rate", 0, "peluang menyisipkan payload rusak per pesan (0..1)")
	flag.Float64Var(&opts.DupRate, "dup-rate", 0, "peluang mengirim ulang pesan yang sama (0..1)")
	flag.DurationVar(&opts.Outage, "outage", 0, "putuskan koneksi selama durasi ini lalu sambung lagi")
	flag.DurationVar(&opts.OutageAfter, "outage-after", 30*time.Second, "kapan outage dimulai, dihitung dari start")
	flag.BoolVar(&opts.AlarmDemo, "alarm-demo", false, "terbitkan sepasang alarm raised/cleared di awal run")
	flag.StringVar(&opts.Scenario, "scenario", "", "soc-full | stg-advisory | comm-fault")
	flag.StringVar(&opts.CountFile, "count-file", "", "tulis jumlah pesan terkirim per device ke file ini")
	flag.StringVar(&opts.QueuePath, "queue", "var/simulator.queue", "berkas buffer store-and-forward")
	flag.DurationVar(&opts.BufferMax, "buffer-max", 72*time.Hour, "umur maksimum pesan di buffer (TBC: menunggu kapasitas buffer TF6701)")
	flag.StringVar(&opts.FW, "fw", "simulator-poc", "nilai field fw pada payload status")

	flag.Float64Var(&plant.STG1KW, "stg1", plant.STG1KW, "base load STG-1 (kW)")
	flag.Float64Var(&plant.STG2KW, "stg2", plant.STG2KW, "base load STG-2 (kW)")
	flag.Float64Var(&plant.DGKW, "dg", plant.DGKW, "daya genset (kW)")
	flag.Float64Var(&plant.InitialSOCPct, "soc", plant.InitialSOCPct, "SOC awal (%)")
	flag.Int64Var(&plant.Seed, "seed", plant.Seed, "seed noise, isi untuk run yang bisa diulang")

	keepalive := flag.Duration("keepalive", 5*time.Second, "MQTT keepalive; 5 s menjaga deteksi LWT di bawah 10 s")
	clientID := flag.String("client-id", "", "clientId MQTT (default: username)")
	flag.Parse()

	opts.Plant = plant

	log := newLogger()

	opts.MQTT = mqttx.ConfigFromEnv()
	opts.MQTT.KeepAlive = *keepalive
	// A single physical client with a stable id, exactly like TF6701.
	opts.MQTT.ClientID = *clientID
	if opts.MQTT.ClientID == "" {
		opts.MQTT.ClientID = opts.MQTT.Username
	}
	// The PLC keeps its session so the broker holds QoS 1 messages for it.
	opts.MQTT.CleanSession = false

	if err := validate(opts); err != nil {
		log.Fatal().Err(err).Msg("opsi tidak valid")
	}

	runner, err := sim.New(opts, log)
	if err != nil {
		log.Fatal().Err(err).Msg("gagal menyiapkan simulator")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	log.Info().
		Str("broker", opts.MQTT.URL).
		Str("clientId", opts.MQTT.ClientID).
		Dur("interval", opts.Interval).
		Dur("keepalive", opts.MQTT.KeepAlive).
		Str("scenario", orNone(opts.Scenario)).
		Str("queue", opts.QueuePath).
		Msg("simulator mulai")

	runErr := runner.Run(ctx)
	stop()
	closeErr := runner.Close()

	if runErr != nil {
		log.Error().Err(runErr).Msg("simulator berhenti dengan error")
		os.Exit(1)
	}
	if closeErr != nil {
		log.Error().Err(closeErr).Msg("gagal menutup buffer")
		os.Exit(1)
	}
}

func validate(o sim.Options) error {
	if err := o.MQTT.Validate(); err != nil {
		return err
	}
	if o.InvalidRate < 0 || o.InvalidRate > 1 {
		return fmt.Errorf("-invalid-rate harus 0..1, bukan %v", o.InvalidRate)
	}
	if o.DupRate < 0 || o.DupRate > 1 {
		return fmt.Errorf("-dup-rate harus 0..1, bukan %v", o.DupRate)
	}
	switch o.Scenario {
	case sim.ScenarioNone, sim.ScenarioSOCFull, sim.ScenarioSTGAdvisory, sim.ScenarioCommFault:
	default:
		return fmt.Errorf("-scenario %q tidak dikenal (soc-full | stg-advisory | comm-fault)", o.Scenario)
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "(tidak ada)"
	}
	return s
}

func newLogger() zerolog.Logger {
	level := zerolog.InfoLevel
	if lv, err := zerolog.ParseLevel(os.Getenv("RAIN_LOG_LEVEL")); err == nil && lv != zerolog.NoLevel {
		level = lv
	}
	if format := os.Getenv("RAIN_LOG_FORMAT"); format == "console" || (format == "" && isTerminal()) {
		return zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}).
			Level(level).With().Timestamp().Str("svc", "simulator").Logger()
	}
	return zerolog.New(os.Stderr).Level(level).With().Timestamp().Str("svc", "simulator").Logger()
}

func isTerminal() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
