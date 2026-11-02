# PoC Android — runbook

Tujuan: membuktikan atau membantah risiko R1, R2, R3, R5, R6 (`docs/05` §1) **sebelum** membangun produk.
Hasilnya mengubah default policy (`docs/03` §4), wizard onboarding (`docs/spec/03`), dan panduan OEM.

> **Status jujur:** skeleton ini lulus 26 unit test (`:core`), 2 tes konstanta terhadap `SmsManager` asli, type-check seluruh `:app`
> terhadap API Android 14, dan self-test server dengan validasi skema. **Belum pernah dijalankan di HP.** Semua perilaku platform
> di bawah adalah hipotesis sampai Anda mengukurnya.

## 0. Yang dibutuhkan

- 1 HP uji Android 10+ (idealnya 2 SIM; tambahan HP lain/merek lain sangat bernilai untuk R3). SIM dengan pulsa.
- **Nomor tujuan milik Anda sendiri** (1–2 nomor, sebaiknya beda operator untuk R5). Jangan uji burst ke nomor orang lain.
- Tailscale terpasang dan login di HP **dan** di mesin yang menjalankan server PoC (bisa winbuilder atau laptop).
- Mesin build dengan JDK 17+ dan Android SDK (platform 35), mis. winbuilder.

## 1. Build APK

Di mesin dengan Android SDK (Windows):

```powershell
git clone <repo> ; cd smsc-app ; git checkout claude/great-dijkstra-frg4xa
powershell -File android\scripts\build-apk.ps1 -Install     # tes + build + adb install
```

Linux/macOS:

```bash
cd android && ./gradlew :core:test :typecheck:test :app:assembleDebug
adb install -r app/build/outputs/apk/debug/app-debug.apk
```

APK: `android/app/build/outputs/apk/debug/app-debug.apk`.

## 2. Jalankan server PoC

Di mesin tailnet:

```bash
pip install -r poc/server/requirements.txt
python3 poc/server/selftest.py                     # harus "SEMUA LULUS"
tailscale ip -4                                    # catat alamat 100.x.y.z
python3 poc/server/poc_server.py --host 0.0.0.0 --port 8765 --token poc-secret
```

Di HP (aplikasi) isi URL `ws://<100.x.y.z>:8765/ws/device` dan token yang sama.
Terminal server menerima: `send`, `burst`, `ping`, `cancel`, `report`, `quit`.

Log server: `poc/server/out/server_events.jsonl`. Pesan yang melanggar `ws-schema.json` dicetak sebagai `[SKEMA]`.

## 3. Eksperimen

Catat semua hasil di `poc/RESULTS_TEMPLATE.md` (salin ke `poc/hasil/`). Ambil berkas dari HP:

```bash
adb pull /sdcard/Android/data/id.smsc.poc/files/ poc/hasil/files
python3 poc/tools/analyze.py poc/hasil/files
```

### R2 — Izin SMS pada app sideload

1. Pasang APK baru (bersih). Tekan **Minta izin**.
2. Catat: apakah dialog izin SMS muncul? Apakah tombol izin abu-abu/ditolak otomatis? Android versi berapa?
3. Jika terblokir: **Info aplikasi → ⋮ → Izinkan setelan terbatas**, ulangi. Catat langkahnya (screenshot).
4. Uji ulang setelah uninstall → install lewat `adb install` vs lewat berkas APK di HP (sumber instalasi memengaruhi restricted settings).
5. *(Untuk run berulang, bukan untuk R2)* `adb shell pm grant id.smsc.poc android.permission.SEND_SMS` melewati dialog.

**Keputusan yang dipengaruhi:** teks wizard onboarding; perlu tidaknya instalasi lewat MDM/installer sesi.

### R6 — Multi SIM & perubahan subscription

1. Buka bagian **SIM**: catat `slot`, `subId`, carrier, `esim`, nomor (sering kosong).
2. Kirim 1 SMS dari slot 0 lalu slot 1 (burst `jumlah=1`); pastikan SMS tiba dari nomor pengirim yang benar.
3. Cabut SIM 2 (atau matikan di Pengaturan) → perhatikan log `[sim] OnSubscriptionsChanged`; pasang kembali. Apakah `subId` berubah?
4. Toggle eSIM (jika ada). Tukar posisi SIM antar slot dan catat perubahan `subId` dan `slot`.
5. Kirim ke slot yang kosong → harus muncul `failed sim_unavailable` / penolakan `sim_unavailable`, bukan crash.

**Keputusan:** validasi ADR-07 (`(device, slot_index)` sebagai identitas) dan frekuensi `sim.report`.

### R1 — Batas kirim OS

Selalu ke nomor Anda sendiri. Mulai kecil (30), baru naik.

