#!/usr/bin/env bash
# Memastikan contracts/schema.sql (snapshot akhir) == hasil menjalankan semua migrasi server (pg_dump --schema-only).
#   SMSC_TEST_DATABASE_URL='postgres://nobody@localhost:55432/postgres?sslmode=disable' bash server/tests/schema_sync.sh
set -euo pipefail
cd "$(dirname "$0")/../.."
BASE=${SMSC_TEST_DATABASE_URL:?SMSC_TEST_DATABASE_URL belum diset}
A=smsc_sync_a_$$; B=smsc_sync_b_$$
cleanup(){ psql "$BASE" -qc "DROP DATABASE IF EXISTS $A WITH (FORCE)" >/dev/null 2>&1 || true; psql "$BASE" -qc "DROP DATABASE IF EXISTS $B WITH (FORCE)" >/dev/null 2>&1 || true; }
trap cleanup EXIT
psql "$BASE" -qc "CREATE DATABASE $A" -qc "CREATE DATABASE $B"
psql "${BASE/\/postgres?/\/$A?}" -q -v ON_ERROR_STOP=1 -f contracts/schema.sql >/dev/null
for f in server/internal/db/migrations/*.sql; do psql "${BASE/\/postgres?/\/$B?}" -q -v ON_ERROR_STOP=1 -f "$f" >/dev/null; done
# seed data (INSERT) tidak relevan: bandingkan struktur saja
dump(){ pg_dump --schema-only --no-owner --no-privileges "${BASE/\/postgres?/\/$1?}" | grep -v -E '^(--|SET |SELECT pg_catalog|\\restrict|\\unrestrict|$)'; }
if diff <(dump $A) <(dump $B) >/tmp/schema_sync.diff; then echo "skema sinkron: schema.sql == semua migrasi"; else echo "SKEMA TIDAK SINKRON:"; head -30 /tmp/schema_sync.diff; exit 1; fi
