package ingest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/tynext/tynext-poc/internal/contract"
	"github.com/tynext/tynext-poc/internal/store"
)

// Service is the whole ingest pipeline.
type Service struct {
	cfg Config
	log zerolog.Logger
	m   *Metrics
}

// New builds a service. Nothing is dialled until Run.
func New(cfg Config, log zerolog.Logger) *Service {
	return &Service{cfg: cfg, log: log, m: NewMetrics()}
}

// Run connects, subscribes and writes until ctx is cancelled. On the way out it
// stops taking new messages, drains what is queued and flushes the last batch,
// so a clean shutdown loses nothing.
func (s *Service) Run(ctx context.Context) error {
	validator, err := contract.NewValidator()
	if err != nil {
		return fmt.Errorf("muat kontrak: %w", err)
	}
	devices, err := contract.LoadDevices()
	if err != nil {
		return err
	}

	pool, err := store.Open(ctx, s.cfg.DSN, 60*time.Second)
	if err != nil {
		return err
	}
	defer pool.Close()

	refs, err := deviceRefs(ctx, pool, devices)
	if err != nil {
		return err
	}
	s.log.Info().Int("devices", len(refs)).Msg("device dimuat dari database")

	h := &health{startedAt: time.Now()}
	h.dbReachable.Store(true)

	stale := newStaleDetector(s.cfg, pool, s.log, s.m, refs, time.Now())
	if err := stale.reconcile(ctx); err != nil {
		return err
	}

	dec := &decoder{validator: validator, devices: refs}

	msgs := make(chan message, s.cfg.QueueSize)
	rejects := make(chan reject, 1024)

	var wg sync.WaitGroup

	// Writer owns every database write and runs alone, which is why its state
	// cache needs no locking.
	w := newWriter(s.cfg, pool, s.log, s.m)
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.run(ctx, msgs, rejects)
	}()

	staleCtx, stopStale := context.WithCancel(ctx)
	wg.Add(1)
	go func() {
		defer wg.Done()
		stale.run(staleCtx)
	}()

	httpCtx, stopHTTP := context.WithCancel(ctx)
	httpErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		httpErr <- serveHTTP(httpCtx, s.cfg.HTTPAddr, h, s.m, s.log)
	}()

	dbWatchCtx, stopDBWatch := context.WithCancel(ctx)
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.watchDatabase(dbWatchCtx, pool, h)
	}()

	client, err := s.connect(ctx, dec, stale, h, msgs, rejects)
	if err != nil {
		stopStale()
		stopHTTP()
		stopDBWatch()
		close(msgs)
		close(rejects)
		wg.Wait()
		return err
	}

	<-ctx.Done()
	s.log.Info().Msg("shutdown: berhenti menerima pesan, menguras buffer")

	// Stop the inflow first, then let the writer drain.
	client.Disconnect(1000)
	s.m.Connected.Set(0)
	h.brokerConnected.Store(false)

	stopStale()
	stopDBWatch()
	close(msgs)
	close(rejects)
	stopHTTP()

	wg.Wait()
	if err := <-httpErr; err != nil {
		s.log.Warn().Err(err).Msg("http server berhenti dengan error")
	}
	s.log.Info().Msg("ingest berhenti dengan rapi")
	return nil
}

// Metrics exposes the registry for tests.
func (s *Service) Metrics() *Metrics { return s.m }

func (s *Service) connect(ctx context.Context, dec *decoder, stale *staleDetector, h *health, msgs chan<- message, rejects chan<- reject) (mqtt.Client, error) {
	opts, err := s.cfg.MQTT.Options()
	if err != nil {
		return nil, err
	}

	handler := s.messageHandler(dec, stale, msgs, rejects)

	opts.SetOnConnectHandler(func(c mqtt.Client) {
		s.m.Connected.Set(1)
		h.brokerConnected.Store(true)
		s.log.Info().Str("clientId", s.cfg.MQTT.ClientID).Msg("tersambung ke broker")
		// Subscriptions are (re)made here so they survive a reconnect even
		// though the session is persistent.
		for _, kind := range contract.Kinds {
			filter := contract.SharedSubscription(s.cfg.ShareGroup, kind)
			token := c.Subscribe(filter, 1, handler)
			if !token.WaitTimeout(10 * time.Second) {
				s.log.Error().Str("filter", filter).Msg("subscribe timeout")
				continue
			}
			if err := token.Error(); err != nil {
				s.log.Error().Err(err).Str("filter", filter).Msg("subscribe gagal")
				continue
			}
			s.log.Info().Str("filter", filter).Msg("subscribe")
		}
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		s.m.Connected.Set(0)
		h.brokerConnected.Store(false)
		s.log.Warn().Err(err).Msg("koneksi broker putus, paho akan reconnect dengan backoff")
	})

	client := mqtt.NewClient(opts)
	token := client.Connect()
	// ConnectRetry is on, so a broker that is not up yet is not fatal: paho
	// keeps trying in the background and OnConnect subscribes when it lands.
	if !token.WaitTimeout(15 * time.Second) {
		s.log.Warn().Msg("broker belum menjawab, tetap mencoba di latar belakang")
		return client, nil
	}
	if err := token.Error(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		s.log.Warn().Err(err).Msg("connect awal gagal, tetap mencoba di latar belakang")
	}
	return client, nil
}

func (s *Service) messageHandler(dec *decoder, stale *staleDetector, msgs chan<- message, rejects chan<- reject) mqtt.MessageHandler {
	return func(_ mqtt.Client, m mqtt.Message) {
		received := time.Now().UTC()
		msg, rej := dec.decode(m.Topic(), m.Payload(), received)

		if rej != nil {
			s.m.Received.WithLabelValues(rej.kind).Inc()
			select {
			case rejects <- *rej:
			default:
				// Never block the broker on reject bookkeeping.
				s.log.Error().Str("topic", rej.topic).Msg("antrean reject penuh, catatan dibuang")
			}
			return
		}

		s.m.Received.WithLabelValues(string(msg.topic.Kind)).Inc()
		if msg.telemetry != nil {
			stale.seen(msg.topic.DeviceID, received)
		}
		// Blocking here is deliberate backpressure: the broker holds QoS 1
		// messages until we catch up instead of us dropping them.
		msgs <- *msg
	}
}

// watchDatabase keeps /healthz honest about whether PostgreSQL is answering.
func (s *Service) watchDatabase(ctx context.Context, pool *pgxpool.Pool, h *health) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := pool.Ping(pingCtx)
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) {
				h.dbReachable.Store(false)
				s.log.Warn().Err(err).Msg("database tidak menjawab")
				continue
			}
			h.dbReachable.Store(err == nil)
		}
	}
}

// deviceRefs resolves contract codes to the uuid primary keys, and refuses to
// start if the database and the contract disagree — a missing device would
// otherwise show up much later as a rejected message.
func deviceRefs(ctx context.Context, pool *pgxpool.Pool, devices []contract.Device) (map[string]deviceRef, error) {
	ids, err := store.DeviceIDs(ctx, pool)
	if err != nil {
		return nil, err
	}
	refs := make(map[string]deviceRef, len(devices))
	var missing []string
	for _, d := range devices {
		id, ok := ids[d.Code]
		if !ok {
			missing = append(missing, d.Code)
			continue
		}
		refs[d.Code] = deviceRef{id: id, class: d.Class}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("device belum ada di database: %v — jalankan 'make migrate'", missing)
	}
	return refs, nil
}
