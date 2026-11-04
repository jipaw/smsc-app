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
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTSAPI: Tailscale API minimal (OAuth token + keys). Key yang dibuat = labKey, yang juga diwajibkan control.
type fakeTSAPI struct {
	mu      sync.Mutex
	created []map[string]any
	deleted []string
}

func (f *fakeTSAPI) handler(key string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/api/v2/oauth/token":
			r.ParseForm()
			if r.Form.Get("client_secret") != "lab-secret" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "lab-token", "expires_in": 3600})
		case r.Header.Get("Authorization") != "Bearer lab-token":
			w.WriteHeader(401)
		case r.Method == "POST" && r.URL.Path == "/api/v2/tailnet/-/keys":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			f.created = append(f.created, in)
			json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("lab%d", len(f.created)), "key": key})
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v2/tailnet/-/keys/"):
			f.deleted = append(f.deleted, strings.TrimPrefix(r.URL.Path, "/api/v2/tailnet/-/keys/"))
		default:
			w.WriteHeader(404)
		}
	})
}

// TestLabTailnetEnforceWithMintedKey: arsitektur fase 46 end-to-end tanpa internet dan tanpa HP.
//
//	smscd = node tailnet sendiri (TAILNET_ENABLED, kebijakan enforce) + auth key enrollment lewat Tailscale API (palsu)
//	HP    = bridge tsnet yang login dengan auth key dari QR (k=), lalu agen JVM enroll + WebSocket lewat proxy bridge
//
// Control mewajibkan auth key itu, jadi HP tidak dapat bergabung tanpa key hasil enrollment.
func TestLabTailnetEnforceWithMintedKey(t *testing.T) {
	smscd, agent, base := os.Getenv("SMSC_LAB_SMSCD"), os.Getenv("SMSC_LAB_AGENT"), os.Getenv("SMSC_TEST_DATABASE_URL")
	if smscd == "" || agent == "" || base == "" {
		t.Skip("set SMSC_LAB_SMSCD, SMSC_LAB_AGENT, SMSC_TEST_DATABASE_URL")
	}
	const labKey = "tskey-auth-lab-sekali-pakai"
	control := startLabControlServer(t, labKey)
	api := &fakeTSAPI{}
	apiSrv := httptest.NewServer(api.handler(labKey))
	defer apiSrv.Close()

	db := fmt.Sprintf("smsc_lab_%d", rand.Int63()%1e9)
	psql := func(dsn, q string) {
		if out, err := exec.Command("psql", dsn, "-qAtc", q).CombinedOutput(); err != nil {
			t.Fatalf("psql: %v %s", err, out)
		}
	}
	psql(base, "CREATE DATABASE "+db)
	t.Cleanup(func() { exec.Command("psql", base, "-qc", "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)").Run() })
	dsn := strings.Replace(base, "/postgres?", "/"+db+"?", 1)
	port := 25000 + rand.Intn(5000)
	local := fmt.Sprintf("http://127.0.0.1:%d", port)
	env := append(os.Environ(), "DATABASE_URL="+dsn, "PUBLIC_BASE_URL=http://smsc", "SESSION_SECRET="+strings.Repeat("s", 40),
		fmt.Sprintf("HTTP_ADDR=127.0.0.1:%d", port), "METRICS_ADDR=", "AUTO_MIGRATE=true", "SECURE_COOKIES=false", "REQUIRE_VPN=false",
		"TAILNET_ENABLED=true", "TAILNET_STATE_DIR="+t.TempDir(), "TAILNET_CONTROL_URL="+control.HTTPTestServer.URL, "TAILNET_LISTEN=http",
		"TAILNET_AUTH_KEY="+labKey, "TAILNET_DEVICE_POLICY=enforce", "TAILNET_DEVICE_TAGS=tag:smsc-device",
		"TAILSCALE_API_URL="+apiSrv.URL, "TAILSCALE_OAUTH_CLIENT_ID=lab", "TAILSCALE_OAUTH_CLIENT_SECRET=lab-secret")
	run := func(args ...string) string {
		c := exec.Command(smscd, args...)
		c.Env = env
		out, err := c.CombinedOutput()
		if err != nil && args[0] != "doctor" {
			t.Fatalf("smscd %v: %v %s", args, err, out)
		}
		return string(out)
	}
	run("migrate")
	run("admin", "create", "--email", "admin@lab.test", "--password", "lab-pagar-biru-2026")
	if doc := run("doctor"); !strings.Contains(doc, "tailnet ter-embed aktif") || !strings.Contains(doc, "OAuth client (token berhasil diperoleh)") {
		t.Fatalf("doctor:\n%s", doc)
	}
	psql(dsn, `UPDATE dispatch_policies SET min_delay_ms=200, max_delay_ms=200, track_delivery=true,
		send_window='{"timezone":"UTC","windows":[{"days":[1,2,3,4,5,6,7],"start":"00:00","end":"00:00"}]}' WHERE is_default`)
	logPath := filepath.Join(t.TempDir(), "smscd.log")
	lf, _ := os.Create(logPath)
	srv := exec.Command(smscd, "serve")
	srv.Env, srv.Stdout, srv.Stderr = env, lf, lf
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Process.Signal(os.Interrupt); srv.Wait() })
	waitUntil(t, 60*time.Second, "smscd siap + tersambung ke tailnet", func() bool {
		b, _ := os.ReadFile(logPath)
		r, err := http.Get(local + "/readyz")
		return err == nil && r.StatusCode == 200 && strings.Contains(string(b), "melayani di tailnet")
	})

	// admin lewat loopback
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
	if st := call("POST", "/api/v1/device-enrollments", map[string]any{"device_name": "hp-lab"}, &en); st != 201 {
		t.Fatalf("enrollment: %d", st)
	}
	u, _ := url.Parse(en.EnrollURL)
	key := u.Query().Get("k")
	if key != labKey {
		t.Fatalf("QR harus memuat auth key hasil API: %q", en.EnrollURL)
	}

	// HP: tanpa auth key tidak dapat bergabung (control mewajibkan key)
	noKey := NewBridge(t.TempDir(), "hp-tanpa-key")
	noKey.SetControlServer(control.HTTPTestServer.URL)
	if _, err := noKey.Start("", 5_000); err == nil {
		t.Fatal("node tanpa auth key tidak boleh tersambung")
	}
	noKey.Close()
	// HP: auth key dari QR → tersambung; admin tailnet memberi tag device (setara key ber-tag di Tailscale)
	b := NewBridge(t.TempDir(), "hp-lab")
	b.SetControlServer(control.HTTPTestServer.URL)
	p, err := b.Start(key, 60_000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	var self struct {
		TailnetIP []string `json:"tailnet_ips"`
	}
	json.Unmarshal([]byte(b.Status()), &self)
	lc, _ := b.srv.LocalClient()
	st, _ := lc.StatusWithoutPeers(context.Background())
	n := control.Node(st.Self.PublicKey).Clone()
	n.Tags = []string{"tag:smsc-device"}
	control.UpdateNode(n)

	// agen enroll lewat tailnet. Sebelum netmap smscd melihat tag, enroll ditolak 403 (token TIDAK hangus) dan agen keluar;
	// jalankan ulang sampai berhasil, lalu agen tetap berjalan (WebSocket).
	state := t.TempDir()
	agentLog := filepath.Join(state, "agent.log")
	alf, _ := os.Create(agentLog)
	var ag *exec.Cmd
	exited := make(chan struct{}, 1)
	start := func() {
		ag = exec.Command(agent, "--state", state, "--latency-ms", "150", "--proxy", p.URL(), "--enroll", en.EnrollURL)
		ag.Env, ag.Stdout, ag.Stderr = os.Environ(), alf, alf
		if err := ag.Start(); err != nil {
			t.Fatal(err)
		}
		go func(c *exec.Cmd) { c.Wait(); exited <- struct{}{} }(ag)
	}
	start()
	t.Cleanup(func() { ag.Process.Signal(os.Interrupt) })
	type dev struct {
		ID, Status string
		Tailnet    *struct {
			NodeID string   `json:"node_id"`
			Tags   []string `json:"tags"`
		} `json:"tailnet"`
	}
	var devs struct{ Items []dev }
	attempts := 1
	waitUntil(t, 60*time.Second, "agen ter-enroll dan online lewat tailnet", func() bool {
		select {
		case <-exited:
			attempts++
			start()
		default:
		}
		call("GET", "/api/v1/devices", nil, &devs)
		return len(devs.Items) == 1 && devs.Items[0].Status == "online"
	})
	d := devs.Items[0]
	if d.Tailnet == nil || d.Tailnet.NodeID != string(st.Self.ID) || len(d.Tailnet.Tags) != 1 {
		t.Fatalf("device harus terikat ke node HP: %+v (node %s)", d.Tailnet, st.Self.ID)
	}
	logb, _ := os.ReadFile(agentLog)
	if strings.Contains(string(logb), labKey) || strings.Contains(string(logb), p.Password) {
		t.Fatal("auth key / password proxy tidak boleh muncul di log agen")
	}
	t.Logf("enroll berhasil pada percobaan ke-%d", attempts)

	// SMS lewat tailnet
	var sent struct{ Messages []struct{ ID string } }
	if st := call("POST", "/api/v1/messages", map[string]any{"recipients": []map[string]string{{"to": "081234567890"}}, "body": "Halo enforce"}, &sent); st != 202 {
		t.Fatalf("kirim: %d", st)
	}
	waitUntil(t, 30*time.Second, "pesan delivered", func() bool {
		var m struct{ Status string }
		call("GET", "/api/v1/messages/"+sent.Messages[0].ID, nil, &m)
		return m.Status == "delivered"
	})

	// key dibuat dengan parameter benar dan dicabut setelah dipakai
	api.mu.Lock()
	created, deleted := api.created, api.deleted
	api.mu.Unlock()
	caps, _ := json.Marshal(created)
	if len(created) != 1 || !strings.Contains(string(caps), `"preauthorized":true`) || !strings.Contains(string(caps), `"reusable":false`) ||
		!strings.Contains(string(caps), `"tag:smsc-device"`) || len(deleted) != 1 || deleted[0] != "lab1" {
		t.Fatalf("Tailscale API: created=%s deleted=%v", caps, deleted)
	}
}
