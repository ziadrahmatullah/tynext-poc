package sim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/rs/zerolog"

	"github.com/tynext/tynext-poc/internal/contract"
	"github.com/tynext/tynext-poc/internal/mqttx"
)

// Scenario names accepted by --scenario.
const (
	ScenarioNone        = ""
	ScenarioSOCFull     = "soc-full"
	ScenarioSTGAdvisory = "stg-advisory"
	ScenarioCommFault   = "comm-fault"
)

var errOffline = errors.New("tidak tersambung ke broker")

// Options configures one simulator run.
type Options struct {
	MQTT  mqttx.Config
	Plant PlantConfig

	Interval time.Duration
	Duration time.Duration
	FW       string

	InvalidRate float64
	DupRate     float64
	Outage      time.Duration
	OutageAfter time.Duration
	AlarmDemo   bool
	Scenario    string

	CountFile string
	QueuePath string
	BufferMax time.Duration
}

// Runner is the simulator: one MQTT connection publishing for every device.
type Runner struct {
	opts    Options
	log     zerolog.Logger
	builder *Builder
	plant   *Plant
	queue   *Queue
	counts  *Counts
	rng     *rand.Rand

	client    mqtt.Client
	connected atomic.Bool
	inOutage  atomic.Bool
}

// New builds a runner for the embedded device inventory.
func New(opts Options, log zerolog.Logger) (*Runner, error) {
	devices, err := contract.LoadDevices()
	if err != nil {
		return nil, err
	}
	if opts.Interval <= 0 {
		opts.Interval = time.Second
	}
	if opts.QueuePath == "" {
		opts.QueuePath = "var/simulator.queue"
	}

	plantCfg := opts.Plant
	plant := NewPlant(plantCfg)
	applyScenario(plant, &plantCfg, opts.Scenario)
	plant.cfg = plantCfg
	plant.SetSOCPct(plantCfg.InitialSOCPct)

	queue, err := OpenQueue(opts.QueuePath, opts.BufferMax)
	if err != nil {
		return nil, err
	}

	return &Runner{
		opts:    opts,
		log:     log,
		builder: NewBuilder(devices, plantCfg.Seed),
		plant:   plant,
		queue:   queue,
		counts:  NewCounts(time.Now(), opts.Interval, opts.MQTT.Username, opts.Scenario),
		rng:     rand.New(rand.NewSource(plantCfg.Seed ^ 0x5eed)), //nolint:gosec // test injection, not cryptography
	}, nil
}

// applyScenario adjusts the plant so the situation under test actually occurs.
func applyScenario(plant *Plant, cfg *PlantConfig, scenario string) {
	switch scenario {
	case ScenarioSOCFull:
		// Start inside the taper band so p_chg_allow_kw and p_pv_lim_kw are
		// visibly held down. The approach to 90 % is asymptotic by design, so
		// what you watch is the limit falling, not the SOC hitting a wall.
		cfg.InitialSOCPct = 86
	case ScenarioSTGAdvisory:
		// Run the turbines well above what the advisory asks for, so |dp| stays
		// outside the 50 kW band and A-11 fires after the 60 s hold.
		cfg.STG1KW = 900
		cfg.STG2KW = 400
	case ScenarioCommFault:
		plant.SetCommFault(true)
	}
}

// Close releases the buffer.
func (r *Runner) Close() error { return r.queue.Close() }

// Counts exposes the tally, for tests.
func (r *Runner) Counts() *Counts { return r.counts }

// Run publishes until the duration elapses or ctx is cancelled.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.connect(); err != nil {
		return err
	}

	started := time.Now()
	if r.opts.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.opts.Duration)
		defer cancel()
	}

	ticker := time.NewTicker(r.opts.Interval)
	defer ticker.Stop()

	progress := time.NewTicker(30 * time.Second)
	defer progress.Stop()

	var last Snapshot
	alarmDemoAt := started.Add(5 * time.Second)
	alarmDemoDone := false
	alarmDemoCleared := false

	for {
		select {
		case <-ctx.Done():
			return r.finish(last, started)

		case <-progress.C:
			r.log.Info().
				Int64("telemetry", r.counts.Totals.Telemetry).
				Int("queued", r.queue.Pending()).
				Bool("connected", r.connected.Load()).
				Str("plant", last.String()).
				Msg("progres")

		case now := <-ticker.C:
			r.handleOutage(started, now)
			r.replayIfPending(now)

			snap := r.plant.Step(now.UTC(), r.opts.Interval)
			last = snap
			r.publishCycle(snap)

			if r.opts.AlarmDemo {
				switch {
				case !alarmDemoDone && now.After(alarmDemoAt):
					r.emitAlarm(AdvisoryAlarm{
						Code: "A-11", Severity: contract.SeverityWarning, State: contract.AlarmRaised,
						Message: "Demo: advisory STG di luar band", Value: 120,
					}, snap)
					alarmDemoDone = true
				case alarmDemoDone && !alarmDemoCleared && now.After(alarmDemoAt.Add(15*time.Second)):
					r.emitAlarm(AdvisoryAlarm{
						Code: "A-11", Severity: contract.SeverityWarning, State: contract.AlarmCleared,
						Message: "Demo: advisory STG kembali di dalam band", Value: 10,
					}, snap)
					alarmDemoCleared = true
				}
			}
		}
	}
}

