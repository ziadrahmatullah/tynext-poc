#!/usr/bin/env bash
# Skenario 01 — koneksi TLS dengan kredensial yang benar, dan verifikasi bahwa
# sertifikat broker memang ditandatangani CA yang kita kirim ke tim OT.
#
# Membuktikan kriteria lulus #1 (sisi positif).
# shellcheck source=_lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"

scenario_begin "01 connect-tls"
require_stack
build_probe

ca="$REPO/${RAIN_MQTT_CA#./}"
url="${RAIN_MQTT_URL}"

step "Verifikasi sertifikat server terhadap ca.crt"
cert_out="$(probe -action verify-cert -url "$url" -ca "$ca" -timeout 10s 2>&1)"; rc=$?
assert "$([[ $rc -eq 0 ]] && echo 1 || echo 0)" \
  "Rantai sertifikat broker terverifikasi oleh $(basename "$ca")" \
  "$(printf '%s' "$cert_out" | tr '\n' '|')"

if [[ $rc -eq 0 ]]; then
  evidence "$(printf '%s' "$cert_out" | grep -E '^(tls version|subject|issuer|san dns|not after)' | tr '\n' ' ')"
fi

step "Connect sebagai ems-pv-01 dengan kredensialnya sendiri"
expect_ok "ems-pv-01 tersambung lewat TLS 8883" \
  probe -action connect -url "$url" -ca "$ca" -user "ems-pv-01" -pass "$RAIN_EMS_PV_PASS" -timeout 10s

step "Connect sebagai ingest dengan kredensialnya sendiri"
expect_ok "ingest tersambung lewat TLS 8883" \
  probe -action connect -url "$url" -ca "$ca" -user "ingest" -pass "$RAIN_INGEST_PASS" -timeout 10s

step "Publish ke topic kontrak"
expect_ok "ems-pv-01 boleh publish ke rain/pts/meter/pm-04/telemetry" \
  probe -action publish -url "$url" -ca "$ca" -user "ems-pv-01" -pass "$RAIN_EMS_PV_PASS" \
    -topic 'rain/pts/meter/pm-04/telemetry' \
    -payload '{"schemaVersion":"1.0","deviceId":"pm-04","ts":"1970-01-01T00:00:00.000Z","seq":0,"metrics":{}}' \
    -timeout 10s

step "Subscribe oleh ingest"
expect_ok "ingest boleh subscribe rain/pts/#" \
  probe -action subscribe -url "$url" -ca "$ca" -user "ingest" -pass "$RAIN_INGEST_PASS" \
    -topic 'rain/pts/#' -timeout 3s

expect_ok "ingest boleh memakai shared subscription \$share/ingest/..." \
  probe -action subscribe -url "$url" -ca "$ca" -user "ingest" -pass "$RAIN_INGEST_PASS" \
    -topic '$share/ingest/rain/pts/+/+/telemetry' -timeout 3s

evidence "broker: $url"
scenario_end
