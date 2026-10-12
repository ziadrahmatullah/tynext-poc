package sim

import (
	"fmt"
	"math"
	"math/rand"
	"strconv"
	"strings"

	"github.com/tynext/tynext-poc/internal/contract"
)

// nominalVoltage is the 400 V bus the whole microgrid sits on.
const nominalVoltage = 400.0

// Builder turns a plant Snapshot into one telemetry payload per device. The
// mapping here is the draft tag list in contracts/tags/ems-pv-tags.md; when the
// OT team sends the final list, this is the file that changes.
type Builder struct {
	devices []contract.Device
	seq     map[string]int64
	rng     *rand.Rand
}

// NewBuilder prepares a builder for the given inventory.
func NewBuilder(devices []contract.Device, seed int64) *Builder {
	return &Builder{
		devices: devices,
		seq:     make(map[string]int64, len(devices)),
		rng:     rand.New(rand.NewSource(seed)), //nolint:gosec // measurement noise, not cryptography
	}
}

// Devices returns the inventory this builder publishes for.
func (b *Builder) Devices() []contract.Device { return b.devices }

// Telemetry builds the payload for one device at this instant.
func (b *Builder) Telemetry(d contract.Device, snap Snapshot) contract.Telemetry {
	b.seq[d.Code]++
	metrics, state := b.tags(d, snap)
	return contract.Telemetry{
		SchemaVersion: contract.SchemaVersion,
		DeviceID:      d.Code,
		TS:            contract.NewTime(snap.TS),
		Seq:           b.seq[d.Code],
		Metrics:       metrics,
		State:         state,
	}
}

// Seq reports how many telemetry messages have been built for a device.
func (b *Builder) Seq(code string) int64 { return b.seq[code] }

//nolint:gocyclo // one case per device family; splitting it would hide the map
func (b *Builder) tags(d contract.Device, s Snapshot) (metrics map[string]float64, state map[string]contract.StateValue) {
	switch {
	case d.Code == contract.PublisherID:
		return map[string]float64{
				"p_pm02_kw":      s.PM02KW,
				"p_pv_inv_kw":    s.PVInvKW,
				"p_pv_fact_kw":   s.PVFactKW,
				"p_lim_fact_kw":  s.PVLimFactKW,
				"pi_trim_kw":     s.PITrimKW,
				"eta_link":       s.EtaLink,
				"p_pv_lim_kw":    s.PVLimKW,
				"p_chg_allow_kw": s.ChargeAllow,
				"p_load_kw":      s.LoadKW,
				"p_gen_kw":       s.GenKW,
				"p_stg_adv_kw":   s.STGAdvKW,
				"dp_kw":          s.DPKW,
				"q_ref_kvar":     s.QRefKvar,
			}, map[string]contract.StateValue{
				"mode":     contract.StateString(s.Mode),
				"override": contract.StateString(s.Override),
				"adv_rule": contract.StateString(s.AdvRule),
				"pv_day":   contract.StateString(s.PVDay),
				"q_mode":   contract.StateString(s.QMode),
			}

	case d.Code == "bess-01":
		return map[string]float64{
				"soc_pct":      s.SOCPct,
				"p_chg_bms_kw": s.ChgBMSKW,
				"p_kw":         s.BESSKW,
				"f_hz":         s.FreqHz,
			}, map[string]contract.StateValue{
				"forming": contract.StateBool(true),
			}

	case d.Code == "combox-01":
		return map[string]float64{
			"p_pv_inv_kw":          s.PVInvKW,
			"p_pv_lim_readback_kw": s.PVLimReadbackKW,
		}, nil

	case strings.HasPrefix(d.Code, "pv-inv-"):
		idx := inverterIndex(d.Code)
		p := 0.0
		if idx >= 0 && idx < len(s.InverterKW) {
			p = s.InverterKW[idx]
		}
		status := "STANDBY"
		if p > 1 {
			status = "RUN"
		}
		return map[string]float64{
				"p_kw":   p,
				"q_kvar": 0,
			}, map[string]contract.StateValue{
				"status": contract.StateString(status),
			}

	case d.Code == "ws-01":
		return map[string]float64{
			"irradiance_wm2": s.IrradianceWm2,
			"t_amb_c":        s.TAmbC,
			"t_module_c":     s.TModuleC,
		}, nil

	// pm-02 is read through the MG-500-B and only exposes active power.
	case d.Code == "pm-02":
		return map[string]float64{"p_kw": s.PM02KW}, nil

	case d.Class == contract.ClassMeter:
		return b.meter(meterPower(d.Code, s), s.FreqHz), nil

	case d.Class == contract.ClassBreaker:
		return map[string]float64{}, map[string]contract.StateValue{
			"closed":  contract.StateBool(s.BreakerClosed[d.Code]),
			"tripped": contract.StateBool(s.BreakerTripped[d.Code]),
		}

	case d.Class == contract.ClassGen:
		return map[string]float64{}, map[string]contract.StateValue{
			"on": contract.StateBool(s.GenOn[d.Code]),
		}

	default:
		return map[string]float64{}, nil
	}
}

