// Package sim is the stand-in for the CX5240 while the PLC is not available.
// It speaks the exact wire format of contracts/, so on the test day the only
// thing that changes is where the messages come from.
//
// The plant model here exists to make the numbers plausible and internally
// consistent. It is NOT a reference implementation of the PLC control loop:
// the real P_PV,lim loop, the SOC envelope and the STG advisory live in
// TwinCAT and belong to the OT team.
package sim

import (
	"math"
	"math/rand"
	"time"
)

// PlantConfig holds the knobs the operator can turn from the command line.
type PlantConfig struct {
	// Generation. Power is only metered per section, so STG1 and the DGs land
	// on pm-03a (Section A) and STG2 on pm-03b (Section B).
	STG1KW float64
	STG2KW float64
	DGKW   float64

	// Load. Daytime is production; night is lighting and the boiler.
	// TBC: the night figure is a guess until PTS confirms it.
	LoadDayMinKW   float64
	LoadDayMaxKW   float64
	LoadNightMinKW float64
	LoadNightMaxKW float64

	// PV.
	PVPeakKW   float64
	PVLimMaxKW float64
	PVRampUpKW float64 // maximum rise per second; falls are immediate

	// BESS.
	CapacityKWh   float64
	InitialSOCPct float64
	ChargeMaxKW   float64
	SOCTaperStart float64
	SOCTaperEnd   float64

	// STG advisory.
	AdvisoryBandKW float64
	AdvisoryHold   time.Duration

	// Location decides what counts as daytime. The site is in Kalimantan.
	Location *time.Location

	Seed int64
}

// DefaultPlantConfig matches the figures in the PoC brief.
func DefaultPlantConfig() PlantConfig {
	loc, err := time.LoadLocation("Asia/Makassar")
	if err != nil {
		loc = time.FixedZone("WITA", 8*60*60)
	}
	return PlantConfig{
		STG1KW: 600, STG2KW: 200, DGKW: 0,
		LoadDayMinKW: 1000, LoadDayMaxKW: 1100,
		LoadNightMinKW: 500, LoadNightMaxKW: 700,
		PVPeakKW: 950, PVLimMaxKW: 990, PVRampUpKW: 50,
		CapacityKWh: 3130, InitialSOCPct: 55, ChargeMaxKW: 1000,
		SOCTaperStart: 85, SOCTaperEnd: 90,
		AdvisoryBandKW: 50, AdvisoryHold: 60 * time.Second,
		Location: loc,
		Seed:     time.Now().UnixNano(),
	}
}

// Override values published in ems-pv-01.state.override.
const (
	OverrideNone = "none"
	OverrideT02  = "T-02"
)

// Snapshot is one instant of the plant, from which every device payload is
// derived. Keeping it in one struct is what makes the balance invariants easy
// to state and easy to test.
type Snapshot struct {
	TS time.Time

	LoadKW  float64
	PM03AKW float64
	PM03BKW float64
	GenKW   float64

	PVAvailKW       float64
	PVLimKW         float64 // what the loop asks the Combox for
	PVLimFactKW     float64 // what is actually in force after ramp and clamp
	PVFactKW        float64
	PVInvKW         float64
	InverterKW      [9]float64
	PVLimReadbackKW float64

	BESSKW      float64 // + discharge, − charge
	SOCPct      float64
	ChargeAllow float64
	ChgBMSKW    float64

	PM02KW float64

	STGAdvKW float64
	DPKW     float64

	PITrimKW float64
	EtaLink  float64
	QRefKvar float64
	FreqHz   float64

	IrradianceWm2 float64
	TAmbC         float64
	TModuleC      float64

	Mode     string
	Override string
	AdvRule  string
	PVDay    string
	QMode    string

	BreakerClosed  map[string]bool
	BreakerTripped map[string]bool
	GenOn          map[string]bool

	// Alarm transition produced by this step, if any.
	Alarm *AdvisoryAlarm
}

// AdvisoryAlarm is the A-11/A-12 transition the advisory rule produced.
type AdvisoryAlarm struct {
	Code     string
	Severity string
	State    string
	Message  string
	Value    float64
}

