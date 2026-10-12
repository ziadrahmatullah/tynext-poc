# Paket serah terima untuk tim OT

Dokumen ini berisi semua yang dibutuhkan untuk menghubungkan **TF6701 di CX5240** ke broker
MQTT kami, plus daftar hal yang masih kami tunggu dari tim OT.

Ruang lingkup kami mulai dari topic MQTT ke atas: broker, ingest, database. Kami **tidak
menyentuh Modbus, EtherCAT, atau logika kontrol**, dan server **tidak pernah mengirim perintah
apa pun ke plant** — tidak ke breaker, tidak ke genset, tidak ke EMS-PV. Arah datanya hanya
satu: dari PLC ke server.

---

## 1. Yang kami kirimkan

| Item | Berkas / nilai |
|---|---|
| Host broker | `mqtt-poc.<domain>` — **TBC, menyusul setelah DNS final** |
| Port | **8883** (MQTT over TLS) |
| Protokol | MQTT **3.1.1** |
| Sertifikat CA | `ca.crt` — dikirim terpisah |
| Username | `ems-pv-01` |
| Password | dikirim lewat jalur terpisah, bukan di dokumen ini |
| Client certificate | `client-ems-pv-01.crt` + `.key` — **opsional**, lihat §2 |
| Kontrak topic | `contracts/mqtt/topics.md` |
| Schema payload | `contracts/mqtt/payloads/*.schema.json` |
| Contoh payload | `contracts/mqtt/examples/` — 12 berkas, satu per jenis device |
| Daftar device | `contracts/devices.json` — 34 device |
| Draft daftar tag | `contracts/tags/ems-pv-tags.md` |

---

## 2. Setelan TF6701 yang disarankan

| Setelan | Nilai | Kenapa |
|---|---|---|
| Protokol | MQTT 3.1.1 | Yang didukung TF6701 dan yang kami uji |
| Host / port | `mqtt-poc.<domain>` : 8883 | Satu-satunya port yang terbuka |
| TLS | TLS 1.2 atau 1.3, verifikasi server pakai `ca.crt` | Broker menolak koneksi tanpa TLS |
| Username / password | `ems-pv-01` / (terpisah) | Anonymous ditolak broker |
| Client certificate | belum diperlukan | Broker saat ini `verify_none`. Kalau tim OT ingin mutual TLS, bilang saja — kami sudah menyiapkan sertifikat clientnya |
| clientId | `ems-pv-01` | **Harus sama persis dan tidak berubah-ubah.** Broker memakai clientId untuk menyimpan sesi |
| Clean session | **false** | Supaya broker menahan pesan QoS 1 kalau sisi kami sempat restart |
| Keepalive | **5 detik** | Dari sinilah deteksi putus < 10 detik berasal. Jangan dinaikkan tanpa memberi tahu kami |
| QoS | **1** untuk semua publish | QoS 0 bisa hilang; QoS 2 ditolak broker |
| Retain | **hanya untuk topic `status`** | Telemetry dan alarm tidak retained |
| Timestamp | UTC dari jam PLC yang sinkron **NTP** | Lihat §4 |

### Last Will & Testament — wajib diisi

```
Topic   : rain/pts/ems/ems-pv-01/status
Payload : {"online":false}
QoS     : 1
Retain  : true
```

Inilah yang membuat kami tahu koneksi putus tanpa menunggu timeout panjang. Tanpa LWT,
kriteria lulus #3 tidak bisa dibuktikan.

Begitu CONNECT berhasil, kirim juga status online ke topic yang sama, retained:

```json
{"schemaVersion":"1.0","ts":"2026-10-12T04:29:58.120Z","online":true,"fw":"TF6701 3.1.0 / TC3.1.4024.x"}
```

---

## 3. Topic

```
rain/pts/{class}/{deviceId}/telemetry   QoS 1, tidak retained, tiap 1 detik
rain/pts/{class}/{deviceId}/status      QoS 1, RETAINED + LWT
rain/pts/{class}/{deviceId}/alarm       QoS 1, tidak retained (raised / cleared)

class ∈ ems | pv | bess | meter | weather | breaker | gen
```

Satu koneksi `ems-pv-01` mem-publish **atas nama semua device**. Jadi
`rain/pts/bess/bess-01/telemetry` tetap dikirim oleh `ems-pv-01`.

