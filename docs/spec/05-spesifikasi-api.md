# Spec 05 — Spesifikasi API

Sumber kebenaran skema: [`contracts/openapi.yaml`](../../contracts/openapi.yaml) (OpenAPI 3.1, tervalidasi). Daftar endpoint hasil generasi: [`generated/api-endpoints.md`](generated/api-endpoints.md) (`python3 contracts/tools/gen_api_ref.py`). Dokumen ini menambahkan semantik, contoh, dan katalog error.

## 1. Konvensi

| Aspek | Aturan |
|---|---|
| Base URL | `https://<host>/api/v1` · health di `/healthz`, `/readyz`, `/metrics` (tanpa prefix) |
| Format | JSON UTF-8; waktu RFC 3339 UTC; id UUIDv7; nomor E.164 |
| Autentikasi | Dashboard: cookie `smsc_session` + header `X-CSRF-Token` untuk `POST/PUT/PATCH/DELETE`. Sistem eksternal: `Authorization: Bearer smsc_live_…`. Device: `Authorization: Bearer <device_secret>` (hanya `/device/*`, `/ws/device`) |
| Pagination | `limit` (1–200, default 50) + `cursor` opak; respons `{items, next_cursor}`. Urutan `created_at DESC, id DESC` kecuali dinyatakan lain |
| Idempotensi | Header `Idempotency-Key` (≤ 128 char) pada `POST /messages` dan `POST /campaigns`; kunci disimpan 24 jam per pemilik; permintaan ulang dengan body berbeda → 409 `idempotency.conflict`; dengan body sama → respons asal |
| Rate limit | API key: 20 permintaan/dtk, burst 40; sesi dashboard: 10/dtk; `POST /auth/login`: 5/menit/IP. Terlampaui → 429 + `Retry-After` |
| Versi | Prefix `/v1`. Perubahan kompatibel (field baru opsional, enum baru pada respons) tidak menaikkan versi; klien wajib mengabaikan field/enum tak dikenal. Perubahan merusak → `/v2` dengan masa tumpang tindih ≥ 6 bulan, `Deprecation`/`Sunset` header |
| Ukuran | Body JSON ≤ 5 MiB; CSV ≤ 20 MiB |
| Field null | Mengirim `null` pada PATCH berarti "hapus/warisi"; field yang tidak dikirim tidak berubah |

## 2. Format error (RFC 9457)

```json
{
  "type": "campaign.invalid_transition",
  "title": "Transisi status tidak valid",
  "status": 409,
  "detail": "Campaign berstatus 'completed' tidak dapat di-pause.",
  "instance": "/api/v1/campaigns/0192…/pause",
  "errors": [{ "field": "recipients[3].to", "message": "bukan nomor yang valid" }]
}
```

### Katalog `type`

| `type` | HTTP | Arti |
|---|---|---|
| `auth.unauthenticated` | 401 | Tidak ada/invalid kredensial |
| `auth.invalid_credentials` | 401 | Email/password salah (pesan seragam) |
| `auth.locked` | 423 | Akun terkunci sementara |
| `auth.forbidden` | 403 | Peran/scope tidak cukup |
| `auth.csrf` | 403 | Token CSRF hilang/salah |
| `validation.failed` | 422 | Validasi field (lihat `errors[]`) |
| `resource.not_found` | 404 | Tidak ditemukan |
| `idempotency.conflict` | 409 | Kunci sama, body berbeda |
| `message.invalid_transition` | 409 | Aksi tidak valid untuk status pesan |
| `campaign.invalid_transition` | 409 | Aksi tidak valid untuk status campaign |
| `campaign.not_editable` | 409 | Field tak boleh diubah pada status ini |
| `campaign.no_capacity` | 422 | Tidak ada SIM aktif yang memenuhi `sim_ids` |
| `policy.empty_window` | 422 | Irisan jendela waktu kosong |
| `policy.in_use` | 409 | Policy dipakai / default |
| `webhook.not_retryable` | 409 | Pengiriman webhook belum berstatus `failed` |
| `privacy.active_messages` | 409 | Penghapusan nomor ditolak: masih ada pesan aktif |
| `device.offline` | 409 | Perintah ke device offline |
| `enrollment.invalid_token` | 401 | Token/kode tidak valid, terpakai, kedaluwarsa |
| `rate_limited` | 429 | Melampaui batas |
| `internal` | 500 | Kesalahan server (dengan `trace_id` di header `X-Request-Id`) |

## 3. Alur dan contoh

### 3.1 Enrollment

```bash
# Dashboard (sesi): buat token
curl -sS -X POST https://smsc.example.ts.net/api/v1/device-enrollments \
  -H 'Content-Type: application/json' -H "X-CSRF-Token: $CSRF" -b cookies.txt \
  -d '{"device_name":"HP Gateway 1"}'
# 201: {"id":"…","token":"…","enroll_url":"smsc://enroll?s=https%3A%2F%2F…&t=…","short_code":"K7QX-92MD","expires_at":"…"}
```

App memanggil `POST /device/enroll` dengan `token`, `install_id`, `device_info`, `sims`; respons memuat `device_id`, `device_secret` (sekali tampil), `ws_url`, `config`.

### 3.2 Kirim non-campaign (API key)

```bash
curl -sS -X POST https://smsc.example.ts.net/api/v1/messages \
  -H "Authorization: Bearer $SMSC_KEY" -H 'Idempotency-Key: inv-2026-1101-0042' \
  -H 'Content-Type: application/json' -d '{
    "recipients":[{"to":"08123456789","vars":{"name":"Budi"},"client_ref":"inv-42"}],
    "body":"Halo {{name}}, tagihan Anda jatuh tempo besok.",
    "priority":7, "sim_ids":[], "ttl_seconds":3600
  }'
```