// Plant is the simulated microgrid. It is not safe for concurrent use; the
// simulator steps it from a single loop.
type Plant struct {
	cfg PlantConfig
	rng *rand.Rand

	pvLim       float64
	socKWh      float64
	cloud       float64
	outOfBandAt time.Time
	openAlarm   string

	// forcePVLimZero and tripQF5 are set by --scenario comm-fault.
	forcePVLimZero bool
	tripQF5        bool
	override       string
}

// NewPlant builds a plant at the configured initial SOC.
func NewPlant(cfg PlantConfig) *Plant {
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	return &Plant{
		cfg:      cfg,
		rng:      rand.New(rand.NewSource(cfg.Seed)), //nolint:gosec // plant noise, not cryptography
		socKWh:   cfg.CapacityKWh * cfg.InitialSOCPct / 100,
		cloud:    0.95,
		override: OverrideNone,
	}
}

// SetCommFault puts the plant in the T-02 override: PV limit forced to zero and
// QF-5 tripped. It is what --scenario comm-fault switches on.
func (p *Plant) SetCommFault(on bool) {
	p.forcePVLimZero = on
	p.tripQF5 = on
	if on {
		p.override = OverrideT02
	} else {
		p.override = OverrideNone
	}
}

// SOCPct is the current state of charge.
func (p *Plant) SOCPct() float64 { return p.socKWh / p.cfg.CapacityKWh * 100 }

// SetSOCPct forces the state of charge, used by --scenario soc-full.
func (p *Plant) SetSOCPct(pct float64) { p.socKWh = p.cfg.CapacityKWh * pct / 100 }

// Step advances the plant by dt and returns the snapshot at ts.
//
// The balance is computed, never duplicated:
//
//	bess = load − gen − pv_fact      (+ = discharge)
//	pm02 = pv_fact + bess            (≡ load − gen)
//
// so both invariants hold by construction rather than by agreement.
func (p *Plant) Step(ts time.Time, dt time.Duration) Snapshot {
	seconds := dt.Seconds()
	if seconds <= 0 {
		seconds = 1
	}

	load := p.load(ts)
	pm03a := p.cfg.STG1KW + p.cfg.DGKW
	pm03b := p.cfg.STG2KW
	gen := pm03a + pm03b

	avail := p.pvAvailable(ts)

	// Charge envelope: full rate below the taper, linearly down to zero
	// between SOCTaperStart and SOCTaperEnd.
	soc := p.SOCPct()
	chargeAllow := p.chargeAllowance(soc)

	// Feed-forward: PV may cover the net load plus whatever the battery is
	// still allowed to absorb.
	target := (load - gen) + chargeAllow
	target = clamp(target, 0, p.cfg.PVLimMaxKW)
	if p.forcePVLimZero {
		target = 0
	}

	// Falls take effect immediately; rises are rate limited.
	maxRise := p.cfg.PVRampUpKW * seconds
	switch {
	case target < p.pvLim:
		p.pvLim = target
	case target-p.pvLim > maxRise:
		p.pvLim += maxRise
	default:
		p.pvLim = target
	}

	pvFact := math.Min(avail, p.pvLim)
	bess := load - gen - pvFact
	pm02 := pvFact + bess

	stgAdv := clamp(load-pvFact, 0, p.cfg.STG1KW+p.cfg.STG2KW+500)
	dp := gen - stgAdv

	snap := Snapshot{
		TS:              ts,
		LoadKW:          round2(load),
		PM03AKW:         round2(pm03a),
		PM03BKW:         round2(pm03b),
		GenKW:           round2(gen),
		PVAvailKW:       round2(avail),
		PVLimKW:         round2(target),
		PVLimFactKW:     round2(p.pvLim),
		PVFactKW:        round2(pvFact),
		PVLimReadbackKW: round2(p.pvLim),
		BESSKW:          round2(bess),
		SOCPct:          round2(soc),
		ChargeAllow:     round2(chargeAllow),
		ChgBMSKW:        round2(p.chargeBMSLimit(soc)),
		PM02KW:          round2(pm02),
		STGAdvKW:        round2(stgAdv),
		DPKW:            round2(dp),
		PITrimKW:        round2(p.rng.NormFloat64() * 5),
		EtaLink:         0.98,
		QRefKvar:        0,
		FreqHz:          round2(50 + p.rng.NormFloat64()*0.02),
		Mode:            "AUTO",
		Override:        p.override,
		AdvRule:         advisoryRule(ts, p.cfg.Location, soc),
		PVDay:           pvDay(avail, p.cfg.PVPeakKW),
		QMode:           "PF1",
		Alarm:           p.advisory(ts, dp),
	}

	snap.InverterKW = p.inverters(pvFact)
	for _, v := range snap.InverterKW {
		snap.PVInvKW += v
	}
	snap.PVInvKW = round2(snap.PVInvKW)

	irr := 0.0
	if p.cfg.PVPeakKW > 0 {
		irr = avail / p.cfg.PVPeakKW * 1000
	}
	snap.IrradianceWm2 = round2(irr)
	snap.TAmbC = round2(26 + 7*irr/1000 + p.rng.NormFloat64()*0.3)
	snap.TModuleC = round2(snap.TAmbC + 18*irr/1000)

	snap.BreakerClosed, snap.BreakerTripped = p.breakers()
	snap.GenOn = p.gens()

	// Integrate last: the snapshot describes the instant ts, so soc_pct and
	// p_chg_allow_kw in it are taken at the same moment. Positive BESS power is
	// discharge, so it lowers the state of charge.
	p.socKWh = clamp(p.socKWh-bess*seconds/3600, 0, p.cfg.CapacityKWh)

	return snap
}

