#!/usr/bin/env bash
# Verifikasi semua yang bisa diperiksa tanpa HP/Android SDK.
set -euo pipefail
cd "$(dirname "$0")/.."
bash contracts/tools/verify.sh
python3 poc/server/selftest.py | tail -n 2
(cd android && ./gradlew :core:test :typecheck:test --console=plain -q)
echo "PoC verify OK"
