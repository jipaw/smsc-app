# Spec 12 — Rencana Uji

Tujuan: membuktikan kebutuhan PRD (spec 01) dan invarian inti (tanpa SMS ganda, state machine, policy, rekonsiliasi). ID kasus: `T-<area>-<no>`; kolom *Trace* menautkan ke FR/NFR.

## 1. Strategi dan piramida

| Lapisan | Alat | Cakupan | Target |
|---|---|---|---|
| Unit (server) | `go test`, uji properti (`rapid`) | Policy resolver, transisi status, segmentasi SMS, normalisasi nomor, template, backoff, jendela waktu | Cakupan ≥ 85% paket `dispatch`, `messages`, `sms` |
| Unit (Android) | JUnit5, Turbine, Robolectric | Parser pesan, CommandProcessor, guardrail, mapper hasil Android→error | Cakupan ≥ 80% `service/` |
| Kontrak | Validator OpenAPI + JSON Schema (`validate_vectors.py`) | REST & WS; respons server divalidasi skema | 100% vector lulus; 0 breaking change tak disengaja |
| Integrasi (server+DB) | Testcontainers Postgres 16 | Dispatcher end-to-end dengan **device simulator** (WS), reaper, materializer, SSE | Semua skenario §4 |
| Instrumented (Android) | Espresso, emulator API 29/31/34, `adb` | Receiver, Room, Foreground service, izin, kill proses | Skenario §5 |
| E2E nyata | HP fisik 2 SIM + server staging | Pengiriman SMS sungguhan, Doze/OEM, Tailscale | Skenario §6 |
| Non-fungsional | k6/skrip, soak 72 jam | Beban, ketahanan | §7 |
| Keamanan | Checklist spec 09 §9, ZAP baseline, uji otorisasi | | Gerbang rilis |

**Device simulator** (Go, bagian repo uji): membuka WS ke server, bisa memainkan perilaku — normal, lambat, hilang ack, kirim event ganda/terlambat/tak berurutan, putus di titik tertentu, restart dengan `inflight` tertentu. Dasar bagi uji dispatcher tanpa HP.

## 2. Uji unit — kasus kunci

| ID | Kasus | Hasil diharapkan | Trace |
|---|---|---|---|
| T-POL-01 | Merge delay antar lapis: global 3–8 s, SIM 5–10 s, campaign 1–2 s | Efektif min 5, max 10 (guardrail menang) | FR-14 |
| T-POL-02 | Rate: global 10/mnt, device 6/mnt, campaign 20/mnt | Efektif 6/mnt | FR-14 |
| T-POL-03 | Irisan jendela: 08–20 ∩ 10–22 | 10–20; irisan kosong → error `policy.empty_window` | FR-14 |
| T-POL-04 | Retry/TTL: pesan > campaign > SIM > device > global | Lapis terdekat non-null | FR-14 |
| T-POL-05 | Properti: hasil tak pernah lebih longgar dari guardrail, `max≥min`, idempoten (10k acak) | Lulus | FR-14 |
| T-SM-01 | Semua transisi valid pada graf (docs/03 §3) dapat dilalui | Berhasil + event tercatat | FR-11 |
| T-SM-02 | Semua transisi tidak valid ditolak | `ErrInvalidTransition` | FR-12 |
| T-SM-03 | Event usang (`attempt` lama) tidak mengubah status | Dicatat, status tetap | NFR-03 |
| T-SM-04 | `unknown` + event `sent` dari outbox | → `sent` | NFR-03 |
| T-SEG-01 | GSM-7: 160 → 1 segmen; 161 → 2; 306 → 2; 307 → 3 | Sesuai | FR-06 |
| T-SEG-02 | UCS-2: 70 → 1; 71 → 2; 134 → 2; 135 → 3 | Sesuai | FR-06 |
| T-SEG-03 | Karakter extension (`€ { }` …) menghitung 2 septet; emoji → UCS-2 (2 unit) | Sesuai | FR-06 |
| T-PH-01 | `08123456789`, `+62 812-3456-789`, `6281…` → `+6281…`; `0812` terlalu pendek → invalid | Sesuai | FR-06 |
| T-TPL-01 | Variabel hilang → penerima ditolak; variabel ekstra diabaikan | Sesuai | FR-08 |
| T-WIN-01 | Jendela melintasi tengah malam & zona waktu/DST | `next_window_open` benar | FR-13 |
| T-BO-01 | Backoff eksponensial ber-batas + jitter | Dalam rentang | FR-13 |

