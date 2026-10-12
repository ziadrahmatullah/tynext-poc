package ingest

import (
	"strings"
	"testing"
	"time"

	"github.com/tynext/tynext-poc/internal/contract"
)

func testDecoder(t *testing.T) *decoder {
	t.Helper()
	v, err := contract.NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return &decoder{
		validator: v,
		devices: map[string]deviceRef{
			"ems-pv-01": {id: "11111111-1111-1111-1111-111111111111", class: contract.ClassEMS},
			"pm-04":     {id: "22222222-2222-2222-2222-222222222222", class: contract.ClassMeter},
			"qf-5":      {id: "33333333-3333-3333-3333-333333333333", class: contract.ClassBreaker},
		},
	}
}

const testTS = "2026-10-12T04:30:00.000Z"

func TestDecodeAcceptsContractPayloads(t *testing.T) {
	d := testDecoder(t)
	now := time.Now().UTC()

	t.Run("telemetry", func(t *testing.T) {
		raw := `{"schemaVersion":"1.0","deviceId":"pm-04","ts":"` + testTS + `","seq":7,` +
			`"metrics":{"p_kw":1050.5,"f_hz":50.01}}`
		msg, rej := d.decode("rain/pts/meter/pm-04/telemetry", []byte(raw), now)
		if rej != nil {
			t.Fatalf("rejected: %s", rej.error())
		}
		if msg.deviceID != "22222222-2222-2222-2222-222222222222" {
			t.Errorf("deviceID = %q", msg.deviceID)
		}
		if msg.telemetry.Seq != 7 || len(msg.telemetry.Metrics) != 2 {
			t.Errorf("telemetry = %+v", msg.telemetry)
		}
		if got := msg.telemetry.Metrics["p_kw"]; got != 1050.5 {
			t.Errorf("p_kw = %v", got)
		}
	})

	t.Run("telemetry with empty metrics and state", func(t *testing.T) {
		raw := `{"schemaVersion":"1.0","deviceId":"qf-5","ts":"` + testTS + `","seq":1,` +
			`"metrics":{},"state":{"closed":true,"tripped":false}}`
		msg, rej := d.decode("rain/pts/breaker/qf-5/telemetry", []byte(raw), now)
		if rej != nil {
			t.Fatalf("breaker telemetry rejected: %s", rej.error())
		}
		if msg.telemetry.State["closed"].String() != "true" {
			t.Errorf("closed = %q", msg.telemetry.State["closed"].String())
		}
		if msg.telemetry.State["tripped"].String() != "false" {
			t.Errorf("tripped = %q", msg.telemetry.State["tripped"].String())
		}
	})

	t.Run("status", func(t *testing.T) {
		msg, rej := d.decode("rain/pts/ems/ems-pv-01/status",
			[]byte(`{"schemaVersion":"1.0","ts":"`+testTS+`","online":true,"fw":"TF6701"}`), now)
		if rej != nil {
			t.Fatalf("rejected: %s", rej.error())
		}
		if !msg.status.Online || msg.status.FW != "TF6701" {
			t.Errorf("status = %+v", msg.status)
		}
	})

	t.Run("LWT", func(t *testing.T) {
		msg, rej := d.decode(contract.LWTTopic, contract.LWTPayload, now)
		if rej != nil {
			t.Fatalf("the will message must be accepted, got: %s", rej.error())
		}
		if msg.status.Online {
			t.Error("the will message means offline")
		}
		if got := msg.status.Timestamp(now); !got.Equal(now) {
			t.Errorf("without ts the receive time should stand in, got %v", got)
		}
	})

	t.Run("alarm", func(t *testing.T) {
		raw := `{"schemaVersion":"1.0","ts":"` + testTS + `","deviceId":"ems-pv-01","code":"A-12",` +
			`"severity":"warning","state":"raised","message":"dp -62 kW","value":-62}`
		msg, rej := d.decode("rain/pts/ems/ems-pv-01/alarm", []byte(raw), now)
		if rej != nil {
			t.Fatalf("rejected: %s", rej.error())
		}
		if msg.alarm.Code != "A-12" || msg.alarm.State != contract.AlarmRaised {
			t.Errorf("alarm = %+v", msg.alarm)
		}
		if msg.alarm.Value == nil || *msg.alarm.Value != -62 {
			t.Errorf("value = %v", msg.alarm.Value)
		}
	})
}

