# Apa yang harus kamu lakukan, berurutan

Checklist dari kondisi sekarang (kode selesai, enam skenario lulus di lokal) sampai PoC
dinyatakan lulus dan kodenya dipindah ke repo backend utama.

Urutannya disusun menurut **ketergantungan**, bukan menurut besar kecilnya pekerjaan.
Tahap 1 ada di depan karena jawabannya bisa memaksa pekerjaan ulang di tahap-tahap setelahnya.

Dokumen lain: [POC_RUNBOOK.md](POC_RUNBOOK.md) untuk menjalankan stack, dan
[HANDOFF_OT.md](HANDOFF_OT.md) untuk paket tim OT.

---

## Tahap 0 — Hari ini, di mesinmu (± 30 menit)

Tidak bergantung pada siapa pun. Kerjakan sekarang.

- [ ] **Inisialisasi git dan commit pertama.** Repo ini belum `git init`.
  ```bash
  cd ~/Documents/Project/tynext-poc
  git init -b main
  git add -A
  git status           # pastikan .env dan deploy/certs/out/ TIDAK muncul
  git commit           # pesan commit yang diusulkan ada di bawah
  ```
  Sebelum commit, pastikan `git status` tidak menampilkan `.env`, `deploy/certs/out/`,
  dan `scripts/scenarios/out/`. Ketiganya sudah di `.gitignore`, tapi cek sekali.

- [ ] **Baca ulang tiga berkas yang akan dilihat tim OT**, karena merekalah yang akan
      memakainya dan koreksi lebih murah sekarang daripada nanti:
  - [`contracts/mqtt/topics.md`](../contracts/mqtt/topics.md)
  - [`contracts/devices.json`](../contracts/devices.json) — terutama kolom `source`
  - [`contracts/tags/ems-pv-tags.md`](../contracts/tags/ems-pv-tags.md)

- [ ] **Jalankan sekali sendiri dari nol**, supaya kamu hafal alurnya sebelum hari uji:
  ```bash
  make clean && make certs && make up
  make scenario-1 && make scenario-2
  DURATION=5m make scenario-3
  ```

**Selesai kalau:** ada commit pertama, dan kamu sudah melihat sendiri ketiga skenario itu PASS.

---

## Tahap 1 — Kirim dua pertanyaan ke tim OT HARI INI (± 15 menit)

Dua ini paling berisiko. Keduanya punya waktu tunggu panjang dan **bisa memaksa pekerjaan
ulang**, jadi kirim sekarang walaupun hal lain belum siap.

- [ ] **Pertanyaan 1 — Apakah outbound ke port 8883 diizinkan dari LAN EMS site?**

  Minta tim OT menjalankan ini dari jaringan EMS:
  ```bash
  openssl s_client -connect <host-atau-ip-sementara>:8883 -brief
  ```
  Atau sekadar `telnet <host> 8883` kalau openssl tidak ada.

  - **Kalau boleh** → lanjut seperti rencana.
  - **Kalau diblokir** → perlu fallback **MQTT over WebSocket port 443**. Kerangkanya sudah
    ada di [`deploy/emqx/emqx.conf`](../deploy/emqx/emqx.conf) pada `listeners.wss.default`
    (sekarang `enable = false`), tapi menyalakannya berarti: terbitkan port 443, sesuaikan
    `RAIN_MQTT_URL` jadi `wss://...`, dan **pastikan TF6701 memang mendukung MQTT over
    WebSocket** — itu yang harus ditanyakan sekalian. Sisihkan waktu untuk ini.

- [ ] **Pertanyaan 2 — Bisakah TF6701 menghasilkan `ts` persis `2026-10-12T04:30:00.000Z`?**

  UTC, **tepat 3 digit** milidetik, akhiran `Z`. Yang ditolak: tanpa milidetik, 6 digit
  pecahan, offset `+07:00`, atau pemisah spasi.

  - **Kalau bisa** → tidak ada yang berubah.
  - **Kalau tidak bisa** → beri tahu saya bentuk persisnya, lalu pola `ts` di ketiga schema
    ([`contracts/mqtt/payloads/`](../contracts/mqtt/payloads/)) dan `TimeLayout` di
    [`internal/contract/payload.go`](../internal/contract/payload.go) dilonggarkan. Ini
    pekerjaan setengah jam **asal dikerjakan sebelum hari uji**. Kalau baru ketahuan saat
    hari uji, seluruh telemetry ditolak dan harinya hangus.

Isi lengkapnya ada di [HANDOFF_OT.md §7](HANDOFF_OT.md) butir 4 dan 5.

**Selesai kalau:** kedua pertanyaan sudah terkirim dan ada tanggal jawaban yang disepakati.

---

## Tahap 2 — Keputusan yang jadi milikmu (± 1 jam + menunggu)

Empat keputusan ini tidak perlu tim OT, tapi memblokir tahap 3.

- [ ] **Provider dan region VPS.** Yang penting latensi wajar ke Kalimantan. Spesifikasi
      minimum ada di [POC_RUNBOOK.md §1](POC_RUNBOOK.md): Ubuntu 24.04, 2 vCPU, 4 GB, 40 GB.

