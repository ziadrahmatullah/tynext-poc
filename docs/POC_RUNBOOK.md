# Runbook PoC — CX5240 → EMQX → ingest → PostgreSQL

Panduan menjalankan PoC dari nol sampai keempat kriteria lulus terbukti, lengkap dengan
cara memantau saat berjalan dan cara mundur kalau gagal.

Paket untuk tim OT ada di [HANDOFF_OT.md](HANDOFF_OT.md).

---

## 1. Persiapan server

### Spesifikasi

| Item | Nilai |
|---|---|
| OS | Ubuntu 24.04 LTS |
| CPU / RAM | 2 vCPU / 4 GB — cukup untuk 34 pesan/detik dengan margin besar |
| Disk | 40 GB. Telemetry 1 jam ≈ 250 MB termasuk indeks; PoC sehari masih jauh di bawah itu |
| Paket | `docker.io`, `docker-compose-v2`, `make`, `git`, `jq`, `curl`, `openssl` |

> `TBC` — provider dan region VPS belum diputuskan. Yang penting latensi ke site di
> Kalimantan wajar dan outbound dari site ke port 8883 diizinkan.

### Firewall

Hanya tiga yang terbuka:

```bash
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw allow 22/tcp      # SSH, key-only
sudo ufw allow 8883/tcp    # MQTT over TLS — satu-satunya jalur dari plant
sudo ufw allow 443/tcp     # cadangan: MQTT over WebSocket kalau 8883 diblokir dari LAN EMS
sudo ufw enable
```

Port **1883** (MQTT polos), **18083** (dashboard EMQX), **5432** (PostgreSQL), dan **8080**
(metrics ingest) sengaja hanya dipublikasikan ke `127.0.0.1` oleh docker-compose. Aksesnya
lewat SSH tunnel, tidak pernah dari internet.

SSH wajib key-only:

```bash
sudo sed -i 's/^#\?PasswordAuthentication .*/PasswordAuthentication no/' /etc/ssh/sshd_config
sudo sed -i 's/^#\?PermitRootLogin .*/PermitRootLogin no/' /etc/ssh/sshd_config
sudo systemctl reload ssh
```

### DNS

Arahkan satu A record ke IP server, misalnya `mqtt-poc.<domain>`. Nama inilah yang masuk
ke SAN sertifikat dan yang diverifikasi TF6701.

> `TBC` — domain/DNS broker belum ditentukan. Sampai ada, `RAIN_BROKER_HOST` boleh diisi
> nama sementara dan sertifikat dibuat ulang setelah DNS final. **Mengganti nama berarti
> `ca.crt` baru harus dikirim ulang ke tim OT**, jadi sebaiknya putuskan sebelum hari uji.

### Sertifikat

PoC ini memakai **CA internal**: satu CA dibuat di server, menandatangani sertifikat broker,
dan `ca.crt`-nya dikirim ke tim OT untuk dipasang di TF6701.

> `TBC` — CA internal vs Let's Encrypt belum diputuskan.
>
> - **CA internal** (dipakai sekarang): tidak perlu port 80, tidak perlu domain publik,
>   berlaku 825 hari. Harganya: tim OT harus memasang `ca.crt` secara manual, dan kalau
>   hilang harus dikirim ulang.
> - **Let's Encrypt**: TF6701 cukup memercayai trust store bawaan, tapi butuh domain publik,
>   port 80 atau DNS-01 untuk ACME, dan perpanjangan tiap 90 hari yang berarti broker harus
>   memuat ulang sertifikat tanpa memutus client.
>
> Untuk PoC yang berumur pendek, CA internal lebih sedikit bagian yang bisa gagal.

---

## 2. Instalasi stack

```bash
git clone <repo> tynext-poc && cd tynext-poc

cp .env.example .env
# Isi: RAIN_BROKER_HOST, RAIN_MQTT_URL, dan semua password.
# Buat password acak:  openssl rand -base64 24
chmod 600 .env

make certs     # CA internal + sertifikat broker (SAN dari RAIN_BROKER_HOST)
make up        # emqx + postgres → bootstrap user → migrasi → ingest
```

`make up` berhenti dengan daftar container dan statusnya. Semuanya harus `healthy`.

Verifikasi cepat:

```bash
make ps
curl -s 127.0.0.1:8080/healthz          # {"status":"ok",...}
make scenario-1                          # koneksi TLS + verifikasi sertifikat
make scenario-2                          # semua yang harus ditolak, ditolak
```

File yang dikirim ke tim OT ada di `deploy/certs/out/ca.crt`.

---

## 3. Urutan hari uji

### Tahap A — smoke test dengan simulator (sebelum tim OT menyalakan PLC)

Membuktikan sisi cloud sehat, supaya kalau nanti ada masalah kita tahu itu bukan dari sini.

```bash
make reset-data
make scenario-1                          # harus PASS
make scenario-2                          # harus PASS
DURATION=5m make scenario-3              # kelengkapan singkat
make scenario-4                          # LWT
make scenario-5                          # replay
make scenario-6                          # restart
```

Keenamnya harus PASS sebelum melangkah. Kalau ada yang FAIL, perbaiki dulu — jangan
menyalakan PLC di atas stack yang belum sehat.

### Tahap B — ganti sumber ke CX5240 sungguhan

1. Pastikan simulator berhenti:
   ```bash
   docker compose --env-file .env -f deploy/docker-compose.yml --profile dev stop simulator
   pkill -f cmd/simulator || true
   ```
2. `make reset-data` supaya angka hari uji tidak tercampur data simulator.
3. Minta tim OT menyalakan koneksi TF6701 dengan setelan di [HANDOFF_OT.md](HANDOFF_OT.md).
4. Pantau sambungan masuk:
   ```bash
   docker exec rain-emqx emqx ctl clients list
   docker logs -f rain-ingest
   ```
5. Begitu pesan pertama masuk, periksa bentuknya sebelum menunggu lama:
   ```bash
   make check WINDOW="5 minutes"
   docker compose --env-file .env -f deploy/docker-compose.yml exec -T postgres \
     psql -U rain -d rain -c "SELECT topic, reason FROM ingest_rejects ORDER BY id DESC LIMIT 10;"
   ```
   **`ingest_rejects` kosong berarti formatnya cocok.** Kalau terisi, alasannya langsung
   menunjuk bedanya — paling sering format `ts` atau nama tag.

### Tahap C — jalankan skenario dengan sumber sungguhan

| Skenario | Perintah | Catatan |
|---|---|---|
| 01 koneksi TLS | `make scenario-1` | Jalankan dengan kredensial `ems-pv-01` |
| 02 uji negatif | `make scenario-2` | Aman dijalankan kapan saja, tidak mengganggu data |
| 03 kelengkapan | `make scenario-3` | **Butuh 1 jam.** Lewati langkah simulatornya; lihat §3.1 |
| 04 LWT | `make scenario-4` | Tim OT mencabut kabel jaringan EMS, bukan `docker network disconnect` |
| 05 replay | `make scenario-5` | Tim OT memutus jaringan 60 detik lalu menyambung lagi |
| 06 restart | `make scenario-6` | Hanya menyentuh container kita, PLC tidak perlu diapa-apakan |

#### 3.1 Skenario 03 dengan PLC sungguhan

Skrip `03-completeness.sh` menjalankan simulator sendiri, jadi dengan PLC sungguhan
kelengkapan dihitung manual. PLC tidak menulis count-file, jadi pembandingnya adalah
`seq` yang ia kirim sendiri:

```sql
-- Jalankan setelah 1 jam data mengalir.
SELECT d.code,
       count(DISTINCT t.ts)                          AS pesan_masuk,
       3600                                          AS pesan_diharapkan,
       round(100.0 * count(DISTINCT t.ts) / 3600, 2) AS persen
FROM devices d
JOIN telemetry t ON t.device_id = d.id
WHERE t.ts > now() - interval '1 hour'
GROUP BY d.code
ORDER BY persen;
```

Lalu `make check` untuk gap, lag, dan duplikat. Kriteria tetap sama: ≥ 99%.

Device class `breaker` dan `gen` tidak muncul di query ini karena mengirim state saja
tanpa metrics. Keaktifannya dibuktikan dari `device_state.ts` dan dari tidak adanya alarm
`STALE` untuk mereka selama jam tersebut:

```sql
SELECT d.code, count(*) AS stale_raised
FROM alarms a JOIN devices d ON d.id = a.device_id
WHERE a.code = 'STALE' AND a.raised_at > now() - interval '1 hour'
GROUP BY d.code ORDER BY 2 DESC;
```

---