func (r *Runner) connect() error {
	opts, err := r.opts.MQTT.Options()
	if err != nil {
		return err
	}

	// The will is what proves the plant went away without saying goodbye.
	opts.SetWill(contract.LWTTopic, string(contract.LWTPayload), 1, true)

	opts.SetOnConnectHandler(func(c mqtt.Client) {
		r.connected.Store(true)
		r.log.Info().Str("clientId", r.opts.MQTT.ClientID).Msg("tersambung ke broker")
		r.publishStatus(c, true)
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		r.connected.Store(false)
		r.log.Warn().Err(err).Msg("koneksi putus, pesan diantre ke disk")
	})

	r.client = mqtt.NewClient(opts)
	token := r.client.Connect()
	if !token.WaitTimeout(20 * time.Second) {
		return fmt.Errorf("CONNECT ke %s timeout", r.opts.MQTT.URL)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("CONNECT ke %s ditolak: %w", r.opts.MQTT.URL, err)
	}
	return nil
}

func (r *Runner) publishStatus(c mqtt.Client, online bool) {
	snap := Snapshot{TS: time.Now().UTC()}
	payload, err := json.Marshal(r.builder.Status(snap, online, r.opts.FW))
	if err != nil {
		r.log.Error().Err(err).Msg("gagal menyusun status")
		return
	}
	token := c.Publish(contract.LWTTopic, 1, true, payload)
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		r.log.Warn().Msg("status retained gagal dikirim")
		return
	}
	r.counts.Status(contract.PublisherID, string(contract.ClassEMS))
}

// handleOutage drops and restores the connection at the configured moment, so
// the store-and-forward path is exercised the way a real link failure would.
func (r *Runner) handleOutage(started, now time.Time) {
	if r.opts.Outage <= 0 {
		return
	}
	begin := started.Add(r.opts.OutageAfter)
	end := begin.Add(r.opts.Outage)

	switch {
	case !r.inOutage.Load() && now.After(begin) && now.Before(end):
		r.inOutage.Store(true)
		r.connected.Store(false)
		r.client.Disconnect(0)
		r.log.Warn().Dur("durasi", r.opts.Outage).Msg("outage dimulai, memutus koneksi")

	case r.inOutage.Load() && !now.Before(end):
		r.inOutage.Store(false)
		r.log.Info().Int("antrean", r.queue.Pending()).Msg("outage selesai, menyambung ulang")
		if err := r.connect(); err != nil {
			r.log.Error().Err(err).Msg("gagal menyambung ulang")
			r.inOutage.Store(true)
		}
	}
}

// replayIfPending drains the disk buffer before any new message goes out, so
// the order the plant measured is the order the database sees.
func (r *Runner) replayIfPending(now time.Time) {
	if r.queue.Pending() == 0 || !r.online() {
		return
	}
	result := r.queue.Replay(now, func(e Entry) error {
		if err := r.rawPublish(e); err != nil {
			return err
		}
		code, class := deviceOfTopic(e.Topic)
		r.counts.Replayed(code, class)
		return nil
	})
	if result.Sent > 0 || result.Expired > 0 {
		r.log.Info().
			Int("terkirim", result.Sent).
			Int("kedaluwarsa", result.Expired).
			Int("sisa", r.queue.Pending()).
			Msg("replay buffer")
	}
	if result.Err != nil && !errors.Is(result.Err, errOffline) {
		r.log.Warn().Err(result.Err).Msg("replay berhenti, dilanjutkan di siklus berikutnya")
	}
}

// publishCycle sends one telemetry message per device for this instant.
func (r *Runner) publishCycle(snap Snapshot) {
	for _, d := range r.builder.Devices() {
		tel := r.builder.Telemetry(d, snap)
		payload, err := json.Marshal(tel)
		if err != nil {
			r.log.Error().Err(err).Str("device", d.Code).Msg("gagal menyusun telemetry")
			continue
		}

		topic := d.Topic(contract.KindTelemetry).String()
		r.emit(Entry{Topic: topic, Payload: payload, QoS: 1, TS: snap.TS})
		r.counts.Telemetry(d.Code, string(d.Class), len(tel.Metrics))

		// A deliberate resend: the database must still end up with one row.
		if r.opts.DupRate > 0 && r.rng.Float64() < r.opts.DupRate {
			r.emit(Entry{Topic: topic, Payload: payload, QoS: 1, TS: snap.TS})
			r.counts.Duplicated(d.Code, string(d.Class))
		}

		// A deliberately broken extra message: it must land in ingest_rejects
		// and must not disturb the valid stream.
		if r.opts.InvalidRate > 0 && r.rng.Float64() < r.opts.InvalidRate {
			r.emit(Entry{Topic: topic, Payload: r.corrupt(payload, d.Code), QoS: 1, TS: snap.TS})
			r.counts.Corrupted(d.Code, string(d.Class))
		}
	}

	if snap.Alarm != nil {
		r.emitAlarm(*snap.Alarm, snap)
	}
}

