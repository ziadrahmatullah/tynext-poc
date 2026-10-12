package ingest

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics is the Prometheus surface exposed on /metrics.
type Metrics struct {
	Registry *prometheus.Registry

	Received   *prometheus.CounterVec
	Written    *prometheus.CounterVec
	Rejected   *prometheus.CounterVec
	Duplicates prometheus.Counter
	DBErrors   prometheus.Counter

	LagMS     prometheus.Histogram
	BatchSize prometheus.Histogram

	Connected  prometheus.Gauge
	OpenAlarms prometheus.Gauge
}

// NewMetrics builds the registry. Everything is prefixed rain_ingest_.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Metrics{
		Registry: reg,
		Received: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rain_ingest_received_total",
			Help: "Pesan MQTT yang diterima, per jenis payload.",
		}, []string{"kind"}),
		Written: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rain_ingest_written_total",
			Help: "Baris yang benar-benar masuk database, per tabel.",
		}, []string{"table"}),
		Rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rain_ingest_rejected_total",
			Help: "Pesan yang ditolak, per jenis payload dan alasan.",
		}, []string{"kind", "reason"}),
		Duplicates: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rain_ingest_duplicates_total",
			Help: "Baris telemetry yang dibuang karena sudah ada (QoS 1 redelivery atau replay).",
		}),
		DBErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rain_ingest_db_errors_total",
			Help: "Kegagalan operasi database.",
		}),
		LagMS: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "rain_ingest_lag_ms",
			Help:    "received_at - ts, dalam milidetik.",
			Buckets: []float64{50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000},
		}),
		BatchSize: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "rain_ingest_batch_size",
			Help:    "Jumlah baris telemetry per flush.",
			Buckets: []float64{1, 10, 50, 100, 200, 300, 400, 500},
		}),
		Connected: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "rain_ingest_broker_connected",
			Help: "1 kalau koneksi ke broker hidup.",
		}),
		OpenAlarms: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "rain_ingest_stale_devices",
			Help: "Device yang sedang punya alarm STALE terbuka.",
		}),
	}

	reg.MustRegister(
		m.Received, m.Written, m.Rejected, m.Duplicates, m.DBErrors,
		m.LagMS, m.BatchSize, m.Connected, m.OpenAlarms,
	)
	return m
}
