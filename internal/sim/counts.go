package sim

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// DeviceCount is what the simulator actually put on the wire for one device.
// Scenario 03 compares Telemetry against the rows in the database.
type DeviceCount struct {
	Class string `json:"class"`
	// MetricsPerMessage is 0 for breaker and gen devices, which publish state
	// only. Their delivery cannot be counted row by row in the telemetry table,
	// so scenario 03 reports them separately instead of scoring them.
	MetricsPerMessage int `json:"metricsPerMessage"`

	Telemetry int64 `json:"telemetry"`
	Status    int64 `json:"status"`
	Alarm     int64 `json:"alarm"`

	// Corrupted and Duplicated are extra messages injected by --invalid-rate
	// and --dup-rate. They are on top of Telemetry, never instead of it.
	Corrupted  int64 `json:"corrupted"`
	Duplicated int64 `json:"duplicated"`

	// Replayed counts messages that went out from the disk buffer.
	Replayed int64 `json:"replayed"`
	// Queued is how many are still on disk when the run ends.
	Queued int64 `json:"queued"`
}

// Totals is the run-wide tally. It deliberately omits the per-device fields so
// the count file does not carry meaningless values at the top level.
type Totals struct {
	Telemetry  int64 `json:"telemetry"`
	Status     int64 `json:"status"`
	Alarm      int64 `json:"alarm"`
	Corrupted  int64 `json:"corrupted"`
	Duplicated int64 `json:"duplicated"`
	Replayed   int64 `json:"replayed"`
	Queued     int64 `json:"queued"`
}

// Counts is the whole count file.
type Counts struct {
	StartedAt  time.Time               `json:"startedAt"`
	FinishedAt time.Time               `json:"finishedAt"`
	IntervalMS int64                   `json:"intervalMs"`
	Publisher  string                  `json:"publisher"`
	Scenario   string                  `json:"scenario,omitempty"`
	Devices    map[string]*DeviceCount `json:"devices"`
	Totals     DeviceCount             `json:"totals"`
}

// NewCounts starts a tally for the given run.
func NewCounts(startedAt time.Time, interval time.Duration, publisher, scenario string) *Counts {
	return &Counts{
		StartedAt:  startedAt.UTC(),
		IntervalMS: interval.Milliseconds(),
		Publisher:  publisher,
		Scenario:   scenario,
		Devices:    map[string]*DeviceCount{},
	}
}

func (c *Counts) device(code, class string) *DeviceCount {
	d, ok := c.Devices[code]
	if !ok {
		d = &DeviceCount{Class: class}
		c.Devices[code] = d
	}
	return d
}

// Telemetry records one published telemetry message.
func (c *Counts) Telemetry(code, class string, metrics int) {
	d := c.device(code, class)
	d.Telemetry++
	d.MetricsPerMessage = metrics
	c.Totals.Telemetry++
}

// Status records one published status message.
func (c *Counts) Status(code, class string) {
	c.device(code, class).Status++
	c.Totals.Status++
}

// Alarm records one published alarm message.
func (c *Counts) Alarm(code, class string) {
	c.device(code, class).Alarm++
	c.Totals.Alarm++
}

// Corrupted records a deliberately broken message injected for the test.
func (c *Counts) Corrupted(code, class string) {
	c.device(code, class).Corrupted++
	c.Totals.Corrupted++
}

// Duplicated records a deliberate resend of an identical message.
func (c *Counts) Duplicated(code, class string) {
	c.device(code, class).Duplicated++
	c.Totals.Duplicated++
}

// Replayed records a message that went out from the disk buffer.
func (c *Counts) Replayed(code, class string) {
	c.device(code, class).Replayed++
	c.Totals.Replayed++
}

// Queued records how many messages for a device are still on disk.
func (c *Counts) Queued(code, class string, n int64) {
	c.device(code, class).Queued = n
	c.Totals.Queued += n
}

// Write saves the tally. Scenario 03 reads this file.
func (c *Counts) Write(path string, finishedAt time.Time) error {
	c.FinishedAt = finishedAt.UTC()

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create count file directory: %w", err)
		}
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode count file: %w", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write count file %s: %w", path, err)
	}
	return nil
}

// Codes lists the devices in the tally, sorted.
func (c *Counts) Codes() []string {
	codes := make([]string, 0, len(c.Devices))
	for code := range c.Devices {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}
