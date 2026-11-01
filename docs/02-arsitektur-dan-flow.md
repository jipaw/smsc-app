# 02 — Arsitektur dan Flow

## 1. Gambaran arsitektur

```mermaid
flowchart LR
  subgraph Tailnet["Tailnet (WireGuard)"]
    direction LR
    subgraph Server["Server gateway (tag:smsc-server)"]
      TS["Tailscale Serve<br/>HTTPS :443"]
      API["API + WS Hub<br/>(Go)"]
      DSP["Dispatcher<br/>lane per SIM"]
      MAT["Campaign materializer"]
      PG[("PostgreSQL")]
      TS --> API
      API <--> PG
      DSP <--> PG
      MAT <--> PG
      DSP <--> API
    end
    subgraph Phone["HP Android (tag:smsc-device)"]
      TSA["App Tailscale"]
      APP["App SMSC Agent<br/>Foreground Service"]
      SMS["SmsManager<br/>SIM 1 / SIM 2"]
      TSA --- APP
      APP --> SMS
    end
    ADMIN["Browser admin<br/>(group:smsc-admins)"]
  end
  EXT["Sistem eksternal<br/>(API key, di tailnet)"]
  APP == "WSS (dibuka device)" ==> TS
  ADMIN -- "HTTPS + SSE" --> TS
  EXT -- "REST" --> TS
  SMS --> CARRIER["Operator seluler"]
```

### 1.1 Komponen server

| Komponen | Tanggung jawab |
|---|---|
| **API** | REST (`openapi.yaml`), autentikasi sesi/API key, RBAC, validasi, idempotensi. |
| **WS Hub** | Menyimpan koneksi device aktif, auth, heartbeat, rekonsiliasi `hello`, ack/event, kirim perintah. Satu koneksi per device. |
| **Dispatcher** | Penjadwal inti. Satu "lane" logis per SIM: memutuskan kapan dan pesan apa berikutnya dikirim, menegakkan delay/rate-limit/jendela waktu, membuat lease. |
| **Campaign materializer** | Mengubah penerima campaign menjadi baris `messages` (batch 1.000) secara asinkron; mengawasi auto-pause. |
| **Reaper** | Memeriksa lease kedaluwarsa (`ack_timeout`), pesan `sending` tanpa kabar (`sent_timeout` → `unknown`), pesan `expired`, device tanpa heartbeat → `offline`. |
| **Event bus (in-process)** | Menyebarkan perubahan ke SSE dashboard dan pemicu webhook (fase 2). |
| **PostgreSQL** | Sumber kebenaran; antrean = tabel `messages` dengan status. |

### 1.2 Komponen aplikasi Android

```mermaid
flowchart TB
  UI["UI (Compose)<br/>Onboarding, Status, Log"] --> VM["ViewModel"]
  VM --> REPO["Repository"]
  subgraph FGS["SmsGatewayService (foreground, specialUse)"]
    CM["ConnectionManager<br/>OkHttp WS + backoff"]
    CP["CommandProcessor<br/>validasi, dedup, guardrail"]
    SS["SmsSender<br/>SmsManager per subId"]
    SR["SmsStatusReceiver<br/>sent / delivered"]
    SIM["SimManager<br/>listener subscription"]
    HB["HealthReporter<br/>heartbeat"]
    OB["OutboxPublisher"]
  end
  CM --> CP --> SS --> SR --> OB --> CM
  SIM --> CM
  HB --> CM
  CP <--> ROOM[("Room<br/>sms_job, outbox")]
  OB <--> ROOM
  BOOT["BootReceiver"] --> FGS
  WD["WatchdogWorker<br/>(WorkManager 15 mnt)"] --> FGS
  NET["NetworkCallback<br/>(VPN/jaringan kembali)"] --> CM
```

Aturan penting di aplikasi:

- **Persist-before-act:** `sms_job` disimpan ke Room sebelum `SmsManager` dipanggil; `outbox` disimpan sebelum event dikirim.
- `SmsStatusReceiver` terdaftar di manifest (bukan dinamis) supaya hasil tetap diterima walau proses sempat dimatikan.
- Wake lock parsial hanya selama pengiriman aktif; koneksi idle mengandalkan foreground service + pengecualian baterai.

## 2. Flow 1 — Setup device (target: ≤ 4 langkah di HP)

Langkah manusia: **(1)** login Tailscale di HP · **(2)** pasang APK · **(3)** satu layar checklist izin · **(4)** scan QR dari dashboard.

