# Hasil PoC — salin ke poc/hasil/ lalu isi

Perangkat: ______  Android: __ (API __)  SIM1: ______  SIM2: ______  Tanggal: ______  Pengisi: ______
Varian APK: standar / tailnet ter-embed   Commit: ______   Server: smscd (`doctor` 0 gagal? __)   Panduan: `poc/WINBUILDER.md`

## F Uji fungsional (`poc/WINBUILDER.md` §5)
| # | Uji | Lulus? | Waktu / catatan |
|---|---|---|---|
| F1 | Izin & wizard | | |
| F2 | Pindai QR (kondisi normal) | | detik sampai terbaca: |
| F3 | QR sulit: mode gelap / redup / miring / kecil | | yang gagal: |
| F4 | Deep link & kode manual; QR non-SMSC ditolak | | |
| F5 | Kirim dari dashboard; pesan > 160 karakter = 2 bagian | | |
| F6 | Rotasi kredensial, tetap online | | |
| F7 | Reboot → online otomatis | | menit sampai online: |
| F8 | Tailnet ter-embed tanpa aplikasi Tailscale; *tailnet ✓*; key dicabut | | |
| F9 | `enforce`: HP sah jalan, node tanpa tag ditolak | | |

## R2 Izin sideload
| Pertanyaan | Jawaban |
|---|---|
| Dialog izin SMS muncul normal? | |
| Perlu "Izinkan setelan terbatas"? Langkah persisnya | |
| Beda instal via adb vs berkas APK? | |

## R6 Multi SIM
| Uji | Hasil |
|---|---|
| subId/slot terbaca benar | |
| Kirim dari slot 0 / slot 1 tiba dari nomor benar | |
| subId berubah saat cabut-pasang / tukar slot / eSIM | |
| Slot kosong → `sim_unavailable` tanpa crash | |

## R1 Batas OS
| Skenario | Terkirim sebelum macet | Dialog OS? (teks) | Jumlah/30 mnt terukur | Catatan |
|---|---|---|---|---|
| A tanpa guard 100 @0ms | | | | |
| B 100 @3–8s | | | | |
| C guard 25/30 mnt | | | | |
| D peran SMS default | | | | |
| E ubah settings global | | | | |
Nilai `sms_outgoing_check_max_count` / `_interval_ms`: ______ / ______

## R5 Delivery report
| Operator | sent | delivered | % | median sent→delivered | `DELIVERY_TANPA_PDU`? |
|---|---|---|---|---|---|

## R3 Standby (per perangkat/merek)
| Uji | Durasi | % online | Putus (jumlah) | Penyebab utama | Pulih (median) | Pengaturan vendor yang diubah |
|---|---|---|---|---|---|---|
| Idle layar mati, pengecualian baterai ON, mengisi | | | | | | |
| Idle, pengecualian OFF | | | | | | |
| Idle di baterai | | | | | | |
| Doze paksa | | | | | | |
| + wake lock | | | | | | |
| Reboot | | | | | | |
| Tailscale off/on | | | | | | |
| Wi-Fi ↔ seluler | | | | | | |
| Force-stop | | | | | | |

Doze paksa terukur (`doze.py`, tempel `doze_summary.txt`):
```
```

## R3/R4 Aplikasi Tailscale vs tailnet ter-embed (HP & kondisi sama)
| Mode | Durasi idle | % online (ledger) | Putus | Pulih median | Baterai %/jam | Catatan |
|---|---|---|---|---|---|---|
| Aplikasi Tailscale + Always-on VPN | | | | | | |
| Tailnet ter-embed (tsnet) | | | | | | |

## R4 Tailscale
| Uji | Hasil |
|---|---|
| VPN dimatikan 2 menit → online lagi ≤ 15 dtk | |
| Node HP ber-tag (tanpa kedaluwarsa key) | |
| Capability "Tailscale aktif" di dashboard akurat | |

## Kesimpulan & keputusan
- Default `os_sms_guard`: ______
- Peran SMS default diperlukan? ______
- Pilihan FLAG PendingIntent (mutable/immutable) terbukti: ______
- Perlu watchdog/alarm cadangan? ______
- Tailscale layak, atau pertimbangkan Profil B? ______
- Tailnet ter-embed menggantikan aplikasi Tailscale (baterai/ketahanan setara atau lebih baik)? ______
- `TAILNET_DEVICE_POLICY=enforce` siap dipakai produksi? ______
- Pemindai QR cukup andal, atau perlu pemindai sistem (Google Code Scanner)? ______
- Perubahan pada wizard / panduan OEM: ______
