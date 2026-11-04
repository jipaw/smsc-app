// Package tsnetbridge menjalankan node Tailscale userspace (tsnet) di dalam proses aplikasi Android dan membuka proxy
// SOCKS5 loopback ber-kredensial untuk klien HTTP/WebSocket aplikasi (OkHttp). Tidak memakai VpnService, tidak butuh
// izin VPN, dan hanya lalu lintas aplikasi ini yang lewat tailnet.
//
// API sengaja memakai tipe sederhana agar dapat dibungkus `gomobile bind` (Java: tsnetbridge.Tsnetbridge / Bridge / Proxy).
package tsnetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"tailscale.com/ipn"
	"tailscale.com/ipn/store/mem"
	"tailscale.com/tsnet"
)

// Logger diimplementasikan sisi Kotlin (mis. diteruskan ke PocLog). Tidak pernah menerima auth key atau kredensial proxy.
// Dapat dipanggil bersamaan dari beberapa goroutine: implementasi harus thread-safe. Berlaku untuk Start berikutnya.
type Logger interface {
	Log(line string)
}

// Proxy: alamat dan kredensial proxy SOCKS5 di 127.0.0.1. Kredensial acak per proses; aplikasi lain di HP yang
// menemukan port ini tetap tidak dapat memakai tailnet tanpa password.
type Proxy struct {
	Host     string
	Port     int
	Username string
	Password string
}

// URL dalam bentuk socks5://user:pass@host:port (format yang dipahami ProxySpec di :core).
func (p *Proxy) URL() string {
	return "socks5://" + p.Username + ":" + p.Password + "@" + net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
}

// Bridge: satu node tailnet per aplikasi. Aman dipanggil dari beberapa thread.
type Bridge struct {
	mu         sync.Mutex
	stateDir   string
	hostname   string
	controlURL string
	ephemeral  bool
	memState   bool
	logger     Logger
	srv        *tsnet.Server
	proxy      *Proxy
}

// NewBridge membuat bridge. stateDir: folder privat aplikasi (kunci node disimpan di sana). hostname: nama node di tailnet.
func NewBridge(stateDir, hostname string) *Bridge {
	return &Bridge{stateDir: stateDir, hostname: hostname}
}

// SetControlServer: server koordinasi selain Tailscale (Headscale, atau server uji). Kosong = default Tailscale.
func (b *Bridge) SetControlServer(u string) { b.mu.Lock(); b.controlURL = u; b.mu.Unlock() }

// SetEphemeral: node dihapus dari tailnet saat lama tidak aktif (untuk uji; HP produksi sebaiknya tidak ephemeral).
func (b *Bridge) SetEphemeral(v bool) { b.mu.Lock(); b.ephemeral = v; b.mu.Unlock() }

// SetLogger: tujuan log tsnet (opsional; default dibuang).
func (b *Bridge) SetLogger(l Logger) { b.mu.Lock(); b.logger = l; b.mu.Unlock() }

// useMemoryState: state tidak ditulis ke disk (hanya untuk tes).
func (b *Bridge) useMemoryState() { b.mu.Lock(); b.memState = true; b.mu.Unlock() }

// Start menyalakan node dan menunggu sampai tersambung ke tailnet (maks timeoutMs), lalu mengembalikan proxy.
// authKey hanya diperlukan pada login pertama; setelah itu kunci node di stateDir dipakai (authKey boleh kosong).
// Memanggil Start lagi saat sudah berjalan mengembalikan proxy yang sama.
func (b *Bridge) Start(authKey string, timeoutMs int64) (*Proxy, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.proxy != nil {
		return b.proxy, nil
	}
	if b.hostname == "" {
		return nil, errors.New("hostname wajib diisi")
	}
	if !b.memState {
		if b.stateDir == "" {
			return nil, errors.New("stateDir wajib diisi")
		}
		if err := os.MkdirAll(b.stateDir, 0o700); err != nil {
			return nil, fmt.Errorf("stateDir: %w", err)
		}
	}
	lg := b.logger // ditangkap di bawah kunci: SetLogger sesudahnya tidak berlomba dengan goroutine tsnet
	logf := func(format string, args ...any) {
		if lg != nil {
			lg.Log(fmt.Sprintf(format, args...))
		}
	}
	srv := &tsnet.Server{
		Dir:        b.stateDir,
		Hostname:   b.hostname,
		AuthKey:    authKey,
		ControlURL: b.controlURL,
		Ephemeral:  b.ephemeral,
		Logf:       func(string, ...any) {}, // log backend sangat ramai; hanya UserLogf yang diteruskan
		UserLogf:   logf,
	}
	if b.memState {
		srv.Store = new(mem.Store)
	}
	if timeoutMs <= 0 {
		timeoutMs = 60_000
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	if _, err := srv.Up(ctx); err != nil {
		_ = srv.Close()
		return nil, fmt.Errorf("tailnet belum tersambung: %w", err)
	}
	addr, cred, _, err := srv.Loopback()
	if err != nil {
		_ = srv.Close()
		return nil, fmt.Errorf("proxy loopback: %w", err)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		_ = srv.Close()
		return nil, err
	}
	p, _ := strconv.Atoi(port)
	b.srv = srv
	b.proxy = &Proxy{Host: host, Port: p, Username: "tsnet", Password: cred}
	logf("tsnetbridge: tailnet tersambung sebagai %q, proxy SOCKS5 di %s", b.hostname, addr)
	return b.proxy, nil
}

// statusView: ringkasan untuk UI/diagnostik (tanpa rahasia).
type statusView struct {
	Running   bool     `json:"running"`
	State     string   `json:"state"`
	Hostname  string   `json:"hostname"`
	DNSName   string   `json:"dns_name,omitempty"`
	TailnetIP []string `json:"tailnet_ips,omitempty"`
	Online    bool     `json:"online"`
	Error     string   `json:"error,omitempty"`
}

// Status: JSON ringkas keadaan node (state backend, IP tailnet, nama MagicDNS).
func (b *Bridge) Status() string {
	b.mu.Lock()
	srv := b.srv
	b.mu.Unlock()
	v := statusView{Hostname: b.hostname, State: ipn.NoState.String()}
	if srv != nil {
		v.Running = true
		lc, err := srv.LocalClient()
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			st, err2 := lc.StatusWithoutPeers(ctx)
			cancel()
			err = err2
			if err == nil {
				v.State = st.BackendState
				if st.Self != nil {
					v.DNSName = st.Self.DNSName
					v.Online = st.Self.Online
				}
				for _, ip := range st.TailscaleIPs {
					v.TailnetIP = append(v.TailnetIP, ip.String())
				}
			}
		}
		if err != nil {
			v.Error = err.Error()
		}
	}
	out, _ := json.Marshal(v)
	return string(out)
}

// Close mematikan node (kunci node tetap tersimpan di stateDir untuk Start berikutnya).
func (b *Bridge) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.srv == nil {
		return nil
	}
	err := b.srv.Close()
	b.srv, b.proxy = nil, nil
	return err
}
