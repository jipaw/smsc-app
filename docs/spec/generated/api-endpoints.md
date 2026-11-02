<!-- DIHASILKAN oleh contracts/tools/gen_api_ref.py dari openapi.yaml. Jangan edit manual. -->
# Daftar endpoint (60 operasi)

Auth: `sessionCookie` = sesi dashboard, `apiKey` = API key sistem eksternal, `deviceToken` = aplikasi Android, `publik` = tanpa auth.

## Auth

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `POST` | `/auth/login` | Login dashboard | publik | 200,401,429 |
| `POST` | `/auth/logout` | Logout | apiKey/sessionCookie | 204 |
| `GET` | `/auth/me` | Profil + peran pengguna saat ini | apiKey/sessionCookie | 200 |

## Devices

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `POST` | `/device-enrollments` | Buat token enrollment sekali pakai (untuk QR) | apiKey/sessionCookie | 201,403 |
| `DELETE` | `/device-enrollments/{enrollmentId}` | Cabut token enrollment yang belum dipakai | apiKey/sessionCookie | 204,404 |
| `GET` | `/devices` | Daftar device | apiKey/sessionCookie | 200 |
| `GET` | `/devices/{deviceId}` | Detail device (termasuk SIM) | apiKey/sessionCookie | 200,404 |
| `PATCH` | `/devices/{deviceId}` | Ubah nama, enable/disable, policy | apiKey/sessionCookie | 200 |
| `DELETE` | `/devices/{deviceId}` | Cabut device (revoke kredensial, putus WebSocket kode 4401) | apiKey/sessionCookie | 204 |
| `POST` | `/devices/{deviceId}/commands` | Kirim perintah ke device via WebSocket | apiKey/sessionCookie | 202,409 |

## DeviceAgent

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `POST` | `/device/enroll` | Aplikasi menukar token QR menjadi kredensial device | publik | 201,401 |
| `GET` | `/device/config` | Ambil konfigurasi terbaru (fallback jika WebSocket belum tersambung) | deviceToken | 200,401 |
| `POST` | `/device/events` | Kirim batch event status SMS via REST (fallback outbox flush) | deviceToken | 200 |

## SIMs

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/sims` | Semua SIM lintas device | apiKey/sessionCookie | 200 |
| `GET` | `/sims/{simId}` | Detail SIM + counter hari ini | apiKey/sessionCookie | 200 |
| `PATCH` | `/sims/{simId}` | Label, enable/disable, policy khusus SIM | apiKey/sessionCookie | 200 |

## Messages

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/messages` | Daftar transaksi | apiKey/sessionCookie | 200 |
| `POST` | `/messages` | Kirim SMS non-campaign (1..1000 penerima) | apiKey/sessionCookie | 202,422,429 |
| `GET` | `/messages/export` | Ekspor CSV dengan filter yang sama seperti daftar | apiKey/sessionCookie | 200 |
| `GET` | `/messages/{messageId}` | Detail transaksi + timeline event | apiKey/sessionCookie | 200,404 |
| `POST` | `/messages/{messageId}/cancel` | Batalkan pesan berstatus `queued` atau `assigned` | apiKey/sessionCookie | 200,409 |
| `POST` | `/messages/{messageId}/retry` | Antrekan ulang pesan `failed`/`expired`/`unknown` | apiKey/sessionCookie | 200,409 |

