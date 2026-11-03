# Spec 10 — Panduan Operasional

Untuk administrator server. Topologi dan keamanan jaringan: `docs/04`. Skema data & backup: spec 07.

## 1. Instalasi

### 1.1 Prasyarat

- Akun Tailscale/tailnet dengan **MagicDNS** dan **HTTPS certificates** aktif; ACL sesuai `docs/04` §1; tag `tag:smsc-server` dan `tag:smsc-device` terdaftar.
- Server Linux 2 vCPU / 2 GB RAM / disk ≥ 50 GB, PostgreSQL 16 (lokal atau kontainer).
- Waktu server tersinkron (chrony/systemd-timesyncd) — dipakai untuk jendela waktu dan TTL.

### 1.2 Langkah (Opsi B: host + systemd)

```bash
# 1. Tailscale
tailscale up --advertise-tags=tag:smsc-server

# 2. Database
createdb smsc && smscd migrate up

# 3. Admin pertama (password acak dicetak sekali)
smscd admin create --email admin@example.com

# 4. Layanan
systemctl enable --now smscd

# 5. Publikasikan di tailnet
tailscale serve --bg --https=443 http://127.0.0.1:8080
```

Opsi A (Compose + sidecar) dan catatan jaringan ada di `docs/04` §4. Verifikasi: `curl https://smsc.<tailnet>.ts.net/readyz` → 200 dari perangkat di tailnet.

## 2. Konfigurasi proses (variabel lingkungan)

| Variabel | Default | Keterangan |
|---|---|---|
| `DATABASE_URL` | – (wajib) | DSN PostgreSQL |
| `HTTP_ADDR` | `127.0.0.1:8080` | Jangan bind ke `0.0.0.0` pada Profil A |
| `PUBLIC_BASE_URL` | – (wajib) | Dipakai di `enroll_url` dan `ws_url` |
| `SESSION_SECRET` | – (wajib) | ≥ 32 byte acak; rotasi tanpa `SESSION_SECRET_PREVIOUS` = logout semua |
| `TRUST_TAILSCALE_HEADERS` | `false` | `true` hanya bila di belakang Serve lokal |
| `TRUSTED_PROXY_ADDRS` | `127.0.0.1` | Sumber header identitas/`X-Forwarded-*` |
| `DEFAULT_REGION` | `ID` | Normalisasi nomor |
| `AUTO_MIGRATE` | `false` | Jangan aktif di produksi |
| `LOG_LEVEL` | `info` | `debug` tetap tidak mencetak body SMS |
| `WS_MAX_MESSAGE_BYTES` | `65536` | |
| `SSE_REPLAY_SECONDS` | `300` | |
| `METRICS_ADDR` | `127.0.0.1:9090` | Endpoint `/metrics` terpisah |
| `WEBHOOK_ENC_KEY` | turunan `SESSION_SECRET` | Kunci enkripsi secret webhook dan rahasia TOTP. Atur terpisah di produksi |
| `SESSION_SECRET_PREVIOUS`, `WEBHOOK_ENC_KEY_PREVIOUS` | – | Kunci lama (koma) selama rotasi; lihat R-06 |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_MODE`, `SMTP_FROM`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_CA_FILE`, `SMTP_TIMEOUT`, `EMAIL_BACKOFF`, `EMAIL_TICK` | email nonaktif | Pengiriman email undangan; lihat `server/README.md`. Simpan kredensial di secret manager, bukan di repo |
| `SCIM_ENABLED`, `SCIM_DEFAULT_ROLE`, `SCIM_ALLOW_ADMIN_ROLE`, `SCIM_ADOPT_EXISTING` | SCIM mati | Provisioning SCIM; lihat `contracts/scim.md` |
| `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_REDIRECT_URL`, `OIDC_NAME`, `OIDC_SCOPES`, `OIDC_AUTO_PROVISION`, `OIDC_ALLOWED_DOMAINS`, `OIDC_DEFAULT_ROLE`, `OIDC_GROUPS_CLAIM`, `OIDC_ADMIN_GROUPS`, `OIDC_OPERATOR_GROUPS`, `OIDC_SYNC_ROLES`, `OIDC_LINK_BY_EMAIL`, `OIDC_TRUST_IDP_MFA`, `OIDC_CA_FILE` | SSO mati | SSO OIDC; tabel di `server/README.md`. Secret klien di secret manager |
| `WEBAUTHN_RP_ID`, `WEBAUTHN_ORIGINS`, `WEBAUTHN_RP_NAME` | host `PUBLIC_BASE_URL` | WebAuthn; lihat `server/README.md` |
| `LOGIN_RATE_PER_MIN`, `LOGIN_BURST` | `5`, `5` | Batas login & verifikasi 2FA per IP (endpoint akun: 2× per pengguna). Naikkan bila banyak admin berbagi satu IP/NAT |
| `DEVICE_ROTATE_GRACE` | `60s` | Masa secret device lama setelah rotasi |
| `WEBHOOK_TIMEOUT`, `WEBHOOK_BACKOFF`, `WEBHOOK_TICK`, `RETENTION_INTERVAL` | lihat `server/README.md` | |

