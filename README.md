# SMSC — SMS Gateway Android + Tailscale

Paket desain (analisis, stack, kontrak, flow) sebagai dasar dokumentasi penuh dan implementasi. Belum ada kode aplikasi.

## Ringkasan keputusan

| Area | Keputusan |
|---|---|
| Client | Android native (Kotlin, Compose), `minSdk 29`, foreground service `specialUse`, distribusi sideload/MDM |
| Konektivitas | Tailscale; server hanya di tailnet via Tailscale Serve (HTTPS); **WebSocket dibuka oleh HP** ke server |
| Setup | Enrollment QR sekali pakai (15 menit) → ≤ 4 langkah di HP |
| Backend | Go + PostgreSQL (antrean `SKIP LOCKED`, tanpa Redis), SSE ke dashboard |
| Dashboard | React + TypeScript (di-embed ke biner server) |
| Multi SIM | Lane pengiriman per SIM, identitas `(device, slot_index)` |
| Manajemen pengiriman | Policy berlapis global → device → SIM → campaign: delay acak `[min,max]`, burst, rate limit, jendela waktu, retry, TTL, auto-pause |
| Kontrak | OpenAPI 3.1 (REST) + protokol WebSocket v1 |

## Isi repo

| Path | Isi |
|---|---|
| [`docs/01-analisis-dan-stack.md`](docs/01-analisis-dan-stack.md) | Kebutuhan, fakta platform Android, keputusan transport & Tailscale, rekomendasi stack, alternatif |
| [`docs/02-arsitektur-dan-flow.md`](docs/02-arsitektur-dan-flow.md) | Arsitektur, flow enrollment/koneksi/kirim/campaign/dispatcher/recovery, struktur dashboard |
| [`docs/03-model-data-dan-state.md`](docs/03-model-data-dan-state.md) | ERD, tabel, state machine pesan, resolusi policy, kode error, retensi |
| [`docs/04-konektivitas-keamanan-deployment.md`](docs/04-konektivitas-keamanan-deployment.md) | ACL Tailscale, autentikasi, ancaman, deployment, panduan setup HP, observability |
| [`docs/05-roadmap-risiko-outline.md`](docs/05-roadmap-risiko-outline.md) | Risiko + PoC, roadmap, pertanyaan terbuka, outline dokumentasi penuh |
| [`contracts/openapi.yaml`](contracts/openapi.yaml) | Kontrak REST (tervalidasi) |
| [`contracts/device-ws-protocol.md`](contracts/device-ws-protocol.md) | Kontrak WebSocket device↔server |

Mulai baca dari dok. 01, lalu dok. 02. Pertanyaan terbuka yang perlu dijawab ada di dok. 05 §3.
