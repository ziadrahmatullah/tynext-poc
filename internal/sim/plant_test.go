package sim

import (
	"math"
	"testing"
	"time"
)

func testPlantConfig() PlantConfig {
	cfg := DefaultPlantConfig()
	cfg.Seed = 42
	return cfg
}

func noon(loc *time.Location) time.Time {
	return time.Date(2026, 10, 12, 12, 0, 0, 0, loc)
}

func midnight(loc *time.Location) time.Time {
	return time.Date(2026, 10, 12, 0, 0, 0, 0, loc)
}

// The two balance equations from the brief have to hold at every step, in every
// condition. They are what makes the simulated numbers usable as a reference
// while reading the dashboard.
func TestPowerBalanceInvariants(t *testing.T) {
	const tolerance = 1.0 // kW

	cases := []struct {
		name  string
		start time.Time
		tune  func(*PlantConfig)
	}{
		{"midday", noon(testPlantConfig().Location), nil},
		{"night", midnight(testPlantConfig().Location), nil},
		{"no generation", noon(testPlantConfig().Location), func(c *PlantConfig) { c.STG1KW, c.STG2KW = 0, 0 }},
		{"generation above load", noon(testPlantConfig().Location), func(c *PlantConfig) { c.STG1KW, c.STG2KW = 900, 600 }},
		{"battery nearly full", noon(testPlantConfig().Location), func(c *PlantConfig) { c.InitialSOCPct = 89.8 }},
		{"battery empty", noon(testPlantConfig().Location), func(c *PlantConfig) { c.InitialSOCPct = 2 }},
		{"no PV", noon(testPlantConfig().Location), func(c *PlantConfig) { c.PVPeakKW = 0 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testPlantConfig()
			if tc.tune != nil {
				tc.tune(&cfg)
			}
			p := NewPlant(cfg)

			ts := tc.start
			for i := 0; i < 1800; i++ {
				ts = ts.Add(time.Second)
				s := p.Step(ts, time.Second)

				if got := math.Abs(s.PM02KW - (s.PVFactKW + s.BESSKW)); got > tolerance {
					t.Fatalf("step %d: p_pm02 (%.2f) != p_pv_fact (%.2f) + p_bess (%.2f), selisih %.3f kW",
						i, s.PM02KW, s.PVFactKW, s.BESSKW, got)
				}
				if got := math.Abs(s.LoadKW - (s.GenKW + s.PM02KW)); got > tolerance {
					t.Fatalf("step %d: p_load (%.2f) != p_gen (%.2f) + p_pm02 (%.2f), selisih %.3f kW",
						i, s.LoadKW, s.GenKW, s.PM02KW, got)
				}
				if got := math.Abs(s.GenKW - (s.PM03AKW + s.PM03BKW)); got > tolerance {
					t.Fatalf("step %d: p_gen (%.2f) != pm-03a (%.2f) + pm-03b (%.2f)",
						i, s.GenKW, s.PM03AKW, s.PM03BKW)
				}
			}
		})
	}
}

func TestPVLimitStaysInRangeAndRampsUpSlowly(t *testing.T) {
	cfg := testPlantConfig()
	p := NewPlant(cfg)

	ts := midnight(cfg.Location)
	prev := 0.0
	for i := 0; i < 24*3600; i += 10 {
		at := ts.Add(time.Duration(i) * time.Second)
		s := p.Step(at, 10*time.Second)

		if s.PVLimFactKW < 0 || s.PVLimFactKW > cfg.PVLimMaxKW {
			t.Fatalf("p_lim_fact %.2f di luar 0..%.0f kW", s.PVLimFactKW, cfg.PVLimMaxKW)
		}
		if rise := s.PVLimFactKW - prev; rise > cfg.PVRampUpKW*10+0.01 {
			t.Fatalf("limit naik %.2f kW dalam 10 s, maksimum %.0f", rise, cfg.PVRampUpKW*10)
		}
		if s.PVFactKW > s.PVAvailKW+0.01 || s.PVFactKW > s.PVLimFactKW+0.01 {
			t.Fatalf("p_pv_fact %.2f melebihi avail %.2f atau limit %.2f", s.PVFactKW, s.PVAvailKW, s.PVLimFactKW)
		}
		prev = s.PVLimFactKW
	}
}

