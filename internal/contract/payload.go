package contract

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// SchemaVersion is the contract version this build publishes.
const SchemaVersion = "1.0"

// TimeLayout is the only accepted ts format: ISO 8601 UTC with exactly three
// fractional digits. The schemas enforce the same shape with a regex, so a
// TF6701 configured differently fails loudly during the PoC instead of silently
// in production.
const TimeLayout = "2006-01-02T15:04:05.000Z"

// Time marshals to TimeLayout and refuses to parse anything else.
type Time struct {
	time.Time
}

// NewTime truncates t to milliseconds in UTC, matching what goes on the wire.
func NewTime(t time.Time) Time {
	return Time{t.UTC().Truncate(time.Millisecond)}
}

// MarshalJSON renders the timestamp in TimeLayout.
func (t Time) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(t.UTC().Format(TimeLayout))), nil
}

// UnmarshalJSON parses a timestamp, rejecting offsets and other precisions.
func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("ts must be a string: %w", err)
	}
	parsed, err := time.Parse(TimeLayout, s)
	if err != nil {
		return fmt.Errorf("ts %q is not ISO 8601 UTC with milliseconds (%s)", s, TimeLayout)
	}
	t.Time = parsed
	return nil
}

// StateValue is a non-numeric tag value: a string or a boolean. The database
// stores it as text, so String is the canonical form.
type StateValue struct {
	str     string
	boolean bool
	isBool  bool
}

// StateString builds a string-valued state.
func StateString(s string) StateValue { return StateValue{str: s} }

// StateBool builds a boolean-valued state.
func StateBool(b bool) StateValue { return StateValue{boolean: b, isBool: true} }

// IsBool reports whether the value arrived as a JSON boolean.
func (v StateValue) IsBool() bool { return v.isBool }

// Bool returns the boolean value; it is false for string-valued states.
func (v StateValue) Bool() bool { return v.boolean }

// String returns the canonical text stored in device_state.value.
func (v StateValue) String() string {
	if v.isBool {
		return strconv.FormatBool(v.boolean)
	}
	return v.str
}

// MarshalJSON writes a JSON string or boolean, matching what was received.
func (v StateValue) MarshalJSON() ([]byte, error) {
	if v.isBool {
		return json.Marshal(v.boolean)
	}
	return json.Marshal(v.str)
}

// UnmarshalJSON accepts a JSON string or boolean and nothing else.
func (v *StateValue) UnmarshalJSON(b []byte) error {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	switch val := raw.(type) {
	case string:
		*v = StateString(val)
	case bool:
		*v = StateBool(val)
	default:
		return fmt.Errorf("state value must be a string or boolean, got %T", raw)
	}
	return nil
}

// Telemetry is the rain/pts/{class}/{deviceId}/telemetry payload.
type Telemetry struct {
	SchemaVersion string                `json:"schemaVersion"`
	DeviceID      string                `json:"deviceId"`
	TS            Time                  `json:"ts"`
	Seq           int64                 `json:"seq"`
	Metrics       map[string]float64    `json:"metrics"`
	State         map[string]StateValue `json:"state,omitempty"`
}

// Status is the retained rain/pts/{class}/{deviceId}/status payload.
// Only Online is mandatory: the LWT form is {"online":false}.
type Status struct {
	SchemaVersion string `json:"schemaVersion,omitempty"`
	TS            *Time  `json:"ts,omitempty"`
	Online        bool   `json:"online"`
	FW            string `json:"fw,omitempty"`
}

// Timestamp returns the payload ts, falling back to fallback when the payload
// carries none — which is the case for the will message.
func (s Status) Timestamp(fallback time.Time) time.Time {
	if s.TS != nil {
		return s.TS.Time
	}
	return fallback.UTC()
}

// Severity levels accepted by the alarm schema.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Alarm transition states.
const (
	AlarmRaised  = "raised"
	AlarmCleared = "cleared"
)

// Alarm is the rain/pts/{class}/{deviceId}/alarm payload.
type Alarm struct {
	SchemaVersion string   `json:"schemaVersion"`
	TS            Time     `json:"ts"`
	DeviceID      string   `json:"deviceId"`
	Code          string   `json:"code"`
	Severity      string   `json:"severity"`
	State         string   `json:"state"`
	Message       string   `json:"message"`
	Value         *float64 `json:"value,omitempty"`
}
