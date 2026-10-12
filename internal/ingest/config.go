// Package ingest turns MQTT messages into PostgreSQL rows: it validates every
// payload against the embedded contract, batches telemetry, keeps device state
// and status, records alarms, and raises its own alarm when a device goes quiet.
//
// It never crashes on bad input. An invalid payload lands in ingest_rejects and
// the service keeps going, because during the PoC a malformed message from the
// PLC is information, not a reason to stop recording the other 33 devices.
package ingest

import (
	"os"
	"time"

	"github.com/tynext/tynext-poc/internal/mqttx"
)

// Env variables specific to ingest. The MQTT and database ones live in
// internal/mqttx and internal/store.
const (
	EnvHTTPAddr = "RAIN_HTTP_ADDR"
	EnvClientID = "RAIN_INGEST_CLIENT_ID"
)

// Defaults chosen in the contract and the pass criteria.
const (
	// DefaultClientID must stay stable: with CleanSession false it is what the
	// broker uses to hold queued QoS 1 messages across an ingest restart.
	DefaultClientID = "ingest"
	// DefaultShareGroup is the $share group, so extra ingest instances split
	// the load instead of each writing the same row.
	DefaultShareGroup = "ingest"

	// DefaultBatchInterval and DefaultBatchMaxRows: flush once a second or
	// every 500 rows, whichever comes first.
	DefaultBatchInterval = time.Second
	DefaultBatchMaxRows  = 500

	// DefaultStaleAfter is the gap that marks a device as stale. Telemetry is
	// published every second, so 5 s means five missed messages.
	DefaultStaleAfter = 5 * time.Second
	// DefaultStaleCheckEvery is how often the detector sweeps.
	DefaultStaleCheckEvery = time.Second

	// StaleCode is the alarm code this service raises itself.
	StaleCode = "STALE"
	// SourceServer marks alarms the server raised, as opposed to the PLC.
	SourceServer = "server"
	// SourcePLC marks alarms that arrived over MQTT.
	SourcePLC = "plc"
)

// Config holds everything the service needs to run.
type Config struct {
	MQTT     mqttx.Config
	DSN      string
	HTTPAddr string

	ShareGroup      string
	BatchInterval   time.Duration
	BatchMaxRows    int
	StaleAfter      time.Duration
	StaleCheckEvery time.Duration

	// QueueSize bounds how many decoded messages wait for the writer. When it
	// fills, the MQTT callback blocks, which is the backpressure we want: the
	// broker holds QoS 1 messages rather than ingest dropping them.
	QueueSize int
}

// ConfigFromEnv builds a config from the RAIN_* variables and the defaults.
func ConfigFromEnv() Config {
	mqttCfg := mqttx.ConfigFromEnv()
	mqttCfg.ClientID = valueOr(os.Getenv(EnvClientID), DefaultClientID)
	// Keeping the session lets the broker queue QoS 1 messages while ingest is
	// restarting, which is what pass criterion #4 asks for.
	mqttCfg.CleanSession = false

	return Config{
		MQTT:            mqttCfg,
		DSN:             os.Getenv("RAIN_PG_DSN"),
		HTTPAddr:        valueOr(os.Getenv(EnvHTTPAddr), "127.0.0.1:8080"),
		ShareGroup:      DefaultShareGroup,
		BatchInterval:   DefaultBatchInterval,
		BatchMaxRows:    DefaultBatchMaxRows,
		StaleAfter:      DefaultStaleAfter,
		StaleCheckEvery: DefaultStaleCheckEvery,
		QueueSize:       8192,
	}
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