- [ ] **Domain broker.** Satu A record, misal `mqtt-poc.<domain>`.

      Kabar baik: `make certs` **memakai ulang CA yang sudah ada**, jadi kalau nanti domainnya
      berubah, hanya `server.crt` yang dibuat ulang — `ca.crt` yang sudah dikirim ke tim OT
      tetap sah dan tidak perlu dikirim ulang. Jadi ini tidak sepenuhnya memblokir, tapi tetap
      lebih rapi diputuskan di depan.

- [ ] **CA internal atau Let's Encrypt.** Perbandingannya ada di
      [POC_RUNBOOK.md §1](POC_RUNBOOK.md). Untuk PoC yang berumur pendek, CA internal lebih
      sedikit bagian yang bisa gagal — itu yang terpasang sekarang. Kalau tetap mau Let's
      Encrypt, putuskan sekarang karena butuh port 80 atau DNS-01.

- [ ] **Mutual TLS atau cukup username/password.** Sekarang broker diset `verify_none`
      dan sertifikat client sudah dibuat tapi tidak dipakai. Kalau mau mutual TLS, ubah
      `verify = verify_peer` dan `fail_if_no_peer_cert = true` di
      [`deploy/emqx/emqx.conf`](../deploy/emqx/emqx.conf), lalu kirim
      `client-ems-pv-01.crt` dan `.key` ke tim OT.

**Selesai kalau:** keempatnya tertulis, dan DNS sudah mengarah ke IP VPS.

---

## Tahap 3 — Siapkan VPS (± 1–2 jam)

Butuh: keputusan tahap 2. Langkah rincinya di [POC_RUNBOOK.md §1–2](POC_RUNBOOK.md).

- [ ] Provision VPS, pasang `docker.io docker-compose-v2 make git jq curl openssl`.
- [ ] Kunci SSH: `PasswordAuthentication no`, `PermitRootLogin no`.
- [ ] Firewall: hanya 22, 8883, dan 443. Semua port lain sudah dibatasi ke `127.0.0.1`
      oleh docker-compose.
- [ ] Clone repo, `cp .env.example .env`, isi `RAIN_BROKER_HOST`, `RAIN_MQTT_URL`, dan
      semua password. Buat password acak:
      ```bash
      openssl rand -base64 24
      chmod 600 .env
      ```
- [ ] `make certs && make up`
- [ ] `make scenario-1 && make scenario-2` → keduanya harus **PASS** di VPS, bukan hanya
      di laptop.

**Selesai kalau:** skenario 01 dan 02 PASS dari VPS, dan `curl -s 127.0.0.1:8080/healthz`
menjawab `{"status":"ok",...}`.

---

## Tahap 4 — Kirim paket ke tim OT (± 30 menit)

Butuh: tahap 3 selesai, karena `ca.crt` dan host finalnya baru ada setelah itu.

- [ ] Kirim lewat jalur biasa (email / drive):
  - `deploy/certs/out/ca.crt` ← **dari VPS, bukan dari laptop**
  - host + port: `mqtt-poc.<domain>` : 8883
  - username: `ems-pv-01`
  - [`docs/HANDOFF_OT.md`](HANDOFF_OT.md)
  - [`contracts/mqtt/topics.md`](../contracts/mqtt/topics.md)
  - [`contracts/mqtt/payloads/`](../contracts/mqtt/payloads/) dan
    [`contracts/mqtt/examples/`](../contracts/mqtt/examples/)
  - [`contracts/devices.json`](../contracts/devices.json)
  - [`contracts/tags/ems-pv-tags.md`](../contracts/tags/ems-pv-tags.md)

- [ ] **Kirim password `ems-pv-01` lewat jalur terpisah** — WhatsApp, telepon, atau password
      manager. Jangan di email yang sama dengan sisanya.

- [ ] Isi tabel kontak di [HANDOFF_OT.md §9](HANDOFF_OT.md) dan sepakati **jadwal + PIC**
      hari uji. Skenario 04 dan 05 butuh orang di site yang mencabut dan menyambung kabel
      jaringan EMS.

- [ ] Tagih enam permintaan sisanya di [HANDOFF_OT.md §7](HANDOFF_OT.md): tag list final,
      kapasitas buffer TF6701, versi TwinCAT/TF6701, dan konfirmasi device ke-35.

**Selesai kalau:** tim OT mengonfirmasi menerima `ca.crt` dan kredensialnya, dan tanggal
hari uji sudah terkunci.

---

## Tahap 5 — Saat jawaban tim OT masuk (bervariasi)

Kerjakan begitu tiap jawaban datang, jangan ditunggu lengkap.

- [ ] **Tag list final datang** → perbarui
      [`contracts/tags/ems-pv-tags.md`](../contracts/tags/ems-pv-tags.md), hapus tanda
      DRAFT, lalu sesuaikan pemetaan di [`internal/sim/messages.go`](../internal/sim/messages.go)
      fungsi `tags()`. Test `TestTagNamesMatchTheDraftList` akan gagal sampai keduanya cocok —
      itu memang gunanya.