func (p *Plant) chargeAllowance(socPct float64) float64 {
	switch {
	case socPct <= p.cfg.SOCTaperStart:
		return p.cfg.ChargeMaxKW
	case socPct >= p.cfg.SOCTaperEnd:
		return 0
	default:
		span := p.cfg.SOCTaperEnd - p.cfg.SOCTaperStart
		return p.cfg.ChargeMaxKW * (p.cfg.SOCTaperEnd - socPct) / span
	}
}

// chargeBMSLimit is what the BMS reports it can take, which is wider than the
// policy limit until the pack is nearly full.
func (p *Plant) chargeBMSLimit(socPct float64) float64 {
	if socPct >= p.cfg.SOCTaperEnd {
		return 0
	}
	return clamp(1500*(p.cfg.SOCTaperEnd-socPct)/p.cfg.SOCTaperEnd*1.2, 0, 1500)
}

// advisory implements the hold-then-alarm rule: the band has to be exceeded
// continuously before anything is published, so a passing cloud does not raise
// an alarm.
func (p *Plant) advisory(ts time.Time, dp float64) *AdvisoryAlarm {
	inBand := math.Abs(dp) <= p.cfg.AdvisoryBandKW

	if inBand {
		p.outOfBandAt = time.Time{}
		if p.openAlarm != "" {
			code := p.openAlarm
			p.openAlarm = ""
			return &AdvisoryAlarm{
				Code: code, Severity: "warning", State: "cleared",
				Message: "Advisory STG kembali di dalam band +/-50 kW",
				Value:   round2(dp),
			}
		}
		return nil
	}

	if p.outOfBandAt.IsZero() {
		p.outOfBandAt = ts
		return nil
	}
	if ts.Sub(p.outOfBandAt) < p.cfg.AdvisoryHold {
		return nil
	}

	code, action := "A-11", "turunkan"
	if dp < 0 {
		code, action = "A-12", "naikkan"
	}
	if p.openAlarm == code {
		return nil
	}
	// The deviation flipped sign. Close the old alarm first; the new direction
	// has to serve its own hold before it is allowed to raise.
	if p.openAlarm != "" {
		old := p.openAlarm
		p.openAlarm = ""
		p.outOfBandAt = ts
		return &AdvisoryAlarm{
			Code: old, Severity: "warning", State: "cleared",
			Message: "Advisory STG: arah simpangan berbalik",
			Value:   round2(dp),
		}
	}
	p.openAlarm = code
	return &AdvisoryAlarm{
		Code: code, Severity: "warning", State: "raised",
		Message: "Advisory STG: " + action + " beban STG, dp di luar band selama 60 s",
		Value:   round2(dp),
	}
}

