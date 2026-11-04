# Panduan uji PoC di HP lewat winbuilder

Panduan ini membawa Anda dari PC Windows (winbuilder) kosong sampai hasil uji di HP Android nyata terkumpul dan siap dilaporkan. Server yang dipakai adalah `smscd` sungguhan dengan dashboard, enrollment QR, dan opsi tailnet ter-embed, bukan server PoC Python lama. Risiko yang diuji: R1, R2, R3, R4, R5, R6 (`docs/05` §1), plus fitur fase 43–47.

> **Status jujur:** semua yang ada di sini sudah lulus di lingkungan tanpa HP:
> - tes Go `-race`, E2E dashboard, dan lab tailnet;
> - agen JVM yang memakai kode `:core` aplikasi, termasuk enrollment dari **gambar QR dashboard**;
> - type-check seluruh aplikasi terhadap API Android 14;
> - tes alat `collect.py`/`doze.py` dengan `adb` palsu.
>
> Yang **belum pernah** dijalankan: aplikasi di HP, skrip `.ps1` di Windows (PowerShell tidak tersedia di lingkungan penyusunan), build AAR tsnet dengan NDK, dan kamera nyata. Bila ada langkah yang gagal, catat keluarannya apa adanya; itu juga hasil uji.

## Ringkasan alur

```
winbuilder (Windows)                                   HP uji (Android 10+)
├─ Docker Desktop: PostgreSQL + smscd (node tailnet)    ├─ APK SMSC PoC (adb install)
│     https://smsc.<tailnet>.ts.net  ◄──── tailnet ────►├─ (opsi) aplikasi Tailscale, ATAU tailnet ter-embed di APK
├─ Android SDK/NDK + Go → APK (+ tsnetbridge.aar)       └─ SIM berpulsa, nomor tujuan milik Anda
└─ Python + adb → poc\tools\collect.py, doze.py
```

| Tahap | Waktu kira-kira |
|---|---|
| 1–3 Persiapan winbuilder, HP, server | 1–2 jam (sekali) |
| 4 Build & pasang APK | 10–20 menit (build pertama mengunduh dependensi) |
| 5 Uji fungsional F1–F9 | ±1 jam |
| 6 Uji risiko R2, R6, R1, R5 | ±2 jam |
| 6 Uji R3/R4 (standby) | 1–3 hari, berjalan sendiri |

## 1. Siapkan winbuilder (sekali)

| Perangkat lunak | Untuk | Cek |
|---|---|---|
| Git | clone repo | `git --version` |
| JDK 17+ (mis. Temurin 17), `JAVA_HOME` diset | Gradle | `java -version` |
| Android Studio → SDK Manager: **Android 15 (API 35) SDK Platform**, **Build-Tools**, **Platform-Tools**; untuk varian tailnet juga **NDK (Side by side)** | build APK, `adb` | `adb version` |
| Variabel lingkungan `ANDROID_HOME` (mis. `%LOCALAPPDATA%\Android\Sdk`); untuk tailnet juga `ANDROID_NDK_HOME` (`...\Sdk\ndk\<versi>`); tambahkan `...\Sdk\platform-tools` ke `PATH` | | `echo $env:ANDROID_HOME` |
| Go **1.24.x** (hanya varian tailnet ter-embed) | `gomobile bind` | `go version` |
| Docker Desktop (backend WSL2, Linux containers) | menjalankan server uji | `docker version` |
| Python 3.8+ (`py` launcher) | alat pengumpul hasil | `py --version` |

```powershell
git clone <url-repo> smsc-app
cd smsc-app
git checkout claude/great-dijkstra-frg4xa
```

## 2. Siapkan HP uji

1. Pakai HP khusus uji, bukan HP pribadi utama: uji burst dan Doze mengubah setelan sistem.
2. **Opsi pengembang**: Pengaturan → Tentang ponsel → ketuk *Nomor build* 7×. Aktifkan **USB debugging**.
3. Colok ke winbuilder, setujui sidik jari RSA di layar HP, lalu periksa:
   ```powershell
   adb devices        # harus: <serial>  device   (bukan "unauthorized")
   ```
