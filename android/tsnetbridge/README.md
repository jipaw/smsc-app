# tsnetbridge — Tailscale ter-embed di aplikasi Android

Node Tailscale **userspace** (`tailscale.com/tsnet`) di dalam proses aplikasi. Aplikasi tidak butuh aplikasi Tailscale
resmi, izin VPN, atau slot VPN Android. Hanya lalu lintas aplikasi SMSC yang lewat tailnet.

```
OkHttp (GatewayClient) --SOCKS5 127.0.0.1 + kredensial--> tsnetbridge (node "smsc-<model>") ==WireGuard/DERP==> server tailnet
```

## API (Go → Java lewat `gomobile bind`)

| Go | Java/Kotlin |
|---|---|
| `NewBridge(stateDir, hostname)` | `tsnetbridge.Tsnetbridge.newBridge(...)` |
| `SetControlServer(url)` | `bridge.setControlServer(...)`: kosong = Tailscale; isi untuk Headscale/uji |
| `SetLogger(Logger)` | `bridge.setLogger { line -> ... }` |
| `Start(authKey, timeoutMs) (*Proxy, error)` | `bridge.start(key, 90_000L)`: blocking sampai tersambung; authKey hanya untuk login pertama |
| `Status() string` | JSON: `running`, `state`, `dns_name`, `tailnet_ips`, `online` |
| `Close()` | `bridge.close()`: kunci node tetap tersimpan di `stateDir` |

`Proxy`: `host` (127.0.0.1), `port`, `username` (`tsnet`), `password` (acak per proses, tidak pernah di-log).
Aplikasi lain di HP yang menemukan port ini tetap ditolak tanpa password.

## Membangun AAR (winbuilder)

Prasyarat: Go 1.24+, Android SDK + NDK, `ANDROID_HOME` dan `ANDROID_NDK_HOME` terisi.

```powershell
.\android\scripts\build-apk.ps1 -Tailnet -Install     # AAR -> app\libs\tsnetbridge.aar -> APK dengan tailnet ter-embed
```

Linux/macOS: `sh android/tsnetbridge/build-aar.sh`. Tanpa AAR, `:app` dibangun dengan varian `src/notsnet` (opsi tailnet nonaktif di UI).

## Di aplikasi

Langkah di bagian **4. Koneksi**:

1. Centang **Tailnet ter-embed**.
2. Isi auth key (`tskey-auth-…`, ber-tag `tag:smsc-device`, sekali pakai). Hostname dan control server opsional.
3. URL server diisi nama MagicDNS, mis. `wss://smsc.<tailnet>.ts.net/ws/device`.
4. Tekan Start service.

Auth key dihapus dari penyimpanan setelah node tersambung. Kunci node disimpan di `noBackupFilesDir/tailnet`, tidak ikut cadangan cloud.

## Pengujian (tanpa HP, tanpa internet)

- `go test ./...` memakai server kontrol uji Tailscale + DERP lokal, jadi tailnet-nya sungguhan dan in-process. Yang diuji:
  - HP menjangkau server lewat nama MagicDNS melalui proxy;
  - password salah ditolak;
  - backend melihat IP tailnet;
  - password proxy tidak masuk log;
  - restart dari state tersimpan memberi IP yang sama.
- `make tailnet` (di `server/`): lab penuh dengan **agen JVM `--proxy` → bridge → node "smsc" (setara `tailscale serve`) → `smscd` nyata**, memakai `PUBLIC_BASE_URL=http://smsc` sehingga server hanya terjangkau lewat tailnet. Yang diuji:
  - enroll gagal tanpa proxy;
  - enroll lewat QR berhasil;
  - WebSocket lewat tailnet;
  - SMS delivered;
  - node HP mati lalu nyala lagi dengan state sama, kemudian online kembali.
- `:typecheck` mengompilasi `app/src/tsnet` terhadap API Java asli hasil `gobind` (`typecheck/src/gobind`, buat ulang dengan `gen-java.sh`), dan varian `src/notsnet` dicek kesamaan API-nya.

## Privasi log

Seperti `tailscaled`, node tsnet secara default mengirim log diagnostik ke Tailscale. Untuk mematikannya, set env proses `TS_NO_LOGS_NO_SUPPORT=true`. Di aplikasi Android, panggil `Os.setenv("TS_NO_LOGS_NO_SUPPORT", "true", true)` sebelum `start` (belum dipasang di PoC).

## Belum terbukti (butuh HP nyata)

- SOCKS5 + autentikasi di OkHttp Android (libcore). Di JVM sudah teruji, di Android perlu dicek.
- Konsumsi baterai WireGuard userspace, perilaku Doze, perpindahan Wi-Fi ↔ seluler, dan ukuran APK per ABI.