`{class}` harus cocok dengan class device di `devices.json`, dan `deviceId` di dalam payload
harus sama dengan yang di topic. Kalau tidak cocok, pesannya kami tolak dan catat.

### Hak akses

`ems-pv-01` **hanya boleh publish** ke ketiga pola di atas. Ia **tidak boleh subscribe
apa pun**. Broker akan memutus koneksi kalau dicoba — ini sengaja, bukan bug.

Tidak ada topic perintah. Downlink di luar ruang lingkup PoC.

---

## 4. Format payload

### Telemetry

```json
{
  "schemaVersion": "1.0",
  "deviceId": "pm-04",
  "ts": "2026-10-12T04:30:00.000Z",
  "seq": 18041,
  "metrics": { "p_kw": 1050.0, "q_kvar": 180.5, "v": 401.2, "i_a": 1540.0, "f_hz": 50.01 },
  "state": { "mode": "AUTO" }
}
```

Aturannya:

- **Datar.** Tanpa nesting dalam, tanpa array — supaya bisa disusun langsung dari struct TwinCAT.
- `metrics` **hanya angka**. Nilai enum atau boolean masuk ke `state`.
- `state` opsional. Device class `breaker` dan `gen` mengirim `"metrics": {}` dan mengisi `state`.
- `seq` integer naik per device. Kami pakai untuk mendeteksi lubang, bukan sebagai kunci.
- Nama tag `snake_case`, satuan di akhiran (`_kw`, `_kvar`, `_pct`, `_v`, `_a`, `_hz`).

### ⚠ Format `ts` — paling sering jadi masalah

```
2026-10-12T04:30:00.000Z
```

**UTC, tepat 3 digit milidetik, diakhiri huruf `Z`.** Yang berikut ini akan **ditolak**:

| Ditolak | Kenapa |
|---|---|
| `2026-10-12T04:30:00Z` | tanpa milidetik |
| `2026-10-12T04:30:00.123456Z` | 6 digit, bukan 3 |
| `2026-10-12T11:30:00.000+07:00` | offset, bukan UTC |
| `2026-10-12 04:30:00.000Z` | spasi, bukan `T` |

Kami sengaja memeriksanya seketat ini supaya ketidakcocokan ketahuan saat PoC, bukan diam-diam
jadi data yang salah di produksi. **Tolong konfirmasi format ini sebelum hari uji** — kalau
TF6701 tidak bisa menghasilkan bentuk persis ini, beri tahu kami dan kami longgarkan schema-nya.

Jamnya diambil dari jam PLC. Pastikan PLC sinkron NTP; kami mencatat waktu terima sendiri,
jadi selisih jam akan terlihat sebagai lag.

### Status

```json
{"schemaVersion":"1.0","ts":"2026-10-12T04:29:58.120Z","online":true,"fw":"TF6701 3.1.0"}
```

Tanpa `deviceId` — sudah ada di topic. Hanya `online` yang wajib, supaya payload LWT
`{"online":false}` tetap sah.

### Alarm

```json
{
  "schemaVersion": "1.0",
  "ts": "2026-10-12T04:31:02.480Z",
  "deviceId": "ems-pv-01",
  "code": "A-12",
  "severity": "warning",
  "state": "raised",
  "message": "Advisory STG: naikkan beban STG, dp -62 kW bertahan 60 s",
  "value": -62.0
}
```

- `severity`: `info` | `warning` | `critical`
- `state`: `raised` | `cleared`
- `code`: `A-xx` atau `T-xx`

Kirim **satu pesan per transisi**: `raised` saat muncul, `cleared` saat hilang. Tidak perlu
mengulang `raised` tiap siklus — kami sudah menanganinya kalau terkirim ulang, tapi lebih
bersih kalau tidak.

---

## 5. Daftar device

34 device, lengkap di `contracts/devices.json`.

| Class | deviceId |
|---|---|
| `ems` | `ems-pv-01` |
| `pv` | `combox-01`, `pv-inv-01` … `pv-inv-09` |
| `bess` | `bess-01` |
| `weather` | `ws-01` |
| `meter` | `pm-01`, `pm-02`, `pm-02b`, `pm-03a`, `pm-03b`, `pm-04` |
| `breaker` | `qf-1` … `qf-6`, `qf-g2`, `qf-t2`, `qf-l`, `qf-p1` |
| `gen` | `stg-01`, `stg-02`, `dg-01`, `dg-02`, `dg-03` |

