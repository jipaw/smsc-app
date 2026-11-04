package tsnetbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tailscale.com/ipn/store/mem"
	"tailscale.com/tsnet"
	"tailscale.com/types/logger"
)

// TestLabAgentOverEmbeddedTailnet: lab end-to-end tanpa internet dan tanpa HP.
//
//	agen JVM (OkHttp, :core) --SOCKS5--> bridge (tsnet, node "hp-lab") ==tailnet==> node "smsc" (setara tailscale serve) --> smscd
//
// PUBLIC_BASE_URL = http://smsc (nama MagicDNS), jadi enroll_url dan ws_url hanya dapat dijangkau lewat tailnet.
// Dilewati kecuali SMSC_LAB_SMSCD (biner smscd), SMSC_LAB_AGENT (bin/smsc-agent), dan SMSC_TEST_DATABASE_URL diset.
func TestLabAgentOverEmbeddedTailnet(t *testing.T) {
	smscd, agent, base := os.Getenv("SMSC_LAB_SMSCD"), os.Getenv("SMSC_LAB_AGENT"), os.Getenv("SMSC_TEST_DATABASE_URL")
	if smscd == "" || agent == "" || base == "" {
		t.Skip("set SMSC_LAB_SMSCD, SMSC_LAB_AGENT, SMSC_TEST_DATABASE_URL")
	}
	controlURL := startLabControl(t)

	// --- smscd (loopback saja) ---
	db := fmt.Sprintf("smsc_lab_%d", rand.Int63()%1e9)
	psql := func(dsn, q string) string {
		out, err := exec.Command("psql", dsn, "-qAtc", q).CombinedOutput()
		if err != nil {
			t.Fatalf("psql %q: %v %s", q, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	psql(base, "CREATE DATABASE "+db)
	t.Cleanup(func() { exec.Command("psql", base, "-qc", "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)").Run() })
	dsn := strings.Replace(base, "/postgres?", "/"+db+"?", 1)
	port := 20000 + rand.Intn(5000)
	local := fmt.Sprintf("http://127.0.0.1:%d", port)
	env := append(os.Environ(), "DATABASE_URL="+dsn, "PUBLIC_BASE_URL=http://smsc", "SESSION_SECRET="+strings.Repeat("s", 40),
		fmt.Sprintf("HTTP_ADDR=127.0.0.1:%d", port), "METRICS_ADDR=", "AUTO_MIGRATE=true", "SECURE_COOKIES=false", "REQUIRE_VPN=false")
	run := func(args ...string) {
		c := exec.Command(smscd, args...)
		c.Env = env
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("smscd %v: %v %s", args, err, out)
		}
	}
	run("migrate")
	run("admin", "create", "--email", "admin@lab.test", "--password", "lab-pagar-biru-2026")
	psql(dsn, `UPDATE dispatch_policies SET min_delay_ms=200, max_delay_ms=200, track_delivery=true,
		send_window='{"timezone":"UTC","windows":[{"days":[1,2,3,4,5,6,7],"start":"00:00","end":"00:00"}]}' WHERE is_default`)
	srvLog, _ := os.Create(filepath.Join(t.TempDir(), "smscd.log"))
	srv := exec.Command(smscd, "serve")
	srv.Env, srv.Stdout, srv.Stderr = env, srvLog, srvLog
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Process.Signal(os.Interrupt); srv.Wait() })
	waitUntil(t, 15*time.Second, "smscd siap", func() bool {
		r, err := http.Get(local + "/readyz")
		return err == nil && r.StatusCode == 200
	})

	// --- node "smsc": tailnet:80 -> smscd (setara tailscale serve), hitung upgrade WebSocket yang lewat tailnet ---
	var wsViaTailnet atomic.Int32
	gw := &tsnet.Server{Dir: t.TempDir(), ControlURL: controlURL, Hostname: "smsc", Store: new(mem.Store), Ephemeral: true, Logf: logger.Discard}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := gw.Up(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gw.Close() })
	ln, err := gw.Listen("tcp", ":80")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(local)
	rp := httputil.NewSingleHostReverseProxy(u)
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws/device" {
			wsViaTailnet.Add(1)
		}
		rp.ServeHTTP(w, r)
	}))

	// --- admin (langsung ke loopback): buat enrollment ---
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	var csrf string
	call := func(method, path string, body any, out any) int {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, local+path, rd)
		req.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}
	var me struct {
		CSRF string `json:"csrf_token"`
	}
	call("POST", "/api/v1/auth/login", map[string]string{"email": "admin@lab.test", "password": "lab-pagar-biru-2026"}, &me)
	csrf = me.CSRF
	var en struct {
		EnrollURL string `json:"enroll_url"`
	}
	if st := call("POST", "/api/v1/device-enrollments", map[string]any{"device_name": "hp-lab"}, &en); st != 201 || !strings.Contains(en.EnrollURL, "s=http%3A%2F%2Fsmsc") {
		t.Fatalf("enrollment: %d %q", st, en.EnrollURL)
	}

	// --- tanpa proxy tailnet: server tidak terjangkau ---
	stateNo := t.TempDir()
	noProxy := exec.Command(agent, "--state", stateNo, "--enroll", en.EnrollURL)
	noProxy.Env = os.Environ()
	out, err := noProxy.CombinedOutput()
	if err == nil || strings.Contains(string(out), "enroll OK") {
		t.Fatalf("tanpa tailnet enroll tidak boleh berhasil: %s", out)
	}

	// --- bridge di sisi "HP" ---
	b := NewBridge(t.TempDir(), "hp-lab")
	b.SetControlServer(controlURL)
	p, err := b.Start("", 60_000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	state := t.TempDir()
	logPath := filepath.Join(state, "agent.log")
	lf, _ := os.Create(logPath)
	ag := exec.Command(agent, "--state", state, "--latency-ms", "150", "--proxy", p.URL(), "--enroll", en.EnrollURL)
	ag.Env, ag.Stdout, ag.Stderr = os.Environ(), lf, lf
	if err := ag.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ag.Process.Signal(os.Interrupt); ag.Wait() })
	type dev struct{ ID, Status string }
	var devs struct{ Items []dev }
	waitUntil(t, 60*time.Second, "agen online lewat tailnet", func() bool {
		call("GET", "/api/v1/devices", nil, &devs)
		return len(devs.Items) == 1 && devs.Items[0].Status == "online"
	})
	if wsViaTailnet.Load() < 1 {
		t.Fatal("WebSocket harus lewat node tailnet")
	}
	agentLog, _ := os.ReadFile(logPath)
	if strings.Contains(string(agentLog), p.Password) || !strings.Contains(string(agentLog), "socks5://tsnet:***@127.0.0.1") {
		t.Fatalf("log agen harus memuat proxy tersamar, bukan password:\n%s", agentLog)
	}

	// --- kirim SMS: server -> tailnet -> agen -> event kembali lewat tailnet ---
	var sent struct{ Messages []struct{ ID string } }
	if st := call("POST", "/api/v1/messages", map[string]any{"recipients": []map[string]string{{"to": "081234567890"}}, "body": "Halo lewat tailnet"}, &sent); st != 202 || len(sent.Messages) != 1 {
		t.Fatalf("kirim: %d", st)
	}
	waitUntil(t, 30*time.Second, "pesan delivered", func() bool {
		var m struct{ Status string }
		call("GET", "/api/v1/messages/"+sent.Messages[0].ID, nil, &m)
		return m.Status == "delivered"
	})

	// --- node HP mati (tailnet putus) -> device offline; nyala lagi dengan state yang sama -> agen baru tersambung ---
	ag.Process.Signal(os.Interrupt)
	ag.Wait()
	b.Close()
	b2 := NewBridge(b.stateDir, "hp-lab")
	b2.SetControlServer(controlURL)
	p2, err := b2.Start("", 60_000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b2.Close() })
	before := wsViaTailnet.Load()
	ag2 := exec.Command(agent, "--state", state, "--latency-ms", "150", "--proxy", p2.URL())
	ag2.Env, ag2.Stdout, ag2.Stderr = os.Environ(), lf, lf
	if err := ag2.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ag2.Process.Signal(os.Interrupt); ag2.Wait() })
	waitUntil(t, 60*time.Second, "agen online lagi setelah node HP restart", func() bool {
		call("GET", "/api/v1/devices", nil, &devs)
		return wsViaTailnet.Load() > before && len(devs.Items) == 1 && devs.Items[0].Status == "online"
	})
}

func waitUntil(t *testing.T, d time.Duration, what string, f func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}
