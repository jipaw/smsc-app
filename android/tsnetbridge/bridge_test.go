package tsnetbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/proxy"
	"tailscale.com/ipn/store/mem"
	"tailscale.com/net/netns"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
	"tailscale.com/tstest/integration"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"
)

const magicDomain = "tail-lab.ts.net"

// startLabControl: server koordinasi uji + DERP/STUN lokal — tailnet sungguhan tanpa internet.
func startLabControl(t testing.TB) string {
	t.Helper()
	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })
	derpMap := integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1")
	control := &testcontrol.Server{
		DERPMap:        derpMap,
		DNSConfig:      &tailcfg.DNSConfig{Proxied: true},
		MagicDNSDomain: magicDomain,
		Logf:           logger.Discard,
	}
	control.HTTPTestServer = httptest.NewUnstartedServer(control)
	control.HTTPTestServer.Start()
	t.Cleanup(control.HTTPTestServer.Close)
	return control.HTTPTestServer.URL
}

// startGatewayNode: node "server" yang meneruskan tailnet:80 ke backend (setara `tailscale serve` di depan smscd).
// X-Forwarded-For berisi IP tailnet klien, sama seperti tailscale serve.
func startGatewayNode(t testing.TB, controlURL, hostname, backend string) *tsnet.Server {
	t.Helper()
	s := &tsnet.Server{Dir: t.TempDir(), ControlURL: controlURL, Hostname: hostname, Store: new(mem.Store), Ephemeral: true, Logf: logger.Discard}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := s.Up(ctx); err != nil {
		t.Fatalf("node server: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	ln, err := s.Listen("tcp", ":80")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(backend)
	rp := httputil.NewSingleHostReverseProxy(u)
	go http.Serve(ln, rp)
	return s
}

func socksClient(p *Proxy, pass string) *http.Client {
	d, _ := proxy.SOCKS5("tcp", net.JoinHostPort(p.Host, fmt.Sprint(p.Port)), &proxy.Auth{User: p.Username, Password: pass}, proxy.Direct)
	return &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, n, a string) (net.Conn, error) {
			return d.(proxy.ContextDialer).DialContext(ctx, n, a)
		},
	}}
}

// HP (bridge) menjangkau server lewat nama MagicDNS melalui proxy SOCKS5; tanpa password yang benar proxy menolak;
// backend melihat IP tailnet HP; Status melaporkan node berjalan; state tersimpan dipakai ulang tanpa auth key.
func TestBridgeReachesServerOverTailnet(t *testing.T) {
	controlURL := startLabControl(t)
	var seen string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Forwarded-For")
		io.WriteString(w, "halo dari smscd")
	}))
	defer backend.Close()
	gw := startGatewayNode(t, controlURL, "smsc", backend.URL)
	gwIP, _ := gw.TailscaleIPs()

	dir := t.TempDir()
	b := NewBridge(dir, "hp-uji")
	b.SetControlServer(controlURL)
	var logMu sync.Mutex
	var logs []string
	b.SetLogger(logFunc(func(s string) { logMu.Lock(); logs = append(logs, s); logMu.Unlock() }))
	p, err := b.Start("", 60_000)
	if err != nil {
		t.Fatal(err)
	}
	if p.Host != "127.0.0.1" || p.Port == 0 || p.Username != "tsnet" || len(p.Password) < 32 || !strings.HasPrefix(p.URL(), "socks5://tsnet:") {
		t.Fatalf("proxy: %+v", p)
	}
	if p2, _ := b.Start("", 1000); p2 != p {
		t.Fatal("Start kedua harus mengembalikan proxy yang sama")
	}
	var st statusView
	if err := json.Unmarshal([]byte(b.Status()), &st); err != nil || !st.Running || st.State != "Running" || len(st.TailnetIP) == 0 {
		t.Fatalf("status: %+v %v", st, err)
	}
	// testcontrol memberi nama node pendek ("smsc"); control Tailscale sungguhan memberi FQDN smsc.<tailnet>.ts.net —
	// dnsMap tsnet memetakan FQDN dan nama pendek, jadi keduanya dapat dipakai sebagai PUBLIC_BASE_URL di produksi.
	host := "http://smsc/"
	var resp *http.Response
	deadline := time.Now().Add(20 * time.Second) // peer baru muncul di netmap beberapa saat setelah Up
	for {
		resp, err = socksClient(p, p.Password).Get(host)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		_, ipErr := socksClient(p, p.Password).Get("http://" + gwIP.String() + "/")
		t.Fatalf("lewat tailnet (MagicDNS): %v; lewat IP %s: %v", err, gwIP, ipErr)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "halo dari smscd" || !strings.Contains(seen, st.TailnetIP[0]) {
		t.Fatalf("respons=%q xff=%q ip=%v", body, seen, st.TailnetIP)
	}
	if _, err := socksClient(p, "salah").Get(host); err == nil {
		t.Fatal("proxy harus menolak password salah")
	}
	logMu.Lock()
	defer logMu.Unlock()
	for _, l := range logs {
		if strings.Contains(l, p.Password) {
			t.Fatal("password proxy tidak boleh muncul di log")
		}
	}
	firstIP := st.TailnetIP[0]
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.Status(), `"running":false`) {
		t.Fatalf("status setelah Close: %s", b.Status())
	}
	// restart dari state tersimpan (tanpa auth key): identitas node sama
	b2 := NewBridge(dir, "hp-uji")
	b2.SetControlServer(controlURL)
	p3, err := b2.Start("", 60_000)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	json.Unmarshal([]byte(b2.Status()), &st)
	if len(st.TailnetIP) == 0 || st.TailnetIP[0] != firstIP {
		t.Fatalf("IP setelah restart %v, sebelumnya %s", st.TailnetIP, firstIP)
	}
	if p3.Password == p.Password {
		t.Fatal("kredensial proxy harus baru per proses")
	}
}

func TestBridgeValidation(t *testing.T) {
	if _, err := NewBridge("", "x").Start("", 10); err == nil {
		t.Fatal("stateDir kosong harus ditolak")
	}
	if _, err := NewBridge(t.TempDir(), "").Start("", 10); err == nil {
		t.Fatal("hostname kosong harus ditolak")
	}
	if NewBridge(t.TempDir(), "x").Close() != nil {
		t.Fatal("Close tanpa Start harus aman")
	}
}

type logFunc func(string)

func (f logFunc) Log(s string) { f(s) }
