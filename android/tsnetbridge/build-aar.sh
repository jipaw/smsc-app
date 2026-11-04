#!/bin/sh
# Bangun app/libs/tsnetbridge.aar dengan gomobile (butuh Go 1.24+, Android SDK + NDK; ANDROID_HOME dan ANDROID_NDK_HOME).
# Setelah AAR ada, build :app otomatis memakai varian tailnet ter-embed (app/src/tsnet).
set -eu
# x/mobile dipin ke versi yang mendukung Go 1.24 (versi terbaru menuntut Go 1.26)
XMOBILE=v0.0.0-20250711185624-d5bb5ecc55c0
export GOTOOLCHAIN=local
cd "$(dirname "$0")"
command -v gomobile >/dev/null 2>&1 || go install golang.org/x/mobile/cmd/gomobile@$XMOBILE
command -v gobind >/dev/null 2>&1 || go install golang.org/x/mobile/cmd/gobind@$XMOBILE
export PATH="$(go env GOPATH)/bin:$PATH"
gomobile init
# x/mobile/bind dibutuhkan saat bind saja; tidak disimpan di go.mod
cp go.mod go.mod.bak; cp go.sum go.sum.bak
trap 'mv go.mod.bak go.mod; mv go.sum.bak go.sum' EXIT
go get golang.org/x/mobile/bind@$XMOBILE
mkdir -p ../app/libs
# hanya ABI HP nyata (arm64 + armv7) agar APK tidak membengkak; -androidapi = minSdk aplikasi
gomobile bind -target=android/arm64,android/arm -androidapi 29 -trimpath -ldflags "-s -w" -o ../app/libs/tsnetbridge.aar .
ls -l ../app/libs/tsnetbridge.aar
