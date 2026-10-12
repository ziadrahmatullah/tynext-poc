#!/usr/bin/env bash
# Membuat/menyegarkan tiga user built-in database di EMQX lewat REST API v5.
#
# Password dibaca dari env (.env), tidak pernah ditulis ke repo. Skrip ini
# idempoten: dijalankan berkali-kali hasilnya sama.
#
#   ems-pv-01     TF6701 di CX5240   — publish saja
#   ingest        subscriber         — subscribe saja
#   simulator-dev stand-in CX5240    — publish saja, khusus dev
#
# Hak aksesnya diatur deploy/emqx/acl.conf, bukan di sini.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"

# shellcheck source=/dev/null
[[ -f "$repo/.env" ]] && set -a && source "$repo/.env" && set +a

port="${RAIN_EMQX_DASHBOARD_HOST_PORT:-18083}"
api="http://127.0.0.1:${port}/api/v5"
authn_id="password_based%3Abuilt_in_database"

user="${RAIN_EMQX_DASHBOARD_USER:-admin}"
pass="${RAIN_EMQX_DASHBOARD_PASS:-}"
if [[ -z "$pass" ]]; then
  echo "RAIN_EMQX_DASHBOARD_PASS belum diset (lihat .env.example)" >&2
  exit 1
fi

for v in RAIN_EMS_PV_PASS RAIN_INGEST_PASS RAIN_SIM_PASS; do
  if [[ -z "${!v:-}" ]]; then
    echo "$v belum diset (lihat .env.example)" >&2
    exit 1
  fi
done

# Tunggu REST API siap — container bisa saja baru naik.
token=""
for _ in $(seq 1 30); do
  token="$(curl -fsS -X POST "$api/login" \
    -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg u "$user" --arg p "$pass" '{username:$u,password:$p}')" \
    2>/dev/null | jq -r '.token // empty')" || true
  [[ -n "$token" ]] && break
  sleep 2
done

if [[ -z "$token" ]]; then
  echo "Gagal login ke REST API EMQX di $api — broker belum siap atau password dashboard salah" >&2
  exit 1
fi

upsert_user() {
  local id="$1" secret="$2" code

  code="$(curl -sS -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer $token" \
    "$api/authentication/$authn_id/users/$id")"

  if [[ "$code" == "200" ]]; then
    curl -fsS -o /dev/null -X PUT \
      -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
      -d "$(jq -nc --arg p "$secret" '{password:$p}')" \
      "$api/authentication/$authn_id/users/$id"
    echo "  ~ $id (password diperbarui)"
  else
    curl -fsS -o /dev/null -X POST \
      -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
      -d "$(jq -nc --arg u "$id" --arg p "$secret" '{user_id:$u,password:$p,is_superuser:false}')" \
      "$api/authentication/$authn_id/users"
    echo "  + $id (dibuat)"
  fi
}

echo "• User built-in database:"
upsert_user "ems-pv-01"     "$RAIN_EMS_PV_PASS"
upsert_user "ingest"        "$RAIN_INGEST_PASS"
upsert_user "simulator-dev" "$RAIN_SIM_PASS"

echo
echo "• Terdaftar sekarang:"
curl -fsS -H "Authorization: Bearer $token" \
  "$api/authentication/$authn_id/users?limit=100" \
  | jq -r '.data[] | "  - \(.user_id) (superuser=\(.is_superuser))"'

echo
echo "• Authorization:"
curl -fsS -H "Authorization: Bearer $token" "$api/authorization/settings" \
  | jq -r '"  no_match=\(.no_match) deny_action=\(.deny_action)"'
curl -fsS -H "Authorization: Bearer $token" "$api/authorization/sources" \
  | jq -r '.sources[] | "  source: type=\(.type) enable=\(.enable)"'
