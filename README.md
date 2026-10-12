# tynext-poc

PoC sisi cloud untuk **RAIN Project — PT Prakarsa Tani Sejati (PTS)**: membuktikan bahwa PLC Beckhoff
CX5240 (TwinCAT 3 + TF6701) bisa mengirim data ke broker kita dan datanya masuk database dengan utuh.

```
CX5240 (TF6701)  →  EMQX 5 (TLS 8883)  →  ingest Go (validasi JSON Schema)  →  PostgreSQL 16
```

PLC belum siap, jadi **simulator Go jadi stand-in CX5240**. Formatnya persis sama dengan yang nanti
dikirim TF6701, sehingga pada hari uji tinggal mengganti sumbernya.

Repo ini **khusus PoC**. Setelah lulus, kontrak, simulator, ingest, dan konfigurasi broker dipindah ke
repo backend utama — karena itu nama topic, field payload, dan nama tabel dikunci.

## Ruang lingkup

Yang dibangun di sini: broker, ingest, database, kontrak MQTT, simulator, skenario uji.

Yang **tidak** dibangun: REST API, WebSocket, Redis, frontend, downlink/command, rule engine kontrol,
multi-site. Server tidak mengeksekusi kontrol apa pun dan tidak pernah mengirim perintah
breaker/genset.

## Isi

| Jalur | Isi |
|---|---|
| [`contracts/`](contracts/) | Kontrak MQTT: [topic](contracts/mqtt/topics.md), JSON Schema, contoh payload, [daftar device](contracts/devices.json), [draft tag PLC](contracts/tags/ems-pv-tags.md) |
| `internal/contract/` | Pemuat + validator kontrak (`go:embed`), dipakai ingest dan simulator |
| `cmd/ingest/` | MQTT → PostgreSQL |
| `cmd/simulator/` | Stand-in CX5240 (TF6701) |
| `migrations/` | Skema database |
| `deploy/` | docker-compose, konfigurasi EMQX, pembuat sertifikat |
| `scripts/scenarios/` | Skenario uji 01–06, tiap skrip mencetak PASS/FAIL |
| `sql/checks.sql` | Query bukti: kelengkapan, gap, duplikat, lag, LWT, alarm |
| `docs/` | Runbook hari-H dan paket handoff untuk tim OT |

## Mulai

```bash
cp .env.example .env     # isi RAIN_BROKER_HOST dan semua password; .env tidak di-commit
chmod 600 .env

make certs               # CA internal + sertifikat broker
make up                  # emqx + postgres → bootstrap user EMQX → migrasi → ingest
make scenario-1          # koneksi TLS + verifikasi sertifikat   → PASS
make scenario-2          # semua yang harus ditolak, ditolak     → PASS
```

Melihat datanya mengalir:

```bash
make sim SIMFLAGS="--duration 60s --alarm-demo"
make check WINDOW="5 minutes"
```

Perintah lain: `make help`. Langkah lengkap hari uji ada di
[docs/POC_RUNBOOK.md](docs/POC_RUNBOOK.md).

## Tes

```bash
make test                # go vet + unit test, termasuk validasi semua contoh payload
make test-integration    # tambahan: test yang butuh PostgreSQL (stack harus 'make up')
make lint                # golangci-lint
make scenarios           # skenario 01..06 berurutan (03 butuh 1 jam; DURATION=5m untuk dev)
```

## Kriteria lulus

| # | Aspek | Kriteria |
|---|---|---|
| 1 | Koneksi & keamanan | Client tersambung via TLS 8883 dengan kredensial sendiri; tanpa/salah kredensial ditolak; publish di luar ACL ditolak |
| 2 | Kelengkapan | Telemetry 1 s masuk DB ≥ 99% selama 1 jam; tidak ada pesan valid yang ditolak schema |
| 3 | Deteksi offline | Status offline (LWT) tercatat < 10 s setelah jaringan client dicabut |
| 4 | Pemulihan | Setelah reconnect, buffer di-replay berurutan tanpa data hilang & tanpa duplikat; restart container pulih otomatis |

Urutan pembuktiannya ada di [`docs/POC_RUNBOOK.md`](docs/POC_RUNBOOK.md).

## Berikutnya

Checklist bertahap dari sekarang sampai PoC dinyatakan lulus — termasuk dua pertanyaan
yang harus dikirim ke tim OT lebih dulu karena bisa memaksa pekerjaan ulang — ada di
[`docs/NEXT_STEPS.md`](docs/NEXT_STEPS.md).
