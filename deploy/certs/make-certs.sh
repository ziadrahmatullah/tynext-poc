#!/usr/bin/env bash
# Membuat CA internal + sertifikat server untuk broker EMQX, dan (opsional)
# client certificate untuk ems-pv-01.
#
# ca.crt yang dihasilkan adalah file yang dikirim ke tim OT supaya TF6701 bisa
# memverifikasi broker.
#
# TBC: keputusan CA belum diambil. Skrip ini memakai CA internal. Alternatif
# Let's Encrypt dijelaskan di docs/POC_RUNBOOK.md.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
out="$here/out"

# shellcheck source=/dev/null
[[ -f "$repo/.env" ]] && set -a && source "$repo/.env" && set +a

host="${RAIN_BROKER_HOST:-}"
if [[ -z "$host" ]]; then
  echo "RAIN_BROKER_HOST belum diset (lihat .env.example). Contoh: mqtt-poc.example.test" >&2
  exit 1
fi

days_ca="${RAIN_CERT_DAYS_CA:-3650}"
days_leaf="${RAIN_CERT_DAYS_LEAF:-825}"
client_cn="${RAIN_CLIENT_CN:-ems-pv-01}"
want_client="${RAIN_MAKE_CLIENT_CERT:-1}"

mkdir -p "$out"
# Direktori dibuat 0755 supaya container (yang berjalan sebagai uid lain) bisa
# menelusurinya untuk membaca ca.crt. Yang menjaga rahasia adalah mode file
# kuncinya sendiri, bukan mode direktorinya.
chmod 755 "$out"

# Idempoten: kalau CA sudah ada, dipakai ulang supaya ca.crt yang sudah
# terlanjur dikirim ke tim OT tidak berubah.
if [[ -f "$out/ca.key" && -f "$out/ca.crt" ]]; then
  echo "• CA sudah ada, dipakai ulang: $out/ca.crt"
else
  echo "• Membuat CA internal"
  openssl genrsa -out "$out/ca.key" 4096 2>/dev/null
  openssl req -x509 -new -nodes -sha256 \
    -key "$out/ca.key" -days "$days_ca" \
    -subj "/C=ID/O=Tynext/OU=RAIN PoC/CN=RAIN PoC Internal CA" \
    -out "$out/ca.crt"
fi

# SAN diisi dari RAIN_BROKER_HOST. localhost dan 127.0.0.1 ikut supaya skenario
# uji bisa jalan di mesin yang sama tanpa DNS.
# "emqx" ikut masuk SAN supaya container ingest dan simulator di dalam
# docker-compose bisa memverifikasi broker lewat nama service-nya.
san="DNS:${host},DNS:emqx,DNS:localhost,IP:127.0.0.1"
if [[ -n "${RAIN_BROKER_IP:-}" ]]; then
  san="${san},IP:${RAIN_BROKER_IP}"
fi

echo "• Membuat sertifikat server (CN=${host}, SAN=${san})"
openssl genrsa -out "$out/server.key" 2048 2>/dev/null
openssl req -new -sha256 -key "$out/server.key" \
  -subj "/C=ID/O=Tynext/OU=RAIN PoC/CN=${host}" \
  -out "$out/server.csr"
openssl x509 -req -sha256 -days "$days_leaf" \
  -in "$out/server.csr" -CA "$out/ca.crt" -CAkey "$out/ca.key" -CAcreateserial \
  -extfile <(printf 'subjectAltName=%s\nbasicConstraints=CA:FALSE\nkeyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n' "$san") \
  -out "$out/server.crt" 2>/dev/null

if [[ "$want_client" == "1" ]]; then
  echo "• Membuat client certificate (CN=${client_cn}) — opsional, dipakai hanya kalau broker diset mutual TLS"
  openssl genrsa -out "$out/client-${client_cn}.key" 2048 2>/dev/null
  openssl req -new -sha256 -key "$out/client-${client_cn}.key" \
    -subj "/C=ID/O=Tynext/OU=RAIN PoC/CN=${client_cn}" \
    -out "$out/client-${client_cn}.csr"
  openssl x509 -req -sha256 -days "$days_leaf" \
    -in "$out/client-${client_cn}.csr" -CA "$out/ca.crt" -CAkey "$out/ca.key" -CAcreateserial \
    -extfile <(printf 'basicConstraints=CA:FALSE\nkeyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=clientAuth\n') \
    -out "$out/client-${client_cn}.crt" 2>/dev/null
fi

rm -f "$out"/*.csr

# Kunci privat tetap tertutup.
chmod 600 "$out"/*.key
# Kecuali server.key: EMQX berjalan sebagai uid 1000 di dalam container dan
# harus bisa membacanya lewat mount read-only.
chmod 640 "$out/server.key"
# Sertifikat publik boleh dibaca siapa saja — ca.crt memang untuk dibagikan.
chmod 644 "$out"/*.crt

echo
echo "Selesai. Isi $out:"
ls -1 "$out"
echo
echo "Verifikasi rantai:"
openssl verify -CAfile "$out/ca.crt" "$out/server.crt"
echo "SAN sertifikat server:"
openssl x509 -in "$out/server.crt" -noout -ext subjectAltName | tail -1
echo
echo "Kirim ke tim OT: $out/ca.crt"
