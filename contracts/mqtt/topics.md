# Kontrak topic MQTT — RAIN / PTS

Sumber kebenaran untuk tim OT (TF6701 di CX5240) dan untuk ingest. Versi payload: `schemaVersion` `"1.0"`.

Arah data **hanya uplink**. Downlink/command **di luar scope PoC** — tidak ada topic `cmd` di sini,
dan ACL broker menolak publish ke topic apa pun di luar daftar di bawah.

## Pola topic

```
rain/pts/{class}/{deviceId}/telemetry
rain/pts/{class}/{deviceId}/status
rain/pts/{class}/{deviceId}/alarm

class ∈ ems | pv | bess | meter | weather | breaker | gen
```

`{deviceId}` memakai `code` dari [`../devices.json`](../devices.json) (34 device), dan `{class}` harus
cocok dengan `class` device tersebut.

| Topic | QoS | Retained | Periode | Publisher | Schema |
|---|---|---|---|---|---|
| `rain/pts/{class}/{deviceId}/telemetry` | 1 | tidak | 1 s | `ems-pv-01` | [`payloads/telemetry.schema.json`](payloads/telemetry.schema.json) |
| `rain/pts/{class}/{deviceId}/status` | 1 | **ya** | saat berubah + saat connect | `ems-pv-01` | [`payloads/status.schema.json`](payloads/status.schema.json) |
| `rain/pts/{class}/{deviceId}/alarm` | 1 | tidak | saat transisi (`raised` / `cleared`) | `ems-pv-01` | [`payloads/alarm.schema.json`](payloads/alarm.schema.json) |

## Satu client fisik

Hanya ada **satu koneksi MQTT** dari plant: TF6701 di CX5240.

```
clientId = username = ems-pv-01
```

Client itu mem-publish **atas nama semua device**. Jadi `rain/pts/bess/bess-01/telemetry` tetap
dikirim oleh `ems-pv-01`, bukan oleh BESS. `deviceId` di dalam payload harus sama dengan segmen
`{deviceId}` di topic — ingest menolak pesan yang tidak cocok dan mencatatnya di `ingest_rejects`.

### Siapa yang mengirim status

Di PoC ini **hanya `ems-pv-01` yang mem-publish status**, karena ia satu-satunya
client fisik. Kalau ia hilang, tidak ada apa pun dari plant yang sampai — jadi menandai
33 device lain "online" secara retained justru akan menyesatkan.

Keaktifan device lain dibuktikan oleh **stale detector** di ingest: device tanpa
telemetry selama lebih dari 5 detik mendapat alarm `source=server`, `code=STALE`
(`warning`; khusus `ems-pv-01` `critical`), yang hilang sendiri saat datanya kembali.

Topic `status` untuk device lain tetap ada di kontrak supaya tidak perlu diubah kalau
nanti ada publisher kedua.

## Last Will & Testament

```
topic   : rain/pts/ems/ems-pv-01/status
payload : {"online":false}
QoS     : 1
retained: true
```

LWT dibekukan saat CONNECT, jadi ia tidak bisa membawa `ts` yang hidup. Karena itu schema `status`
hanya mewajibkan `online`; ingest memakai waktu terima sebagai `last_seen` kalau `ts` tidak ada.

Saat connect berhasil, publisher langsung mengirim `{"schemaVersion":"1.0","ts":…,"online":true,"fw":…}`
retained ke topic yang sama supaya status lama tertimpa.

## Subscription ingest

```
$share/ingest/rain/pts/+/+/telemetry
$share/ingest/rain/pts/+/+/status
$share/ingest/rain/pts/+/+/alarm
```

QoS 1, `clean session = false`, clientId tetap — supaya pesan tidak hilang saat ingest restart.
Shared subscription `$share/ingest/...` menyiapkan jalur untuk menambah instance ingest tanpa
menggandakan data.

## ACL