```mermaid
sequenceDiagram
  autonumber
  actor Admin
  participant D as Dashboard
  participant S as Server
  actor Op as Operator di HP
  participant A as App Android
  Admin->>D: Devices > Tambah device
  D->>S: POST /device-enrollments
  S-->>D: token (15 mnt, sekali pakai), enroll_url
  D-->>Admin: Tampilkan QR + kode 8 karakter
  Op->>A: Buka app, ikuti wizard
  A->>A: Cek Tailscale aktif dan server terjangkau (opsional dari QR)
  A->>Op: Checklist izin SMS, Phone, Notifikasi, Baterai
  Op->>A: Scan QR
  A->>S: POST /device/enroll (token, install_id, device_info, sims)
  S->>S: Validasi dan konsumsi token atomik, buat device + SIM
  S-->>A: device_id, device_secret, ws_url, config
  A->>A: Simpan secret (Keystore), start foreground service
  A->>S: WSS connect (Bearer device_secret) + hello
  S-->>A: hello.ack
  S-->>D: SSE device.updated (online)
  D-->>Admin: Device muncul Online, SIM terdeteksi
```

Detail wizard di app (satu layar, berurutan, status hijau/merah masing-masing):

| Item | Cara cek | Bila gagal |
|---|---|---|
| Tailscale aktif | `TRANSPORT_VPN` + reachability server | Tombol buka app Tailscale |
| Izin SMS | `SEND_SMS` | Jika terblokir "restricted": arahkan ke App info → Allow restricted settings |
| Izin telepon | `READ_PHONE_STATE`, `READ_PHONE_NUMBERS` | Minta izin |
| Notifikasi (API 33+) | `POST_NOTIFICATIONS` | Minta izin |
| Abaikan optimasi baterai | `PowerManager.isIgnoringBatteryOptimizations` | Intent sistem + panduan per merek |
| (Opsional) Default SMS app | `RoleManager` | Dijelaskan manfaat/risikonya |

Alternatif tanpa QR: dashboard menampilkan tautan `https://smsc.<tailnet>.ts.net/enroll/<token>`; dibuka di browser HP, halaman itu memicu app lewat App Link atau menampilkan kode manual.

Kegagalan: token kedaluwarsa/terpakai → 401, app menampilkan "Minta QR baru"; `install_id` sudah terdaftar dengan token baru → server mengganti kredensial device lama (re-enroll yang sah) dan mencatat audit.

## 3. Flow 2 — Siklus koneksi (standby)

```mermaid
stateDiagram-v2
  [*] --> Disconnected
  Disconnected --> Connecting: service start / jaringan kembali / timer backoff
  Connecting --> Handshaking: 101 Upgrade
  Connecting --> Disconnected: gagal (backoff + jitter)
  Handshaking --> Online: hello.ack, outbox terkirim
  Online --> Disconnected: ping timeout / close
  Online --> Stopped: close 4401 (revoked)
  Stopped --> [*]
```

- Heartbeat 30 dtk + ping WS 20 dtk. Server menandai `offline` setelah 90 dtk senyap.
- Perubahan jaringan/VPN (`NetworkCallback`) memicu reconnect langsung tanpa menunggu backoff.
- Watchdog WorkManager memastikan service hidup; jika mati, memulai ulang dan mencatat insiden (dikirim sebagai atribut di `hello`).
- Selama offline, **tidak ada pesan baru yang di-lease ke device tersebut**; pesan `queued` tetap menunggu atau diambil SIM lain bila rute mengizinkan.

## 4. Flow 3 — Kirim SMS non-campaign (dan inti pengiriman semua pesan)

```mermaid
sequenceDiagram
  autonumber
  participant C as Dashboard atau API klien
  participant S as Server API
  participant DB as PostgreSQL
  participant DSP as Dispatcher
  participant A as App Android
  participant OS as SmsManager / Operator
  C->>S: POST /messages (Idempotency-Key)
  S->>DB: Validasi, normalisasi E.164, cek suppression, insert status=queued
  S-->>C: 202 daftar pesan
  DSP->>DB: Lane SIM X eligible (delay lewat, rate ok, window ok)
  DSP->>DB: Ambil 1 pesan queued (SKIP LOCKED), status=assigned, attempt, lease
  DSP->>A: WS sms.send
  A->>A: INSERT sms_job (dedup), cek guardrail
  A-->>S: command.ack accepted
  S->>DB: status=sending
  A->>OS: sendMultipartTextMessage (sub id SIM X)
  OS-->>A: sentIntent per part
  A->>S: sms.event sent
  S->>DB: status=sent, sent_at, hitung next_eligible_at = sekarang + acak(min,max)
  S-->>C: SSE message.updated
  OS-->>A: deliveryIntent (jika operator mengirim)
  A->>S: sms.event delivered
  S->>DB: status=delivered
  S-->>A: event.ack (outbox dibersihkan)
```

Cabang kegagalan:

