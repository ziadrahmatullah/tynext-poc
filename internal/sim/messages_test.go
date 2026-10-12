package sim

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/tynext/tynext-poc/internal/contract"
)

// Every payload the simulator builds must satisfy the same schema the OT team
// is given. If this fails, the PoC would be proving the wrong format.
func TestEveryBuiltPayloadSatisfiesTheContract(t *testing.T) {
	devices, err := contract.LoadDevices()
	if err != nil {
		t.Fatalf("LoadDevices: %v", err)
	}
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	cfg := testPlantConfig()
	plant := NewPlant(cfg)
	builder := NewBuilder(devices, cfg.Seed)

	ts := noon(cfg.Location)
	for step := 0; step < 120; step++ {
		snap := plant.Step(ts.Add(time.Duration(step)*time.Second), time.Second)

		for _, d := range devices {
			tel := builder.Telemetry(d, snap)
			raw, err := json.Marshal(tel)
			if err != nil {
				t.Fatalf("%s: marshal: %v", d.Code, err)
			}
			if err := validator.Validate(contract.KindTelemetry, raw); err != nil {
				t.Fatalf("%s telemetry does not satisfy the schema: %v\n%s", d.Code, err, raw)
			}
			if tel.DeviceID != d.Code {
				t.Errorf("%s: payload deviceId = %q", d.Code, tel.DeviceID)
			}
		}

		if snap.Alarm != nil {
			raw, err := json.Marshal(builder.Alarm(*snap.Alarm, snap))
			if err != nil {
				t.Fatalf("marshal alarm: %v", err)
			}
			if err := validator.Validate(contract.KindAlarm, raw); err != nil {
				t.Fatalf("alarm does not satisfy the schema: %v\n%s", err, raw)
			}
		}
	}

	raw, err := json.Marshal(builder.Status(Snapshot{TS: ts}, true, "simulator-poc"))
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if err := validator.Validate(contract.KindStatus, raw); err != nil {
		t.Fatalf("status does not satisfy the schema: %v\n%s", err, raw)
	}
}

// seq has to rise per device, which is how a gap is spotted on the wire.
func TestSeqRisesPerDevice(t *testing.T) {
	devices, err := contract.LoadDevices()
	if err != nil {
		t.Fatal(err)
	}
	cfg := testPlantConfig()
	plant := NewPlant(cfg)
	builder := NewBuilder(devices, cfg.Seed)

	ts := noon(cfg.Location)
	last := map[string]int64{}
	for step := 1; step <= 20; step++ {
		snap := plant.Step(ts.Add(time.Duration(step)*time.Second), time.Second)
		for _, d := range devices {
			tel := builder.Telemetry(d, snap)
			if want := last[d.Code] + 1; tel.Seq != want {
				t.Fatalf("%s step %d: seq = %d, want %d", d.Code, step, tel.Seq, want)
			}
			last[d.Code] = tel.Seq
		}
	}
}

// Breaker and gen devices carry no power, by design: generator output is only
// metered per section.
func TestStateOnlyDevicesCarryNoMetrics(t *testing.T) {
	devices, err := contract.LoadDevices()
	if err != nil {
		t.Fatal(err)
	}
	cfg := testPlantConfig()
	plant := NewPlant(cfg)
	builder := NewBuilder(devices, cfg.Seed)
	snap := plant.Step(noon(cfg.Location), time.Second)

	for _, d := range devices {
		tel := builder.Telemetry(d, snap)
		switch d.Class {
		case contract.ClassBreaker:
			if len(tel.Metrics) != 0 {
				t.Errorf("%s has metrics %v, breakers publish state only", d.Code, tel.Metrics)
			}
			if _, ok := tel.State["closed"]; !ok {
				t.Errorf("%s has no closed state", d.Code)
			}
			if _, ok := tel.State["tripped"]; !ok {
				t.Errorf("%s has no tripped state", d.Code)
			}
		case contract.ClassGen:
			if len(tel.Metrics) != 0 {
				t.Errorf("%s has metrics %v; generator power is only metered per section", d.Code, tel.Metrics)
			}
			if _, ok := tel.State["on"]; !ok {
				t.Errorf("%s has no on state", d.Code)
			}
		default:
			if len(tel.Metrics) == 0 {
				t.Errorf("%s (class %s) published no metrics", d.Code, d.Class)
			}
		}
	}
}

// The tag names have to match the draft tag list, because that list is what the
// OT team maps their TwinCAT variables onto.
func TestTagNamesMatchTheDraftList(t *testing.T) {
	devices, err := contract.LoadDevices()
	if err != nil {
		t.Fatal(err)
	}
	cfg := testPlantConfig()
	plant := NewPlant(cfg)
	builder := NewBuilder(devices, cfg.Seed)
	snap := plant.Step(noon(cfg.Location), time.Second)
	byCode := contract.DeviceByCode(devices)

	want := map[string][]string{
		"ems-pv-01": {"p_pm02_kw", "p_pv_inv_kw", "p_pv_fact_kw", "p_lim_fact_kw", "pi_trim_kw",
			"eta_link", "p_pv_lim_kw", "p_chg_allow_kw", "p_load_kw", "p_gen_kw",
			"p_stg_adv_kw", "dp_kw", "q_ref_kvar"},
		"bess-01":   {"soc_pct", "p_chg_bms_kw", "p_kw", "f_hz"},
		"combox-01": {"p_pv_inv_kw", "p_pv_lim_readback_kw"},
		"pm-02":     {"p_kw"},
		"pm-04":     {"p_kw", "q_kvar", "v", "i_a", "f_hz"},
		"pv-inv-01": {"p_kw", "q_kvar"},
		"ws-01":     {"irradiance_wm2", "t_amb_c", "t_module_c"},
	}

	for code, tags := range want {
		d, ok := byCode[code]
		if !ok {
			t.Fatalf("%s is not in devices.json", code)
		}
		tel := builder.Telemetry(d, snap)
		if len(tel.Metrics) != len(tags) {
			t.Errorf("%s has %d metrics %v, want %d", code, len(tel.Metrics), keys(tel.Metrics), len(tags))
		}
		for _, tag := range tags {
			if _, ok := tel.Metrics[tag]; !ok {
				t.Errorf("%s is missing metric %q", code, tag)
			}
		}
	}

	wantState := map[string][]string{
		"ems-pv-01": {"mode", "override", "adv_rule", "pv_day", "q_mode"},
		"bess-01":   {"forming"},
		"pv-inv-01": {"status"},
		"qf-5":      {"closed", "tripped"},
		"stg-01":    {"on"},
	}
	for code, tags := range wantState {
		tel := builder.Telemetry(byCode[code], snap)
		for _, tag := range tags {
			if _, ok := tel.State[tag]; !ok {
				t.Errorf("%s is missing state %q", code, tag)
			}
		}
	}
}

func keys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
