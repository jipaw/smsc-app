#!/bin/sh
# Membuat berkas rahasia acak untuk docker compose (sekali saja; tidak menimpa yang sudah ada).
set -eu
cd "$(dirname "$0")"
umask 077
mkdir -p secrets
gen() { head -c 48 /dev/urandom | base64 | tr -d '\n/+=' | cut -c1-48; }
[ -s secrets/db_password ] || gen > secrets/db_password
[ -s secrets/session_secret ] || gen > secrets/session_secret
[ -s secrets/webhook_enc_key ] || gen > secrets/webhook_enc_key
[ -s secrets/database_url ] || printf 'postgres://smsc:%s@db:5432/smsc?sslmode=disable' "$(cat secrets/db_password)" > secrets/database_url
# image berjalan sebagai nonroot (uid 65532): berkas harus terbaca olehnya
chmod 0444 secrets/* 2>/dev/null || true
echo "rahasia siap di $(pwd)/secrets — cadangkan webhook_enc_key: tanpanya secret webhook & TOTP tidak dapat dibuka."