// load follows the production day; outside 06:00–18:00 local it drops to the
// night band.
func (p *Plant) load(ts time.Time) float64 {
	h := localHour(ts, p.cfg.Location)
	lo, hi := p.cfg.LoadNightMinKW, p.cfg.LoadNightMaxKW
	if h >= 6 && h < 18 {
		lo, hi = p.cfg.LoadDayMinKW, p.cfg.LoadDayMaxKW
	}
	mid := (lo + hi) / 2
	span := (hi - lo) / 2
	// A slow sine plus a little noise, so consecutive samples look related.
	wave := math.Sin(float64(ts.Unix()%600) / 600 * 2 * math.Pi)
	return clamp(mid+span*wave*0.8+p.rng.NormFloat64()*4, lo-20, hi+20)
}

// pvAvailable is a bell curve across 06:00–18:00 with a slowly drifting cloud
// factor on top.
func (p *Plant) pvAvailable(ts time.Time) float64 {
	h := localHour(ts, p.cfg.Location)
	if h < 6 || h >= 18 {
		return 0
	}
	bell := math.Sin(math.Pi * (h - 6) / 12)

	p.cloud += p.rng.NormFloat64() * 0.01
	p.cloud = clamp(p.cloud, 0.55, 1.0)

	return clamp(p.cfg.PVPeakKW*bell*p.cloud, 0, p.cfg.PVPeakKW)
}

// inverters splits the array output across nine units with a little spread.
func (p *Plant) inverters(total float64) [9]float64 {
	var out [9]float64
	share := total / 9
	var sum float64
	for i := 0; i < 8; i++ {
		v := clamp(share*(1+p.rng.NormFloat64()*0.01), 0, 115)
		out[i] = round2(v)
		sum += out[i]
	}
	// The last unit absorbs the rounding so the nine still add up to the array.
	out[8] = round2(clamp(total-sum, 0, 115))
	return out
}

func (p *Plant) breakers() (closed, tripped map[string]bool) {
	closed = map[string]bool{}
	tripped = map[string]bool{}
	for _, code := range []string{"qf-1", "qf-2", "qf-3", "qf-4", "qf-5", "qf-6", "qf-g2", "qf-t2", "qf-l", "qf-p1"} {
		closed[code] = true
		tripped[code] = false
	}
	// DG breakers follow the DGs, which are off in the default scenario.
	for _, code := range []string{"qf-1", "qf-3", "qf-g2"} {
		closed[code] = p.cfg.DGKW > 0
	}
	if p.tripQF5 {
		closed["qf-5"] = false
		tripped["qf-5"] = true
	}
	return closed, tripped
}

func (p *Plant) gens() map[string]bool {
	on := map[string]bool{
		"stg-01": p.cfg.STG1KW > 0,
		"stg-02": p.cfg.STG2KW > 0,
		"dg-01":  p.cfg.DGKW > 0,
		"dg-02":  false,
		"dg-03":  false,
	}
	return on
}

func advisoryRule(ts time.Time, loc *time.Location, socPct float64) string {
	day := "NIGHT"
	if h := localHour(ts, loc); h >= 6 && h < 18 {
		day = "DAY"
	}
	switch {
	case socPct >= 85:
		return day + "-SOC-HIGH"
	case socPct <= 25:
		return day + "-SOC-LOW"
	default:
		return day + "-SOC-MID"
	}
}

func pvDay(avail, peak float64) string {
	if peak <= 0 {
		return "NONE"
	}
	switch ratio := avail / peak; {
	case ratio <= 0.05:
		return "NONE"
	case ratio < 0.4:
		return "LOW"
	case ratio < 0.75:
		return "MID"
	default:
		return "HIGH"
	}
}

func localHour(ts time.Time, loc *time.Location) float64 {
	if loc == nil {
		loc = time.UTC
	}
	t := ts.In(loc)
	return float64(t.Hour()) + float64(t.Minute())/60 + float64(t.Second())/3600
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