4. SIM berpulsa. Siapkan **1–2 nomor tujuan milik Anda sendiri**, sebaiknya beda operator untuk R5. **Jangan pernah uji burst ke nomor orang lain.**
5. Catat: merek, model, versi Android, operator SIM 1/SIM 2. `collect.py` juga mencatatnya otomatis.

## 3. Siapkan server uji (Docker Desktop di winbuilder)

Disarankan varian **tanpa sidecar** (`docker-compose.tsnet.yml`): `smscd` menjadi node tailnet sendiri dan dapat memverifikasi identitas node HP.

**Di admin console Tailscale (sekali):**

1. *Access controls*: tambahkan `tag:smsc-server` dan `tag:smsc-device` beserta ACL dari `INSTALL.md` §0.
2. *DNS*: aktifkan MagicDNS dan **HTTPS Certificates**.
3. *Settings → Keys*: buat auth key ber-tag `tag:smsc-server` (sekali pakai) untuk server.
4. (Untuk QR berisi auth key device) *Settings → OAuth clients*: buat client dengan scope **Auth Keys: write** dan tag `tag:smsc-device`.

**Di winbuilder:**

```powershell
cd smsc-app
docker build -t smsc/server:latest .
cd deploy\compose
```

`init-secrets.sh` adalah skrip shell. Jalankan dari WSL (`wsl sh init-secrets.sh`) atau Git Bash, atau buat berkasnya manual. Folder `secrets\` harus berisi berkas berikut, masing-masing satu baris tanpa baris baru:

| Berkas | Isi |
|---|---|
| `db_password` | password acak ≥ 24 karakter |
| `database_url` | `postgres://smsc:<db_password>@db:5432/smsc?sslmode=disable` |
| `session_secret`, `webhook_enc_key` | masing-masing string acak ≥ 32 karakter (berbeda). **Cadangkan `webhook_enc_key`.** |
| `tailnet_auth_key` | auth key `tag:smsc-server` |
| `tailscale_oauth_secret` | secret OAuth client (kosongkan bila tidak memakai auth key device otomatis) |

Buat `.env` (salin dari `.env.example`) lalu isi:

```ini
TS_HOSTNAME=smsc
PUBLIC_BASE_URL=https://smsc.<nama-tailnet>.ts.net
TAILSCALE_OAUTH_CLIENT_ID=<id OAuth client>   # kosong = QR tanpa auth key device
TAILNET_DEVICE_POLICY=record
ALLOWED_DESTINATION_PREFIXES=+62
DAILY_QUOTA_PER_USER=500                      # pengaman biaya selama uji
DAILY_QUOTA_PER_KEY=200
```

```powershell
docker compose -f docker-compose.tsnet.yml up -d
docker compose -f docker-compose.tsnet.yml logs -f server     # tunggu "melayani di tailnet"
docker compose -f docker-compose.tsnet.yml exec server smscd doctor
docker compose -f docker-compose.tsnet.yml exec server smscd admin create --email anda@contoh.id
```

`doctor` harus **0 gagal**. Buka dashboard dari perangkat di tailnet (winbuilder perlu aplikasi Tailscale Windows untuk membuka URL ini) dan login.

**Atur policy pengiriman untuk uji** (*Pengaturan → Policy → Default → Ubah*): jendela kirim `00:00–00:00` (sepanjang hari). Bawaan policy *Default* yang dipertahankan:
- jeda 3–8 dtk;
- batas 10/menit, 300/jam, 2.000/hari;
- laporan delivery dilacak.

Batas-batas ini juga menjadi pengaman biaya.

> Alternatif tanpa Docker atau tanpa tailnet ter-embed: `INSTALL.md` varian A (sidecar Tailscale) atau B (systemd di Linux). Untuk uji cepat R1/R3 tanpa server penuh, server PoC Python di `poc/README.md` §2 masih bisa dipakai.

