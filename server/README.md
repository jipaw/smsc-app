# Server SMSC (`smscd`)

Go 1.24 + PostgreSQL 16. Satu biner: REST API, WebSocket device, SSE, dispatcher, reaper, dan dashboard (embed).
Spesifikasi: `docs/spec/04`, kontrak: `contracts/`.

## Status irisan fase 1

| Sudah | Belum (fase 2/3, endpoint membalas `501 not_implemented`) |
|---|---|
| Auth admin (argon2id, sesi, CSRF, lockout), RBAC admin/operator/viewer, API key bertingkat scope | Webhook (fase 3) |
| Enrollment sekali pakai, registry device/SIM, revoke, perintah jarak jauh | Retensi otomatis, rotasi kredensial |
| WS hub protokol v1 (hello + rekonsiliasi, heartbeat, sms.event idempoten, sim.report) | Metrik Prometheus (`/metrics`), 2FA |
| Kirim pesan (normalisasi E.164, GSM-7/UCS-2, template, suppression, Idempotency-Key, `client_ref`) | Multi-instance (hanya leader tunggal via advisory lock) |
| Dispatcher per-SIM: delay acak [min,max], burst, rate menit/jam/hari, jendela waktu, concurrency, kill switch | |
| Reaper: lease/ack timeout, `unknown` (sent timeout), kedaluwarsa, device offline | |
| Mesin status satu pintu, retry + backoff, cancel/retry manual, transaksi + timeline, ekspor CSV aman | |
| Policy berlapis (global/device/SIM/pesan) + CRUD + tampilan efektif, SSE dengan replay, statistik, audit log | |
| **Campaign (fase 2)**: draft, impor penerima JSON/CSV, preview + estimasi, start/jadwal/jeda/lanjut/batal, materializer dengan backpressure, auto-pause, `no_capacity`, deteksi selesai | |

## Campaign — perilaku yang perlu diketahui

- **Impor** (`POST /campaigns/{id}/recipients`): JSON atau CSV (BOM, `,`/`;`, kolom `phone`, `client_ref`, sisanya variabel; sel kosong = variabel tidak ada). Nomor dinormalisasi ke E.164, **satu nomor sekali per campaign**, daftar blokir dilewati. Variabel per baris hanya bisa diperiksa saat impor jika isi pesan sudah diketahui; selain itu diperiksa pada `preview` dan **wajib lolos sebelum `start`** (422 dengan jumlah penerima per variabel).
- **Materializer**: mengubah penerima menjadi pesan per batch ≤ 1.000, dan **menahan diri bila pesan `queued` campaign ≥ `CAMPAIGN_QUEUE_CAP`** (default 20.000) agar antrean/tabel tidak membengkak. Terukur: 50.000 penerima diimpor 2,6 dtk, materialisasi ±2.000 pesan/dtk.
- **Auto-pause** menilai **percobaan** (event `sent` vs `requeued/failed` berkode infrastruktur: `no_service, radio_off, generic_failure, os_rate_limited, ack_timeout, sim_unavailable, device_lost, device_revoked`) pada `window` terakhir sejak baseline (start/resume), butuh ≥ min(window,10) sampel. `invalid_destination` dan kode non-infrastruktur lain tidak dihitung.
- **`no_capacity`**: ada pekerjaan tersisa tetapi tak ada SIM online selama `NO_CAPACITY_AFTER` (default 10 mnt).
- **Selesai** = semua penerima termaterialisasi dan tidak ada pesan `queued/assigned/sending`. Pesan `sent` (menunggu laporan delivery) dan `unknown` tidak menahan status selesai.
- **Prioritas** campaign default 3 < kirim cepat 5. Policy campaign adalah *preferensi* (tidak bisa melonggarkan guardrail SIM/device). Perubahan `policy_id`/`sim_ids` saat berjalan berlaku untuk pesan yang belum di-lease.
- **TTL**: pesan campaign kedaluwarsa menurut `message_ttl_seconds` sejak dimaterialisasi (juga saat campaign dijeda). Untuk campaign panjang, naikkan TTL di policy campaign.
- Isi template dibaca **saat materialisasi**: mengubah template di tengah campaign memengaruhi batch berikutnya.
- Estimasi durasi kasar (jeda rata-rata + 2 dtk kirim, batas per jendela hanya bila jumlah per SIM melampauinya); mengabaikan jendela waktu dan retry.

## Menjalankan

```bash
export DATABASE_URL='postgres://user:pass@localhost:5432/smsc?sslmode=disable'
export PUBLIC_BASE_URL='https://smsc.<tailnet>.ts.net'     # dipakai untuk enroll_url/ws_url
export SESSION_SECRET="$(openssl rand -hex 32)"
export SECURE_COOKIES=false                                   # hanya untuk dev tanpa HTTPS

go run ./cmd/smscd migrate
go run ./cmd/smscd admin create --email admin@example.com     # password acak dicetak sekali
go run ./cmd/smscd serve                                       # 127.0.0.1:8080
```

Variabel lain: lihat `internal/config/config.go` dan `docs/spec/10` §2. Di produksi biarkan `HTTP_ADDR` di loopback
dan publikasikan lewat Tailscale Serve (docs/04).

> Policy default bawaan memakai jendela kirim **08:00–20:00 Asia/Jakarta**. Di luar jam itu pesan menunggu `queued`
> (perilaku benar). Ubah di dashboard/`PUT /dispatch-policies/{id}`; `00:00`–`00:00` berarti sepanjang hari.

## Tes

```bash
make unit                 # policy, SMS (segmen, nomor, template)
make integration          # Postgres asli + simulator HP lewat WebSocket (butuh DB, lihat DB=...)
make contract             # biner nyata; tiap respons divalidasi terhadap contracts/openapi.yaml
make schema               # contracts/schema.sql == hasil semua migrasi (pg_dump)
make load                 # beban campaign 50.000 penerima (ukur impor, materialisasi, latensi API)
```

Tes integrasi membuat dan menghapus database sementara sendiri; set `SMSC_TEST_DATABASE_URL` ke database admin
(`.../postgres`). Tanpa variabel itu, tes integrasi dilewati. `SMSC_TEST_LOG=1` menampilkan log server saat tes.

## Catatan desain yang perlu diketahui

- `attempt` pada pesan `queued` setelah kegagalan adalah nomor percobaan **berikutnya** (naik saat requeue); event dari attempt lama dicatat sebagai `stale_event`.
- Timer reaper (`sent_timeout`) memakai policy **global**; ack timeout memakai policy efektif (global+device+SIM) saat lease.
- "Hari ini" pada statistik memakai tengah malam zona waktu sesi database (UTC).
- Pemeriksaan jeda/inflight/rate dilakukan di dalam transaksi penugasan di bawah kunci baris SIM (mencegah balapan dengan event `sent`; bug ini sempat tertangkap oleh tes).
