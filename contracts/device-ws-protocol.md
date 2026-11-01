# Kontrak WebSocket Device (protocol v1)

Kanal dua arah antara aplikasi Android dan server. Device **selalu yang memulai koneksi** (outbound ke alamat tailnet server), sehingga tidak perlu port terbuka di HP dan server tidak perlu tahu IP HP.

- Endpoint: `wss://<host-tailnet>/ws/device`
- Autentikasi: header `Authorization: Bearer <device_secret>` pada HTTP upgrade. Tanpa/invalid → HTTP 401, tidak di-upgrade.
- Framing: satu pesan = satu *text frame* berisi JSON UTF-8, maksimal 64 KiB.
- Subprotocol: `smsc.v1`. Versi tidak didukung → close `4426`.
- Satu device = satu koneksi aktif. Koneksi baru menggantikan yang lama (yang lama di-close `4409`).

## 1. Envelope

```json
{
  "v": 1,
  "type": "sms.send",
  "id": "0192f3c0-7a1e-7c3e-9a55-3b8a1f0e2d11",
  "ref": null,
  "ts": "2026-11-01T10:15:30.123Z",
  "payload": { }
}
```

| Field | Arti |
|---|---|
| `v` | Versi protokol (integer). |
| `type` | Jenis pesan (tabel di bawah). |
| `id` | UUIDv7 pesan ini, dibuat oleh pengirim. Dipakai untuk dedup dan korelasi. |
| `ref` | `id` pesan yang dijawab (untuk `*.ack`, `error`, `hello.ack`), selain itu `null`. |
| `ts` | Waktu pengirim. Server **tidak** mempercayai jam device untuk logika; hanya untuk diagnosa skew (`clock_skew_ms` di heartbeat). |
| `payload` | Objek sesuai `type`. |

Pesan dengan `type` tidak dikenal harus **diabaikan** (forward-compatible) dan dibalas `error` bila memiliki `id`.

## 2. Daftar pesan

| Arah | `type` | Tujuan | Perlu ack |
|---|---|---|---|
| D→S | `hello` | Pembuka sesi + rekonsiliasi state | `hello.ack` |
| S→D | `hello.ack` | Konfigurasi, perintah rekonsiliasi | – |
| D→S | `heartbeat` | Kesehatan periodik | – |
| S→D | `sms.send` | Perintah kirim satu SMS | `command.ack` |
| S→D | `sms.cancel` | Batalkan pesan yang belum dikirim | `command.ack` |
| D→S | `command.ack` | Terima/tolak perintah | – |
| D→S | `sms.event` | Perubahan status SMS (sending/sent/delivered/failed) | `event.ack` |
| S→D | `event.ack` | Konfirmasi event tersimpan → boleh dihapus dari outbox | – |
| D→S | `sim.report` | Daftar SIM terkini (saat berubah / diminta) | – |
| S→D | `sim.refresh` | Minta `sim.report` | – |
| S→D | `config.update` | Dorong konfigurasi/policy guardrail baru | `command.ack` |
| S→D | `device.command` | `ping`, `restart_service`, `upload_logs`, `rotate_credential` | `command.ack` + hasil |
| D→S | `device.command_result` | Hasil `device.command` | – |
| keduanya | `error` | Kesalahan protokol/validasi | – |

Selain itu, ping/pong level WebSocket dikirim server tiap `ws_ping_interval_sec` (default 20 dtk). Tidak ada pong dalam 2 interval → koneksi dianggap mati dan ditutup.

## 3. Payload

### 3.1 `hello` (D→S)

Dikirim segera setelah tersambung. `inflight` adalah isi tabel `sms_job` lokal (Room) yang belum terminal, dan `pending_events` jumlah event di outbox.

```json
{
  "app_version": "0.1.0",
  "protocol": 1,
  "config_version": 7,
  "capabilities": {
    "default_sms_app": false,
    "os_sms_limit_unlocked": false,
    "battery_optimization_ignored": true,
    "tailscale_vpn_active": true
  },
  "sims": [ { "slot_index": 0, "subscription_id": 3, "carrier_name": "Telkomsel", "registered": true, "signal_level": 3, "roaming": false } ],
  "inflight": [
    { "message_id": "0192...", "attempt": 1, "state": "sending", "parts_total": 2, "parts_sent": 1, "parts_delivered": 0 }
  ],
  "pending_events": 4
}
```

### 3.2 `hello.ack` (S→D)

```json
{
  "session_id": "0192...",
  "heartbeat_interval_sec": 30,
  "config_version": 8,
  "config": { "guardrails": { "min_gap_ms": 3000, "max_per_minute": 12 } },
  "reconcile": {
    "drop": ["<message_id yang sudah cancelled/expired/tidak dikenal server>"],
    "keep": ["<message_id yang masih valid>"]
  }
}
```

