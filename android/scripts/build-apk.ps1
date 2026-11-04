<#
 Build APK PoC di PC Windows (winbuilder). BELUM DIUJI di Windows oleh pembuatnya (disusun di Linux):
 jika ada kesalahan, laporkan keluarannya apa adanya.

 Pemakaian (PowerShell, dari mana saja):
   .\android\scripts\build-apk.ps1               # tes + build debug APK
   .\android\scripts\build-apk.ps1 -Install      # + adb install -r ke HP yang tersambung
   .\android\scripts\build-apk.ps1 -Install -Serial R5CT123456
   .\android\scripts\build-apk.ps1 -Tailnet    # bangun dulu tsnetbridge.aar (Go + NDK) -> APK dengan tailnet ter-embed
 Prasyarat: JDK 17+ (JAVA_HOME), Android SDK (ANDROID_HOME atau %LOCALAPPDATA%\Android\Sdk) dengan platform android-35
            dan build-tools; adb di PATH bila memakai -Install.
#>
param([switch]$Install, [string]$Serial = "", [switch]$Tailnet)
$ErrorActionPreference = "Stop"
$androidDir = Split-Path -Parent $PSScriptRoot
Set-Location $androidDir

# 1. JDK
$javaVer = (& java -version 2>&1 | Select-Object -First 1).ToString()
if ($javaVer -notmatch '"(\d+)') { throw "java tidak ditemukan. Pasang JDK 17+ dan set JAVA_HOME." }
if ([int]$Matches[1] -lt 17) { throw "Perlu JDK 17+, terdeteksi: $javaVer" }
Write-Host "JDK OK: $javaVer"

# 2. Android SDK -> local.properties (pakai garis miring maju agar aman untuk Gradle)
$sdk = $env:ANDROID_HOME
if (-not $sdk) { $sdk = $env:ANDROID_SDK_ROOT }
if (-not $sdk) { $sdk = Join-Path $env:LOCALAPPDATA "Android\Sdk" }
if (-not (Test-Path (Join-Path $sdk "platforms"))) { throw "Android SDK tidak ditemukan di '$sdk'. Set ANDROID_HOME atau pasang lewat Android Studio." }
$sdkFwd = $sdk -replace '\\', '/'
Set-Content -Path (Join-Path $androidDir "local.properties") -Value "sdk.dir=$sdkFwd" -Encoding ascii
Write-Host "SDK: $sdk"

# 2b. Tailnet ter-embed (opsional): AAR gomobile dari android\tsnetbridge
if ($Tailnet) {
    & (Join-Path $androidDir "tsnetbridge\build-aar.ps1")
    Set-Location $androidDir
}

# 3. Tes unit + type-check + build
& .\gradlew.bat :core:test :typecheck:test :app:assembleDebug --console=plain
if ($LASTEXITCODE -ne 0) { throw "Gradle gagal (exit $LASTEXITCODE)" }

$apk = Join-Path $androidDir "app\build\outputs\apk\debug\app-debug.apk"
if (-not (Test-Path $apk)) { throw "APK tidak ditemukan: $apk" }
Write-Host "APK: $apk"

# 4. Pasang (opsional)
if ($Install) {
    $adbArgs = @()
    if ($Serial) { $adbArgs += @("-s", $Serial) }
    & adb @adbArgs install -r $apk
    if ($LASTEXITCODE -ne 0) { throw "adb install gagal" }
    Write-Host "Terpasang. Buka aplikasi 'SMSC PoC'."
}