// meterPower picks which plant quantity each meter sees.
func meterPower(code string, s Snapshot) float64 {
	switch code {
	case "pm-01": // Combox meter #1, on the PV side
		return s.PVFactKW
	case "pm-02b": // second meter on the same point as pm-02
		return s.PM02KW
	case "pm-03a": // Section A: STG-1 and the DGs
		return s.PM03AKW
	case "pm-03b": // Section B: STG-2
		return s.PM03BKW
	case "pm-04": // factory load
		return s.LoadKW
	default:
		return 0
	}
}

// meter derives the electrical trio from active power at the 400 V bus.
func (b *Builder) meter(p, freq float64) map[string]float64 {
	pf := 0.97
	q := p * math.Tan(math.Acos(pf))
	v := nominalVoltage + b.rng.NormFloat64()*1.2
	current := 0.0
	if v > 0 {
		current = math.Abs(p) * 1000 / (math.Sqrt(3) * v * pf)
	}
	return map[string]float64{
		"p_kw":   round2(p),
		"q_kvar": round2(q),
		"v":      round2(v),
		"i_a":    round2(current),
		"f_hz":   freq,
	}
}

// Status is the retained payload published on connect and on a clean exit.
// Only ems-pv-01 publishes status, because it is the only physical client; the
// liveness of the other devices comes from the stale detector in ingest.
func (b *Builder) Status(snap Snapshot, online bool, fw string) contract.Status {
	ts := contract.NewTime(snap.TS)
	return contract.Status{
		SchemaVersion: contract.SchemaVersion,
		TS:            &ts,
		Online:        online,
		FW:            fw,
	}
}

// Alarm wraps an advisory transition in the contract payload.
func (b *Builder) Alarm(a AdvisoryAlarm, snap Snapshot) contract.Alarm {
	value := a.Value
	return contract.Alarm{
		SchemaVersion: contract.SchemaVersion,
		TS:            contract.NewTime(snap.TS),
		DeviceID:      contract.PublisherID,
		Code:          a.Code,
		Severity:      a.Severity,
		State:         a.State,
		Message:       a.Message,
		Value:         &value,
	}
}

func inverterIndex(code string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(code, "pv-inv-"))
	if err != nil {
		return -1
	}
	return n - 1
}

// String renders a snapshot for the periodic progress log.
func (s Snapshot) String() string {
	return fmt.Sprintf(
		"load=%.1f gen=%.1f pv_avail=%.1f pv_lim=%.1f pv_fact=%.1f bess=%.1f pm02=%.1f soc=%.1f%% dp=%.1f",
		s.LoadKW, s.GenKW, s.PVAvailKW, s.PVLimFactKW, s.PVFactKW, s.BESSKW, s.PM02KW, s.SOCPct, s.DPKW)
}