## 3. Uji kontrak

| ID | Kasus |
|---|---|
| T-CT-01 | `openapi.yaml` valid; klien TS dihasilkan tanpa error |
| T-CT-02 | Setiap respons integrasi lolos validasi skema OpenAPI (REST) |
| T-CT-03 | `validate_vectors.py`: valid lolos, invalid ditolak (CI wajib) |
| T-CT-04 | Setiap pesan WS yang dikirim server pada uji integrasi lolos `ws-schema.json` |
| T-CT-05 | Round-trip Kotlin: serialisasi sealed class → JSON → validasi skema untuk semua tipe device→server |
| T-CT-06 | Detektor breaking-change membandingkan `openapi.yaml` dengan `main` |
| T-CT-07 | `schema.sql` identik dengan hasil semua migrasi (diff skema) |

## 4. Uji integrasi server (dengan device simulator)

| ID | Skenario | Harapan | Trace |
|---|---|---|---|
| T-INT-01 | 2 SIM, 100 pesan, delay 3–8 s (waktu dipercepat 100×) | Tiap SIM serial; jeda ∈ [min,max]; tak ada pesan di 2 SIM | FR-13 |
| T-INT-02 | Prioritas: campaign besar berjalan, kirim cepat prioritas 5 masuk | Kirim cepat didahulukan di slot berikutnya | FR-06 |
| T-INT-03 | `command.ack` tak datang | Lease habis → `queued`; kirim ulang dgn attempt sama; device mendedup | NFR-03 |
| T-INT-04 | Device putus saat `sending`, kembali < `sent_timeout` dengan outbox `sent` | `sending→sent`, tanpa kirim ulang | NFR-03 |
| T-INT-05 | Device putus saat `sending`, tak kembali | → `unknown`; tidak otomatis retry; retry manual butuh konfirmasi | NFR-03, FR-12 |
| T-INT-06 | `retry_on_unknown=true` | → `queued` attempt+1 | FR-13 |
| T-INT-07 | Kegagalan retryable (`no_service`) sampai `max_attempts` | Retry dgn backoff; lalu `failed` | FR-13 |
| T-INT-08 | Kegagalan non-retryable (`invalid_destination`) | `failed` langsung | FR-13 |
| T-INT-09 | Event `sent` ganda & tak berurutan | Idempoten; status tepat | NFR-03 |
| T-INT-10 | `hello` dengan 6 kombinasi inflight vs DB | `drop/keep`/`unknown` benar (spec 04 §6) | NFR-03 |
| T-INT-11 | Rate limit 5/mnt dengan 50 pesan | Tak pernah > 5 pada jendela geser mana pun | FR-13 |
| T-INT-12 | Di luar jendela waktu | Tak ada lease; lanjut saat jendela buka | FR-13 |
| T-INT-13 | Campaign: start→pause→resume→cancel | Counter & state sesuai | FR-09 |
| T-INT-14 | Auto-pause: 30 dari 50 gagal `no_service` | `paused(auto_failure_ratio)`; `invalid_destination` tak dihitung | FR-10 |
| T-INT-15 | Materializer idempoten saat dihentikan di tengah & diulang | Tak ada pesan ganda per penerima | FR-08 |
| T-INT-16 | Idempotency-Key sama 2× paralel | Satu set pesan | FR-07 |
| T-INT-17 | Suppression pada kirim langsung, CSV, API | Tak pernah terkirim | FR-15 |
| T-INT-18 | Revoke saat online | WS tutup 4401 ≤ 5 dtk; reconnect ditolak | FR-04 |
| T-INT-19 | Sesi ke-2 device sama | Sesi 1 ditutup 4409 | FR-02 |
| T-INT-20 | Restart `smscd` di tengah beban | Tanpa kehilangan/duplikasi; lane dibangun ulang | NFR-07 |
| T-INT-21 | Device offline → semua SIM lane berhenti, pesan `queued` tak hilang | Sesuai | FR-02 |
| T-INT-22 | Policy diubah saat berjalan | `config.update` terkirim; hanya pesan baru terdampak | FR-14 |
| T-INT-23 | SSE: reconnect dengan `Last-Event-ID` (≤ 5 mnt / > 5 mnt) | Replay / `resync` | FR-19 |
| T-INT-24 | Cancel `assigned` ketika device sudah `sending` | 409, status tetap | FR-12 |

