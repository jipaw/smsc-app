#!/usr/bin/env bash
# Verifikasi semua yang bisa diperiksa tanpa HP/Android SDK.
set -euo pipefail
cd "$(dirname "$0")/.."
bash contracts/tools/verify.sh
python3 poc/server/selftest.py | tail -n 2
(cd android && ./gradlew :core:test :typecheck:test :agent:test --console=plain -q)
python3 poc/tools/test_tools.py 2>&1 | tail -n 1   # collect.py / doze.py dengan adb palsu
echo "PoC verify OK"