## 4. Build & pasang APK

Dua varian. Bangun dan uji **standar dulu**.

```powershell
# Standar: koneksi lewat aplikasi Tailscale di HP
.\android\scripts\build-apk.ps1 -Install

# Tailnet ter-embed: butuh Go 1.24 + NDK; membangun android\app\libs\tsnetbridge.aar lalu APK
.\android\scripts\build-apk.ps1 -Tailnet -Install
```

- Skrip menjalankan tes `:core` dan `:typecheck` sebelum build.
- Log Gradle menulis `tsnetbridge.aar ditemukan: mode tailnet ter-embed AKTIF` atau `... tidak ada`.
- APK ada di `android\app\build\outputs\apk\debug\app-debug.apk`.
- Untuk menginstal ulang bersih (penting untuk R2): `adb uninstall id.smsc.poc`, lalu jalankan skrip lagi.

## 5. Uji fungsional (F1–F9)

Jalankan berurutan. Setiap butir punya kriteria lulus; catat hasilnya di `RESULTS_TEMPLATE.md` bagian F.

| # | Uji | Langkah | Lulus bila |
|---|---|---|---|
| F1 | Izin & wizard | Buka aplikasi → **Minta izin** → **Abaikan baterai** | Semua izin diberikan, atau lanjut ke R2 bila terblokir |
| F2 | **Pindai QR** | Dashboard: *Devices → Tambah device*. HP: *4. Koneksi → Daftarkan device → Pindai QR enrollment*. Arahkan ke QR di monitor (kecerahan maksimal, jarak 20–40 cm) | QR terbaca ≤ 5 dtk; dialog konfirmasi menampilkan host server yang benar; setelah *Daftarkan*, dashboard menampilkan **✓ Device terhubung** dan device **online** |
| F3 | Kondisi QR sulit | Ulangi F2 dengan: dashboard mode gelap, kecerahan rendah, layar laptop miring 45°, QR kecil (zoom browser 50%) | Catat mana yang terbaca/tidak dan waktunya |
| F4 | Deep link & kode manual | *Salin tautan* di dashboard, kirim ke HP (chat ke diri sendiri), ketuk tautannya. Lalu coba kode manual + URL server | Aplikasi terbuka di layar *Daftarkan* dengan konfirmasi host. QR bukan SMSC/tautan lain ditolak dengan pesan |
| F5 | Kirim dari dashboard | *Kirim SMS* ke nomor Anda; ulangi dengan pesan > 160 karakter | Status *Terkirim* → *Sampai* tanpa reload; timeline lengkap; pesan panjang tercatat 2 bagian |
| F6 | Rotasi kredensial | Dashboard: device → **Putar kredensial** | Hasil perintah *ok*; device tetap/kembali **online** ≤ 60 dtk; `poc.log` memuat `command rotate_credential -> ok=true` |
| F7 | Reboot | Restart HP tanpa membuka aplikasi | `boot` di log; online ≤ 2 menit setelah buka kunci pertama |
| F8 | **Tailnet ter-embed** (APK `-Tailnet`, server dengan OAuth) | Uninstall aplikasi Tailscale di HP (atau logout). Buat QR baru: dialog QR menyebut *memuat auth key Tailscale*. Pindai | Device online **tanpa aplikasi Tailscale**; dashboard menampilkan **tailnet ✓**; di admin console Tailscale muncul node `smsc-<model>` ber-tag `tag:smsc-device`; auth key enrollment hilang dari daftar key |
| F9 | Kebijakan `enforce` | Setelah semua HP uji tampil *tailnet ✓*: set `TAILNET_DEVICE_POLICY=enforce` di `.env`, lalu `docker compose ... up -d`. Kemudian (a) HP F8 kirim SMS; (b) HP lain tanpa tag mencoba enroll | (a) tetap berfungsi; (b) ditolak "node tailnet HP ini belum ber-tag device" |