Konfigurasi bisnis (policy, retensi, timeouts) diubah dari dashboard/`settings`, tanpa restart.

## 3. Upgrade

1. Baca catatan rilis (migrasi, perubahan protokol).
2. `pg_dump` cadangan.
3. `smscd migrate up` (kompatibel mundur dengan biner lama sesuai pola expand→contract).
4. Ganti biner & restart (`systemctl restart smscd`): HP otomatis reconnect ≤ 1 menit; tidak ada pengiriman hilang.
5. Verifikasi: `/readyz`, jumlah device online kembali, antrean mengalir.
6. Rollback: biner lama + (hanya bila migrasi bersifat down-safe) `migrate down 1`.

Upgrade aplikasi Android: distribusikan APK baru; device lama tetap berfungsi selama protokol didukung; `app_version` terlihat di dashboard. Versi di bawah minimum → `4426` dan peringatan.

## 4. Backup dan pemulihan

| Item | Prosedur |
|---|---|
| Backup | Cron harian `pg_dump -Fc smsc > smsc-$(date +%F).dump`; retensi 14 hari; salin terenkripsi ke lokasi lain |
| Uji pulih | Bulanan ke instance sementara (`pg_restore`), jalankan `smscd` read-only, cek jumlah pesan & login |
| Pemulihan bencana | Instal ulang → `pg_restore` → `smscd` start → device reconnect sendiri (kredensial valid karena hash ada di DB) → pesan `assigned` kedaluwarsa dikembalikan ke `queued`, `sending` basi menjadi `unknown` untuk ditinjau |

## 5. Kapasitas

- Throughput nyata = Σ SIM × (1 / (delay + waktu kirim)); lihat tabel di dok. 01 §5.5. Contoh: 6 SIM, delay 3–8 dtk ⇒ ±2.700 pesan/jam.
- Pantau `smsc_queue_depth` dan estimasi selesai campaign; bila konsisten terlalu panjang: tambah SIM/HP atau kurangi delay *dalam batas aman operator*.
- Disk: ±2 GB per juta pesan sebelum retensi (spec 07 §7).

## 6. Monitoring dan alert

Aturan alert siap pakai: [`ops/prometheus/alerts.yml`](../../ops/prometheus/alerts.yml) (26 alert; ambang awal, setel dengan data nyata; belum dijalankan di Prometheus sungguhan). Nama metrik sesuai implementasi (`smsc_devices`, `smsc_queue_depth{priority}`, `smsc_message_transitions_total{from,to}`, `smsc_webhook_deliveries_total{result}`, `smsc_device_battery_percent`, dst.; daftar lengkap: `curl 127.0.0.1:9090/metrics`). Tabel di bawah menjelaskan maksud tiap sinyal; bila nama berbeda, implementasi yang benar.

Metrik utama dan ambang awal (Prometheus/Alertmanager atau setara):

| Alert | Kondisi | Tingkat |
|---|---|---|
| `SmscHubRelayDropped` | `increase(smsc_hub_relay_total{result="dropped"}[10m]) > 0` | Peringatan |
| `SmscNoLeader` | `sum(smsc_leader) == 0` selama 2 mnt (worker tidak berjalan) | Kritis |
| `SmscLeaderMetricAbsent` | `absent(smsc_leader)` 5 mnt (smscd tidak ter-scrape) | Kritis |
| `SmscMultipleLeaders` | `sum(smsc_leader) > 1` (mustahil bila lock sehat) | Kritis |
| `SmscLeaderLost` | `increase(smsc_leader_changes_total{event="lost"}[15m]) > 0` | Peringatan |
| `AllDevicesOffline` | `sum(smsc_devices{status="online"}) == 0` selama 5 mnt | Kritis |
| `DeviceOffline` | device `enabled` offline > 10 mnt | Peringatan |
| `QueueStuck` | `smsc_queue_depth > 0` dan `rate(smsc_messages_total{status="sent"}[10m]) == 0` selama 10 mnt (di luar jendela kirim) | Kritis |
| `HighFailureRatio` | gagal/total 15 mnt > 30% | Peringatan |
| `SimNoService` | SIM `no_service` > 15 mnt | Peringatan |
| `LowBattery` | baterai < 15% & tak mengisi | Peringatan |
| `LeaseExpiredSpike` | `increase(smsc_lease_expired_total[10m]) > 20` | Peringatan |
| `DbDisk` | disk > 70% / 85% | Peringatan / Kritis |
| `UnknownMessages` | ada pesan `unknown` > 1 jam | Info |

