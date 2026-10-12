#!/usr/bin/env bash
# Skenario 02 — semua yang HARUS ditolak. Setiap langkah di sini lolos justru
# kalau brokernya menolak.
#
# Membuktikan kriteria lulus #1 (sisi negatif).
# shellcheck source=_lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

scenario_begin "02 negative"
require_stack
build_probe

ca="$REPO/${RAIN_MQTT_CA#./}"
url="${RAIN_MQTT_URL}"

step "Autentikasi"
expect_fail "Tanpa kredensial → ditolak" \
  probe -action connect -url "$url" -ca "$ca" -no-credentials -timeout 8s

expect_fail "Password salah → ditolak" \
  probe -action connect -url "$url" -ca "$ca" -user "ems-pv-01" -pass "password-yang-salah" -timeout 8s

expect_fail "Username tak dikenal → ditolak" \
  probe -action connect -url "$url" -ca "$ca" -user "penyusup" -pass "apa-saja" -timeout 8s

step "ACL publish"
expect_fail "ems-pv-01 publish ke rain/pts/cmd/x/set → ditolak" \
  probe -action publish -url "$url" -ca "$ca" -user "ems-pv-01" -pass "$RAIN_EMS_PV_PASS" \
    -topic 'rain/pts/cmd/x/set' -payload '{"action":"set_param"}' -timeout 8s

expect_fail "ems-pv-01 publish ke luar namespace rain/pts → ditolak" \
  probe -action publish -url "$url" -ca "$ca" -user "ems-pv-01" -pass "$RAIN_EMS_PV_PASS" \
    -topic 'lain/topic' -payload '{}' -timeout 8s

expect_fail "ingest publish ke topic telemetry → ditolak" \
  probe -action publish -url "$url" -ca "$ca" -user "ingest" -pass "$RAIN_INGEST_PASS" \
    -topic 'rain/pts/meter/pm-04/telemetry' -payload '{}' -timeout 8s

step "ACL subscribe"
expect_fail "ems-pv-01 subscribe rain/# → ditolak" \
  probe -action subscribe -url "$url" -ca "$ca" -user "ems-pv-01" -pass "$RAIN_EMS_PV_PASS" \
    -topic 'rain/#' -timeout 8s

expect_fail "ems-pv-01 subscribe \$share/ingest/... → ditolak" \
  probe -action subscribe -url "$url" -ca "$ca" -user "ems-pv-01" -pass "$RAIN_EMS_PV_PASS" \
    -topic '$share/ingest/rain/pts/+/+/telemetry' -timeout 8s

expect_fail "ems-pv-01 subscribe \$SYS/# → ditolak" \
  probe -action subscribe -url "$url" -ca "$ca" -user "ems-pv-01" -pass "$RAIN_EMS_PV_PASS" \
    -topic '$SYS/#' -timeout 8s

step "TLS"
expect_fail "Client tanpa ca.crt tidak mempercayai broker → handshake gagal" \
  probe -action verify-cert -url "$url" -ca "" -timeout 8s

evidence "broker: $url"
evidence "default-deny aktif: authorization.no_match=deny, deny_action=disconnect"
scenario_end
