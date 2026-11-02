# 05 — Roadmap, Risiko, Asumsi, dan Outline Dokumentasi Penuh

## 1. Risiko dan validasi (PoC sebelum membangun penuh)

| ID | Risiko | Dampak | Validasi PoC | Mitigasi bila terbukti |
|---|---|---|---|---|
| R1 | Batas kirim OS (dialog konfirmasi ±30 SMS/30 mnt) memblokir pengiriman | Throughput nyata jauh di bawah policy | Kirim 100 SMS cepat di Android 10, 13, 15; amati dialog/`RESULT_ERROR_LIMIT_EXCEEDED` | `os_sms_guard`; jadi default SMS app (`ROLE_SMS`); menaikkan `sms_outgoing_check_max_count` via adb/MDM pada device khusus |
| R2 | Izin SMS terblokir untuk app sideload (restricted settings) | Onboarding macet | Instal APK pada Android 13+ dan lihat alur izin | Wizard terpandu; distribusi via MDM/installer sesi yang menandai sumber terpercaya |
| R3 | Koneksi WS mati di Doze/OEM-kill | Perintah tidak diterima "kapan saja" | Uji 24–72 jam semalam di Xiaomi/Oppo/Samsung/Pixel dengan layar mati | Panduan per merek, watchdog, heartbeat adaptif; fallback jendela *wake* via alarm |
| R4 | Tailscale dimatikan sistem / key kedaluwarsa | Device offline | Pantau di tes R3; cek tagged node tanpa expiry | Peringatan "tailscale_vpn_active=false", panduan Always-on VPN |
| R5 | Delivery report tidak datang dari operator | Status `delivered` tidak akurat | Ukur persentase DR per operator utama | `sent` sebagai akhir sah; tampilkan "tidak terkonfirmasi" |
| R6 | `subscriptionId` berubah / eSIM | Salah SIM | Cabut-pasang SIM & toggle eSIM saat uji kirim | Identitas `slot_index`, resolusi subId saat kirim, `sim.report` pada perubahan |
| R7 | Satu VPN per HP | Tidak bisa pakai VPN lain | – | Opsi Tailscale tertanam (dok. 01 §4.2) |
| R8 | Kebijakan operator/regulasi (spam, izin pengirim) | Nomor diblokir | – | Delay/jitter, kuota harian, suppression, auto-pause; panduan kepatuhan (UU PDP, opt-in) |
| R9 | Auth key Tailscale di Android | Setup lebih panjang dari target | Uji login Tailscale memakai auth key pada Android | Dokumentasikan alur login terbaik; bagian di luar kendali app |

Urutan PoC: **R1 → R3 → R2 → R6 → R5**, di perangkat nyata. Hasilnya dapat mengubah default policy dan wizard.

> **Skeleton PoC tersedia:** `android/` (aplikasi uji), `poc/server/` (server uji), `poc/README.md` (prosedur per risiko) dan `poc/RESULTS_TEMPLATE.md`.

## 2. Roadmap

| Fase | Cakupan | Keluaran |
|---|---|---|
| **0 — Kontrak & PoC** | Dokumen ini, PoC R1–R3, R6, repo skeleton + CI codegen OpenAPI | Keputusan terkunci, `contracts/` v1 |
| **1 — MVP inti** | Enrollment QR, WS + heartbeat, multi SIM, kirim non-campaign, dispatcher dengan delay/rate/retry, transaksi + timeline, dashboard Overview/Devices/Kirim/Transaksi, auth admin | Bisa kirim SMS end-to-end via ≥ 2 SIM |
| **2 — Campaign & manajemen** | Template, impor CSV, wizard & kontrol campaign, auto-pause, policy berlapis + editor, send window, suppression, API key, SSE penuh | Fitur sesuai seluruh kebutuhan awal |
| **3 — Operasional** | Webhook status, ekspor, audit UI, metrik/alert, retensi, rotasi kredensial, update APK, role operator/viewer matang | Siap produksi |
| **4 — Lanjutan (opsional)** | SMS masuk/balasan, default SMS app, Tailscale tertanam, multi-instance server (leader election + pub/sub), multi-tenant | Sesuai kebutuhan |

## 3. Pertanyaan terbuka (butuh keputusan Anda)

1. **Skala & jumlah HP/SIM** yang direncanakan (menentukan perlu tidaknya fase 4 multi-instance)?
2. **Distribusi app:** sideload manual, atau ada MDM? (memengaruhi R2 dan update APK)
3. **SMS masuk** (balasan/OTP) perlu di v1? Saat ini di luar cakupan.
4. **Sistem eksternal** akan memakai API (API key) atau murni dashboard di awal?
5. **Tailscale:** pakai akun tailnet organisasi yang ada? Boleh memakai *tagged devices* dan MagicDNS/HTTPS?
6. **Kepatuhan:** ada kebutuhan opt-out otomatis (balas "STOP"), masking nomor, atau retensi tertentu?
7. **Bahasa UI:** Indonesia saja atau bilingual?
8. **Pilihan stack backend:** Go direkomendasikan; apakah tim lebih nyaman dengan TypeScript (NestJS)? Kontrak tidak berubah.

## 4. Outline dokumentasi penuh (turunan dari paket ini)

| Dokumen | Sumber utama | Isi |
|---|---|---|
| Product Requirements | dok. 01 §1 | Persona, user story, kriteria penerimaan per fitur |
| Arsitektur Sistem | dok. 02 §1, dok. 04 | Komponen, deployment, keputusan (ADR) |
| Desain Aplikasi Android | dok. 02 §1.2, WS protocol | Modul, service lifecycle, izin per versi Android, penanganan OEM, strategi uji |
| Desain Backend | dok. 02 §4–6, dok. 03 | Dispatcher, WS hub, reaper, materializer, transaksi DB |
| Spesifikasi API | `contracts/openapi.yaml` | Referensi + contoh + kode error |
| Spesifikasi Protokol Device | `contracts/device-ws-protocol.md` | + JSON Schema per pesan dan test vector |
| Model Data & Migrasi | dok. 03 | DDL final, indeks, retensi |
| Spesifikasi Dashboard | dok. 02 §9 | Wireframe, state, komponen, aksesibilitas |
| Keamanan | dok. 04 §2–3 | Threat model lengkap, hardening, respons insiden |
| Panduan Operasional | dok. 04 §4–6 | Instalasi, backup, monitoring, runbook (device offline, antrean macet, SIM bermasalah) |
| Panduan Pengguna | dok. 04 §5, dok. 02 §9 | Setup HP, membuat campaign, membaca transaksi |
| Rencana Uji | semua | Unit, integrasi (emulator + server palsu), E2E dengan HP nyata, uji ketahanan 72 jam, uji kegagalan (cabut jaringan, kill service, reboot) |

## 5. Definisi selesai untuk fase 1 (kriteria penerimaan awal)

- Enrollment dari nol ≤ 3 menit, ≤ 4 interaksi di HP setelah Tailscale tersambung.
- HP idle dengan layar mati ≥ 24 jam tetap menerima dan mengeksekusi `sms.send` dalam ≤ 5 dtk setelah di-lease (tiga merek berbeda).
- Mematikan Wi-Fi 2 menit saat pengiriman: tidak ada SMS ganda, status akhir benar setelah reconnect.
- 100 pesan melalui 2 SIM mengikuti delay `[min,max]` yang dikonfigurasi (toleransi ±500 ms) dan tidak ada pesan di dua SIM sekaligus.
- Semua transisi status tercatat di timeline dan sesuai graf pada dok. 03 §3.