| Skenario | Pengaturan | Catat |
|---|---|---|
| A. Tanpa guard | jumlah 100, jeda 0/0, os-guard mati | Berapa SMS terkirim sebelum macet? Muncul dialog sistem ("mengirim banyak pesan")? Teks dialognya? Apa yang terjadi pada `sentIntent` saat dialog tampil (log `SLOW`/peringatan macet)? |
| B. Jeda wajar | jumlah 100, jeda 3000/8000, os-guard mati | Apakah batas tetap tercapai? Pada jumlah ke berapa/menit ke berapa? |
| C. Dengan guard | jumlah 60, jeda 0/0, **os-guard 25/30 mnt** | Apakah SMS tertahan oleh guard (bukan oleh OS)? Benar tidak ada dialog? |
| D. Peran SMS default | tekan **Jadi SMS default**, ulangi A | Apakah batas hilang/berubah? (**Kembalikan SMS default semula setelahnya**.) |
| E. Pengaturan sistem | `adb shell settings get global sms_outgoing_check_max_count` (dan `_interval_ms`); bila bisa, `adb shell settings put global sms_outgoing_check_max_count 200` lalu ulangi A | Nilai asli; apakah mengubahnya mungkin dan berpengaruh? (hipotesis, dicatat apa adanya) |

Ulangi A di ≥ 2 versi Android bila memungkinkan (mis. 10/11 dan 13+). Bagian **Statistik** di app menampilkan jeda antar `sending`, latensi `sending→sent`, dan peringatan macet >20 dtk; `analyze.py` memberi `pesan 'sending' dalam 30 menit pertama`.

**Keputusan:** default `os_sms_guard`, apakah peran SMS default diperlukan, dan batas throughput nyata per HP di `docs/03` §4.

### R5 — Laporan delivery

1. Kirim 30 SMS (jeda 5000/8000) ke nomor beda operator.
2. Hitung persentase `delivered` dari `sent` per operator (`analyze.py`), dan `sent→delivered` latensi.
3. Cari baris log **`DELIVERY_TANPA_PDU`**: jika ada, hipotesis "FLAG_MUTABLE membuat extras `pdu` diteruskan" gagal/berbeda — catat versi Android. Cek juga log `rx delivered ... pdu=true/false tpStatus=...`.
4. Matikan HP penerima sebelum kirim, lalu nyalakan: apakah laporan delivery tertunda dan berapa lama?

**Keputusan:** `delivery_timeout`, perlakuan `sent` sebagai akhir sah, pilihan FLAG pada `PendingIntent` (spec 03 §5.1).

### R3 — Standby & koneksi

Mulai service (**Start service**), pastikan server menunjukkan `device online`. Biarkan HP: layar mati, tercolok daya kecuali dinyatakan lain, Tailscale aktif. Baca `ledger.csv`/`analyze.py` dan `report` di server (celah heartbeat).

| Uji | Cara | Lulus bila |
|---|---|---|
| Idle 1 jam, 8 jam, 24 jam | layar mati | ≥ 99% online; tiap putus pulih ≤ 60 dtk |
| Tanpa pengecualian baterai | jangan beri "Abaikan baterai" | bandingkan dengan yang diberi pengecualian |
| Di baterai (tidak mengisi) | cabut kabel | bandingkan |
| Doze paksa | `adb shell dumpsys battery unplug` → `adb shell dumpsys deviceidle force-idle` → tunggu 10–30 mnt → `adb shell dumpsys deviceidle unforce` → `adb shell dumpsys battery reset` | koneksi/heartbeat tetap? kirim `ping` dari server saat idle |
| Wake lock | centang "Partial wake lock", ulangi idle | selisih uptime & konsumsi baterai (%/jam) |
| Reboot | restart HP tanpa membuka app | service otomatis hidup (`boot` di log); online ≤ 2 mnt setelah unlock pertama |
| Tailscale mati/hidup | matikan VPN 2 mnt | `disconnected` lalu `online` ≤ 15 dtk sesudah VPN kembali (`network available`) |
| Pindah jaringan | Wi-Fi → seluler → Wi-Fi | pulih ≤ 15 dtk |
| Mode pesawat 1 mnt | | pulih otomatis |
| Force-stop app | Info aplikasi → Paksa berhenti | catat: tidak ada pemulihan otomatis (hanya buka manual); watchdog WorkManager belum ada di skeleton |
| Server restart | hentikan/ulang `poc_server.py` | HP reconnect sendiri (backoff) |
| Perintah tidur | dari server: `send` saat HP idle ≥ 30 mnt | `sms.send` dieksekusi ≤ 5 dtk |

Ulangi di ≥ 2–3 merek (Samsung, Xiaomi/Oppo/Vivo, Pixel) — di sinilah OEM berbeda. Catat pengaturan vendor yang Anda ubah agar bisa jalan (autostart, sleeping apps, dll.).

**Keputusan:** kebutuhan watchdog/alarm cadangan, isi panduan OEM (`docs/spec/03` §9), apakah Profil B (tanpa Tailscale, `docs/01` §4.3) layak bila Tailscale terbukti jadi sumber putus.

## 4. Yang dilaporkan kembali

Isi `RESULTS_TEMPLATE.md`, lampirkan `analyze.py` output, `poc.log`, `ledger.csv`, `events_*.csv`, versi Android/merek/model, dan (R2) screenshot. Dari situ default policy, wizard, dan desain watchdog diperbarui.

## 5. Batasan skeleton

Lihat `android/README.md`. Penting: job yang belum terkirim hilang bila proses mati; ini bukan perilaku produk.