Log JSON ke journald/stdout → pengumpul log pilihan (Loki/ELK). Dashboard Grafana contoh: device, antrean, laju kirim, rasio gagal, latensi dispatch. Dashboard siap impor untuk SCIM dan rekonsiliasi grup: [`ops/grafana/smsc-scim.json`](../../ops/grafana/smsc-scim.json) (Grafana → Dashboards → Import → unggah JSON → pilih data source Prometheus); berisi status/umur/hasil run, jumlah peran dikoreksi, lalu lintas dan latensi `/scim/v2`. Dashboard menandai restart, deploy (versi `smsc_build_info` berubah; bangun dengan `make build` agar versi terisi) dan pergantian leader sebagai anotasi, sehingga lonjakan error dapat dikaitkan dengan rilis. Ambang oranye/merah umur run mengikuti alert `SmscScimReconcileStale` (3 jam).

## 7. Runbook

### R-01 Device offline

1. Dashboard → Devices → lihat `last_seen_at`, `status_reason`.
2. Terakhir terlihat < 5 menit & baterai OK → tunggu reconnect (backoff maks 60 dtk).
3. Periksa di HP: notifikasi SMSC ada? Tailscale terhubung? Jika Tailscale mati: buka app, aktifkan; pastikan *Always-on VPN*.
4. Jaringan HP (Wi-Fi/seluler), baterai, mode hemat daya.
5. Jika app tidak berjalan: buka app (memulai ulang service); periksa pengaturan OEM (spec 03 §9).
6. Berulang tiap beberapa jam → lihat `service_restarts` di detail device; terapkan panduan OEM; pertimbangkan HP lain.
7. Tidak mungkin dipulihkan: revoke & enroll ulang.

### R-02 Antrean menumpuk / tidak mengalir

1. Cek kill switch global (`Pause semua`) dan status campaign (`paused`?).
2. Cek jendela waktu policy (di luar jam kirim = normal) dan `rate_per_*` yang tercapai (SIM → "Berikutnya").
3. Cek ada device `online` dengan SIM `active` dan `enabled` yang sesuai `route`/`sim_ids` pesan.
4. Cek `os_sms_guard` (capability `os_sms_limit_unlocked=false`) — throughput dibatasi 25/30 mnt per HP.
5. Cek log `dispatch` untuk alasan lane tidak siap; `GET /dispatch-policies/{id}/effective?sim_id=…`.

### R-03 Banyak `failed`

1. Lihat distribusi `error_code` pada Transaksi (filter failed + kelompokkan).
2. `no_service`/`radio_off` → periksa sinyal/mode pesawat, SIM di HP. `os_rate_limited` → kurangi laju atau buka batas OS (R1). `generic_failure` → pulsa/blokir operator, coba SIM lain. `permission_denied` → izin SMS hilang (Android mencabut izin): beri izin kembali, matikan "auto-reset permissions".
3. Campaign auto-paused: perbaiki penyebab → Resume.

### R-04 Pesan `unknown`

Penyebab: device hilang saat `sending`. Langkah: tunggu device kembali (outbox akan mengoreksi status). Jika device tak kembali: periksa di HP/operator apakah SMS terkirim; bila yakin belum terkirim → Retry dengan konfirmasi risiko ganda; bila ragu → biarkan/hapus dari antrean tindak lanjut.

### R-05 SIM bermasalah / diganti

SIM dicabut → `absent`; pasang SIM baru → muncul sebagai SIM pada slot sama; beri label ulang dan sesuaikan policy. Pesan `queued` dengan `route_sim_ids` ke SIM lama akan menunggu sampai kedaluwarsa — gunakan Cancel/Retry dengan SIM lain.

### R-06 Rotasi kredensial / HP dicuri

HP dicuri: **Revoke** → blokir SIM ke operator → tinjau Transaksi.

Rotasi terencana — semuanya tanpa downtime:

