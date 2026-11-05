# SMSC — SMS Gateway Android + Tailscale

Paket desain (analisis, stack, kontrak, flow), server + dashboard (fase 1–50) yang teruji, dan skeleton PoC Android. Aplikasi Android produksi belum ada (menunggu hasil PoC).

> **Menguji PoC di HP?** Mulai dari [`poc/WINBUILDER.md`](poc/WINBUILDER.md). Versi halaman web dengan daftar centang dan tombol salin ada di [halaman panduan uji PoC](https://claude.ai/artifact/QCAQ5jUt1j1KLH6LbWx2Vk). Halaman itu privat: minta pemiliknya membagikannya bila tidak bisa dibuka; isinya sama dengan berkas Markdown.

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

**Paket desain (dasar keputusan)**

| Path | Isi |
|---|---|
| [`docs/01-analisis-dan-stack.md`](docs/01-analisis-dan-stack.md) | Kebutuhan, fakta platform Android, transport & Tailscale (termasuk profil tanpa Tailscale), stack |
| [`docs/02-arsitektur-dan-flow.md`](docs/02-arsitektur-dan-flow.md) | Arsitektur, flow enrollment/koneksi/kirim/campaign/dispatcher/recovery, dashboard |
| [`docs/03-model-data-dan-state.md`](docs/03-model-data-dan-state.md) | ERD, state machine, resolusi policy, kode error |
| [`docs/04-konektivitas-keamanan-deployment.md`](docs/04-konektivitas-keamanan-deployment.md) | ACL Tailscale, autentikasi, deployment, panduan HP, Profil B (WSS publik) |
| [`docs/05-roadmap-risiko-outline.md`](docs/05-roadmap-risiko-outline.md) | Risiko + PoC, roadmap, pertanyaan terbuka |

**Dokumentasi penuh (`docs/spec/`)**

| # | Dokumen |
|---|---|
| 01 | [Product Requirements](docs/spec/01-product-requirements.md) — FR/NFR dengan kriteria penerimaan |
| 02 | [Arsitektur Sistem + ADR](docs/spec/02-arsitektur-sistem.md) |
| 03 | [Desain Aplikasi Android](docs/spec/03-desain-aplikasi-android.md) |
| 04 | [Desain Backend](docs/spec/04-desain-backend.md) — dispatcher, WS hub, reaper |
| 05 | [Spesifikasi API](docs/spec/05-spesifikasi-api.md) · [daftar endpoint](docs/spec/generated/api-endpoints.md) |
| 06 | [Protokol Device (normatif)](docs/spec/06-spesifikasi-protokol-device.md) |
| 07 | [Model Data & Migrasi](docs/spec/07-model-data-dan-migrasi.md) |
| 08 | [Spesifikasi Dashboard](docs/spec/08-spesifikasi-dashboard.md) |
| 09 | [Keamanan](docs/spec/09-keamanan.md) |
| 10 | [Panduan Operasional](docs/spec/10-panduan-operasional.md) |
| 11 | [Panduan Pengguna](docs/spec/11-panduan-pengguna.md) |
| 12 | [Rencana Uji](docs/spec/12-rencana-uji.md) |

**Kontrak (`contracts/`)** — sumber kebenaran yang dapat diverifikasi mesin

| Path | Isi |
|---|---|
| [`openapi.yaml`](contracts/openapi.yaml) | REST, OpenAPI 3.1 (124 operasi) + [`scim.md`](contracts/scim.md) (profil SCIM 2.0) |
| [`ws-schema.json`](contracts/ws-schema.json) + [`test-vectors/`](contracts/test-vectors/) | Protokol WebSocket: JSON Schema, 17 contoh valid, 15 invalid |
| [`device-ws-protocol.md`](contracts/device-ws-protocol.md) | Penjelasan protokol WebSocket |
| [`schema.sql`](contracts/schema.sql) | DDL PostgreSQL 16 (diuji: dimuat bersih, constraint diverifikasi) |
| [`tools/verify.sh`](contracts/tools/verify.sh) | Verifikasi kontrak (OpenAPI, vector, tabel endpoint) |

**Server & dashboard (`server/`, `web/`)** — implementasi fase 1–13 (campaign, webhook, retensi, metrik, rotasi kredensial, 2FA TOTP + WebAuthn + login passkey + SSO OIDC + SCIM (User + Group) + kewajiban 2FA, manajemen pengguna, undangan + email SMTP, kebijakan password), teruji (lihat [`docs/STATUS.md`](docs/STATUS.md))

| Path | Isi |
|---|---|
| [`server/`](server/README.md) | `smscd` (Go + PostgreSQL): REST, WS hub, dispatcher/reaper, SSE; tes integrasi dengan simulator HP + `contract_check` |
| [`web/`](web/README.md) | Dashboard React/TS; tipe dari OpenAPI; E2E Chromium (axe, mobile, RBAC) |
| [`ops/prometheus/alerts.yml`](ops/prometheus/alerts.yml) | 16 aturan alert Prometheus |
| [`server/tests/fake_device.py`](server/tests/fake_device.py) | HP palsu untuk demo: `--server … --token <kode QR>` |

**PoC Android (`android/`, `poc/`)** — skeleton untuk membuktikan risiko R1/R2/R3/R5/R6 di HP nyata

| Path | Isi |
|---|---|
| [`android/`](android/README.md) | `:core` (logika, enrollment, decoder QR; 43 tes), `:typecheck` (type-check terhadap API Android asli + tes manifest), `:agent` (agen perangkat JVM), `tsnetbridge/` (Tailscale ter-embed), `:app` (APK uji; butuh Android SDK) |
| [`poc/WINBUILDER.md`](poc/WINBUILDER.md) | **Panduan uji di HP lewat winbuilder** (server smscd, build APK, F1–F9, R1–R6) · [versi halaman web](https://claude.ai/artifact/QCAQ5jUt1j1KLH6LbWx2Vk) |
| [`poc/README.md`](poc/README.md) | Runbook eksperimen per risiko + [lembar hasil](poc/RESULTS_TEMPLATE.md) |
| [`poc/server/`](poc/server/poc_server.py) | Server PoC satu-device (+ `selftest.py` memvalidasi skema dua arah) |
| [`poc/tools/`](poc/tools/collect.py) | `collect.py` (bukti dari HP via adb), `doze.py` (Doze paksa terukur), `analyze.py` (ringkas `ledger.csv`/`events_*.csv`) |

Urutan baca: `docs/01` → `docs/02` → `docs/spec/01` → sisanya sesuai peran (Android: spec 03/06; backend: 04/05/07; frontend: 08; ops: 10/09; pengguna: 11). Pertanyaan terbuka ada di `docs/05` §3.
