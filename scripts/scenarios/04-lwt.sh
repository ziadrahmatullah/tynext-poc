#!/usr/bin/env bash
# Skenario 04 — deteksi offline lewat Last Will.
#
# Simulator diputus dari jaringan tanpa DISCONNECT, persis seperti kabel yang
# dicabut. Yang diukur: berapa lama sampai device_status.online jadi false.
#
# Membuktikan kriteria lulus #3: tercatat di DB < 10 s.
# shellcheck source=_lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

LIMIT_SECONDS="${LIMIT_SECONDS:-10}"
NETWORK="${NETWORK:-rain-poc_default}"

scenario_begin "04 lwt"
require_stack
build_bin simulator

step "Mengosongkan tabel data"
reset_data

queue="$OUT/04-sim.queue"
rm -f "$queue" "$queue.offset"

step "Menjalankan simulator di container supaya jaringannya bisa diputus"
"${COMPOSE[@]}" --profile dev up -d simulator >/dev/null 2>&1
container="rain-simulator"

# Tunggu sampai status online tercatat.
online_wait=$(wait_until 30 \
  "SELECT online FROM device_status s JOIN devices d ON d.id=s.device_id WHERE d.code='ems-pv-01';" "t")
online_rc=$?
assert "$([[ $online_rc -eq 0 ]] && echo 1 || echo 0)" \
  "Status online=true tercatat setelah simulator connect" "butuh ${online_wait} s"

if [[ $online_rc -ne 0 ]]; then
  "${COMPOSE[@]}" --profile dev stop simulator >/dev/null 2>&1
  scenario_end
fi

step "Memastikan telemetry mengalir sebelum jaringan dicabut"
sleep 6
rows=$(sql "SELECT count(*) FROM telemetry;")
assert "$([[ "$rows" -gt 0 ]] && echo 1 || echo 0)" "Telemetry mengalir" "${rows} baris sebelum pemutusan"

step "Memutus jaringan container simulator (tanpa DISCONNECT)"
cut_at=$(date +%s)
docker network disconnect "$NETWORK" "$container" >"$OUT/04-disconnect.log" 2>&1
disconnect_rc=$?
assert "$([[ $disconnect_rc -eq 0 ]] && echo 1 || echo 0)" \
  "Container terputus dari jaringan $NETWORK" "$(head -1 "$OUT/04-disconnect.log")"

step "Menunggu device_status.online menjadi false"
offline_wait=$(wait_until $((LIMIT_SECONDS + 20)) \
  "SELECT online FROM device_status s JOIN devices d ON d.id=s.device_id WHERE d.code='ems-pv-01';" "f")
offline_rc=$?
measured=$(( $(date +%s) - cut_at ))

assert "$([[ $offline_rc -eq 0 ]] && echo 1 || echo 0)" \
  "LWT tercatat di database" "terukur ${measured} s setelah jaringan dicabut"

assert "$([[ $offline_rc -eq 0 && $measured -lt $LIMIT_SECONDS ]] && echo 1 || echo 0)" \
  "Waktu deteksi ${measured} s < ${LIMIT_SECONDS} s" \
  "keepalive simulator 5 s, broker menyatakan client mati setelah 1.5x keepalive"

step "Memeriksa alarm STALE untuk ems-pv-01"
stale_wait=$(wait_until 20 \
  "SELECT count(*) FROM alarms a JOIN devices d ON d.id=a.device_id
   WHERE d.code='ems-pv-01' AND a.code='STALE' AND a.severity='critical' AND a.cleared_at IS NULL;" "1")
stale_rc=$?
assert "$([[ $stale_rc -eq 0 ]] && echo 1 || echo 0)" \
  "Alarm STALE severity=critical terbit untuk ems-pv-01" "butuh ${stale_wait} s"

step "Memulihkan jaringan dan merapikan"
docker network connect "$NETWORK" "$container" >/dev/null 2>&1
"${COMPOSE[@]}" --profile dev stop simulator >/dev/null 2>&1
"${COMPOSE[@]}" --profile dev rm -f simulator >/dev/null 2>&1

evidence "deteksi offline: ${measured} s (batas ${LIMIT_SECONDS} s)"
evidence "telemetry sebelum pemutusan: ${rows} baris"
evidence "status online tercatat ${online_wait} s setelah connect"

scenario_end