Aturan rekonsiliasi (server sebagai sumber kebenaran):

1. Setiap item `inflight` yang server anggap terminal/tidak ada → masuk `drop` (device menghentikan retry lokal dan menghapus job setelah event terminalnya terkirim).
2. Pesan `assigned` yang belum di-ack tetapi lease masih berlaku → server mengirim ulang `sms.send` setelah `hello.ack`. Device men-*dedup* berdasarkan `message_id` (lihat §5).
3. Pesan `sending` di server tetapi **tidak ada** di `inflight` device (mis. app di-reinstall) → server menandai `unknown`.
4. Seluruh `pending_events` dikirim ulang oleh device setelah `hello.ack`.

### 3.3 `heartbeat` (D→S)

```json
{
  "battery": { "level": 82, "charging": true, "temperature_c": 31.2 },
  "network": { "type": "wifi", "tailscale_vpn_active": true, "rtt_ms": 38 },
  "sims": [ { "slot_index": 0, "registered": true, "signal_level": 3, "roaming": false } ],
  "queue_depth_local": 0,
  "clock_skew_ms": -120,
  "uptime_sec": 86400
}
```

Server menurunkan status device menjadi `degraded` jika: baterai < 15% dan tidak mengisi daya, semua SIM `registered=false`, `tailscale_vpn_active=false`, atau `clock_skew_ms` > 60 dtk. `offline` bila tidak ada heartbeat/ping selama 90 dtk atau socket tertutup.

### 3.4 `sms.send` (S→D)

```json
{
  "message_id": "0192...",
  "attempt": 1,
  "sim": { "slot_index": 0, "subscription_id_hint": 3 },
  "to": "+6281234567890",
  "body": "Halo Budi, tagihan Rp150.000 jatuh tempo 5 Nov.",
  "delivery_report": true,
  "not_after": "2026-11-01T12:15:30Z",
  "min_gap_ms": 3000
}
```

- Device memilih SIM lewat `slot_index`, lalu memetakan ke `subscriptionId` **saat ini** (hint hanya fallback). Slot kosong/tidak aktif → event `failed` dengan `sim_unavailable`.
- `not_after` terlewati saat diterima → langsung `failed` `expired`.
- Pesan dibagi dengan `SmsManager.divideMessage` dan dikirim via `sendMultipartTextMessage`, masing-masing part diberi `sentIntent` dan (jika `delivery_report`) `deliveryIntent`.
- `min_gap_ms` adalah guardrail lokal: device menunda eksekusi bila gap dari pengiriman sebelumnya pada SIM yang sama lebih kecil, tanpa melaporkan error.

### 3.5 `command.ack` (D→S)

```json
{ "accepted": true, "reason": null }
```

`reason` bila `accepted=false`: `duplicate`, `expired`, `sim_unavailable`, `permission_denied`, `busy`, `invalid`. `accepted=true` memindahkan pesan ke `sending` (setelah job disimpan ke Room, **sebelum** SmsManager dipanggil). `duplicate` berarti job sudah ada: server mengambil state terakhir dari event outbox, bukan menganggap gagal.

### 3.6 `sms.event` (D→S)

```json
{
  "event_id": "0192...",
  "message_id": "0192...",
  "attempt": 1,
  "state": "sent",
  "part_index": null,
  "parts_total": 2,
  "error_code": null,
  "android_result_code": -1,
  "at": "2026-11-01T10:15:33.870Z"
}
```

`state`: `sending`, `part_sent`, `sent`, `part_delivered`, `delivered`, `failed`. Device mengagregasi per-part: `sent` hanya ketika **semua** part `RESULT_OK`; `failed` jika salah satu part gagal pada tahap sent; `delivered` ketika semua laporan delivery diterima. Event `part_*` bersifat informatif.

Pemetaan hasil Android → `error_code`:

| `SmsManager` result | `error_code` | Retry default |
|---|---|---|
| `RESULT_ERROR_GENERIC_FAILURE` | `generic_failure` | ya |
| `RESULT_ERROR_RADIO_OFF` | `radio_off` | ya |
| `RESULT_ERROR_NULL_PDU` | `null_pdu` | tidak |
| `RESULT_ERROR_NO_SERVICE` | `no_service` | ya |
| `RESULT_ERROR_LIMIT_EXCEEDED` | `os_rate_limited` | ya (dengan backoff panjang) |
| `RESULT_ERROR_SHORT_CODE_NOT_ALLOWED` / `..._NEVER_ALLOWED` | `short_code_blocked` | tidak |
| `RESULT_INVALID_ARGUMENTS`, `RESULT_INVALID_SMS_FORMAT` | `invalid_destination` | tidak |
| `SecurityException` / izin dicabut | `permission_denied` | tidak, device → `degraded` |
| status report delivery = gagal | `delivery_failed` | tidak |