## Campaigns

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/campaigns` | Daftar campaign | apiKey/sessionCookie | 200 |
| `POST` | `/campaigns` | Buat campaign (status `draft`) | apiKey/sessionCookie | 201,422 |
| `GET` | `/campaigns/{campaignId}` | Detail + counter | apiKey/sessionCookie | 200,404 |
| `PATCH` | `/campaigns/{campaignId}` | Ubah campaign. Penuh saat `draft`; saat `running`/`paused` hanya policy dan sim_ids. | apiKey/sessionCookie | 200,409 |
| `DELETE` | `/campaigns/{campaignId}` | Hapus campaign berstatus `draft` | apiKey/sessionCookie | 204,409 |
| `POST` | `/campaigns/{campaignId}/recipients` | Impor penerima (hanya saat `draft`; bisa dipanggil berulang) | apiKey/sessionCookie | 200 |
| `POST` | `/campaigns/{campaignId}/start` | Mulai (atau jadwalkan jika `schedule.start_at` di masa depan) | apiKey/sessionCookie | 200,409,422 |
| `POST` | `/campaigns/{campaignId}/pause` | Jeda. Pesan `queued` tertahan, yang sedang `sending` diselesaikan. | apiKey/sessionCookie | 200 |
| `POST` | `/campaigns/{campaignId}/resume` | Lanjutkan dari `paused` | apiKey/sessionCookie | 200 |
| `POST` | `/campaigns/{campaignId}/cancel` | Batalkan. Semua pesan `queued` menjadi `cancelled`. | apiKey/sessionCookie | 200 |
| `POST` | `/campaigns/{campaignId}/preview` | Render template untuk N penerima contoh + hitung segmen SMS | apiKey/sessionCookie | 200 |

## Templates

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/templates` | Daftar template | apiKey/sessionCookie | 200 |
| `POST` | `/templates` | Buat template | apiKey/sessionCookie | 201 |
| `GET` | `/templates/{templateId}` | Detail | apiKey/sessionCookie | 200 |
| `PUT` | `/templates/{templateId}` | Ganti isi template | apiKey/sessionCookie | 200 |
| `DELETE` | `/templates/{templateId}` | Hapus | apiKey/sessionCookie | 204 |

## Policies

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/dispatch-policies` | Daftar policy pengiriman | apiKey/sessionCookie | 200 |
| `POST` | `/dispatch-policies` | Buat policy | apiKey/sessionCookie | 201 |
| `GET` | `/dispatch-policies/{policyId}` | Detail | apiKey/sessionCookie | 200 |
| `PUT` | `/dispatch-policies/{policyId}` | Ubah. Perubahan di-push ke device (`config.update`) dan berlaku untuk pesan yang belum di-lease. | apiKey/sessionCookie | 200 |
| `DELETE` | `/dispatch-policies/{policyId}` | Hapus (ditolak jika masih dipakai atau default) | apiKey/sessionCookie | 204,409 |
| `GET` | `/dispatch-policies/{policyId}/effective` | Simulasi policy efektif untuk kombinasi tertentu (debug) | apiKey/sessionCookie | 200 |
| `PUT` | `/settings/default-policy` | Tetapkan policy global default | apiKey/sessionCookie | 204 |

## Compliance

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/suppressions` | Daftar nomor yang diblokir (opt-out / blacklist) | apiKey/sessionCookie | 200 |
| `POST` | `/suppressions` | Tambah nomor (bulk) | apiKey/sessionCookie | 201 |
| `DELETE` | `/suppressions/{suppressionId}` | Hapus dari daftar blokir | apiKey/sessionCookie | 204 |

## Admin

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/api-keys` | Daftar API key (tanpa secret) | apiKey/sessionCookie | 200 |
| `POST` | `/api-keys` | Buat API key. Secret hanya tampil sekali. | apiKey/sessionCookie | 201 |
| `DELETE` | `/api-keys/{apiKeyId}` | Cabut | apiKey/sessionCookie | 204 |
| `GET` | `/users` | Daftar pengguna dashboard (admin) | apiKey/sessionCookie | 200 |
| `POST` | `/users` | Buat pengguna | apiKey/sessionCookie | 201 |
| `GET` | `/audit-logs` | Jejak audit tindakan admin | apiKey/sessionCookie | 200 |
| `GET` | `/webhooks` | Daftar webhook status pesan (fase 2) | apiKey/sessionCookie | 200 |
| `POST` | `/webhooks` | Daftarkan webhook. Payload ditandatangani HMAC-SHA256 (`X-SMSC-Signature`). (fase 2) | apiKey/sessionCookie | 201 |

## Stats

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/stats/overview` | Ringkasan dashboard | apiKey/sessionCookie | 200 |
| `GET` | `/stats/timeseries` | Deret waktu jumlah per status | apiKey/sessionCookie | 200 |
| `GET` | `/healthz` | Liveness. `/readyz` memeriksa koneksi DB. | publik | 200 |

## Realtime

| Method | Path | Fungsi | Auth | Status |
|---|---|---|---|---|
| `GET` | `/events/stream` | Server-Sent Events untuk dashboard | apiKey/sessionCookie | 200 |