## 4. Memantau saat berjalan

### Dashboard EMQX lewat SSH tunnel

```bash
ssh -N -L 18083:127.0.0.1:18083 user@<server>
# lalu buka http://127.0.0.1:18083  (user admin, password dari RAIN_EMQX_DASHBOARD_PASS)
```

Yang dilihat: jumlah client tersambung, pesan per detik, dan **Dropped** — kalau Dropped
naik, berarti ACL atau rate limit menolak sesuatu.

### Mengintip pesan mentah

Tanpa memasang apa pun, pakai probe di repo ini:

```bash
set -a && . ./.env && set +a
go run ./cmd/mqttprobe -action subscribe \
  -url "$RAIN_MQTT_URL" -ca deploy/certs/out/ca.crt \
  -user ingest -pass "$RAIN_INGEST_PASS" \
  -topic 'rain/pts/+/+/telemetry' -timeout 30s
```

Kalau `mqttx` tersedia dan lebih disukai:

```bash
mqttx sub -h "$RAIN_BROKER_HOST" -p 8883 --protocol mqtts \
  --ca deploy/certs/out/ca.crt -u ingest -P "$RAIN_INGEST_PASS" \
  -t 'rain/pts/#' -v
```

> `ingest` hanya boleh subscribe, jadi kredensial ini aman dipakai untuk mengintip.
> Jangan memakai kredensial `ems-pv-01` — ACL-nya melarang subscribe dan brokernya akan
> memutus koneksi.

### Log ingest

```bash
docker logs -f rain-ingest                      # semua
docker logs -f rain-ingest | grep -i ditolak    # hanya payload yang gagal validasi
```

### Metrics

```bash
curl -s 127.0.0.1:8080/metrics | grep -E '^rain_ingest' | grep -v bucket
```

Yang penting: `rain_ingest_rejected_total` (harus diam), `rain_ingest_duplicates_total`
(boleh naik, artinya dedup bekerja), `rain_ingest_lag_ms` dan `rain_ingest_broker_connected`.

### Query bukti

```bash
make check                      # jendela 1 jam
make check WINDOW="15 minutes"
```

---

## 5. Tabel kriteria lulus

Isi kolom hasil saat hari uji.

| # | Aspek | Kriteria | Dibuktikan oleh | Hasil | Angka | Catatan |
|---|---|---|---|---|---|---|
| 1 | Koneksi & keamanan | Tersambung TLS 8883 dengan kredensial sendiri | `make scenario-1` | ☐ PASS ☐ FAIL | | |
| 1 | | Tanpa kredensial / password salah ditolak | `make scenario-2` | ☐ PASS ☐ FAIL | | |
| 1 | | Publish di luar ACL ditolak | `make scenario-2` | ☐ PASS ☐ FAIL | | |
| 2 | Kelengkapan | Telemetry 1 s masuk DB ≥ 99% selama 1 jam | `make scenario-3` / §3.1 | ☐ PASS ☐ FAIL | ____ % | |
| 2 | | Tidak ada pesan valid yang ditolak schema | `ingest_rejects` | ☐ PASS ☐ FAIL | ____ baris | |
| 3 | Deteksi offline | LWT tercatat < 10 s | `make scenario-4` | ☐ PASS ☐ FAIL | ____ s | |
| 4 | Pemulihan | Replay berurutan, tanpa hilang & tanpa duplikat | `make scenario-5` | ☐ PASS ☐ FAIL | gap ____ | |
| 4 | | Restart container pulih otomatis | `make scenario-6` | ☐ PASS ☐ FAIL | ____ s | |

Angka yang terukur di lingkungan dev dengan simulator, pada stack yang dibangun dari
nol. Ini pembanding, bukan janji — jaringan site akan berbeda.

| Ukuran | Terukur | Skenario |
|---|---|---|
| Kelengkapan, run 5 menit | 100% — 5700/5700 pesan dari 19 device bermetrics | 03 |
| Payload rusak yang tertangkap | 79 disisipkan, 79 masuk `ingest_rejects`, 0 lolos | 03 |
| Duplikat yang dibuang | 99 pesan dikirim ulang, 0 baris ganda di database | 03 |
| Lag p95 | 0.7–1.0 s (sejalan dengan interval batch ingest 1 s) | 03 |
| Deteksi LWT | **8 s**, sama pada setiap percobaan | 04 |
| Replay setelah outage 60 s | 2075 pesan lewat buffer disk, gap terbesar 1.000 s, kelengkapan 100% | 05 |
| Pemulihan setelah restart broker | 2–9 s sejak perintah restart | 06 |
| Pemulihan setelah restart ingest | 2–3 s sejak perintah restart | 06 |