func TestDecodeRejects(t *testing.T) {
	d := testDecoder(t)
	now := time.Now().UTC()

	cases := []struct {
		name       string
		topic      string
		payload    string
		wantReason string
	}{
		{
			name: "topic outside the contract", topic: "rain/pts/cmd/ems-pv-01/set",
			payload: `{}`, wantReason: reasonTopic,
		},
		{
			name: "topic with too few segments", topic: "rain/pts/meter/pm-04",
			payload: `{}`, wantReason: reasonTopic,
		},
		{
			name: "device not in the inventory", topic: "rain/pts/meter/pm-99/telemetry",
			payload:    `{"schemaVersion":"1.0","deviceId":"pm-99","ts":"` + testTS + `","seq":1,"metrics":{}}`,
			wantReason: reasonUnknownDevice,
		},
		{
			name: "class in topic disagrees with inventory", topic: "rain/pts/gen/pm-04/telemetry",
			payload:    `{"schemaVersion":"1.0","deviceId":"pm-04","ts":"` + testTS + `","seq":1,"metrics":{}}`,
			wantReason: reasonClassMismatch,
		},
		{
			name: "payload deviceId disagrees with topic", topic: "rain/pts/meter/pm-04/telemetry",
			payload:    `{"schemaVersion":"1.0","deviceId":"ems-pv-01","ts":"` + testTS + `","seq":1,"metrics":{}}`,
			wantReason: reasonDeviceMismatch,
		},
		{
			name: "alarm deviceId disagrees with topic", topic: "rain/pts/ems/ems-pv-01/alarm",
			payload:    `{"schemaVersion":"1.0","ts":"` + testTS + `","deviceId":"pm-04","code":"A-12","severity":"info","state":"raised","message":"x"}`,
			wantReason: reasonDeviceMismatch,
		},
		{
			name: "not JSON", topic: "rain/pts/meter/pm-04/telemetry",
			payload: `{broken`, wantReason: reasonSchema,
		},
		{
			name: "ts without milliseconds", topic: "rain/pts/meter/pm-04/telemetry",
			payload:    `{"schemaVersion":"1.0","deviceId":"pm-04","ts":"2026-10-12T04:30:00Z","seq":1,"metrics":{}}`,
			wantReason: reasonSchema,
		},
		{
			name: "metric carries a string", topic: "rain/pts/meter/pm-04/telemetry",
			payload:    `{"schemaVersion":"1.0","deviceId":"pm-04","ts":"` + testTS + `","seq":1,"metrics":{"p_kw":"1050"}}`,
			wantReason: reasonSchema,
		},
		{
			name: "status missing online", topic: "rain/pts/ems/ems-pv-01/status",
			payload: `{"schemaVersion":"1.0","ts":"` + testTS + `"}`, wantReason: reasonSchema,
		},
		{
			name: "alarm with unknown severity", topic: "rain/pts/ems/ems-pv-01/alarm",
			payload:    `{"schemaVersion":"1.0","ts":"` + testTS + `","deviceId":"ems-pv-01","code":"A-12","severity":"fatal","state":"raised","message":"x"}`,
			wantReason: reasonSchema,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, rej := d.decode(tc.topic, []byte(tc.payload), now)
			if rej == nil {
				t.Fatalf("payload was accepted: %+v", msg)
			}
			if msg != nil {
				t.Error("decode must return either a message or a reject, not both")
			}
			if rej.reason != tc.wantReason {
				t.Errorf("reason = %q, want %q (detail: %s)", rej.reason, tc.wantReason, rej.detail)
			}
			if rej.topic != tc.topic || string(rej.payload) != tc.payload {
				t.Error("the reject must carry the original topic and bytes for later inspection")
			}
			if line := rej.error(); line == "" || strings.Contains(line, "\n") {
				t.Errorf("reject error should be one line, got %q", line)
			}
		})
	}
}

// Reject reasons become a Prometheus label, so the set has to stay small and
// fixed. This test fails if someone adds a reason built from message content.
func TestRejectReasonsAreLowCardinality(t *testing.T) {
	known := map[string]bool{
		reasonTopic: true, reasonUnknownDevice: true, reasonClassMismatch: true,
		reasonSchema: true, reasonDeviceMismatch: true,
	}
	if len(known) != 5 {
		t.Fatalf("expected 5 reject reasons, found %d", len(known))
	}
	for reason := range known {
		if strings.ContainsAny(reason, " \"{}") {
			t.Errorf("reason %q looks like it was built from a payload", reason)
		}
	}
}