// At night there is no sun, so PV must be exactly zero rather than merely small.
func TestNoPVAtNight(t *testing.T) {
	cfg := testPlantConfig()
	p := NewPlant(cfg)

	ts := time.Date(2026, 10, 12, 22, 0, 0, 0, cfg.Location)
	for i := 0; i < 600; i++ {
		s := p.Step(ts.Add(time.Duration(i)*time.Second), time.Second)
		if s.PVAvailKW != 0 || s.PVFactKW != 0 {
			t.Fatalf("pukul 22:00 avail=%.2f fact=%.2f, keduanya harus 0", s.PVAvailKW, s.PVFactKW)
		}
		if s.IrradianceWm2 != 0 {
			t.Fatalf("irradiance %.2f pada malam hari", s.IrradianceWm2)
		}
	}
}

// The taper is the whole point of the SOC envelope: charging has to stop by the
// time the pack reaches 90 %.
func TestChargeTaperClosesAtNinetyPercent(t *testing.T) {
	cfg := testPlantConfig()
	p := NewPlant(cfg)

	cases := []struct {
		soc  float64
		want float64
	}{
		{50, cfg.ChargeMaxKW},
		{85, cfg.ChargeMaxKW},
		{87.5, cfg.ChargeMaxKW / 2},
		{90, 0},
		{95, 0},
	}
	for _, tc := range cases {
		if got := p.chargeAllowance(tc.soc); math.Abs(got-tc.want) > 0.01 {
			t.Errorf("SOC %.1f%%: izin charge %.2f kW, ingin %.2f", tc.soc, got, tc.want)
		}
	}
}

func TestSOCStaysWithinCapacity(t *testing.T) {
	cfg := testPlantConfig()
	cfg.InitialSOCPct = 89.9
	p := NewPlant(cfg)

	ts := noon(cfg.Location)
	for i := 0; i < 6*3600; i += 5 {
		s := p.Step(ts.Add(time.Duration(i)*time.Second), 5*time.Second)
		if s.SOCPct < 0 || s.SOCPct > 100 {
			t.Fatalf("SOC %.2f%% di luar 0..100", s.SOCPct)
		}
		if s.ChargeAllow < 0 {
			t.Fatalf("izin charge negatif: %.2f kW", s.ChargeAllow)
		}
		// soc_pct is published rounded to two decimals, so allow the one
		// kilowatt of slack that 0.005 % of taper band is worth.
		const roundingSlackKW = 1.1
		if want := p.chargeAllowance(s.SOCPct); s.ChargeAllow > want+roundingSlackKW {
			t.Fatalf("SOC %.2f%%: izin charge %.2f kW melebihi taper (%.2f kW)", s.SOCPct, s.ChargeAllow, want)
		}
	}
}

// The hold is what keeps a passing cloud from raising an alarm.
func TestAdvisoryAlarmNeedsTheFullHold(t *testing.T) {
	cfg := testPlantConfig()
	cfg.STG1KW, cfg.STG2KW = 900, 400 // well above what the advisory asks for
	p := NewPlant(cfg)

	ts := noon(cfg.Location)
	var raised *AdvisoryAlarm
	var raisedAt time.Time

	for i := 0; i < 200; i++ {
		at := ts.Add(time.Duration(i) * time.Second)
		s := p.Step(at, time.Second)
		if s.Alarm != nil && s.Alarm.State == "raised" {
			raised = s.Alarm
			raisedAt = at
			break
		}
	}

	if raised == nil {
		t.Fatal("tidak ada alarm advisory padahal dp jauh di luar band")
	}
	if raised.Code != "A-11" {
		t.Errorf("code = %q, ingin A-11 karena dp > 0 (gen di atas advisory)", raised.Code)
	}
	if held := raisedAt.Sub(ts); held < cfg.AdvisoryHold {
		t.Errorf("alarm terbit setelah %s, seharusnya menunggu %s", held, cfg.AdvisoryHold)
	}

	// The operator follows the advisory, which brings dp back into the band.
	var cleared *AdvisoryAlarm
	for i := 0; i < 300; i++ {
		s := p.Step(raisedAt.Add(time.Duration(i+1)*time.Second), time.Second)
		p.cfg.STG1KW, p.cfg.STG2KW = s.STGAdvKW, 0
		if s.Alarm != nil && s.Alarm.State == "cleared" {
			cleared = s.Alarm
			break
		}
	}
	if cleared == nil {
		t.Fatal("alarm tidak pernah dibersihkan setelah dp kembali ke band")
	}
	if cleared.Code != raised.Code {
		t.Errorf("cleared code %q != raised code %q", cleared.Code, raised.Code)
	}
}