Nilai mentah selalu disertakan di `android_result_code` supaya pemetaan dapat dikoreksi tanpa rilis app.

### 3.7 `event.ack` (S→D)

```json
{ "event_ids": ["0192...", "0192..."] }
```

Device menghapus event dari outbox hanya setelah menerima ini. Server idempoten terhadap `event_id` ganda dan mengabaikan event dengan `attempt` lebih lama dari attempt saat ini kecuali bersifat terminal informatif.

### 3.8 `sim.report` (D→S) dan `sim.refresh` (S→D)

`sim.report.payload.sims` memakai skema `SimReport` di OpenAPI. Dikirim saat `OnSubscriptionsChangedListener` terpanggil (SIM dicabut/dipasang/eSIM diaktifkan), saat start, dan atas `sim.refresh`.

### 3.9 `config.update` (S→D)

```json
{ "config_version": 9, "config": { "heartbeat_interval_sec": 30, "guardrails": { "min_gap_ms": 3000, "max_per_minute": 12, "os_sms_guard": { "max_per_window": 25, "window_sec": 1800 } } } }
```

### 3.10 `device.command` / `device.command_result`

```json
{ "command_id": "0192...", "action": "restart_service" }
```
```json
{ "command_id": "0192...", "ok": true, "detail": null }
```

`rotate_credential` membuat device meminta secret baru lewat WS: server mengirim `{"new_secret": "..."}` pada `device.command_result` jawaban; secret lama berlaku 60 detik (grace) lalu mati.

### 3.11 `error`

```json
{ "code": "invalid_payload", "message": "to must be E.164", "fatal": false }
```

`fatal=true` diikuti close frame.

## 4. Siklus koneksi

```
connect (Bearer) ─▶ 101 Upgrade ─▶ D: hello ─▶ S: hello.ack ─▶ D: flush outbox, kirim heartbeat
                                                ─▶ S: kirim ulang sms.send assigned ─▶ steady state
```

Close code khusus:

| Code | Arti | Reaksi device |
|---|---|---|
| 1001/1006/1012 | Server restart / jaringan putus | Reconnect dengan backoff |
| 4401 | Kredensial tidak valid / device di-revoke | **Berhenti**, hapus kredensial, tampilkan layar "Tidak terdaftar" |
| 4409 | Digantikan koneksi lain milik device yang sama | Tunggu 30 dtk lalu reconnect (hindari flapping) |
| 4426 | Versi protokol tidak didukung | Berhenti reconnect, minta update app |
| 4429 | Terlalu sering reconnect | Backoff penuh (max) |

Reconnect: eksponensial 1 dtk → 60 dtk dengan full jitter; reset ke 1 dtk setelah koneksi stabil ≥ 60 dtk. Trigger tambahan: `ConnectivityManager.NetworkCallback` (jaringan/VPN kembali), start service, boot.

## 5. Jaminan pengiriman dan dedup

- **Server→device: at-least-once.** `sms.send` dikirim ulang bila belum di-`command.ack` sebelum `ack_timeout_ms` atau setelah reconnect.
- **Device: effectively-once per `(message_id)`.** Sebelum memanggil `SmsManager`, device melakukan `INSERT` ke tabel Room `sms_job(message_id PRIMARY KEY, attempt, state, ...)`. Jika baris sudah ada dengan `attempt >= incoming.attempt` → balas `command.ack{accepted:false, reason:"duplicate"}` dan **jangan kirim ulang**. Attempt yang lebih besar hanya datang dari retry keputusan server setelah attempt sebelumnya terminal-gagal.
- **Device→server: at-least-once dengan outbox.** Setiap `sms.event` ditulis ke tabel `outbox` sebelum dikirim, dihapus saat `event.ack`. Server dedup oleh `event_id`.
- **Kasus tak pasti:** device ter-ack `sending` lalu hilang (crash/offline) lebih lama dari `sent_timeout_ms` tanpa event lanjutan → server menandai `unknown` dan **tidak** otomatis mengirim ulang (menghindari SMS ganda) kecuali `retry_on_unknown=true` atau operator memilih retry manual. Jika device kembali dan outbox-nya berisi `sent`/`delivered`, status dikoreksi otomatis dari `unknown`.

## 6. Batas dan keamanan

- Pesan > 64 KiB atau > 50 pesan/dtk dari satu device → `error` fatal + close `4429`.
- Isi SMS (body) **tidak boleh** muncul di log aplikasi maupun log server pada level `info`.
- Device tidak menerima URL/perintah shell; satu-satunya aksi yang bisa dipicu server dibatasi enum `device.command.action`.