## 5. Uji instrumented Android — kegagalan & ketahanan

Dijalankan di emulator + `adb` dan perangkat fisik.

| ID | Skenario | Harapan | Trace |
|---|---|---|---|
| T-AND-01 | Kill proses tepat setelah `sms_job accepted`, sebelum `SmsManager` | Saat start ulang: tidak mengirim sendiri; menunggu `sms.send` ulang dari server; dedup menolak double | NFR-03 |
| T-AND-02 | Kill proses setelah `SmsManager` dipanggil, sebelum `sent` diterima | Receiver (manifest) tetap memproses hasil; outbox terisi | NFR-03 |
| T-AND-03 | Mode pesawat di tengah pengiriman 2 part | `failed(radio_off/no_service)`; tidak ada event `sent` | FR-13 |
| T-AND-04 | Reboot HP | `BootReceiver` memulai service; reconnect otomatis; `hello` benar | NFR-01 |
| T-AND-05 | Force-stop app | Watchdog memulihkan ≤ 15 mnt | NFR-01 |
| T-AND-06 | Cabut izin SMS saat berjalan | `permission_denied`; device `degraded`; tidak crash | FR-02 |
| T-AND-07 | Cabut/pasang SIM, toggle eSIM | `sim.report` ≤ 5 dtk; `subscriptionId` berubah ditangani | FR-05 |
| T-AND-08 | Jaringan: Wi-Fi off/on, ganti ke seluler, Tailscale off/on | Reconnect ≤ 10 dtk setelah jaringan kembali | NFR-01 |
| T-AND-09 | `min_gap_ms` & `os_sms_guard` | Penundaan tanpa kegagalan; `not_after` terlewat → `expired` | FR-13 |
| T-AND-10 | `sms.send` duplikat 5× | Tepat 1× panggilan `SmsManager` | NFR-03 |
| T-AND-11 | Close 4401 | Kredensial dihapus; UI onboarding | FR-04 |
| T-AND-12 | Outbox 10.000 baris saat offline lama | Batas ditegakkan; event terminal dipertahankan | NFR-07 |
| T-AND-13 | Pengiriman part: 1, 2, 5 part; laporan delivery sebagian | Agregasi benar; `delivered` hanya jika semua part | FR-11 |
| T-AND-14 | Matriks versi API 29, 31, 33, 34, 35+ | Izin, FGS, notifikasi, restricted settings sesuai spec 03 §2 | NFR-08 |

## 6. Uji E2E nyata (HP fisik)

| ID | Skenario | Kriteria lulus | Trace |
|---|---|---|---|
| T-E2E-01 | Enrollment dari nol | ≤ 3 menit, ≤ 4 interaksi di HP | FR-01, NFR-04 |
| T-E2E-02 | Kirim ke nomor uji pada SIM 1 dan SIM 2 | Diterima; status `sent→delivered` (jika operator mengirim DR) | FR-05, FR-06 |
| T-E2E-03 | Idle layar mati 24 jam di 3 merek (mis. Samsung, Xiaomi, Pixel/Oppo) | Tetap `online`; perintah dieksekusi ≤ 5 dtk | NFR-01, NFR-02 |
| T-E2E-04 | 100 SMS campaign (nomor uji) melalui 2 SIM, delay 5–10 s | Jeda sesuai ±500 ms; tak ada dialog OS (atau tercatat bila R1 terjadi) | FR-13 |
| T-E2E-05 | Matikan Wi-Fi 2 menit di tengah pengiriman | Tanpa SMS ganda; status akhir benar setelah reconnect | NFR-03 |
| T-E2E-06 | Uji batas OS (R1): 100 SMS cepat tanpa guard | Mendokumentasikan perilaku untuk menetapkan default | R1 |
| T-E2E-07 | Delivery report per operator utama | Persentase DR tercatat | R5 |
| T-E2E-08 | Revoke & enroll ulang | Sesuai | FR-04 |

