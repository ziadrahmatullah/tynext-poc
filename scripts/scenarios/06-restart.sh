#!/usr/bin/env bash
# Skenario 06 — pemulihan otomatis setelah container di-restart.
#
# Broker dan ingest dimatikan-hidupkan satu per satu sementara simulator terus
# mengirim. Yang diukur: berapa lama sampai data mengalir lagi, tanpa ada yang
# perlu dijalankan manual.
#
# Membuktikan kriteria lulus #4 bagian kedua.
# shellcheck source=_lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

LIMIT_SECONDS="${LIMIT_SECONDS:-30}"

scenario_begin "06 restart"
require_stack

step "Mengosongkan tabel data"
reset_data

step "Menjalankan simulator di container sebagai sumber data yang terus mengalir"
"${COMPOSE[@]}" --profile dev up -d simulator >/dev/null 2>&1
cleanup() {
  "${COMPOSE[@]}" --profile dev stop simulator >/dev/null 2>&1
  "${COMPOSE[@]}" --profile dev rm -f simulator >/dev/null 2>&1
}
trap cleanup EXIT

# baris_sekarang menghitung baris telemetry; kita pakai pertumbuhannya sebagai
# bukti data mengalir lagi.
baris_sekarang() { sql "SELECT count(*) FROM telemetry;"; }

# tunggu_tumbuh <batas detik> <baseline> [mulai-dari-epoch] → cetak detik yang
# dibutuhkan. Argumen ketiga ada supaya waktu pemulihan dihitung sejak perintah
# restart DIJALANKAN, bukan sejak ia selesai — "docker compose restart" sendiri
# menunggu container siap, jadi mengukur setelahnya akan menyembunyikan
# sebagian besar downtime-nya.
tunggu_tumbuh() {
  local limit="$1" baseline="$2" start now
  start="${3:-$(date +%s)}"
  while :; do
    if [[ "$(baris_sekarang)" -gt "$baseline" ]]; then
      now=$(date +%s); echo $((now - start)); return 0
    fi
    now=$(date +%s)
    if (( now - start >= limit )); then echo $((now - start)); return 1; fi
    sleep 1
  done
}

step "Menunggu aliran data awal"
awal=$(tunggu_tumbuh 40 0)
awal_rc=$?
assert "$([[ $awal_rc -eq 0 ]] && echo 1 || echo 0)" "Data mengalir sebelum restart" "butuh ${awal} s"
if [[ $awal_rc -ne 0 ]]; then scenario_end; fi
sleep 5

# ── Restart broker ────────────────────────────────────────────────────────────
step "Restart container EMQX"
before_emqx=$(baris_sekarang)
emqx_t0=$(date +%s)
"${COMPOSE[@]}" restart emqx >"$OUT/06-restart-emqx.log" 2>&1
emqx_rc=$?
assert "$([[ $emqx_rc -eq 0 ]] && echo 1 || echo 0)" "docker compose restart emqx berhasil" ""

pulih_emqx=$(tunggu_tumbuh "$((LIMIT_SECONDS + 30))" "$before_emqx" "$emqx_t0")
emqx_wait_rc=$?
assert "$([[ $emqx_wait_rc -eq 0 ]] && echo 1 || echo 0)" \
  "Data mengalir lagi setelah broker restart" "butuh ${pulih_emqx} s"
assert "$([[ $emqx_wait_rc -eq 0 && $pulih_emqx -lt $LIMIT_SECONDS ]] && echo 1 || echo 0)" \
  "Pemulihan broker ${pulih_emqx} s < ${LIMIT_SECONDS} s" \
  "simulator dan ingest reconnect sendiri dengan backoff"

sleep 5

# ── Restart ingest ────────────────────────────────────────────────────────────
step "Restart container ingest"
before_ingest=$(baris_sekarang)
ingest_t0=$(date +%s)
"${COMPOSE[@]}" restart ingest >"$OUT/06-restart-ingest.log" 2>&1
ingest_rc=$?
assert "$([[ $ingest_rc -eq 0 ]] && echo 1 || echo 0)" "docker compose restart ingest berhasil" ""

pulih_ingest=$(tunggu_tumbuh "$((LIMIT_SECONDS + 30))" "$before_ingest" "$ingest_t0")
ingest_wait_rc=$?
assert "$([[ $ingest_wait_rc -eq 0 ]] && echo 1 || echo 0)" \
  "Data mengalir lagi setelah ingest restart" "butuh ${pulih_ingest} s"
assert "$([[ $ingest_wait_rc -eq 0 && $pulih_ingest -lt $LIMIT_SECONDS ]] && echo 1 || echo 0)" \
  "Pemulihan ingest ${pulih_ingest} s < ${LIMIT_SECONDS} s" \
  "clean session false membuat broker menahan pesan QoS 1 selama ingest mati"

step "Memeriksa tidak ada duplikat akibat restart"
sleep 5
dupes=$(sql "SELECT count(*) FROM (SELECT device_id, metric, ts FROM telemetry GROUP BY 1,2,3 HAVING count(*) > 1) x;")
assert "$([[ "$dupes" == "0" ]] && echo 1 || echo 0)" \
  "Tidak ada baris ganda meski pesan QoS 1 dikirim ulang broker" "grup duplikat: ${dupes}"

step "Memeriksa kesehatan akhir"
health=$(curl -s --max-time 5 "http://${INGEST_HTTP}/healthz" | jq -r '.status' 2>/dev/null)
assert "$([[ "$health" == "ok" ]] && echo 1 || echo 0)" "/healthz kembali ok tanpa intervensi" "status=${health:-tidak terjawab}"

unhealthy=$("${COMPOSE[@]}" ps emqx postgres ingest --format '{{.Name}} {{.Health}}' | grep -cv ' healthy$' || true)
assert "$([[ "$unhealthy" == "0" ]] && echo 1 || echo 0)" \
  "Semua container kembali healthy" "container tidak healthy: ${unhealthy}"

total=$(baris_sekarang)
evidence "pemulihan setelah restart broker: ${pulih_emqx} s sejak perintah restart (batas ${LIMIT_SECONDS} s)"
evidence "pemulihan setelah restart ingest: ${pulih_ingest} s sejak perintah restart (batas ${LIMIT_SECONDS} s)"
evidence "total baris telemetry pada akhir skenario: ${total}"
evidence "tidak ada perintah manual selain 'docker compose restart'"

scenario_end
