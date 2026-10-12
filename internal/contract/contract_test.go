package contract_test

import (
	"encoding/json"
	"io/fs"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/tynext/tynext-poc/contracts"
	"github.com/tynext/tynext-poc/internal/contract"
)

func newValidator(t *testing.T) *contract.Validator {
	t.Helper()
	v, err := contract.NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return v
}

// kindOfExample maps the example filename prefix to the schema it must satisfy.
func kindOfExample(name string) (contract.Kind, bool) {
	switch {
	case strings.HasPrefix(name, "telemetry-"):
		return contract.KindTelemetry, true
	case strings.HasPrefix(name, "status-"):
		return contract.KindStatus, true
	case strings.HasPrefix(name, "alarm-"):
		return contract.KindAlarm, true
	}
	return "", false
}

func exampleFiles(t *testing.T) []string {
	t.Helper()
	entries, err := fs.ReadDir(contracts.FS, contracts.ExamplesDir)
	if err != nil {
		t.Fatalf("read examples dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Fatal("no example payloads are embedded")
	}
	return names
}

func TestEveryExampleValidates(t *testing.T) {
	v := newValidator(t)
	for _, name := range exampleFiles(t) {
		t.Run(name, func(t *testing.T) {
			kind, ok := kindOfExample(name)
			if !ok {
				t.Fatalf("example %s has no telemetry-/status-/alarm- prefix, so no schema can be picked", name)
			}
			raw, err := contracts.FS.ReadFile(path.Join(contracts.ExamplesDir, name))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if err := v.Validate(kind, raw); err != nil {
				t.Fatalf("does not satisfy the %s schema: %v", kind, err)
			}
		})
	}
}

// Examples must also decode into the Go types the simulator and ingest use,
// otherwise the schema and the code could drift apart unnoticed.
func TestExamplesDecodeIntoGoTypes(t *testing.T) {
	v := newValidator(t)
	devices := contract.DeviceByCode(loadDevices(t))

	for _, name := range exampleFiles(t) {
		raw, err := contracts.FS.ReadFile(path.Join(contracts.ExamplesDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		kind, _ := kindOfExample(name)

		switch kind {
		case contract.KindTelemetry:
			tel, err := v.ValidateTelemetry(raw)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if _, known := devices[tel.DeviceID]; !known {
				t.Errorf("%s: deviceId %q is not in devices.json", name, tel.DeviceID)
			}
			if tel.TS.IsZero() {
				t.Errorf("%s: ts did not decode", name)
			}
		case contract.KindStatus:
			if _, err := v.ValidateStatus(raw); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		case contract.KindAlarm:
			al, err := v.ValidateAlarm(raw)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if _, known := devices[al.DeviceID]; !known {
				t.Errorf("%s: deviceId %q is not in devices.json", name, al.DeviceID)
			}
		}
	}
}

// The will message is frozen at CONNECT, so it carries neither schemaVersion
// nor ts. It still has to pass the status schema or ingest would reject the one
// message that proves the plant went away.
func TestLWTPayloadValidates(t *testing.T) {
	v := newValidator(t)
	if err := v.Validate(contract.KindStatus, contract.LWTPayload); err != nil {
		t.Fatalf("LWT payload rejected: %v", err)
	}
	st, err := v.ValidateStatus(contract.LWTPayload)
	if err != nil {
		t.Fatalf("decode LWT: %v", err)
	}
	if st.Online {
		t.Error("LWT payload must mean offline")
	}
	fallback := time.Date(2026, 10, 12, 4, 30, 0, 0, time.UTC)
	if got := st.Timestamp(fallback); !got.Equal(fallback) {
		t.Errorf("Timestamp fallback = %v, want %v", got, fallback)
	}
}

func TestInvalidPayloadsAreRejected(t *testing.T) {
	v := newValidator(t)
	cases := []struct {
		name string
		kind contract.Kind
		raw  string
	}{
		{"telemetry: not JSON", contract.KindTelemetry, `{"schemaVersion":`},
		{"telemetry: missing seq", contract.KindTelemetry, `{"schemaVersion":"1.0","deviceId":"pm-04","ts":"2026-10-12T04:30:00.000Z","metrics":{"p_kw":1.0}}`},
		{"telemetry: missing metrics", contract.KindTelemetry, `{"schemaVersion":"1.0","deviceId":"pm-04","ts":"2026-10-12T04:30:00.000Z","seq":1}`},
		{"telemetry: ts without milliseconds", contract.KindTelemetry, `{"schemaVersion":"1.0","deviceId":"pm-04","ts":"2026-10-12T04:30:00Z","seq":1,"metrics":{}}`},
		{"telemetry: ts with offset instead of Z", contract.KindTelemetry, `{"schemaVersion":"1.0","deviceId":"pm-04","ts":"2026-10-12T11:30:00.000+07:00","seq":1,"metrics":{}}`},
		{"telemetry: metric is a string", contract.KindTelemetry, `{"schemaVersion":"1.0","deviceId":"pm-04","ts":"2026-10-12T04:30:00.000Z","seq":1,"metrics":{"p_kw":"1050"}}`},
		{"telemetry: metric name not snake_case", contract.KindTelemetry, `{"schemaVersion":"1.0","deviceId":"pm-04","ts":"2026-10-12T04:30:00.000Z","seq":1,"metrics":{"P_kW":1050}}`},
		{"telemetry: nested metrics", contract.KindTelemetry, `{"schemaVersion":"1.0","deviceId":"pm-04","ts":"2026-10-12T04:30:00.000Z","seq":1,"metrics":{"p":{"a":1}}}`},
		{"telemetry: negative seq", contract.KindTelemetry, `{"schemaVersion":"1.0","deviceId":"pm-04","ts":"2026-10-12T04:30:00.000Z","seq":-1,"metrics":{}}`},
		{"telemetry: unknown top-level field", contract.KindTelemetry, `{"schemaVersion":"1.0","deviceId":"pm-04","ts":"2026-10-12T04:30:00.000Z","seq":1,"metrics":{},"extra":1}`},
		{"telemetry: state holds a number", contract.KindTelemetry, `{"schemaVersion":"1.0","deviceId":"pm-04","ts":"2026-10-12T04:30:00.000Z","seq":1,"metrics":{},"state":{"mode":1}}`},
		{"telemetry: deviceId uppercase", contract.KindTelemetry, `{"schemaVersion":"1.0","deviceId":"PM-04","ts":"2026-10-12T04:30:00.000Z","seq":1,"metrics":{}}`},

		{"status: online missing", contract.KindStatus, `{"schemaVersion":"1.0","ts":"2026-10-12T04:30:00.000Z"}`},
		{"status: online is a string", contract.KindStatus, `{"online":"true"}`},
		{"status: carries deviceId", contract.KindStatus, `{"online":true,"deviceId":"ems-pv-01"}`},

		{"alarm: unknown severity", contract.KindAlarm, `{"schemaVersion":"1.0","ts":"2026-10-12T04:30:00.000Z","deviceId":"ems-pv-01","code":"A-12","severity":"fatal","state":"raised","message":"x"}`},
		{"alarm: unknown state", contract.KindAlarm, `{"schemaVersion":"1.0","ts":"2026-10-12T04:30:00.000Z","deviceId":"ems-pv-01","code":"A-12","severity":"warning","state":"active","message":"x"}`},
		{"alarm: malformed code", contract.KindAlarm, `{"schemaVersion":"1.0","ts":"2026-10-12T04:30:00.000Z","deviceId":"ems-pv-01","code":"a12","severity":"warning","state":"raised","message":"x"}`},
		{"alarm: empty message", contract.KindAlarm, `{"schemaVersion":"1.0","ts":"2026-10-12T04:30:00.000Z","deviceId":"ems-pv-01","code":"A-12","severity":"warning","state":"raised","message":""}`},
		{"alarm: missing deviceId", contract.KindAlarm, `{"schemaVersion":"1.0","ts":"2026-10-12T04:30:00.000Z","code":"A-12","severity":"warning","state":"raised","message":"x"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := v.Validate(tc.kind, []byte(tc.raw))
			if err == nil {
				t.Fatal("payload was accepted but should have been rejected")
			}
			if reason := contract.Reason(err); reason == "" || strings.Contains(reason, "\n") {
				t.Errorf("Reason() must be a non-empty single line, got %q", reason)
			}
		})
	}
}

func TestTimeMarshalsInWireFormat(t *testing.T) {
	ts := contract.NewTime(time.Date(2026, 10, 12, 4, 30, 0, 123_456_789, time.UTC))
	got, err := json.Marshal(ts)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `"2026-10-12T04:30:00.123Z"`; string(got) != want {
		t.Fatalf("marshal = %s, want %s", got, want)
	}

	// A non-UTC instant must still be published as UTC.
	jakarta := time.FixedZone("WIB", 7*60*60)
	local := contract.NewTime(time.Date(2026, 10, 12, 11, 30, 0, 0, jakarta))
	got, err = json.Marshal(local)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `"2026-10-12T04:30:00.000Z"`; string(got) != want {
		t.Fatalf("marshal non-UTC = %s, want %s", got, want)
	}
}

func TestTimeRejectsOtherFormats(t *testing.T) {
	for _, raw := range []string{
		`"2026-10-12T04:30:00Z"`,
		`"2026-10-12T04:30:00.123456Z"`,
		`"2026-10-12T11:30:00.000+07:00"`,
		`"2026-10-12 04:30:00.000Z"`,
		`1760243400`,
	} {
		var ts contract.Time
		if err := json.Unmarshal([]byte(raw), &ts); err == nil {
			t.Errorf("ts %s was accepted but is not the contract format", raw)
		}
	}
}

func TestStateValueRoundTrip(t *testing.T) {
	var state map[string]contract.StateValue
	if err := json.Unmarshal([]byte(`{"closed":true,"mode":"AUTO"}`), &state); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := state["closed"]; !got.IsBool() || !got.Bool() || got.String() != "true" {
		t.Errorf("closed decoded as %#v / %q", got, got.String())
	}
	if got := state["mode"]; got.IsBool() || got.String() != "AUTO" {
		t.Errorf("mode decoded as %#v / %q", got, got.String())
	}

	out, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"closed":true,"mode":"AUTO"}`; string(out) != want {
		t.Errorf("round trip = %s, want %s", out, want)
	}

	var bad map[string]contract.StateValue
	if err := json.Unmarshal([]byte(`{"mode":1}`), &bad); err == nil {
		t.Error("a numeric state value should not decode")
	}
}

func TestParseTopic(t *testing.T) {
	ok := []struct {
		topic string
		want  contract.Topic
	}{
		{"rain/pts/ems/ems-pv-01/telemetry", contract.Topic{Class: contract.ClassEMS, DeviceID: "ems-pv-01", Kind: contract.KindTelemetry}},
		{"rain/pts/breaker/qf-5/status", contract.Topic{Class: contract.ClassBreaker, DeviceID: "qf-5", Kind: contract.KindStatus}},
		{"rain/pts/gen/stg-02/alarm", contract.Topic{Class: contract.ClassGen, DeviceID: "stg-02", Kind: contract.KindAlarm}},
	}
	for _, tc := range ok {
		got, err := contract.ParseTopic(tc.topic)
		if err != nil {
			t.Fatalf("ParseTopic(%q): %v", tc.topic, err)
		}
		if got != tc.want {
			t.Errorf("ParseTopic(%q) = %+v, want %+v", tc.topic, got, tc.want)
		}
		if got.String() != tc.topic {
			t.Errorf("round trip = %q, want %q", got.String(), tc.topic)
		}
	}

	bad := []string{
		"rain/pts/ems/ems-pv-01",
		"rain/pts/ems/ems-pv-01/telemetry/extra",
		"rain/pts/unknown/x/telemetry",
		"rain/pts/ems/x/command",
		"rain/pts/ems//telemetry",
		"other/pts/ems/ems-pv-01/telemetry",
		"rain/pts/cmd/ems-pv-01/set",
	}
	for _, topic := range bad {
		if _, err := contract.ParseTopic(topic); err == nil {
			t.Errorf("ParseTopic(%q) should have failed", topic)
		}
	}
}

func TestLWTTopicMatchesContract(t *testing.T) {
	parsed, err := contract.ParseTopic(contract.LWTTopic)
	if err != nil {
		t.Fatalf("LWT topic is not a valid contract topic: %v", err)
	}
	if parsed.DeviceID != contract.PublisherID || parsed.Kind != contract.KindStatus {
		t.Errorf("LWT topic = %+v, want the status topic of %s", parsed, contract.PublisherID)
	}
}

func loadDevices(t *testing.T) []contract.Device {
	t.Helper()
	devices, err := contract.LoadDevices()
	if err != nil {
		t.Fatalf("LoadDevices: %v", err)
	}
	return devices
}

func TestDeviceInventory(t *testing.T) {
	devices := loadDevices(t)

	// Rev P enumerates 34 devices. The PoC prompt says 35; the discrepancy is
	// tracked in docs/HANDOFF_OT.md and this test pins what we actually ship.
	if len(devices) != 34 {
		t.Fatalf("devices.json has %d devices, want 34", len(devices))
	}

	wantPerClass := map[contract.Class]int{
		contract.ClassEMS: 1, contract.ClassPV: 10, contract.ClassBESS: 1,
		contract.ClassWeather: 1, contract.ClassMeter: 6, contract.ClassBreaker: 10,
		contract.ClassGen: 5,
	}
	got := map[contract.Class]int{}
	byCode := contract.DeviceByCode(devices)
	for _, d := range devices {
		got[d.Class]++
	}
	for class, want := range wantPerClass {
		if got[class] != want {
			t.Errorf("class %s has %d devices, want %d", class, got[class], want)
		}
	}

	if _, ok := byCode[contract.PublisherID]; !ok {
		t.Errorf("the publishing device %q is missing from devices.json", contract.PublisherID)
	}

	for _, d := range devices {
		if d.Breaker == "" {
			continue
		}
		ref, ok := byCode[d.Breaker]
		if !ok {
			t.Errorf("device %q points at breaker %q, which is not a device", d.Code, d.Breaker)
			continue
		}
		if ref.Class != contract.ClassBreaker {
			t.Errorf("device %q points at %q, which is class %q and not a breaker", d.Code, d.Breaker, ref.Class)
		}
	}

	for _, d := range devices {
		if d.Section != "" && d.Section != "A" && d.Section != "B" {
			t.Errorf("device %q has section %q, want A or B", d.Code, d.Section)
		}
		if _, err := d.SourceJSON(); err != nil {
			t.Errorf("device %q has a source that will not serialise: %v", d.Code, err)
		}
		if _, err := contract.ParseTopic(d.Topic(contract.KindTelemetry).String()); err != nil {
			t.Errorf("device %q produces an invalid topic: %v", d.Code, err)
		}
	}
}