Sebaran pada pemulihan broker berasal dari berapa lama container EMQX butuh untuk naik,
bukan dari sisi client. Keduanya jauh di bawah batas 30 detik.

### Kalau deteksi LWT mepet

Dengan keepalive 5 s, broker diset `keepalive_multiplier = 1.25` dan
`keepalive_check_interval = 1s`, sehingga deteksi jatuh di sekitar 8 s. Default EMQX
(`1.5` dan `30s`) menghasilkan 9–10 s dan **gagal separuh percobaan**.

Kalau di jaringan site angkanya masih mepet, turunkan keepalive TF6701 ke 3 s —
itu memangkas deteksi ke sekitar 5 s. Jangan menaikkan `keepalive_multiplier` kembali.

---

## 6. Masalah yang mungkin muncul

| Gejala | Kemungkinan sebab | Langkah |
|---|---|---|
| TF6701 gagal connect, broker tidak mencatat apa pun | Outbound 8883 diblokir dari LAN EMS | Minta tim OT tes `openssl s_client -connect <host>:8883` dari jaringan EMS. Kalau buntu, siapkan fallback MQTT over WebSocket 443 (lihat `listeners.wss` di `deploy/emqx/emqx.conf`) |
| `CONNECT refused: bad user name or password` | Password salah atau user belum dibuat | `make bootstrap`, lalu cocokkan dengan yang dikirim ke tim OT |
| TLS gagal: `unknown authority` | `ca.crt` di TF6701 bukan yang sekarang | Kirim ulang `deploy/certs/out/ca.crt`. Sertifikat berubah setiap `make certs` membuat CA baru |
| TLS gagal: nama host tidak cocok | TF6701 menyambung ke IP, bukan ke nama di SAN | Pakai nama DNS, atau jalankan ulang `make certs` dengan `RAIN_BROKER_IP` terisi |
| Koneksi berhasil tapi langsung terputus saat publish | Topic di luar ACL | Lihat `docker logs rain-emqx \| grep -i authorization`. Topic harus persis `rain/pts/{class}/{deviceId}/{telemetry\|status\|alarm}` |
| `ingest_rejects` penuh alasan `/ts does not match pattern` | Format timestamp TF6701 berbeda | Harus ISO 8601 UTC dengan **tepat 3 digit** milidetik dan akhiran `Z` |
| `ingest_rejects` penuh alasan `unknown-device` | deviceId tidak ada di `contracts/devices.json` | Cocokkan daftar device dengan tim OT, lalu `make seed` |
| `ingest_rejects` penuh alasan `class-mismatch` | Segmen class di topic tidak cocok dengan device | Lihat tabel di `contracts/mqtt/topics.md` |
| Semua device dapat alarm `STALE` saat ingest baru jalan | Normal: tidak ada yang mengirim telemetry | Hilang sendiri begitu data masuk |
| `rain_ingest_db_errors_total` naik | PostgreSQL bermasalah | `docker logs rain-postgres`. Ingest menahan batch di memori dan mencoba lagi, jadi data tidak langsung hilang |

---

## 7. Rollback

PoC ini berdiri sendiri dan **tidak menyentuh plant sama sekali** — tidak ada downlink,
tidak ada perintah breaker atau genset. Membatalkannya tidak berisiko bagi operasi.

**Menghentikan sementara (data disimpan):**

```bash
make down
```

**Mundur ke versi sebelumnya:**

```bash
git checkout <commit-sebelumnya>
make migrate-down && make migrate     # skema turun lalu naik lagi, bersih
make up
```

**Membatalkan seluruhnya (data ikut hilang):**

```bash
make clean                   # compose down -v: volume emqx dan postgres dihapus
rm -rf deploy/certs/out      # CA dan kunci
```

Setelah ini tim OT hanya perlu mematikan koneksi TF6701. Tidak ada yang tertinggal di
sisi plant.

**Yang perlu dikabarkan saat rollback:** tim OT, supaya mereka tidak menunggu data dan
tidak menyangka PLC-nya yang bermasalah.