// A single alarm must not be republished on every tick.
func TestAdvisoryAlarmRaisesOnce(t *testing.T) {
	cfg := testPlantConfig()
	cfg.STG1KW, cfg.STG2KW = 900, 400
	p := NewPlant(cfg)

	ts := noon(cfg.Location)
	raises := 0
	for i := 0; i < 600; i++ {
		s := p.Step(ts.Add(time.Duration(i)*time.Second), time.Second)
		if s.Alarm != nil && s.Alarm.State == "raised" {
			raises++
		}
	}
	if raises != 1 {
		t.Errorf("alarm raised %d kali dalam 10 menit, ingin 1", raises)
	}
}

// When the deviation flips sign the open alarm has to be closed before the
// other code is raised, or it would stay open in the database forever.
func TestAdvisoryClosesTheOldAlarmWhenTheSignFlips(t *testing.T) {
	cfg := testPlantConfig()
	cfg.STG1KW, cfg.STG2KW = 900, 400
	p := NewPlant(cfg)

	ts := noon(cfg.Location)
	at := ts
	for i := 0; i < 200; i++ {
		at = ts.Add(time.Duration(i) * time.Second)
		if s := p.Step(at, time.Second); s.Alarm != nil && s.Alarm.State == "raised" {
			if s.Alarm.Code != "A-11" {
				t.Fatalf("alarm pertama = %q, ingin A-11", s.Alarm.Code)
			}
			break
		}
	}
	if p.openAlarm != "A-11" {
		t.Fatalf("setup: openAlarm = %q, ingin A-11", p.openAlarm)
	}

	// Push generation far below the advisory so dp flips to negative.
	p.cfg.STG1KW, p.cfg.STG2KW = 0, 0

	var sawClear, sawRaise *AdvisoryAlarm
	for i := 0; i < 400; i++ {
		s := p.Step(at.Add(time.Duration(i+1)*time.Second), time.Second)
		if s.Alarm == nil {
			continue
		}
		switch {
		case sawClear == nil && s.Alarm.State == "cleared":
			sawClear = s.Alarm
		case sawClear != nil && s.Alarm.State == "raised":
			sawRaise = s.Alarm
		}
		if sawRaise != nil {
			break
		}
	}

	if sawClear == nil || sawClear.Code != "A-11" {
		t.Fatalf("A-11 tidak ditutup saat arah berbalik: %+v", sawClear)
	}
	if sawRaise == nil || sawRaise.Code != "A-12" {
		t.Fatalf("A-12 tidak terbit setelah A-11 ditutup: %+v", sawRaise)
	}
}

func TestCommFaultForcesPVToZero(t *testing.T) {
	cfg := testPlantConfig()
	p := NewPlant(cfg)
	p.SetCommFault(true)

	ts := noon(cfg.Location)
	for i := 0; i < 120; i++ {
		s := p.Step(ts.Add(time.Duration(i)*time.Second), time.Second)
		if s.PVLimKW != 0 {
			t.Fatalf("p_pv_lim %.2f, ingin 0 saat override T-02", s.PVLimKW)
		}
		if s.Override != OverrideT02 {
			t.Fatalf("override = %q, ingin T-02", s.Override)
		}
		if !s.BreakerTripped["qf-5"] || s.BreakerClosed["qf-5"] {
			t.Fatalf("qf-5 harus tripped dan terbuka saat comm fault")
		}
		// The limit falls immediately; PV must follow within one ramp step.
		if i > 1 && s.PVFactKW != 0 {
			t.Fatalf("p_pv_fact %.2f pada detik %d, ingin 0", s.PVFactKW, i)
		}
	}
}

func TestInvertersSumToTheArray(t *testing.T) {
	cfg := testPlantConfig()
	b := NewBuilder(nil, cfg.Seed)
	for _, total := range []float64{0, 100, 500, 950} {
		inv := b.rng // keep the generator warm across cases
		_ = inv
		p := NewPlant(cfg)
		got := p.inverters(total)
		var sum float64
		for _, v := range got {
			if v < 0 {
				t.Errorf("inverter negatif: %v", got)
			}
			sum += v
		}
		if math.Abs(sum-total) > 1 {
			t.Errorf("total inverter %.2f, ingin %.2f (±1 kW)", sum, total)
		}
	}
}
