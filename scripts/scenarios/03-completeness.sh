#!/usr/bin/env bash
# Skenario 03 — kelengkapan.
#
# Jalankan simulator selama DURATION, lalu bandingkan jumlah pesan yang benar
# benar dikirim (count-file) dengan yang tersimpan di database.
#
# Membuktikan kriteria lulus #2: telemetry 1 s masuk DB >= 99%, dan tidak ada
# pesan valid yang ditolak schema.
#
#   DURATION=5m bash scripts/scenarios/03-completeness.sh
# shellcheck source=_lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

DURATION="${DURATION:-60m}"
INVALID_RATE="${INVALID_RATE:-0.01}"
DUP_RATE="${DUP_RATE:-0.01}"
THRESHOLD="${THRESHOLD:-99.0}"

scenario_begin "03 completeness (durasi ${DURATION})"
require_stack
build_bin simulator

counts="$OUT/03-counts.json"
queue="$OUT/03-sim.queue"
rm -f "$queue" "$queue.offset" "$counts"

step "Mengosongkan tabel data supaya hitungan berdiri sendiri"
reset_data

# Counter ingest bersifat kumulatif, jadi yang bermakna adalah selisihnya.
dupes_before=$(ingest_metric rain_ingest_duplicates_total)
dupes_before=${dupes_before:-0}

step "Menjalankan simulator ${DURATION} (invalid-rate=${INVALID_RATE}, dup-rate=${DUP_RATE})"
RAIN_MQTT_USER="simulator-dev" RAIN_MQTT_PASS="$RAIN_SIM_PASS" RAIN_LOG_FORMAT=json \
  "$BIN/simulator" \
    --duration "$DURATION" --interval 1s \
    --invalid-rate "$INVALID_RATE" --dup-rate "$DUP_RATE" \
    --queue "$queue" --count-file "$counts" \
    > "$OUT/03-simulator.log" 2>&1
sim_rc=$?
assert "$([[ $sim_rc -eq 0 ]] && echo 1 || echo 0)" "Simulator selesai tanpa error" "kode keluar $sim_rc"

step "Menunggu batch terakhir ditulis ingest"
sleep 5

if [[ ! -f "$counts" ]]; then
  assert 0 "count-file ada" "$counts tidak ditulis"
  scenario_end
fi

# Bandingkan per device. Analisisnya ada di completeness.py supaya skrip ini
# tetap bisa dibaca, dan supaya aturan "device state-only tidak diberi skor"
# tertulis di satu tempat.
db_counts="$OUT/03-db-counts.txt"
psql_q -c "
  SELECT d.code || ' ' || count(DISTINCT t.ts)
  FROM devices d LEFT JOIN telemetry t ON t.device_id = d.id
  GROUP BY d.code ORDER BY d.code;" > "$db_counts"

report="$(python3 "$REPO/scripts/scenarios/completeness.py" "$counts" "$db_counts" "$THRESHOLD")"

overall=$(jq -r '.overall' <<<"$report")
total_sent=$(jq -r '.totalSent' <<<"$report")
total_stored=$(jq -r '.totalStored' <<<"$report")
scored=$(jq -r '.scoredDevices' <<<"$report")
worst=$(jq -r '"\(.worst.code) \(.worst.stored)/\(.worst.sent) = \(.worst.pct)%"' <<<"$report")
below=$(jq -r '.below | length' <<<"$report")
corrupted=$(jq -r '.corrupted' <<<"$report")
duplicated=$(jq -r '.duplicated' <<<"$report")
queued=$(jq -r '.queued' <<<"$report")
skipped=$(jq -r '.skipped | length' <<<"$report")

assert "$(awk -v a="$overall" -v b="$THRESHOLD" 'BEGIN{print (a>=b)?1:0}')" \
  "Kelengkapan keseluruhan ${overall}% >= ${THRESHOLD}%" \
  "${total_stored}/${total_sent} pesan dari ${scored} device yang membawa metrics"

assert "$([[ "$below" == "0" ]] && echo 1 || echo 0)" \
  "Tidak ada device di bawah ${THRESHOLD}%" \
  "device terburuk: ${worst}"

assert "$([[ "$queued" == "0" ]] && echo 1 || echo 0)" \
  "Buffer simulator kosong saat berhenti" "sisa antrean: ${queued}"

step "Memeriksa ingest_rejects: hanya pesan yang sengaja dirusak"
rejects=$(sql "SELECT count(*) FROM ingest_rejects;")
assert "$([[ "$rejects" == "$corrupted" ]] && echo 1 || echo 0)" \
  "ingest_rejects berisi tepat pesan yang sengaja dirusak" \
  "reject=${rejects}, sengaja dirusak=${corrupted}"

non_schema=$(sql "SELECT count(*) FROM ingest_rejects WHERE reason NOT LIKE 'schema:%';")
assert "$([[ "$non_schema" == "0" ]] && echo 1 || echo 0)" \
  "Semua reject beralasan schema, bukan topic atau device tak dikenal" \
  "reject non-schema: ${non_schema}"

step "Memeriksa duplikat di database"
dupes=$(sql "SELECT count(*) FROM (SELECT device_id, metric, ts FROM telemetry GROUP BY 1,2,3 HAVING count(*) > 1) x;")
assert "$([[ "$dupes" == "0" ]] && echo 1 || echo 0)" \
  "Tidak ada baris telemetry ganda meski ${duplicated} pesan dikirim ulang" \
  "grup duplikat: ${dupes}"

dupes_after=$(ingest_metric rain_ingest_duplicates_total)
dupes_after=${dupes_after:-0}
discarded=$(awk -v a="$dupes_after" -v b="$dupes_before" 'BEGIN{printf "%d", a-b}')
evidence "duplikat yang dibuang ingest selama run ini: ${discarded} (dikirim ulang ${duplicated})"
evidence "kelengkapan ${overall}% — ${total_stored}/${total_sent} pesan, ${scored} device bermetrics"
evidence "${skipped} device class breaker/gen mengirim state saja, tidak diberi skor"
evidence "reject: ${rejects} (semuanya payload yang sengaja dirusak)"

lag=$(sql "SELECT coalesce(round((percentile_cont(0.95) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (received_at - ts)) * 1000))::numeric, 1), 0) FROM telemetry;")
evidence "lag p95: ${lag} ms"

scenario_end