| User | Boleh publish | Boleh subscribe |
|---|---|---|
| `ems-pv-01` | `rain/pts/+/+/telemetry`, `rain/pts/+/+/status`, `rain/pts/+/+/alarm` | — (tidak ada) |
| `ingest` | — (tidak ada) | `rain/pts/#` |
| `simulator-dev` | sama seperti `ems-pv-01` (khusus dev, tidak dipakai di hari uji) | — |

Selain itu **deny**. Anonymous ditolak.

## Format waktu

`ts` = ISO 8601 UTC, **persis 3 digit milidetik**, diakhiri `Z`:

```
2026-10-12T04:30:00.000Z
```

Diambil dari jam PLC (NTP). Schema menolak bentuk lain (offset `+07:00`, tanpa milidetik, 6 digit
pecahan) supaya ketidakcocokan ketahuan di PoC, bukan di produksi.

> `TBC` — acuan timestamp (jam PLC/NTP vs waktu terima server) belum diputuskan. Sementara ini jam
> PLC yang dipakai sebagai `ts`, dan server mencatat `received_at` sendiri sehingga selisihnya
> (`lag_ms`) terukur.

Format ketat ini **wajib dikonfirmasi ke tim OT sebelum hari uji**. Kalau TF6701 ternyata
mengirim `+07:00` atau 6 digit pecahan, seluruh telemetry akan ditolak schema — dan itu
memang disengaja: lebih baik ketahuan saat PoC daripada diam-diam salah di produksi.

## Konvensi nama tag

`snake_case`, satuan di akhiran: `_kw`, `_kvar`, `_pct`, `_v`, `_a`, `_hz`, `_wm2`, `_c`.
Beberapa tag warisan tidak bersufiks (`eta_link`, `v`) sehingga schema hanya memaksa `^[a-z][a-z0-9_]*$`
dan konvensi di atas ditegakkan lewat review, bukan regex.

`metrics` hanya berisi angka. Nilai non-numerik (enum, boolean) masuk ke `state`.
Device class `breaker` dan `gen` mengirim `"metrics": {}` karena dayanya tidak terukur per unit —
daya genset hanya terukur per section lewat `pm-03a` (Section A) dan `pm-03b` (Section B).

## Contoh

Semua file di [`examples/`](examples/) wajib lolos validasi (`make test`).

| File | Topic |
|---|---|
| `telemetry-ems.json` | `rain/pts/ems/ems-pv-01/telemetry` |
| `telemetry-pv-combox.json` | `rain/pts/pv/combox-01/telemetry` |
| `telemetry-pv-inverter.json` | `rain/pts/pv/pv-inv-01/telemetry` |
| `telemetry-bess.json` | `rain/pts/bess/bess-01/telemetry` |
| `telemetry-meter.json` | `rain/pts/meter/pm-04/telemetry` |
| `telemetry-weather.json` | `rain/pts/weather/ws-01/telemetry` |
| `telemetry-breaker.json` | `rain/pts/breaker/qf-5/telemetry` |
| `telemetry-gen.json` | `rain/pts/gen/stg-01/telemetry` |
| `status-online.json` | `rain/pts/ems/ems-pv-01/status` (retained) |
| `status-lwt-offline.json` | `rain/pts/ems/ems-pv-01/status` (LWT, retained) |
| `alarm-raised.json` | `rain/pts/ems/ems-pv-01/alarm` |
| `alarm-cleared.json` | `rain/pts/ems/ems-pv-01/alarm` |

Contoh `telemetry-ems.json` konsisten dengan invarian power balance:

```
p_pm02 = p_pv_fact + p_bess   → 250 = 600 + (−350)
p_load = p_gen + p_pm02       → 1050 = 800 + 250
```

## Beban satu siklus

34 device × 1 pesan/detik = **34 pesan/detik** telemetry pada kondisi normal, ditambah status dan
alarm yang jarang. Ukuran payload terbesar (`ems-pv-01`) ± 480 byte.