| Titik | Kejadian | Hasil |
|---|---|---|
| `assigned` | Tidak ada `command.ack` dalam `ack_timeout` | Lease dicabut → `queued` lagi (tanpa menambah attempt kirim karena belum terkirim), SIM bisa ditandai bermasalah bila berulang |
| `sending` | `sms.event failed` retryable (`no_service`, `radio_off`, `generic_failure`, `os_rate_limited`) dan `attempt < max_attempts` | `queued` dengan `not_before = now + backoff`, attempt+1 |
| `sending` | `failed` non-retryable (`invalid_destination`, `short_code_blocked`, `permission_denied`) | `failed` final |
| `sending` | Device hilang > `sent_timeout` tanpa event | `unknown` (tidak otomatis dikirim ulang) |
| `sent` | Tidak ada laporan delivery sampai `delivery_timeout` (24 jam) | Tetap `sent` (final), ditandai "delivery tidak terkonfirmasi" |
| semua non-terminal | Melewati `expires_at` | `expired` |

Pembatalan: `POST /messages/{id}/cancel` valid saat `queued`/`assigned`. Untuk `assigned`, server mengirim `sms.cancel`; jika device menjawab sudah `sending`, pembatalan ditolak (409).

## 5. Flow 4 — Campaign

```mermaid
stateDiagram-v2
  [*] --> draft
  draft --> scheduled: start (start_at di masa depan)
  draft --> running: start
  scheduled --> running: waktu tiba
  running --> paused: pause / auto-pause / tidak ada kapasitas
  paused --> running: resume
  running --> completed: semua pesan terminal
  running --> cancelled: cancel
  paused --> cancelled: cancel
  scheduled --> cancelled: cancel
  completed --> [*]
  cancelled --> [*]
```

```mermaid
sequenceDiagram
  autonumber
  actor Admin
  participant D as Dashboard
  participant S as Server
  participant M as Materializer
  participant DSP as Dispatcher
  Admin->>D: Wizard: Detail > Penerima (CSV) > Pesan/template > SIM dan policy > Review
  D->>S: POST /campaigns (draft)
  D->>S: POST /campaigns/{id}/recipients (CSV)
  S-->>D: accepted, duplicates, suppressed, invalid
  D->>S: POST /campaigns/{id}/preview
  S-->>D: contoh render, jumlah segmen, estimasi durasi
  Admin->>D: Start
  D->>S: POST /campaigns/{id}/start
  S->>S: Validasi pra-start
  S-->>D: running atau scheduled
  loop batch 1.000
    M->>S: insert messages (queued, source=campaign, priority campaign)
  end
  DSP->>DSP: Lane SIM mengambil pesan sesuai urutan prioritas
  Note over DSP: Evaluasi auto-pause: jika rasio gagal pada N pesan terakhir lebih dari ambang, campaign paused (auto_failure_ratio)
  S-->>D: SSE campaign.updated (progres live)
```

Aturan:

- **Prioritas:** non-campaign default 5, campaign default 3 → kirim cepat tidak tertahan oleh campaign besar. Urutan antrean: `priority DESC, not_before ASC, id ASC`.
- **Pause** menahan pesan `queued` (dispatcher mengabaikan pesan campaign yang `paused`); pesan yang sudah `assigned/sending` diselesaikan.
- **Auto-pause** (default aktif): jika dalam `window` pesan terakhir rasio `failed` > `max_failure_ratio`, campaign di-pause dengan `pause_reason=auto_failure_ratio` agar tidak membakar seluruh daftar penerima saat SIM bermasalah. `no_capacity` bila tidak ada SIM yang memenuhi `sim_ids` online selama 10 menit.
- Variabel template yang hilang pada suatu penerima → penerima itu ditolak saat impor/pra-start, bukan dikirim dengan placeholder kosong.
- Penerima yang masuk daftar suppression dilewati dan dihitung di ringkasan impor.

## 6. Flow 5 — Algoritma Dispatcher (manajemen pengiriman)

Dispatcher bersifat *pull per lane*: setiap SIM aktif+online adalah lane yang menarik pesan berikutnya saat siap. Ini otomatis memberi *load balancing* (SIM yang lebih cepat mengambil lebih banyak) tanpa strategi routing terpisah. Dipicu oleh event (pesan masuk, ack, status, timer `next_eligible_at`), bukan polling buta.

