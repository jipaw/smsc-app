# Hasil PoC — salin ke poc/hasil/ lalu isi

Perangkat: ______  Android: __ (API __)  SIM1: ______  SIM2: ______  Tanggal: ______  Pengisi: ______

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

## Kesimpulan & keputusan
- Default `os_sms_guard`: ______
- Peran SMS default diperlukan? ______
- Pilihan FLAG PendingIntent (mutable/immutable) terbukti: ______
- Perlu watchdog/alarm cadangan? ______
- Tailscale layak, atau pertimbangkan Profil B? ______
- Perubahan pada wizard / panduan OEM: ______