Respons `202`:

```json
{ "messages": [{ "id":"0192…", "status":"queued", "to":"+628123456789", "parts":1, "encoding":"gsm7", "client_ref":"inv-42", "attempt":0, "max_attempts":3, "expires_at":"…" }],
  "rejected": [] }
```

Catatan: `{{var}}` pada `body` langsung (tanpa `template_id`) dirender memakai `vars` tiap penerima. Penerima dengan variabel tidak lengkap masuk `rejected` (`reason: missing_variable`). Bila **semua** penerima ditolak, respons `422 validation.failed`; bila sebagian, `202` parsial.

### 3.3 Polling status

`GET /messages/{id}` mengembalikan `status` dan `events[]`. Status berikut final: `delivered`, `failed`, `cancelled`, `expired`; `sent` final setelah `delivery_unconfirmed` atau jika `track_delivery=false`; `unknown` perlu tindakan.

### 3.4 Campaign end-to-end

```text
POST /campaigns                         -> 201 draft
POST /campaigns/{id}/recipients (CSV)   -> 200 {accepted,duplicates,suppressed,invalid[]}   (boleh berulang; dedupe wajib)
DELETE /campaigns/{id}/recipients       -> 200 {removed}   (hapus semua untuk impor ulang; hanya draft)
POST /campaigns/{id}/preview            -> 200 contoh render + estimasi
POST /campaigns/{id}/start              -> 200 running|scheduled
GET  /campaigns/{id}                    -> counters, estimated_finish_at
POST /campaigns/{id}/pause|resume|cancel
GET  /messages?campaign_id=…&status=failed
```

Format CSV: UTF-8 (BOM diterima), baris pertama header, kolom wajib `phone`; kolom lain menjadi variabel. Pemisah `,` atau `;` terdeteksi otomatis.

```csv
phone,name,amount,due
08123456789,Budi,150000,5 Nov
+6281298765432,Sari,90000,6 Nov
```

### 3.5 Policy

`PUT /dispatch-policies/{id}` memvalidasi `min ≤ max`, irisan jendela, dan mem-push `config.update` ke device terkait. Perubahan hanya memengaruhi pesan yang **belum di-lease**; pesan `assigned/sending` memakai policy saat lease.

## 4. SSE `/events/stream`

`Content-Type: text/event-stream`. Contoh:

```text
id: 1042
event: message.updated
data: {"id":"0192…","status":"sent","campaign_id":null,"sim_id":"0192…","at":"2026-11-01T10:15:33Z"}

id: 1043
event: device.updated
data: {"id":"0192…","status":"degraded","status_reason":"low_battery","battery_level":12}

event: resync
data: {}
```

| Event | Payload ringkas |
|---|---|
| `message.updated` | `id,status,error_code,campaign_id,sim_id,at` |
| `campaign.updated` | `id,status,pause_reason,counters` |
| `device.updated` | `id,status,status_reason,battery_level,network_type` |
| `sim.updated` | `id,status,signal_level,next_eligible_at` |
| `device.command_result` | `command_id,device_id,ok,detail` |
| `resync` | klien refetch semua data aktif |

## 5. Webhook

Request `POST` ke URL terdaftar, `Content-Type: application/json`, header:
`X-SMSC-Event`, `X-SMSC-Delivery` (uuid), `X-SMSC-Timestamp` (epoch detik), `X-SMSC-Signature: sha256=<hex>` = HMAC-SHA256(secret, `timestamp + "." + body`). Penerima wajib menolak timestamp > 5 menit. Retry: 30 dtk, 2 mnt, 10 mnt, 1 jam, 6 jam setelah percobaan pertama (6 percobaan total, dapat dikonfigurasi) pada non-2xx/timeout 10 dtk; kemudian ditandai `failed` dan dapat dikirim ulang manual. `X-SMSC-Delivery` sama pada semua percobaan — penerima wajib dedup (at-least-once). Redirect tidak diikuti. Event: `message.sent|delivered|failed`, `campaign.completed|paused`, `device.offline`; `webhook.test` hanya dari endpoint uji. Isi SMS tidak pernah disertakan. Secret (`whsec_…`) hanya ada pada respons pembuatan. Payload:

```json
{ "event":"message.delivered", "delivery_id":"0192…", "at":"…",
  "data":{ "message_id":"0192…", "client_ref":"inv-42", "to":"+628123456789", "status":"delivered", "sim_id":"0192…", "delivered_at":"…" } }
```

## 6. Matriks izin ringkas (detail di spec 08 §3)

| Area | admin | operator | viewer | API key (scope) |
|---|---|---|---|---|
| Device/SIM baca | ✓ | ✓ | ✓ | – |
| Device/SIM ubah, revoke, enroll | ✓ | – | – | – |
| Kirim pesan | ✓ | ✓ | – | `messages:send` |
| Baca transaksi | ✓ | ✓ | ✓ (masking) | `messages:read` |
| Campaign CRUD/kontrol | ✓ | ✓ | – | – |
| Template | ✓ | ✓ | baca | – |
| Policy, suppression, API key, pengguna, audit | ✓ | – | – | – |

## 7. Kompatibilitas dan uji kontrak

- CI: validasi `openapi.yaml` (spec validator), regenerasi klien TS dan stub server; diff kontrak diperiksa terhadap `main` (breaking-change detector).
- Uji kontrak server: setiap respons divalidasi terhadap skema OpenAPI dalam test integrasi.