- [ ] **Kapasitas buffer TF6701 datang** → ganti default `--buffer-max` di
      [`cmd/simulator/main.go`](../cmd/simulator/main.go) (sekarang 72 jam, asumsi), dan
      sesuaikan lebar outage di skenario 05 supaya menguji batas yang nyata.

- [ ] **Format `ts` tidak bisa 3 digit** → longgarkan pola di ketiga schema dan `TimeLayout`
      di [`internal/contract/payload.go`](../internal/contract/payload.go). Jalankan
      `make test` — ada test yang memang menguji penolakan format lain, dan harus ikut
      diperbarui.

- [ ] **Outbound 8883 diblokir** → nyalakan `listeners.wss.default` di
      [`deploy/emqx/emqx.conf`](../deploy/emqx/emqx.conf), terbitkan port 443 di
      [`deploy/docker-compose.yml`](../deploy/docker-compose.yml), ganti `RAIN_MQTT_URL`
      jadi `wss://...`, lalu **jalankan ulang skenario 01 dan 02** lewat jalur baru itu.

- [ ] **Device ke-35 ada** → tambahkan ke [`contracts/devices.json`](../contracts/devices.json),
      perbarui angka 34 di `TestDeviceInventory`, lalu `make seed`.

- [ ] Setiap perubahan di atas: `make test && make lint`, lalu jalankan ulang
      `DURATION=5m make scenario-3` untuk memastikan tidak ada yang rusak.

**Selesai kalau:** tidak ada lagi tanda `TBC` yang menyangkut hari uji. Cari sisanya dengan:
```bash
grep -rn "TBC" --include='*.go' --include='*.md' --include='*.conf' --include='*.json' .
```

---

## Tahap 6 — Hari uji

Urutan lengkap ada di [POC_RUNBOOK.md §3](POC_RUNBOOK.md). Ringkasnya:

- [ ] **Pagi, sebelum tim OT menyalakan PLC** — smoke test dengan simulator. Keenam skenario
      harus PASS. Kalau ada yang FAIL, perbaiki dulu; jangan menyalakan PLC di atas stack
      yang belum sehat.
- [ ] `make reset-data` supaya angka hari uji tidak tercampur data simulator.
- [ ] Minta tim OT menyalakan koneksi TF6701.
- [ ] **Periksa `ingest_rejects` dalam 5 menit pertama.** Kosong berarti formatnya cocok.
      Terisi berarti alasannya langsung menunjuk bedanya — jangan menunggu satu jam untuk
      tahu formatnya salah.
- [ ] Jalankan skenario 01–06 dengan sumber sungguhan. Skenario 03 butuh satu jam penuh;
      cara menghitungnya ada di [POC_RUNBOOK.md §3.1](POC_RUNBOOK.md).
- [ ] Isi tabel kriteria lulus di [POC_RUNBOOK.md §5](POC_RUNBOOK.md) sambil jalan, bukan
      dari ingatan setelahnya.

**Selesai kalau:** kedelapan baris di tabel kriteria lulus terisi.

---

## Tahap 7 — Setelah PoC lulus

- [ ] Pindahkan ke repo backend utama `rain-ems`, sesuai
      `docs/IMPLEMENTATION_PLAN.md` Fase 1, 4, dan 5. Yang dipindah:
      `contracts/`, `internal/contract/`, `internal/mqttx/`, `internal/ingest/`,
      `internal/sim/`, `migrations/`, `deploy/emqx/`, `deploy/certs/`.
      Ganti prefix import `github.com/tynext/tynext-poc` → module repo utama.
- [ ] Hal yang sengaja ditunda di PoC dan harus dihidupkan di produksi:
  - partisi harian `telemetry` — lihat `-- TODO(prod)` di
    [`migrations/000002_telemetry.up.sql`](../migrations/000002_telemetry.up.sql)
  - retensi data dan `pg_partman` + `pg_cron`
  - backup PostgreSQL
  - `keepalive_check_interval` di EMQX: `1s` wajar untuk satu client, naikkan lagi kalau
    jumlah client bertambah banyak
- [ ] Simpan hasil hari uji (tabel kriteria lulus yang sudah terisi) sebagai lampiran
      ke klien.

---

## Pesan commit yang diusulkan untuk tahap 0

```
feat: PoC cloud CX5240 → EMQX → ingest → PostgreSQL

Kontrak MQTT (topic, JSON Schema 2020-12, 34 device, draft tag list),
broker EMQX 5.8 TLS 8883 dengan ACL default-deny, migrasi PostgreSQL 16,
ingest Go dengan dedup CopyFrom + ON CONFLICT dan stale detector, simulator
stand-in CX5240 dengan store-and-forward, serta skenario uji 01-06.

Setelan broker menyimpang dari default EMQX: keepalive_multiplier 1.25 dan
keepalive_check_interval 1s. Default (1.5 / 30s) membuat deteksi LWT jatuh di
9-10 s dan gagal kriteria < 10 s pada separuh percobaan.

Kelengkapan diukur pada 19 device yang membawa metrics; 15 device class
breaker/gen mengirim state saja sehingga tidak ada baris telemetry per pesan.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
```
