#!/usr/bin/env bash
# Skenario 05 — pemulihan setelah koneksi putus.
#
# Simulator diputus selama OUTAGE detik di tengah run. Selama putus ia mengantre
# ke disk dengan ts asli, lalu me-replay berurutan saat tersambung lagi.
#
# Membuktikan kriteria lulus #4: tidak ada data hilang, tidak ada duplikat.
# shellcheck source=_lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

OUTAGE="${OUTAGE:-60s}"
OUTAGE_AFTER="${OUTAGE_AFTER:-30s}"
DURATION="${DURATION:-180s}"

scenario_begin "05 replay (outage ${OUTAGE})"
require_stack
build_bin simulator

queue="$OUT/05-sim.queue"
counts="$OUT/05-counts.json"
rm -f "$queue" "$queue.offset" "$counts"

step "Mengosongkan tabel data"
reset_data

step "Menjalankan simulator ${DURATION} dengan outage ${OUTAGE} pada detik ${OUTAGE_AFTER}"
RAIN_MQTT_USER="simulator-dev" RAIN_MQTT_PASS="$RAIN_SIM_PASS" RAIN_LOG_FORMAT=json \
  "$BIN/simulator" \
    --duration "$DURATION" --interval 1s \
    --outage "$OUTAGE" --outage-after "$OUTAGE_AFTER" \
    --queue "$queue" --count-file "$counts" \
    > "$OUT/05-simulator.log" 2>&1
sim_rc=$?
assert "$([[ $sim_rc -eq 0 ]] && echo 1 || echo 0)" "Simulator selesai tanpa error" "kode keluar $sim_rc"

step "Menunggu batch terakhir ditulis ingest"
sleep 5

replayed=$(jq -r '.totals.replayed' "$counts")
queued=$(jq -r '.totals.queued' "$counts")
sent=$(jq -r '.totals.telemetry' "$counts")

assert "$([[ "$replayed" -gt 0 ]] && echo 1 || echo 0)" \
  "Ada pesan yang benar-benar lewat buffer disk" "${replayed} pesan di-replay"

assert "$([[ "$queued" == "0" ]] && echo 1 || echo 0)" \
  "Buffer kosong saat run berakhir" "sisa antrean: ${queued}"

step "Memeriksa gap per device per detik"
# Jarak antar pesan berurutan harus 1 detik. Kalau replay kehilangan sesuatu,
# lubangnya muncul di sini — termasuk lubang selama outage.
gaps=$(sql "
  WITH pesan AS (
    SELECT DISTINCT device_id, ts FROM telemetry
  ), berurutan AS (
    SELECT device_id, ts, lag(ts) OVER (PARTITION BY device_id ORDER BY ts) AS prev FROM pesan
  )
  SELECT count(*) FROM berurutan
  WHERE prev IS NOT NULL AND ts - prev > interval '1.5 seconds';")
assert "$([[ "$gaps" == "0" ]] && echo 1 || echo 0)" \
  "Tidak ada gap > 1.5 s di seluruh rentang run" "gap ditemukan: ${gaps}"

worst_gap=$(sql "
  WITH pesan AS (
    SELECT DISTINCT device_id, ts FROM telemetry
  ), berurutan AS (
    SELECT device_id, ts, lag(ts) OVER (PARTITION BY device_id ORDER BY ts) AS prev FROM pesan
  )
  SELECT coalesce(round(max(EXTRACT(EPOCH FROM (ts - prev)))::numeric, 3), 0) FROM berurutan WHERE prev IS NOT NULL;")

step "Memeriksa duplikat"
dupes=$(sql "SELECT count(*) FROM (SELECT device_id, metric, ts FROM telemetry GROUP BY 1,2,3 HAVING count(*) > 1) x;")
assert "$([[ "$dupes" == "0" ]] && echo 1 || echo 0)" \
  "Tidak ada baris telemetry ganda setelah replay" "grup duplikat: ${dupes}"

step "Memeriksa bahwa pesan dari masa outage memakai ts aslinya"
# Inti store-and-forward: lubangnya diisi dengan waktu yang diukur plant, bukan
# waktu kita menyusulnya. Jadi di jendela outage harus ada baris yang ts-nya
# jauh lebih awal daripada received_at.
late=$(sql "
  SELECT count(*) FROM telemetry
  WHERE received_at - ts > interval '20 seconds';")
assert "$([[ "$late" -gt 0 ]] && echo 1 || echo 0)" \
  "Ada baris dengan ts jauh lebih awal dari received_at" \
  "${late} baris ditulis belakangan tapi membawa ts asli"

max_lag=$(sql "SELECT coalesce(round(max(EXTRACT(EPOCH FROM (received_at - ts)))::numeric, 1), 0) FROM telemetry;")

step "Memeriksa kelengkapan per device"
db_counts="$OUT/05-db-counts.txt"
psql_q -c "
  SELECT d.code || ' ' || count(DISTINCT t.ts)
  FROM devices d LEFT JOIN telemetry t ON t.device_id = d.id
  GROUP BY d.code ORDER BY d.code;" > "$db_counts"
report="$(python3 "$REPO/scripts/scenarios/completeness.py" "$counts" "$db_counts" 100)"
overall=$(jq -r '.overall' <<<"$report")
below=$(jq -r '.below | length' <<<"$report")

assert "$([[ "$below" == "0" ]] && echo 1 || echo 0)" \
  "Setiap device bermetrics lengkap 100% setelah replay" \
  "kelengkapan keseluruhan ${overall}%, device di bawah 100%: ${below}"

evidence "pesan dikirim: ${sent}, di antaranya ${replayed} lewat buffer disk"
evidence "gap terbesar antar pesan: ${worst_gap} s"
evidence "lag terbesar (received_at - ts): ${max_lag} s — ini lebar outage-nya"
evidence "kelengkapan setelah replay: ${overall}%"

scenario_end
