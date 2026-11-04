<#
 Bangun android\app\libs\tsnetbridge.aar di winbuilder (Windows). BELUM DIUJI di Windows oleh pembuatnya.
 Prasyarat: Go 1.24+ di PATH, Android SDK + NDK (Android Studio > SDK Manager > NDK (Side by side)),
            ANDROID_HOME dan ANDROID_NDK_HOME diset. Lalu jalankan android\scripts\build-apk.ps1 -Tailnet.
#>
$ErrorActionPreference = "Stop"
$xmobile = "v0.0.0-20250711185624-d5bb5ecc55c0"   # mendukung Go 1.24 (versi terbaru menuntut Go 1.26)
$env:GOTOOLCHAIN = "local"
Set-Location $PSScriptRoot
$gobin = Join-Path (& go env GOPATH) "bin"
$env:Path = "$gobin;$env:Path"
if (-not (Get-Command gomobile -ErrorAction SilentlyContinue)) { & go install golang.org/x/mobile/cmd/gomobile@$xmobile }
if (-not (Get-Command gobind -ErrorAction SilentlyContinue)) { & go install golang.org/x/mobile/cmd/gobind@$xmobile }
if (-not $env:ANDROID_NDK_HOME) { throw "ANDROID_NDK_HOME belum diset (folder NDK, mis. ...\Sdk\ndk\27.x)" }
& gomobile init
Copy-Item go.mod go.mod.bak -Force; Copy-Item go.sum go.sum.bak -Force
try {
    & go get golang.org/x/mobile/bind@$xmobile
    New-Item -ItemType Directory -Force -Path ..\app\libs | Out-Null
    & gomobile bind -target=android/arm64,android/arm -androidapi 29 -trimpath -ldflags "-s -w" -o ..\app\libs\tsnetbridge.aar .
    if ($LASTEXITCODE -ne 0) { throw "gomobile bind gagal (exit $LASTEXITCODE)" }
} finally {
    Move-Item go.mod.bak go.mod -Force; Move-Item go.sum.bak go.sum -Force
}
Get-Item ..\app\libs\tsnetbridge.aar | Format-List Name, Length