| Yang diputar | Cara | Masa tumpang tindih |
|---|---|---|
| Secret device | Devices → detail → **Putar kredensial** (device harus online) | Dua tahap: secret baru dikirim lewat WSS dan baru berlaku setelah HP mengonfirmasi (atau langsung dipakai); secret lama sah 60 dtk (`DEVICE_ROTATE_GRACE`). HP gagal menyimpan → secret baru dibuang, tidak ada risiko terkunci. Secret tertunda kedaluwarsa 10 mnt |
| API key | Pengaturan → API key → **Putar** (`POST /api-keys/{id}/rotate`) | Pilihan 0 / 1 jam / 24 jam / 7 hari. Id, nama, scope tetap |
| Secret webhook | Pengaturan → Webhook → **Putar secret** | Selama masa itu pengiriman membawa dua tanda tangan (`sha256=<baru>,sha256=<lama>`); penerima menerima bila salah satu cocok |
| Password sendiri | Akun → Ganti password | Sesi lain pengguna dikeluarkan. Lupa password: `smscd admin reset-password --email …` |
| `SESSION_SECRET` | Setel nilai baru, simpan yang lama di `SESSION_SECRET_PREVIOUS` (koma bila banyak), restart. Kosongkan `…_PREVIOUS` setelah ≥ 7 hari (umur maks. sesi). Tanpa `…_PREVIOUS` = logout massal | Sesi lama tetap sah |
| `WEBHOOK_ENC_KEY` (juga kunci rahasia TOTP) | Setel kunci baru, pindahkan yang lama ke `WEBHOOK_ENC_KEY_PREVIOUS`, restart, jalankan **`smscd secrets reencrypt`**, lalu hapus kunci lama. Bila `WEBHOOK_ENC_KEY` tidak diatur, kunci mengikuti `SESSION_SECRET` dan rotasinya otomatis ikut `SESSION_SECRET_PREVIOUS` | Selama kunci lama ada di `…_PREVIOUS`, data lama terbaca |

Kunci yang salah **tidak** merusak data diam-diam: pengiriman webhook gagal dengan pesan jelas dan login 2FA ditolak; pulihkan kunci lama lalu ulangi. Pengguna 2FA yang terkunci keluar: `DELETE /users/{id}/2fa` oleh admin lain, atau `smscd admin reset-2fa --email …`.

### R-06b Mewajibkan 2FA / kelola pengguna

Menyalakan: pastikan **Anda** sudah memakai 2FA (Akun), lalu Pengaturan → Keamanan → *Wajib untuk admin* (atau *semua*). Dampak seketika: pengguna dalam cakupan tanpa 2FA — termasuk yang sedang login — hanya dapat membuka halaman Akun sampai mengaktifkan 2FA; API key tidak terpengaruh. Beri tahu pengguna dahulu; halaman Keamanan menampilkan berapa yang akan terdampak. Mematikan: pilih *Opsional* (efek seketika).

Kebijakan password baru berlaku di semua pintu (spec 09); bila admin menerima 422 `terlalu lemah`, pilih frasa panjang yang tidak memuat nama akun. Pengaturan → Retensi juga mengatur masa simpan undangan selesai (default 90 hari).

SCIM: aktifkan `SCIM_ENABLED=true`, buat token di Pengaturan → SCIM, lalu di IdP isi Base URL `PUBLIC_BASE_URL/scim/v2` + bearer token; petakan `userName` ← email, `active`, `roles` (viewer/operator), `externalId`. Uji: pengguna uji di IdP → muncul di Pengguna dengan lencana SCIM dan masuk lewat SSO. Putar token dengan membuat yang baru lalu mencabut yang lama. Admin tetap dikelola manual (kecuali `SCIM_ALLOW_ADMIN_ROLE`). **Peran lewat grup**: di IdP dorong grup (mis. “Gateway Ops”) beserta anggotanya, lalu set `SCIM_OPERATOR_GROUPS=Gateway Ops` (dst. untuk viewer/admin) dan restart; peran efektif = tertinggi dari atribut `roles` dan grup, sehingga untuk menurunkan peran cabut keanggotaan/atribut. Mengubah daftar `SCIM_*_GROUPS` berlaku pada restart (rekonsiliasi otomatis); rekonsiliasi juga berjalan tiap `SCIM_RECONCILE_INTERVAL` (default 1 jam, `0` = hanya saat start) dan status run terakhir tampil di Pengaturan → SCIM. Karyawan keluar: nonaktifkan di IdP → SCIM `active=false` mencabut sesi seketika (jangan hanya mengandalkan penghapusan akun IdP tanpa SCIM).

