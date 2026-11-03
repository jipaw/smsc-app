# Dashboard SMSC

React 18 + TypeScript + Vite, TanStack Query, React Router 7. CSS polos dengan token tema (terang/gelap otomatis), tanpa library UI.
Tipe API **dihasilkan dari `contracts/openapi.yaml`** (`npm run gen:api` → `src/api/schema.d.ts`), jadi perubahan kontrak yang
merusak UI langsung gagal di `tsc`. Spesifikasi: `docs/spec/08`.

```bash
npm ci
npm run build        # gen:api + tsc + vite build  (JS ±96 KB gzip)
npm run test         # vitest: kalkulator segmen (kasus sama dengan server)
npm run e2e          # Chromium + HP palsu + biner nyata (lihat server/Makefile `make e2e`)
npm run dev          # dev server; proxy /api & /ws ke 127.0.0.1:8080
```

Produksi: hasil `dist/` disalin ke `server/internal/webui/dist` dan di-embed ke biner (`cd server && make web build`).

## Halaman (fase 1–9)
Login · Overview (statistik, grafik 24 jam, kill switch, perlu perhatian) · Devices (QR enrollment + kode manual, deteksi otomatis HP masuk via SSE,
detail, perintah, revoke) · SIM · Kirim SMS (penghitung segmen, template, rute SIM, jadwal, policy) · Transaksi (filter, live via SSE,
timeline, retry/batal massal, ekspor CSV) · Template · **Campaigns** (daftar dengan progres, wizard 5 langkah dengan draft tersimpan otomatis, impor/unggah CSV + laporan baris bermasalah, pemeriksaan variabel, preview + estimasi, jadwal; detail dengan jeda/lanjut/batal, banner penyebab jeda, pesan campaign, ubah policy/SIM saat berjalan) · **Undangan** (publik `/invite/<token>`) · **Akun** (ganti password, 2FA TOTP: QR + kode pemulihan, **kunci keamanan / passkey**; login dua langkah dengan pilihan kunci) · Pengaturan (policy, daftar blokir, API key, **Webhook** — buat/uji/riwayat/kirim ulang, **Keamanan** — kewajiban 2FA, **Email** — status SMTP + email uji, **Retensi** — masa simpan + ringkasan pembersihan terakhir, pengguna, audit).

## Catatan
- Realtime: satu `EventSource`; perubahan di-coalesce 400 ms lalu meng-invalidate query terkait; `resync` → refetch semua.
- Peran: tombol admin disembunyikan untuk non-admin, tetapi **server tetap otoritatif** (403).
- `npm audit --omit=dev` = 0 kerentanan. Temuan `npm audit` yang tersisa hanya di tooling dev (`vite`/`esbuild` dev server) dan tidak ikut ter-bundle;
  jangan menjalankan `npm run dev` di jaringan yang tidak tepercaya.
- Kontras warna diverifikasi dengan axe-core di E2E (terang dan gelap); asersi visual tambahan memeriksa grafik dan hitung mundur QR
  (dua bug nyata yang lolos dari asersi teks, tertangkap lewat screenshot).