```text
lane_ready(sim):
  sim.enabled AND sim.status == active AND device.status in (online, degraded-yang-mengizinkan)
  AND now >= sim.next_eligible_at
  AND inflight(sim) < policy.max_inflight_per_sim          # default 1
  AND dalam send_window (zona waktu policy)
  AND rate_limit(per_minute/hour/day) belum tercapai
  AND os_sms_guard belum tercapai

pick_message(sim):                                         # satu transaksi DB
  SELECT ... FROM messages
   WHERE status='queued' AND not_before <= now() AND expires_at > now()
     AND (campaign_id IS NULL OR campaign.status='running')
     AND route_allows(sim)                                 # sim_ids/device_ids allow-list pesan atau campaign
   ORDER BY priority DESC, not_before, id
   FOR UPDATE SKIP LOCKED LIMIT 1
  UPDATE ... SET status='assigned', sim_id, device_id, attempt=attempt+1 (jika attempt baru), lease_expires_at=now()+ack_timeout

on_terminal_for_inflight(sim, msg):                        # sent / failed / unknown
  burst_count += 1
  delay = uniform(policy.min_delay_ms, policy.max_delay_ms)
  if burst_size and burst_count % burst_size == 0: delay += policy.burst_pause_ms
  sim.next_eligible_at = max(sim.next_eligible_at, now() + delay)
```

Makna "delay antar transaksi": jeda dihitung **dari saat pesan sebelumnya pada SIM yang sama mencapai `sent`/terminal** sampai pesan berikutnya boleh di-lease. Jitter acak `[min,max]` mencegah pola pengiriman seragam.

Resolusi policy efektif (detail di dok. 03 §4): empat lapis; *guardrail* (global/device/SIM) bersifat membatasi — nilai paling konservatif menang — sedangkan lapis campaign/pesan adalah preferensi yang tidak boleh melonggarkan guardrail.

## 7. Flow 6 — Device hilang, reconnect, rekonsiliasi

```mermaid
sequenceDiagram
  autonumber
  participant DSP as Dispatcher/Reaper
  participant S as Server
  participant A as App Android
  Note over A: Koneksi putus saat pesan M berstatus sending
  S->>S: Tak ada heartbeat 90 dtk, device=offline, lane SIM berhenti
  A->>A: SmsManager tetap menyelesaikan kirim, event sent masuk outbox
  A->>S: Reconnect (backoff), hello (inflight, pending_events)
  S->>S: Bandingkan inflight dengan DB
  S-->>A: hello.ack (drop/keep) + kirim ulang sms.send untuk assigned
  A->>S: Flush outbox: sms.event sent/delivered
  S->>S: M: sending -> sent -> delivered (koreksi otomatis)
  Note over S: Jika device tak kembali sampai sent_timeout, M menjadi unknown (tanpa retry otomatis)
```

## 8. Flow 7 — Revoke / ganti device

1. Admin `DELETE /devices/{id}` → kredensial dihapus, WS di-close `4401`, audit tercatat.
2. App menerima `4401` → hapus secret dan data lokal sensitif, kembali ke layar onboarding.
3. Pesan `queued` tidak terpengaruh; `assigned` dikembalikan ke `queued`; `sending` → `unknown` (device-lost rule).

## 9. Dashboard — struktur halaman

| Halaman | Isi utama | Endpoint |
|---|---|---|
| **Overview** | Device online/degraded/offline, kedalaman antrean, kirim hari ini, success rate 24 jam, grafik per jam, peringatan (SIM no service, baterai rendah) | `/stats/overview`, `/stats/timeseries`, SSE |
| **Devices** | Daftar + status; **Tambah device** (QR); detail: info HP, baterai, jaringan, capability flags, SIM, policy, perintah (ping, refresh SIM, restart service, log), revoke | `/devices*`, `/device-enrollments` |
| **SIM** | Tabel lintas device: operator, slot, sinyal, enable/disable, label, policy, counter hari ini, `next_eligible_at` | `/sims*` |
| **Campaigns** | Daftar; wizard pembuatan 5 langkah; detail dengan progres live, pause/resume/cancel, tabel pesan terfilter | `/campaigns*`, `/messages?campaign_id=` |
| **Kirim SMS** | Tempel/ketik nomor (≤ 1.000), isi atau template, pilih SIM/device, jadwal, policy, tombol kirim | `POST /messages` |
| **Transaksi** | Tabel dengan filter (status, sumber, campaign, device, SIM, nomor, tanggal), pencarian, aksi massal retry/cancel, ekspor CSV; detail dengan timeline event | `/messages*` |
| **Template** | CRUD + deteksi variabel + hitung segmen | `/templates*` |
| **Pengaturan** | Dispatch policy (editor delay/rate/jendela/retry + simulasi efektif), policy default, daftar blokir, API key, pengguna & peran, webhook (fase 2), audit log | `/dispatch-policies*`, `/suppressions*`, `/api-keys*`, `/users`, `/audit-logs` |

Peran: **admin** (semua), **operator** (campaign, kirim, transaksi, lihat device/SIM; tidak boleh revoke/pengaturan/pengguna), **viewer** (baca saja, nomor telepon dapat dimasking).
