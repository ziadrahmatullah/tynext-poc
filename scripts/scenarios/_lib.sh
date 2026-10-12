# Dipakai bersama oleh skenario 01..06. Bukan skrip yang berdiri sendiri.
#
# Menyediakan: pemuatan .env, assertion yang mencetak OK/GAGAL per langkah, dan
# verdict PASS/FAIL di akhir beserta angka buktinya.

set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="$REPO/scripts/scenarios/out"
BIN="$OUT/bin"
mkdir -p "$OUT" "$BIN"

if [[ -f "$REPO/.env" ]]; then
  set -a
  # shellcheck source=/dev/null
  source "$REPO/.env"
  set +a
else
  echo "Tidak ada $REPO/.env — salin dari .env.example dan isi passwordnya." >&2
  exit 2
fi

COMPOSE=(docker compose --env-file "$REPO/.env" -f "$REPO/deploy/docker-compose.yml")

_checks_total=0
_checks_failed=0
_evidence=()

if [[ -t 1 ]]; then
  _c_ok=$'\033[32m'; _c_bad=$'\033[31m'; _c_dim=$'\033[2m'; _c_off=$'\033[0m'
else
  _c_ok=""; _c_bad=""; _c_dim=""; _c_off=""
fi

scenario_begin() {
  SCENARIO_NAME="$1"
  printf '\n── %s ──────────────────────────────────────\n' "$SCENARIO_NAME"
}

# evidence "teks" — angka bukti yang ikut dicetak di ringkasan akhir.
evidence() { _evidence+=("$1"); }

step() { printf '  %s…\n' "$1"; }

_record() {
  local ok="$1" label="$2" detail="${3:-}"
  _checks_total=$((_checks_total + 1))
  if [[ "$ok" == "1" ]]; then
    printf '  %s✔%s %s\n' "$_c_ok" "$_c_off" "$label"
  else
    _checks_failed=$((_checks_failed + 1))
    printf '  %s✘%s %s\n' "$_c_bad" "$_c_off" "$label"
  fi
  [[ -n "$detail" ]] && printf '      %s%s%s\n' "$_c_dim" "$detail" "$_c_off"
  return 0
}

# expect_ok "label" cmd...   — perintah harus sukses.
expect_ok() {
  local label="$1"; shift
  local output rc
  output="$("$@" 2>&1)"; rc=$?
  if [[ $rc -eq 0 ]]; then
    _record 1 "$label" "$(printf '%s' "$output" | head -3 | tr '\n' '|')"
  else
    _record 0 "$label" "keluar dengan kode $rc: $(printf '%s' "$output" | head -3 | tr '\n' '|')"
  fi
}

# expect_fail "label" cmd... — perintah harus GAGAL. Dipakai untuk uji negatif:
# yang membuktikan keamanan adalah penolakannya.
expect_fail() {
  local label="$1"; shift
  local output rc
  output="$("$@" 2>&1)"; rc=$?
  if [[ $rc -ne 0 ]]; then
    _record 1 "$label" "ditolak: $(printf '%s' "$output" | head -2 | tr '\n' '|')"
  else
    _record 0 "$label" "DITERIMA padahal seharusnya ditolak: $(printf '%s' "$output" | head -2 | tr '\n' '|')"
  fi
}

# assert "label" <kondisi 0/1> "detail"
assert() { _record "$1" "$2" "${3:-}"; }

scenario_end() {
  printf '\n'
  if [[ ${#_evidence[@]} -gt 0 ]]; then
    printf '  Bukti:\n'
    local e
    for e in "${_evidence[@]}"; do printf '    • %s\n' "$e"; done
    printf '\n'
  fi
  local passed=$((_checks_total - _checks_failed))
  if [[ $_checks_failed -eq 0 ]]; then
    printf '%sPASS%s  %s — %d/%d cek lolos\n\n' "$_c_ok" "$_c_off" "$SCENARIO_NAME" "$passed" "$_checks_total"
    exit 0
  fi
  printf '%sFAIL%s  %s — %d/%d cek lolos, %d gagal\n\n' "$_c_bad" "$_c_off" "$SCENARIO_NAME" "$passed" "$_checks_total" "$_checks_failed"
  exit 1
}

# Build sekali, lalu dipakai berulang supaya skenario tidak mengkompilasi ulang
# di setiap langkah.
build_probe() { build_bin mqttprobe; }

build_bin() {
  ( cd "$REPO" && go build -o "$BIN/$1" "./cmd/$1" ) || {
    echo "gagal build cmd/$1" >&2; exit 2; }
}

probe() { "$BIN/mqttprobe" "$@"; }

# psql dijalankan di dalam container supaya versi client selalu cocok dengan
# server, berapa pun versi psql yang terpasang di mesin ini.
psql_q() {
  "${COMPOSE[@]}" exec -T postgres psql -U "${RAIN_PG_USER:-rain}" -d "${RAIN_PG_DB:-rain}" \
    -X -q -A -t "$@"
}

# sql "SELECT ..." → satu nilai skalar.
sql() { psql_q -c "$1" | head -1 | tr -d ' \r'; }

reset_data() {
  psql_q -c "TRUNCATE telemetry, device_state, state_log, device_status, alarms, ingest_rejects RESTART IDENTITY;" >/dev/null
}

INGEST_HTTP="127.0.0.1:${RAIN_INGEST_HTTP_PORT:-8080}"

ingest_metric() {
  curl -s --max-time 5 "http://${INGEST_HTTP}/metrics" 2>/dev/null \
    | awk -v n="$1" '$1 == n { print $2 }' | head -1
}

# Menunggu sampai sebuah query mengembalikan nilai yang diinginkan, atau timeout.
# wait_until <detik> <sql> <nilai-yang-ditunggu>
wait_until() {
  local limit="$1" query="$2" want="$3" start now
  start=$(date +%s)
  while :; do
    if [[ "$(sql "$query")" == "$want" ]]; then
      now=$(date +%s); echo $((now - start)); return 0
    fi
    now=$(date +%s)
    if (( now - start >= limit )); then echo $((now - start)); return 1; fi
    sleep 0.5
  done
}

# Argumen koneksi yang benar, dipakai berulang.
probe_args_ok() {
  printf -- '-url\n%s\n-ca\n%s\n' "${RAIN_MQTT_URL}" "$REPO/${RAIN_MQTT_CA#./}"
}

require_stack() {
  local unhealthy
  unhealthy="$("${COMPOSE[@]}" ps --format '{{.Name}} {{.Health}}' 2>/dev/null | grep -v ' healthy$' || true)"
  if [[ -n "$unhealthy" ]]; then
    echo "Container belum healthy:" >&2
    echo "$unhealthy" >&2
    echo "Jalankan 'make up' dulu." >&2
    exit 2
  fi
}