> **Perlu konfirmasi:** dokumen PoC yang kami terima menyebut "35 device", tetapi tabel
> device di diagram Rev P yang kami punya hanya menghasilkan **34**. Kami jalan dengan 34.
> Kalau ada device ke-35 yang terlewat, mohon beri tahu kodenya dan class-nya.

Class `gen` **hanya status, tanpa kW**, karena daya genset tidak terukur per unit — hanya per
section lewat `pm-03a` (Section A) dan `pm-03b` (Section B).

---

## 6. Daftar tag

Draft lengkap ada di `contracts/tags/ems-pv-tags.md`, beserta nomor register sumber untuk
traceability. Isinya **usulan kami**, bukan keputusan — yang mengikat adalah daftar final
dari tim OT.

Yang sudah kami isi dari dokumen Rev P:

- `bess-01`: `soc_pct` (MG-500-B reg 38), `p_chg_bms_kw` (reg 46), `p_kw` (Σ PCS reg 15018)
- `pm-02`: `p_kw` (MG-500-B reg 1078)
- `combox-01`: `p_pv_inv_kw` (reg 2604), `p_pv_lim_readback_kw` (reg 2612)

Yang masih `TBC` di draft itu: nomor register `f_hz` dan `forming` pada BESS, register map
per inverter PV, alamat PM5560, slot terminal EL3453, dan enumerasi nilai `adv_rule`,
`pv_day`, `q_mode`, `pv-inv-*.status`.

---

## 7. Yang kami tunggu dari tim OT

| # | Yang dibutuhkan | Untuk apa | Kapan |
|---|---|---|---|
| 1 | **Daftar tag final** dari TwinCAT: nama variabel, tipe, skala, dead-band | Mengunci nama metric dan schema. Tanpa ini kami jalan dengan draft | Sebelum hari uji |
| 2 | **Kapasitas buffer TF6701** — berapa lama ia bisa menyimpan saat link putus | Menyamakan `--buffer-max` kami (sekarang 72 jam, asumsi) dan menentukan berapa lama outage yang masih aman | Sebelum hari uji |
| 3 | **Versi TwinCAT dan TF6701** | Memastikan asumsi kami benar: MQTT 3.1.1, TLS 1.2, username/password, LWT, clean session false | Sebelum hari uji |
| 4 | **Konfirmasi outbound 8883 diizinkan dari LAN EMS site** | Kalau diblokir, kami siapkan fallback MQTT over WebSocket port 443 — tapi itu butuh waktu, jangan sampai ketahuan di hari-H | **Secepatnya** |
| 5 | **Konfirmasi format `ts`** bisa dihasilkan persis seperti §4 | Kalau tidak bisa, schema kami longgarkan sebelum hari uji, bukan saat uji | **Secepatnya** |
| 6 | **Konfirmasi device ke-35** kalau memang ada | Melengkapi `devices.json` | Sebelum hari uji |
| 7 | **Jadwal dan PIC hari uji** | Skenario 04 dan 05 butuh orang di site untuk mencabut dan menyambung jaringan EMS | Sebelum hari uji |
| 8 | Keputusan **mutual TLS**: perlu client certificate atau cukup username/password | Kalau perlu, kami ubah setelan broker dan kirim sertifikat clientnya | Sebelum hari uji |

---

## 8. Yang kami minta dilakukan di hari uji

Dua skenario butuh bantuan tim OT di site:

**Skenario 04 — deteksi offline.** Cabut kabel jaringan EMS (atau matikan port switch-nya)
**tanpa** mematikan PLC dan tanpa disconnect yang rapi. Kami mengukur berapa lama sampai LWT
tercatat; targetnya di bawah 10 detik. Setelah kami bilang selesai, pasang lagi.

**Skenario 05 — pemulihan.** Putuskan jaringan yang sama selama **60 detik**, lalu sambungkan
kembali. Kami memeriksa bahwa data selama 60 detik itu tersusul lengkap, berurutan, dan tidak
dobel. Di sinilah buffer TF6701 diuji — karena itu kami perlu tahu kapasitasnya lebih dulu.

Keduanya **tidak menyentuh proses**: PLC tetap jalan, loop kontrol tetap jalan, breaker dan
genset tidak tersentuh. Yang diputus hanya jalur data ke cloud.

---

## 9. Kontak

| Peran | Nama | Kontak |
|---|---|---|
| Backend / cloud (dokumen ini) | | |
| OT / PLC | | |
| PIC site hari uji | | |