## 7. Non-fungsional

| ID | Uji | Target |
|---|---|---|
| T-NF-01 | Beban penjadwalan: 50 device simulator × 2 SIM, 100.000 pesan antre | ≥ 50 pesan/dtk dilepas; P95 `lease→sms.send` < 200 ms server |
| T-NF-02 | Dashboard: 1 juta baris `messages`, daftar & filter | P95 < 400 ms (cursor), tanpa seq scan pada kueri utama (cek `EXPLAIN`) |
| T-NF-03 | 200 koneksi SSE + 100 WS, 1 jam | Memori stabil (tanpa kebocoran), tanpa event hilang |
| T-NF-04 | Soak 72 jam: 2 HP fisik + simulator, pengiriman periodik | Uptime koneksi ≥ 99%, 0 duplikat, 0 lease yatim |
| T-NF-05 | Chaos server: restart acak tiap 5 menit selama 1 jam | 0 pesan hilang/duplikat; semua mencapai status terminal/unknown |
| T-NF-06 | Backup/restore | Pulih < 30 menit pada 5 juta baris; verifikasi data |
| T-NF-07 | Aksesibilitas dashboard | axe-core 0 pelanggaran serius; navigasi keyboard di tabel/dialog |

## 8. Keamanan

Rujuk spec 09 §9. Tambahan otomatis: uji otorisasi negatif matriks (setiap endpoint × peran), fuzz payload WS terhadap skema, uji brute force enrollment (6 percobaan → blok), uji CSRF, uji formula injection ekspor CSV, ZAP baseline pada dashboard, grep log untuk `body`/secret.

## 9. Keterlacakan kebutuhan

| Kebutuhan | Kasus uji utama |
|---|---|
| FR-01 | T-E2E-01, T-INT-18 |
| FR-02/05 | T-INT-19/21, T-AND-06/07 |
| FR-04 | T-INT-18, T-AND-11, T-E2E-08 |
| FR-06/07 | T-SEG-*, T-PH-01, T-INT-16/17 |
| FR-08–10 | T-INT-13/14/15, T-TPL-01 |
| FR-11/12 | T-SM-*, T-INT-24, T-AND-13 |
| FR-13/14 | T-POL-*, T-INT-01/07/11/12/22, T-AND-09, T-E2E-04 |
| FR-19 | T-INT-23 |
| NFR-01/02 | T-E2E-03, T-AND-04/05/08, T-NF-04 |
| NFR-03 | T-INT-03/04/05/09/10, T-AND-01/02/10, T-E2E-05, T-NF-04/05 |
| NFR-05 | T-NF-01 |
| NFR-07 | T-INT-20, T-NF-05/06 |
| NFR-08 | T-AND-14 |

## 10. Pipeline CI

1. Lint & validasi kontrak (`openapi`, `ws-schema`, `validate_vectors.py`, diff `schema.sql`).
2. Build + unit test server & Android.
3. Integrasi server (Testcontainers + device simulator).
4. Instrumented Android pada emulator matriks API.
5. Pemindaian keamanan (govulncheck, OSV, secret scan, ZAP baseline pada build staging).
6. Artefak: biner, APK bertanda tangan debug, laporan cakupan.
7. Nightly: soak singkat (2 jam) + chaos server; mingguan: E2E fisik terjadwal (manual/semi-otomatis).

**Gerbang rilis fase 1:** semua T-INT, T-AND (kecuali yang butuh fisik, dijalankan manual), T-E2E-01..05, T-NF-01/02/05, checklist keamanan; kriteria penerimaan di `docs/05` §5.
