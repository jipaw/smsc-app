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
| `SESSION_SECRET` | – (wajib) | ≥ 32 byte acak; rotasi = logout semua |
| `TRUST_TAILSCALE_HEADERS` | `false` | `true` hanya bila di belakang Serve lokal |
| `TRUSTED_PROXY_ADDRS` | `127.0.0.1` | Sumber header identitas/`X-Forwarded-*` |
| `DEFAULT_REGION` | `ID` | Normalisasi nomor |
| `AUTO_MIGRATE` | `false` | Jangan aktif di produksi |
| `LOG_LEVEL` | `info` | `debug` tetap tidak mencetak body SMS |
| `WS_MAX_MESSAGE_BYTES` | `65536` | |
| `SSE_REPLAY_SECONDS` | `300` | |
| `METRICS_ADDR` | `127.0.0.1:9090` | Endpoint `/metrics` terpisah |
| `WEBHOOK_ENC_KEY` | turunan `SESSION_SECRET` | Atur terpisah di produksi (merotasi SESSION_SECRET tanpa ini membuat secret webhook tak terbaca) |
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

Aturan alert siap pakai: [`ops/prometheus/alerts.yml`](../../ops/prometheus/alerts.yml) (16 alert; ambang awal, setel dengan data nyata; belum dijalankan di Prometheus sungguhan). Nama metrik sesuai implementasi (`smsc_devices`, `smsc_queue_depth{priority}`, `smsc_message_transitions_total{from,to}`, `smsc_webhook_deliveries_total{result}`, `smsc_device_battery_percent`, dst.; daftar lengkap: `curl 127.0.0.1:9090/metrics`). Tabel di bawah menjelaskan maksud tiap sinyal; bila nama berbeda, implementasi yang benar.

Metrik utama dan ambang awal (Prometheus/Alertmanager atau setara):

| Alert | Kondisi | Tingkat |
|---|---|---|
| `AllDevicesOffline` | `sum(smsc_devices{status="online"}) == 0` selama 5 mnt | Kritis |
| `DeviceOffline` | device `enabled` offline > 10 mnt | Peringatan |
| `QueueStuck` | `smsc_queue_depth > 0` dan `rate(smsc_messages_total{status="sent"}[10m]) == 0` selama 10 mnt (di luar jendela kirim) | Kritis |
| `HighFailureRatio` | gagal/total 15 mnt > 30% | Peringatan |
| `SimNoService` | SIM `no_service` > 15 mnt | Peringatan |
| `LowBattery` | baterai < 15% & tak mengisi | Peringatan |
| `LeaseExpiredSpike` | `increase(smsc_lease_expired_total[10m]) > 20` | Peringatan |
| `DbDisk` | disk > 70% / 85% | Peringatan / Kritis |
| `UnknownMessages` | ada pesan `unknown` > 1 jam | Info |

Log JSON ke journald/stdout → pengumpul log pilihan (Loki/ELK). Dashboard Grafana contoh: device, antrean, laju kirim, rasio gagal, latensi dispatch.

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

HP dicuri: **Revoke** → blokir SIM ke operator → tinjau Transaksi. Rotasi terencana: Devices → Rotasi kredensial (device harus online).

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
