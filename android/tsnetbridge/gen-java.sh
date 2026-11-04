#!/bin/sh
# Buat ulang API Java hasil gobind (android/typecheck/src/gobind/java) setelah API Go bridge berubah.
# Tidak butuh Android SDK/NDK. :typecheck mengompilasi app/src/tsnet terhadap berkas ini.
set -eu
# x/mobile dipin ke versi yang mendukung Go 1.24 (versi terbaru menuntut Go 1.26)
XMOBILE=v0.0.0-20250711185624-d5bb5ecc55c0
export GOTOOLCHAIN=local
cd "$(dirname "$0")"
command -v gobind >/dev/null 2>&1 || go install golang.org/x/mobile/cmd/gobind@$XMOBILE
export PATH="$(go env GOPATH)/bin:$PATH"
out=$(mktemp -d)
cp go.mod go.mod.bak; cp go.sum go.sum.bak
trap 'mv go.mod.bak go.mod; mv go.sum.bak go.sum; rm -rf "$out"' EXIT
GOFLAGS=-mod=mod gobind -lang=java -outdir "$out" .
rm -rf ../typecheck/src/gobind/java && mkdir -p ../typecheck/src/gobind/java
cp -r "$out/java/go" "$out/java/tsnetbridge" ../typecheck/src/gobind/java/
echo "diperbarui: android/typecheck/src/gobind/java"