SSO OIDC: (1) daftarkan klien di IdP (jenis *web application*, authorization code; redirect URI persis `PUBLIC_BASE_URL/api/v1/auth/oidc/callback`; scope `openid email profile` + klaim grup bila perlu); (2) isi `OIDC_*` (lihat `server/README.md`) lalu restart — salah konfigurasi menggagalkan start; (3) Pengaturan → SSO → *Uji koneksi ke IdP*; (4) putuskan kebijakan: `OIDC_AUTO_PROVISION` + `OIDC_ALLOWED_DOMAINS` + pemetaan grup, atau biarkan pengguna menautkan dari Akun. Pastikan **IdP menegakkan MFA** (atau set `OIDC_TRUST_IDP_MFA=false`). Karyawan keluar: nonaktifkan di IdP **dan** di dashboard (sesi aktif hanya dicabut oleh dashboard). `?sso_error=not_linked`: akun belum tertaut — tautkan dari Akun (login password) atau aktifkan penyediaan/penautan email. Admin terkunci di IdP: tetapkan password lokal untuk akunnya sebagai jalur darurat.

Passkey (login tanpa password): nyalakan di Pengaturan → Keamanan setelah memutuskan kebijakan (lihat spec 09 soal passkey tersinkron); pengguna mendaftarkan dari Akun → Tambah kunci → *Daftarkan sebagai passkey*. Passkey hilang/perangkat dicuri: hapus kunci (password + kode) atau reset 2FA oleh admin (menghapus semua kredensial dan mencabut sesi); mematikan fitur menolak login passkey seketika.

Kunci keamanan: Akun → *Kunci keamanan & passkey*. Pastikan dashboard diakses lewat nama domain HTTPS (di Tailscale: nama `*.ts.net`) — alamat IP tidak dapat dipakai untuk WebAuthn. `WEBAUTHN_RP_ID` harus sama dengan domain yang dipakai pengguna; **mengganti domain berarti semua kunci harus didaftarkan ulang** (pengguna masuk dengan kode pemulihan/TOTP, atau admin mereset 2FA). Sarankan minimal dua kunci per akun (cadangan).

Mengundang: Pengaturan → Pengguna → *Undang pengguna* → salin tautan (tampil sekali) dan kirim lewat jalur aman, atau centang *Kirim tautan ke email penerima* (butuh SMTP; uji dulu di Pengaturan → Email). Email gagal (kolom *Email* = gagal, arahkan kursor untuk alasan): periksa SMTP lalu *Kirim ulang* atau bagikan tautan manual. Salah kirim / batal: *Cabut* (email yang masih antre ikut dibatalkan). Pensiun akun: *Nonaktifkan* lalu (opsional) *Hapus*.

Terkunci keluar (HP authenticator hilang, kebijakan menyala): admin lain → Pengguna → *Reset 2FA*; bila tidak ada admin lain: `smscd admin reset-2fa --email …` di server. Pengguna lalu menyiapkan 2FA baru (kebijakan tetap berlaku). Karyawan keluar: *Nonaktifkan* (sesi langsung dicabut) — jangan hapus agar jejak audit tetap bermakna. Lupa password: *Set password* atau `smscd admin reset-password`. Tidak bisa mengubah akun sendiri; minta admin lain.

### R-07 Kunci/tailnet bermasalah

Node server atau HP kehilangan akses karena key expiry/ACL: periksa admin console Tailscale (Machines → expiry), pastikan tag benar dan expiry dinonaktifkan untuk node gateway; `tailscale status` di server; `tailscale serve status`.

### R-08 DB penuh / lambat

Cek ukuran tabel (`messages`, `message_events`), jalankan retensi manual, `VACUUM (ANALYZE)`, tambah disk; evaluasi partisi (spec 07 §6).

### R-09 Server tidak start

`journalctl -u smscd`; kesalahan umum: `DATABASE_URL`, migrasi tertunda (`smscd migrate status`), port dipakai, `SESSION_SECRET` hilang.

## 8. Kill switch dan mode pemeliharaan

- **Pause semua pengiriman**: tombol admin (Overview) → dispatcher berhenti melepas lease baru; yang sedang `sending` selesai. Banner merah muncul sampai dilanjutkan.
- **Pemeliharaan server**: pause semua → tunggu `sending` = 0 (dashboard) → restart/upgrade → resume.

## 9. Housekeeping rutin

| Frekuensi | Tugas |
|---|---|
| Harian | Retensi otomatis berjalan; cek alert; backup |
| Mingguan | Tinjau device `degraded`, SIM `no_service`, pesan `unknown`; cek ruang disk |
| Bulanan | Uji pulih; tinjau API key tak terpakai; tinjau ACL/akses pengguna; pembaruan dependensi/OS |
| Per kuartal | Rotasi `SESSION_SECRET`/API key terjadwal; uji kill switch & prosedur HP hilang |