Setelah F1–F9, ambil bukti:

```powershell
py poc\tools\collect.py --label fungsional --logcat
```

## 6. Uji risiko

Urutan yang disarankan: **R2 → R6 → R1 → R5 → R3/R4**. Prosedur rinci tiap skenario ada di `poc/README.md` §3; berikut penyesuaian untuk server `smscd` dan alat bantu barunya. **Selalu kirim ke nomor Anda sendiri.**

| Risiko | Cara dengan smscd | Bukti |
|---|---|---|
| **R2** izin sideload | Instal bersih (`adb uninstall` lalu skrip). Juga instal APK dari berkas di HP (salin APK ke HP, buka dari File Manager) | `collect.py --label r2-adb` dan `--label r2-berkas`: kolom *installer* dan *Izin* di `summary.md`; screenshot dialog |
| **R6** multi SIM | Dashboard *SIM*: kedua slot terdaftar. Kirim SMS dengan SIM tertentu (*Kirim SMS → SIM*). Cabut/pasang SIM 2, tukar slot, toggle eSIM | Dashboard memperbarui status SIM; SMS keluar dari nomor yang benar; slot kosong → `sim_unavailable` |
| **R1** batas OS | Burst lokal di aplikasi (*3. Uji burst*) skenario A–E (`poc/README.md` §3 R1). Lalu lewat server: buat policy baru "Uji R1" (jeda 0, batas per menit/jam dikosongkan), lalu *Kirim SMS* dengan nomor Anda ditempel 100 baris dan policy "Uji R1". Campaign tidak bisa dipakai di sini karena menghapus nomor duplikat. Hapus policy setelah uji | `collect.py --label r1-a` dst.: *sms_outgoing_check_max_count*, peringatan ">20 dtk sending→sent", "pesan 'sending' dalam 30 menit pertama" |
| **R5** delivery report | Kirim 30 SMS ke tiap operator dari dashboard/campaign | Dashboard *Transaksi*: rasio *Sampai*/*Terkirim* per operator; `analyze.py` `sent->delivered`; cari `DELIVERY_TANPA_PDU` di `poc.log` |
| **R3** standby | Service berjalan, layar mati, idle 1 j / 8 j / 24 j. Bandingkan: pengecualian baterai ON/OFF, mengisi/tidak, **aplikasi Tailscale vs tailnet ter-embed** | Setiap selesai: `collect.py --label r3-idle-8j-embedded`; baterai %/jam dari `device.json` awal-akhir |
| **R3** Doze paksa | `py poc\tools\doze.py --minutes 20 --server https://smsc.<tailnet>.ts.net --email anda@contoh.id --send-to 08xxxxxxxxxx` (password: `$env:SMSC_PASSWORD` atau ditanya; pakai akun tanpa 2FA) | `doze_summary.txt`: % sampel server melihat device online selama Doze, dan latensi SMS saat HP tidur (lulus ≤ 5 dtk sampai `sent`) |
| **R4** Tailscale | Dengan aplikasi Tailscale: matikan VPN 2 menit, lalu hidupkan. Key expiry: node HP harus ber-tag (tanpa kedaluwarsa) | `ledger.csv`: putus → online ≤ 15 dtk sesudah VPN kembali; dashboard *Capability* "Tailscale aktif" |

Tips R3:
- Mulai uji sore, kumpulkan pagi.
- Jangan sentuh HP selama uji.
- Catat setelan vendor yang Anda ubah (autostart, *sleeping apps*, *battery: unrestricted*). Isi panduan OEM bergantung pada catatan ini.
- `doze.py` selalu mengembalikan HP dari Doze paksa, juga saat Ctrl+C. Bila skrip terhenti paksa (mis. jendela ditutup), jalankan sendiri:
  ```powershell
  adb shell dumpsys deviceidle unforce
  adb shell dumpsys battery reset
  ```

## 7. Kumpulkan & laporkan

1. Setiap selesai satu uji: `py poc\tools\collect.py --label <nama-uji> [--logcat]`. Hasilnya ada di `poc\hasil\<merek>-<model>-<waktu>-<label>\`:
   - `summary.md`: siap tempel ke laporan;
   - `device.json`;
   - `files\`: `poc.log`, `ledger.csv`, `events_*.csv`.
2. Salin `poc\RESULTS_TEMPLATE.md` ke `poc\hasil\` dan isi. Lampirkan screenshot (R2, F2/F3) dan setelan vendor (R3).
3. Kompres `poc\hasil\` dan kirim. **Periksa dulu**: jangan sertakan berkas `secrets\` atau `.env` server.

## 8. Pemecahan masalah

| Gejala | Penyebab umum | Tindakan |
|---|---|---|
| `adb devices` → `unauthorized` | Sidik jari RSA belum disetujui | Buka kunci HP, setujui; `adb kill-server` lalu ulangi |
| `Android SDK tidak ditemukan` di build | `ANDROID_HOME` kosong | Set `ANDROID_HOME`, buka terminal baru |
| `ANDROID_NDK_HOME belum diset` / `gomobile bind gagal` (`-Tailnet`) | NDK belum dipasang | SDK Manager → *NDK (Side by side)*; set `ANDROID_NDK_HOME` ke folder versinya. Laporkan keluaran lengkap bila masih gagal |
| Tombol izin SMS abu-abu | *Restricted settings* (R2) | Info aplikasi → ⋮ → *Izinkan setelan terbatas*; catat langkahnya sebagai hasil R2 |
| QR tidak terbaca | Pantulan/kecerahan/fokus | Kecerahan monitor maksimal, mundur sedikit, matikan mode gelap; alternatif: *Salin tautan* (F4). Catat sebagai hasil F3 |
| Enroll: "kode/QR tidak valid, kedaluwarsa, atau sudah dipakai" | QR berumur 15 menit dan sekali pakai | Buat QR baru di dashboard |
| Enroll: "server … tidak terjangkau" | HP tidak di tailnet / ACL / server belum siap | Varian standar: aplikasi Tailscale di HP aktif dan login. Varian embedded: QR harus memuat auth key (OAuth diisi). Cek ACL `tag:smsc-device → tag:smsc-server:443` dan `doctor` |
| Enroll: "server hanya menerima device lewat tailnet" / "belum ber-tag device" | `TAILNET_DEVICE_POLICY=enforce` | Pakai QR dengan auth key (node otomatis ber-tag), atau beri tag node di admin console, atau kembali ke `record` |
| Device online lalu putus setiap layar mati | Optimasi baterai / OEM (R3) | *Abaikan baterai*, setelan vendor *unrestricted/autostart*; catat sebagai hasil R3 |
| `doctor`: FAIL `WEBAUTHN_RP_ID` / `PUBLIC_BASE_URL` | URL tidak cocok dengan nama node | `PUBLIC_BASE_URL=https://<TS_HOSTNAME>.<tailnet>.ts.net` |
| Browser: sertifikat tidak valid | HTTPS tailnet belum aktif | Admin console → DNS → *HTTPS Certificates*; tunggu sertifikat pertama (bisa ±1 menit) |
| Pesan diterima server tetapi tidak dikirim | Di luar jendela kirim, kuota habis, atau tujuan di luar allow-list | Policy jendela `00:00–00:00`; *Pengaturan → Pengguna → Kuota*; `ALLOWED_DESTINATION_PREFIXES` |

## 9. Selesai uji

1. Dashboard: *Cabut device* untuk device uji, cabut API key uji, hapus policy "Uji R1".
2. HP: *Daftarkan device → Hapus pendaftaran dari HP ini*. Bila menguji peran SMS default, kembalikan aplikasi SMS default semula.
3. Admin console Tailscale: hapus node HP uji dan OAuth client bila tidak dipakai lagi.
4. QR enrollment memuat auth key: jangan dibagikan atau disimpan di screenshot yang ikut dikirim.