func (r *Runner) emitAlarm(a AdvisoryAlarm, snap Snapshot) {
	payload, err := json.Marshal(r.builder.Alarm(a, snap))
	if err != nil {
		r.log.Error().Err(err).Msg("gagal menyusun alarm")
		return
	}
	topic := contract.Topic{Class: contract.ClassEMS, DeviceID: contract.PublisherID, Kind: contract.KindAlarm}.String()
	r.emit(Entry{Topic: topic, Payload: payload, QoS: 1, TS: snap.TS})
	r.counts.Alarm(contract.PublisherID, string(contract.ClassEMS))
	r.log.Info().Str("code", a.Code).Str("state", a.State).Float64("dp_kw", a.Value).Msg("alarm advisory")
}

// emit publishes, or buffers when the link is down. It also buffers while a
// replay is still in flight, so a new message never overtakes an older one.
func (r *Runner) emit(e Entry) {
	if r.queue.Pending() > 0 || !r.online() {
		if err := r.queue.Append(e); err != nil {
			r.log.Error().Err(err).Msg("gagal menulis ke buffer disk")
		}
		return
	}
	if err := r.rawPublish(e); err != nil {
		if err := r.queue.Append(e); err != nil {
			r.log.Error().Err(err).Msg("gagal menulis ke buffer disk")
		}
	}
}

func (r *Runner) rawPublish(e Entry) error {
	if !r.online() {
		return errOffline
	}
	token := r.client.Publish(e.Topic, e.QoS, e.Retain, e.Payload)
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("publish ke %s tanpa PUBACK", e.Topic)
	}
	return token.Error()
}

func (r *Runner) online() bool {
	return r.connected.Load() && !r.inOutage.Load() && r.client != nil && r.client.IsConnectionOpen()
}

// corrupt produces a payload that must fail validation. The forms rotate so the
// run exercises more than one rejection path.
func (r *Runner) corrupt(valid []byte, code string) []byte {
	switch r.rng.Intn(4) {
	case 0: // truncated JSON
		if len(valid) > 12 {
			return valid[:len(valid)/2]
		}
		return []byte(`{"schemaVersion":`)
	case 1: // a metric that is not a number
		return []byte(fmt.Sprintf(
			`{"schemaVersion":"1.0","deviceId":%q,"ts":"2026-10-12T04:30:00.000Z","seq":1,"metrics":{"p_kw":"banyak"}}`, code))
	case 2: // ts without milliseconds
		return []byte(fmt.Sprintf(
			`{"schemaVersion":"1.0","deviceId":%q,"ts":"2026-10-12T04:30:00Z","seq":1,"metrics":{}}`, code))
	default: // a field the contract does not define
		return []byte(fmt.Sprintf(
			`{"schemaVersion":"1.0","deviceId":%q,"ts":"2026-10-12T04:30:00.000Z","seq":1,"metrics":{},"tambahan":true}`, code))
	}
}

// finish publishes the offline status, writes the count file and disconnects.
func (r *Runner) finish(last Snapshot, started time.Time) error {
	r.log.Info().
		Int64("telemetry", r.counts.Totals.Telemetry).
		Int64("corrupted", r.counts.Totals.Corrupted).
		Int64("duplicated", r.counts.Totals.Duplicated).
		Int64("replayed", r.counts.Totals.Replayed).
		Int("queued", r.queue.Pending()).
		Dur("durasi", time.Since(started)).
		Msg("selesai")

	if r.online() {
		r.publishStatus(r.client, false)
	}
	if r.client != nil {
		r.client.Disconnect(1000)
	}
	r.connected.Store(false)

	if pending := r.queue.Pending(); pending > 0 {
		r.counts.Queued(contract.PublisherID, string(contract.ClassEMS), int64(pending))
		r.log.Warn().Int("antrean", pending).Str("file", r.queue.Path()).
			Msg("masih ada pesan di buffer disk saat berhenti")
	}

	if r.opts.CountFile != "" {
		if err := r.counts.Write(r.opts.CountFile, time.Now()); err != nil {
			return err
		}
		r.log.Info().Str("file", r.opts.CountFile).Msg("count file ditulis")
	}
	_ = last
	return nil
}

// deviceOfTopic recovers the device and class from a buffered entry.
func deviceOfTopic(topic string) (code, class string) {
	parsed, err := contract.ParseTopic(topic)
	if err != nil {
		return "unknown", "unknown"
	}
	return parsed.DeviceID, string(parsed.Class)
}
