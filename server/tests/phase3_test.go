package tests

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"

	"github.com/jipaw/smsc-app/server/internal/config"
	"github.com/jipaw/smsc-app/server/internal/core"
	"github.com/jipaw/smsc-app/server/internal/proto"
)

// ---------- penerima webhook palsu ----------

type hit struct {
	At     time.Time
	Header http.Header
	Body   []byte
}

type receiver struct {
	mu     sync.Mutex
	hits   []hit
	status int // kode balasan (default 200)
	path   map[string]int
	h      func(w http.ResponseWriter, r *http.Request) bool // true = sudah ditangani
	srv    *httptest.Server
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{status: 200, path: map[string]int{}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.path[req.URL.Path]++
		r.hits = append(r.hits, hit{time.Now(), req.Header.Clone(), b})
		st, h := r.status, r.h
		r.mu.Unlock()
		if h != nil && h(w, req) {
			return
		}
		w.WriteHeader(st)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) setStatus(c int) { r.mu.Lock(); r.status = c; r.mu.Unlock() }
func (r *receiver) count() int      { r.mu.Lock(); defer r.mu.Unlock(); return len(r.hits) }
func (r *receiver) all() []hit {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hit(nil), r.hits...)
}

type envelope struct {
	Event      string          `json:"event"`
	DeliveryID string          `json:"delivery_id"`
	At         string          `json:"at"`
	Data       map[string]any  `json:"data"`
	Raw        json.RawMessage `json:"-"`
}

func (h hit) env(t *testing.T) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal(h.Body, &e); err != nil {
		t.Fatalf("body webhook bukan JSON: %v\n%s", err, h.Body)
	}
	return e
}

func (h hit) validSig(secret string) bool {
	want := core.Sign(secret, h.Header.Get("X-SMSC-Timestamp"), h.Body)
	return hmac.Equal([]byte(want), []byte(h.Header.Get("X-SMSC-Signature")))
}

func (r *receiver) events(t *testing.T) []string {
	var out []string
	for _, h := range r.all() {
		out = append(out, h.env(t).Event)
	}
	return out
}

type whResp struct {
	ID            string   `json:"id"`
	URL           string   `json:"url"`
	Events        []string `json:"events"`
	Enabled       bool     `json:"enabled"`
	SigningSecret string   `json:"signing_secret"`
}

func (c *client) newWebhook(url string, events ...string) whResp {
	c.e.t.Helper()
	var w whResp
	if st := c.do("POST", "/api/v1/webhooks", map[string]any{"url": url, "events": events}, &w); st != 201 {
		c.e.t.Fatalf("POST /webhooks: %d", st)
	}
	return w
}

type delivery struct {
	ID         string  `json:"id"`
	Event      string  `json:"event"`
	Status     string  `json:"status"`
	Attempts   int     `json:"attempts"`
	LastStatus *int    `json:"last_status"`
	LastError  *string `json:"last_error"`
}

func (c *client) deliveries(webhookID string) []delivery {
	c.e.t.Helper()
	var out struct{ Items []delivery }
	if st := c.do("GET", "/api/v1/webhooks/"+webhookID+"/deliveries?limit=200", nil, &out); st != 200 {
		c.e.t.Fatalf("deliveries: %d", st)
	}
	return out.Items
}

func (c *client) waitDelivery(webhookID, status string, d time.Duration) delivery {
	c.e.t.Helper()
	var got delivery
	waitFor(c.e.t, d, "delivery "+status, func() bool {
		for _, x := range c.deliveries(webhookID) {
			if x.Status == status {
				got = x
				return true
			}
		}
		return false
	})
	return got
}

// ================= WEBHOOK =================

func TestWebhookCreateValidationSecretAndSSRF(t *testing.T) {
	e := newEnv(t)
	admin := e.loginAs("admin")
	op := e.loginAs("operator")
	var pr struct {
		Type   string
		Errors []struct{ Field string }
	}
	for name, body := range map[string]map[string]any{
		"skema ftp":         {"url": "ftp://example.com/x", "events": []string{"message.sent"}},
		"tanpa url":         {"events": []string{"message.sent"}},
		"kredensial di url": {"url": "http://user:pass@10.0.0.5/x", "events": []string{"message.sent"}},
		"event kosong":      {"url": "http://10.0.0.5/x", "events": []string{}},
		"event tak dikenal": {"url": "http://10.0.0.5/x", "events": []string{"message.exploded"}},
		"metadata cloud":    {"url": "http://169.254.169.254/latest/meta-data", "events": []string{"message.sent"}},
		"link-local ipv6":   {"url": "http://[fe80::1]/x", "events": []string{"message.sent"}},
		"0.0.0.0":           {"url": "http://0.0.0.0:80/x", "events": []string{"message.sent"}},
	} {
		if st := admin.do("POST", "/api/v1/webhooks", body, &pr); st != 422 || pr.Type != "validation.failed" {
			t.Errorf("%s: harus 422, dapat %d %s", name, st, pr.Type)
		}
	}
	if st := op.do("POST", "/api/v1/webhooks", map[string]any{"url": "http://10.0.0.5/x", "events": []string{"message.sent"}}, nil); st != 403 {
		t.Fatalf("operator tidak boleh membuat webhook: %d", st)
	}
	// jaringan privat/tailnet DIIZINKAN
	if w := admin.newWebhook("http://100.100.1.2:9000/hook", "message.sent"); !strings.HasPrefix(w.SigningSecret, "whsec_") {
		t.Fatalf("secret: %q", w.SigningSecret)
	}
	w := admin.newWebhook("http://10.20.30.40/hook", "message.sent", "message.delivered")
	// secret hanya pada respons pembuatan
	var raw map[string]any
	admin.do("GET", "/api/v1/webhooks/"+w.ID, nil, &raw)
	if _, has := raw["signing_secret"]; has {
		t.Fatal("GET tidak boleh membocorkan signing_secret")
	}
	var list struct{ Items []map[string]any }
	admin.do("GET", "/api/v1/webhooks", nil, &list)
	for _, it := range list.Items {
		if _, has := it["signing_secret"]; has {
			t.Fatal("daftar tidak boleh memuat signing_secret")
		}
	}
	// terenkripsi di database
	var blob []byte
	_ = e.pool.QueryRow(context.Background(), `SELECT signing_secret FROM webhooks WHERE id=$1`, w.ID).Scan(&blob)
	if len(blob) == 0 || strings.Contains(string(blob), w.SigningSecret) || strings.Contains(string(blob), "whsec_") {
		t.Fatal("secret tidak boleh tersimpan sebagai teks biasa")
	}
}

func TestWebhookLoopbackBlockedByDefault(t *testing.T) { // SSRF: loopback ditolak kecuali diizinkan eksplisit
	e := newEnvOpts(t, func(c *config.Config) { c.WebhookAllowLoopback = false })
	admin := e.loginAs("admin")
	for _, u := range []string{"http://127.0.0.1:8080/x", "http://localhost:8080/x", "http://[::1]/x"} {
		if st := admin.do("POST", "/api/v1/webhooks", map[string]any{"url": u, "events": []string{"message.sent"}}, nil); st != 422 {
			t.Errorf("%s harus ditolak (422), dapat %d", u, st)
		}
	}
}

func TestWebhookDeliversSignedEventsEndToEnd(t *testing.T) {
	e := newEnv(t)
	admin := e.loginAs("admin")
	rcv := newReceiver(t)
	other := newReceiver(t)
	w := admin.newWebhook(rcv.srv.URL+"/hook", "message.sent", "message.delivered", "message.failed")
	admin.newWebhook(other.srv.URL+"/hook", "message.failed") // langganan lain: tidak boleh menerima sent/delivered
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	m := admin.send(num, "ISI-RAHASIA-JANGAN-BOCOR")[0]
	admin.waitStatus(m.ID, "delivered", 15*time.Second)
	waitFor(t, 8*time.Second, "2 event diterima", func() bool { return rcv.count() >= 2 })
	time.Sleep(400 * time.Millisecond)
	hits := rcv.all()
	if len(hits) != 2 {
		t.Fatalf("mau tepat 2 event (sent, delivered), dapat %d: %v", len(hits), rcv.events(t))
	}
	seen := map[string]bool{}
	for _, h := range hits {
		env := h.env(t)
		seen[env.Event] = true
		if !h.validSig(w.SigningSecret) {
			t.Errorf("tanda tangan tidak valid untuk %s", env.Event)
		}
		if h.validSig("whsec_salah") {
			t.Error("tanda tangan harus gagal dengan secret lain")
		}
		ts, _ := strconv.ParseInt(h.Header.Get("X-SMSC-Timestamp"), 10, 64)
		if d := time.Since(time.Unix(ts, 0)); d < -2*time.Second || d > 10*time.Second {
			t.Errorf("timestamp di luar jendela: %v", d)
		}
		if h.Header.Get("X-SMSC-Event") != env.Event || h.Header.Get("X-SMSC-Delivery") != env.DeliveryID || env.DeliveryID == "" {
			t.Errorf("header tidak konsisten dengan body: %v vs %+v", h.Header, env)
		}
		if h.Header.Get("Content-Type") != "application/json" || !strings.HasPrefix(h.Header.Get("User-Agent"), "SMSC-Webhook/") {
			t.Errorf("header standar salah: %v", h.Header)
		}
		if env.Data["message_id"] != m.ID || env.Data["to"] != "+628123456789" {
			t.Errorf("data pesan salah: %+v", env.Data)
		}
		if strings.Contains(string(h.Body), "ISI-RAHASIA") {
			t.Fatal("isi SMS TIDAK boleh ada di payload webhook")
		}
	}
	if !seen["message.sent"] || !seen["message.delivered"] {
		t.Fatalf("event: %v", seen)
	}
	if other.count() != 0 {
		t.Fatalf("webhook yang hanya berlangganan message.failed tidak boleh menerima apa pun, dapat %d", other.count())
	}
	for _, d := range admin.deliveries(w.ID) {
		if d.Status != "delivered" || d.Attempts != 1 || d.LastStatus == nil || *d.LastStatus != 200 {
			t.Errorf("riwayat: %+v", d)
		}
	}
}

func TestWebhookOutboxCreatedAtomicallyWithTransition(t *testing.T) {
	e := newEnv(t)
	admin := e.loginAs("admin")
	rcv := newReceiver(t)
	rcv.setStatus(503) // penerima gagal terus: baris outbox tetap harus ada tepat sekali per event
	admin.newWebhook(rcv.srv.URL, "message.sent", "message.delivered")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	m := admin.send(num, "x")[0]
	admin.waitStatus(m.ID, "delivered", 15*time.Second)
	var sent, delivered int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE event_type='message.sent'), count(*) FILTER (WHERE event_type='message.delivered') FROM webhook_deliveries`).Scan(&sent, &delivered)
	if sent != 1 || delivered != 1 {
		t.Fatalf("outbox: sent=%d delivered=%d (masing-masing harus 1)", sent, delivered)
	}
}

func TestWebhookRetryBackoffFailThenManualRetry(t *testing.T) {
	e := newEnv(t) // backoff uji: 300ms, 400ms, 500ms -> total 4 percobaan
	admin := e.loginAs("admin")
	rcv := newReceiver(t)
	rcv.setStatus(500)
	w := admin.newWebhook(rcv.srv.URL, "message.failed")
	var tr struct {
		DeliveryID string `json:"delivery_id"`
	}
	if st := admin.do("POST", "/api/v1/webhooks/"+w.ID+"/test", map[string]any{}, &tr); st != 202 {
		t.Fatalf("test: %d", st)
	}
	d := admin.waitDelivery(w.ID, "failed", 15*time.Second)
	if d.Attempts != 4 || d.LastStatus == nil || *d.LastStatus != 500 || d.ID != tr.DeliveryID {
		t.Fatalf("gagal final: %+v", d)
	}
	hits := rcv.all()
	if len(hits) != 4 {
		t.Fatalf("mau 4 percobaan, dapat %d", len(hits))
	}
	ids := map[string]bool{}
	for i, h := range hits {
		ids[h.Header.Get("X-SMSC-Delivery")] = true
		if i > 0 && h.At.Sub(hits[i-1].At) < 250*time.Millisecond {
			t.Errorf("jeda percobaan #%d terlalu singkat: %v", i, h.At.Sub(hits[i-1].At))
		}
		if h.env(t).Event != "webhook.test" {
			t.Errorf("event uji: %s", h.env(t).Event)
		}
	}
	if len(ids) != 1 {
		t.Fatalf("delivery_id harus SAMA pada semua percobaan (dedup penerima), dapat %v", ids)
	}
	// perbaiki penerima, kirim ulang manual -> berhasil, percobaan dimulai dari nol
	rcv.setStatus(200)
	if st := admin.do("POST", "/api/v1/webhooks/"+w.ID+"/deliveries/"+d.ID+"/retry", map[string]any{}, nil); st != 204 {
		t.Fatalf("retry manual: %d", st)
	}
	ok := admin.waitDelivery(w.ID, "delivered", 10*time.Second)
	if ok.Attempts != 1 || ok.LastStatus == nil || *ok.LastStatus != 200 {
		t.Fatalf("setelah retry: %+v", ok)
	}
	var pr struct{ Type string }
	if st := admin.do("POST", "/api/v1/webhooks/"+w.ID+"/deliveries/"+d.ID+"/retry", map[string]any{}, &pr); st != 409 || pr.Type != "webhook.not_retryable" {
		t.Fatalf("retry pada delivered harus 409: %d %s", st, pr.Type)
	}
}

func TestWebhookTimeoutRedirectAndUnreachableCountAsFailure(t *testing.T) {
	e := newEnvOpts(t, func(c *config.Config) {
		c.WebhookTimeout = 300 * time.Millisecond
		c.WebhookBackoff = []time.Duration{100 * time.Millisecond}
	})
	admin := e.loginAs("admin")

	slow := newReceiver(t)
	slow.h = func(w http.ResponseWriter, r *http.Request) bool { time.Sleep(1500 * time.Millisecond); return false }
	ws := admin.newWebhook(slow.srv.URL, "message.failed")
	admin.do("POST", "/api/v1/webhooks/"+ws.ID+"/test", map[string]any{}, nil)
	d := admin.waitDelivery(ws.ID, "failed", 15*time.Second)
	if d.LastStatus != nil || d.LastError == nil || !(strings.Contains(*d.LastError, "timeout") || strings.Contains(*d.LastError, "deadline")) {
		t.Fatalf("timeout harus tercatat: %+v err=%v", d, d.LastError)
	}

	redir := newReceiver(t)
	redir.h = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/hook" {
			http.Redirect(w, r, "/dituju", http.StatusFound)
			return true
		}
		return false
	}
	wr := admin.newWebhook(redir.srv.URL+"/hook", "message.failed")
	admin.do("POST", "/api/v1/webhooks/"+wr.ID+"/test", map[string]any{}, nil)
	d = admin.waitDelivery(wr.ID, "failed", 15*time.Second)
	redir.mu.Lock()
	followed := redir.path["/dituju"]
	redir.mu.Unlock()
	if d.LastStatus == nil || *d.LastStatus != 302 || followed != 0 {
		t.Fatalf("redirect tidak boleh diikuti (SSRF): %+v diikuti=%d", d, followed)
	}

	dead := newReceiver(t)
	url := dead.srv.URL
	dead.srv.Close() // koneksi ditolak
	wd := admin.newWebhook(url, "message.failed")
	admin.do("POST", "/api/v1/webhooks/"+wd.ID+"/test", map[string]any{}, nil)
	d = admin.waitDelivery(wd.ID, "failed", 15*time.Second)
	if d.LastStatus != nil || d.LastError == nil || d.Attempts != 2 {
		t.Fatalf("penerima mati: %+v", d)
	}
}

func TestWebhookDisabledDeletedAndTestEndpoint(t *testing.T) {
	e := newEnv(t)
	admin := e.loginAs("admin")
	rcv := newReceiver(t)
	w := admin.newWebhook(rcv.srv.URL, "message.failed")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	// event uji dikirim walau tidak berlangganan 'webhook.test'
	admin.do("POST", "/api/v1/webhooks/"+w.ID+"/test", map[string]any{}, nil)
	waitFor(t, 8*time.Second, "event uji", func() bool { return rcv.count() == 1 })
	if ev := rcv.events(t); ev[0] != "webhook.test" || !rcv.all()[0].validSig(w.SigningSecret) {
		t.Fatalf("event uji: %v", ev)
	}
	// nonaktif: event baru tidak menghasilkan baris outbox
	var up whResp
	if st := admin.do("PATCH", "/api/v1/webhooks/"+w.ID, map[string]any{"enabled": false, "events": []string{"message.sent"}}, &up); st != 200 || up.Enabled {
		t.Fatalf("nonaktifkan: %d %+v", st, up)
	}
	m := admin.send(num, "x")[0]
	admin.waitStatus(m.ID, "delivered", 15*time.Second)
	time.Sleep(500 * time.Millisecond)
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM webhook_deliveries WHERE event_type='message.sent'`).Scan(&n)
	if n != 0 || rcv.count() != 1 {
		t.Fatalf("webhook nonaktif tidak boleh menerima/mengantrekan event: outbox=%d diterima=%d", n, rcv.count())
	}
	// aktifkan lagi
	admin.do("PATCH", "/api/v1/webhooks/"+w.ID, map[string]any{"enabled": true}, nil)
	m2 := admin.send("08129998877", "y")[0]
	admin.waitStatus(m2.ID, "delivered", 15*time.Second)
	waitFor(t, 8*time.Second, "event setelah diaktifkan", func() bool { return rcv.count() == 2 })
	// validasi PATCH
	if st := admin.do("PATCH", "/api/v1/webhooks/"+w.ID, map[string]any{"url": "ftp://x"}, nil); st != 422 {
		t.Fatalf("patch url buruk: %d", st)
	}
	// hapus -> riwayat ikut terhapus
	if st := admin.do("DELETE", "/api/v1/webhooks/"+w.ID, nil, nil); st != 204 {
		t.Fatalf("hapus: %d", st)
	}
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM webhook_deliveries WHERE webhook_id=$1`, w.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("riwayat harus ikut terhapus: %d", n)
	}
	if st := admin.do("GET", "/api/v1/webhooks/"+w.ID, nil, nil); st != 404 {
		t.Fatalf("setelah hapus: %d", st)
	}
}

func TestWebhookCampaignAndDeviceEvents(t *testing.T) {
	e := newEnv(t)
	e.fastPolicy(100, 100)
	admin := e.loginAs("admin")
	rcv := newReceiver(t)
	w := admin.newWebhook(rcv.srv.URL, "campaign.completed", "campaign.paused", "device.offline")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	cm := admin.newCampaign(map[string]any{"name": "Webhook Camp", "body": "Halo {{name}}"})
	admin.importJSON(cm.ID, recs(3, "A"))
	admin.action(cm.ID, "start", 200)
	admin.waitCampaign(cm.ID, "completed", 25*time.Second)
	waitFor(t, 8*time.Second, "campaign.completed", func() bool {
		for _, ev := range rcv.events(t) {
			if ev == "campaign.completed" {
				return true
			}
		}
		return false
	})
	for _, h := range rcv.all() {
		env := h.env(t)
		if env.Event == "campaign.completed" {
			c := env.Data["counters"].(map[string]any)
			if env.Data["campaign_id"] != cm.ID || env.Data["name"] != "Webhook Camp" || c["delivered"].(float64) != 3 || !h.validSig(w.SigningSecret) {
				t.Fatalf("payload completed: %+v", env.Data)
			}
		}
	}
	// pause oleh pengguna
	cm2 := admin.newCampaign(map[string]any{"name": "P2", "body": "Halo {{name}}"})
	admin.importJSON(cm2.ID, recs(6, "B"))
	e.fastPolicy(2000, 2000)
	admin.action(cm2.ID, "start", 200)
	admin.action(cm2.ID, "pause", 200)
	waitFor(t, 8*time.Second, "campaign.paused", func() bool {
		for _, h := range rcv.all() {
			if env := h.env(t); env.Event == "campaign.paused" && env.Data["reason"] == "user" {
				return true
			}
		}
		return false
	})
	// device terputus
	s.cancel()
	_ = s.conn.CloseNow()
	waitFor(t, 8*time.Second, "device.offline", func() bool {
		for _, h := range rcv.all() {
			if env := h.env(t); env.Event == "device.offline" && env.Data["device_id"] == id.String() {
				return true
			}
		}
		return false
	})
	admin.action(cm2.ID, "cancel", 200)
}

// ================= RETENSI =================

func TestRetentionPurgesOnlyOldFinishedData(t *testing.T) {
	e := newEnv(t)
	e.fastPolicy(50, 50)
	admin := e.loginAs("admin")
	other := e.loginAs("operator")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	old := admin.send(num, "PESAN-LAMA")[0]
	recent := admin.send("08129998877", "PESAN-BARU")[0]
	admin.waitStatus(old.ID, "delivered", 15*time.Second)
	admin.waitStatus(recent.ID, "delivered", 15*time.Second)
	// pesan antre (belum selesai) yang sangat lama TIDAK boleh disentuh
	admin.do("PUT", "/api/v1/settings/dispatch-pause", map[string]any{"paused": true}, nil)
	queued := admin.send("08121110000", "MASIH-ANTRE")[0]
	e.sql(`UPDATE messages SET created_at=now()-interval '300 days', not_before=now()+interval '1 day' WHERE id=$1`, queued.ID)
	// kampanye selesai lama + kampanye baru
	cmOld := admin.newCampaign(map[string]any{"name": "Lama", "body": "Halo {{name}}"})
	admin.importJSON(cmOld.ID, recs(2, "X"))
	cmNew := admin.newCampaign(map[string]any{"name": "Baru", "body": "Halo {{name}}"})
	admin.importJSON(cmNew.ID, recs(2, "Y"))
	e.sql(`UPDATE campaigns SET status='completed', finished_at=now()-interval '120 days' WHERE id=$1`, cmOld.ID)
	e.sql(`UPDATE campaigns SET status='completed', finished_at=now()-interval '5 days' WHERE id=$1`, cmNew.ID)
	// usangkan satu pesan, event-nya, sesi, enrollment, dan riwayat webhook
	e.sql(`UPDATE messages SET finished_at=now()-interval '100 days', delivered_at=now()-interval '100 days', sent_at=now()-interval '100 days' WHERE id=$1`, old.ID)
	e.sql(`UPDATE message_events SET at=now()-interval '200 days' WHERE message_id=$1`, old.ID)
	e.sql(`UPDATE sessions SET expires_at=now()-interval '1 day' WHERE user_id=(SELECT id FROM users WHERE email=(SELECT email FROM users WHERE role='operator' LIMIT 1))`)
	e.sql(`INSERT INTO device_enrollments(id,token_hash,short_code_hash,expires_at,consumed_at,created_at) VALUES (gen_random_uuid(),'\x01','\x02',now()-interval '40 days',now()-interval '40 days',now()-interval '40 days')`)
	rcv := newReceiver(t)
	w := admin.newWebhook(rcv.srv.URL, "message.failed")
	e.sql(`INSERT INTO webhook_deliveries(id,webhook_id,event_type,payload,delivered_at,created_at) VALUES (gen_random_uuid(),$1,'message.sent','{}',now()-interval '40 days',now()-interval '40 days')`, w.ID)
	var auditBefore int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs`).Scan(&auditBefore)

	run, err := e.svc.RetentionOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if run.BodiesPurged != 1 || run.RecipientsScrubbed != 2 || run.EventsDeleted == 0 || run.SessionsDeleted != 1 || run.EnrollmentsDeleted != 1 || run.DeliveriesDeleted != 1 {
		t.Fatalf("ringkasan retensi: %+v", run)
	}
	body := func(id string) *string {
		var b *string
		_ = e.pool.QueryRow(context.Background(), `SELECT body FROM messages WHERE id=$1`, id).Scan(&b)
		return b
	}
	if body(old.ID) != nil {
		t.Fatal("isi pesan lama harus dibersihkan")
	}
	if b := body(recent.ID); b == nil || *b != "PESAN-BARU" {
		t.Fatal("pesan baru tidak boleh disentuh")
	}
	if b := body(queued.ID); b == nil || *b != "MASIH-ANTRE" {
		t.Fatal("pesan yang belum selesai tidak boleh disentuh, seberapa pun lamanya")
	}
	got := admin.msg(old.ID) // metadata tetap ada; timeline kosong
	if got.Status != "delivered" || got.To != "+628123456789" || len(got.Events) != 0 {
		t.Fatalf("metadata harus tetap: %+v", got)
	}
	if len(admin.msg(recent.ID).Events) == 0 {
		t.Fatal("event pesan baru harus tetap")
	}
	var varsOld, varsNew, rawOld string
	_ = e.pool.QueryRow(context.Background(), `SELECT vars::text, to_raw FROM campaign_recipients WHERE campaign_id=$1 ORDER BY row_no LIMIT 1`, cmOld.ID).Scan(&varsOld, &rawOld)
	_ = e.pool.QueryRow(context.Background(), `SELECT vars::text FROM campaign_recipients WHERE campaign_id=$1 ORDER BY row_no LIMIT 1`, cmNew.ID).Scan(&varsNew)
	if varsOld != "{}" || !strings.HasPrefix(rawOld, "+62") || varsNew == "{}" {
		t.Fatalf("variabel penerima: lama=%s (raw=%s) baru=%s", varsOld, rawOld, varsNew)
	}
	if st := admin.do("GET", "/api/v1/auth/me", nil, nil); st != 200 {
		t.Fatalf("sesi admin yang aktif harus tetap valid: %d", st)
	}
	_ = other
	var auditAfter, scrubAudit int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs`).Scan(&auditAfter)
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='retention.run'`).Scan(&scrubAudit)
	if auditAfter < auditBefore || scrubAudit != 1 {
		t.Fatalf("audit tidak boleh berkurang (%d -> %d) dan run dicatat sekali (%d)", auditBefore, auditAfter, scrubAudit)
	}
	// idempoten
	run2, _ := e.svc.RetentionOnce(context.Background())
	if run2.BodiesPurged+run2.EventsDeleted+run2.RecipientsScrubbed+run2.DeliveriesDeleted != 0 {
		t.Fatalf("run kedua harus kosong: %+v", run2)
	}
	var rs struct {
		LastRun *struct {
			BodiesPurged int `json:"bodies_purged"`
		} `json:"last_run"`
	}
	admin.do("GET", "/api/v1/settings/retention", nil, &rs)
	if rs.LastRun == nil {
		t.Fatal("last_run harus tersedia di pengaturan")
	}
}

func TestRetentionSettingsAPIAndZeroDays(t *testing.T) {
	e := newEnv(t)
	e.fastPolicy(50, 50)
	admin := e.loginAs("admin")
	op := e.loginAs("operator")
	var rs struct {
		Body   int `json:"body_retention_days"`
		Events int `json:"event_retention_days"`
	}
	if st := admin.do("GET", "/api/v1/settings/retention", nil, &rs); st != 200 || rs.Body != 90 || rs.Events != 180 {
		t.Fatalf("default: %d %+v", st, rs)
	}
	for name, b := range map[string]map[string]any{
		"negatif":       {"body_retention_days": -1, "event_retention_days": 10},
		"terlalu besar": {"body_retention_days": 4000, "event_retention_days": 10},
		"event nol":     {"body_retention_days": 10, "event_retention_days": 0},
		"field kurang":  {"body_retention_days": 10},
	} {
		if st := admin.do("PUT", "/api/v1/settings/retention", b, nil); st != 422 {
			t.Errorf("%s: harus 422, dapat %d", name, st)
		}
	}
	if st := op.do("PUT", "/api/v1/settings/retention", map[string]any{"body_retention_days": 5, "event_retention_days": 5}, nil); st != 403 {
		t.Fatalf("operator: %d", st)
	}
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = func(s *Sim, env proto.Envelope, cmd proto.SmsSend) {
		s.Ack(env, true, "")
		s.Event(cmd, "sending", "")
		s.Event(cmd, "failed", "invalid_destination")
	}
	if st := admin.do("PUT", "/api/v1/settings/retention", map[string]any{"body_retention_days": 0, "event_retention_days": 30}, &rs); st != 200 || rs.Body != 0 {
		t.Fatalf("set 0 hari: %d %+v", st, rs)
	}
	m := admin.send(num, "SEGERA-HAPUS")[0]
	admin.waitStatus(m.ID, "failed", 15*time.Second)
	if _, err := e.svc.RetentionOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var b *string
	_ = e.pool.QueryRow(context.Background(), `SELECT body FROM messages WHERE id=$1`, m.ID).Scan(&b)
	if b != nil {
		t.Fatal("dengan 0 hari, isi pesan yang sudah selesai harus langsung dibersihkan")
	}
	// retry pesan yang isinya sudah dibersihkan -> 422 jelas, bukan macet diam-diam
	var pr struct {
		Errors []struct{ Message string }
	}
	if st := admin.do("POST", "/api/v1/messages/"+m.ID+"/retry", map[string]any{}, &pr); st != 422 || len(pr.Errors) == 0 || !strings.Contains(pr.Errors[0].Message, "retensi") {
		t.Fatalf("retry tanpa isi harus 422 dengan alasan retensi: %d %+v", st, pr)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='retention.update'`).Scan(&n)
	if n != 1 {
		t.Fatalf("perubahan retensi harus diaudit: %d", n)
	}
}

func TestEraseNumberAnonymizesTraces(t *testing.T) {
	e := newEnv(t)
	e.fastPolicy(50, 50)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	rcv := newReceiver(t)
	w := admin.newWebhook(rcv.srv.URL, "message.delivered")
	target := admin.send("08121234567", "Halo target")[0]
	keep := admin.send("08129998877", "Halo lain")[0]
	admin.waitStatus(target.ID, "delivered", 15*time.Second)
	admin.waitStatus(keep.ID, "delivered", 15*time.Second)
	cm := admin.newCampaign(map[string]any{"name": "C", "body": "Halo {{name}}"})
	admin.importJSON(cm.ID, []map[string]any{{"to": "08121234567", "vars": map[string]string{"name": "T"}}, {"to": "08127777777", "vars": map[string]string{"name": "U"}}})
	waitFor(t, 8*time.Second, "webhook terkirim", func() bool { return rcv.count() >= 2 })

	// pesan aktif -> ditolak
	admin.do("PUT", "/api/v1/settings/dispatch-pause", map[string]any{"paused": true}, nil)
	admin.send("08121234567", "aktif")
	if _, err := e.svc.EraseNumber(context.Background(), "08121234567", false, "tes"); err == nil || !strings.Contains(err.Error(), "privacy.active_messages") {
		t.Fatalf("pesan aktif harus menolak penghapusan: %v", err)
	}
	e.sql(`UPDATE messages SET status='cancelled', finished_at=now() WHERE to_e164='+628121234567' AND status='queued'`)

	res, err := e.svc.EraseNumber(context.Background(), "0812-1234-567", true, "cli:privacy")
	if err != nil {
		t.Fatal(err)
	}
	if res.Messages != 2 || res.Recipients != 1 || res.Deliveries < 1 || !res.Suppressed {
		t.Fatalf("hasil: %+v", res)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM messages WHERE to_e164='+628121234567' OR (body IS NOT NULL AND id=$1)`, target.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("masih ada jejak nomor/isi: %d", n)
	}
	var kept *string
	_ = e.pool.QueryRow(context.Background(), `SELECT body FROM messages WHERE id=$1`, keep.ID).Scan(&kept)
	if kept == nil || *kept != "Halo lain" {
		t.Fatal("pesan nomor lain tidak boleh berubah")
	}
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM campaign_recipients WHERE to_e164='+628121234567'`).Scan(&n)
	if n != 0 {
		t.Fatal("penerima campaign harus terhapus")
	}
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM webhook_deliveries WHERE webhook_id=$1 AND payload::text LIKE '%628121234567%'`, w.ID).Scan(&n)
	if n != 0 {
		t.Fatal("payload webhook harus dianonimkan")
	}
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM suppressions WHERE number_e164='+628121234567'`).Scan(&n)
	if n != 1 {
		t.Fatal("harus masuk daftar blokir")
	}
	var data string
	_ = e.pool.QueryRow(context.Background(), `SELECT data::text || target_id FROM audit_logs WHERE action='privacy.erase'`).Scan(&data)
	if strings.Contains(data, "8121234567") {
		t.Fatalf("audit tidak boleh memuat nomor: %s", data)
	}
	if _, err := e.svc.EraseNumber(context.Background(), "bukan", false, "tes"); err == nil {
		t.Fatal("nomor tidak valid harus ditolak")
	}
}

// ================= METRIK =================

func scrape(t *testing.T, e *env) (map[string]*dto.MetricFamily, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	promhttp.HandlerFor(e.svc.M.Reg, promhttp.HandlerOpts{}).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("scrape: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	var p expfmt.TextParser
	fams, err := p.TextToMetricFamilies(strings.NewReader(rec.Body.String())) // format eksposisi harus valid
	if err != nil {
		t.Fatalf("format eksposisi tidak valid: %v", err)
	}
	return fams, rec.Body.String()
}

func metricValue(fams map[string]*dto.MetricFamily, name string, labels map[string]string) (float64, bool) {
	f := fams[name]
	if f == nil {
		return 0, false
	}
	for _, m := range f.Metric {
		match := true
		for k, v := range labels {
			found := false
			for _, l := range m.Label {
				if l.GetName() == k && l.GetValue() == v {
					found = true
				}
			}
			if !found {
				match = false
			}
		}
		if match {
			switch {
			case m.Gauge != nil:
				return m.Gauge.GetValue(), true
			case m.Counter != nil:
				return m.Counter.GetValue(), true
			case m.Histogram != nil:
				return float64(m.Histogram.GetSampleCount()), true
			}
		}
	}
	return 0, false
}

func TestMetricsExpositionValuesCardinalityAndNoPII(t *testing.T) {
	e := newEnv(t)
	e.fastPolicy(50, 50)
	admin := e.loginAs("admin")
	rcv := newReceiver(t)
	admin.newWebhook(rcv.srv.URL, "message.delivered")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	m := admin.send(num, "ISI-SANGAT-RAHASIA")[0]
	admin.waitStatus(m.ID, "delivered", 15*time.Second)
	waitFor(t, 8*time.Second, "webhook terkirim", func() bool { return rcv.count() >= 1 })
	e.sql(`UPDATE devices SET battery_level=80, charging=true`)
	// banyak ID berbeda pada rute yang sama -> tetap SATU seri (kardinalitas)
	for i := 0; i < 8; i++ {
		admin.do("GET", fmt.Sprintf("/api/v1/messages/%s", m.ID[:len(m.ID)-1]+strconv.Itoa(i)), nil, nil)
		time.Sleep(110 * time.Millisecond)
	}
	e.svc.M.Reg.Gather() // paksa pengumpulan; cache DB 5 dtk dilewati dengan menunggu
	time.Sleep(5200 * time.Millisecond)
	fams, raw := scrape(t, e)
	check := func(name string, labels map[string]string, atLeast float64) {
		t.Helper()
		v, ok := metricValue(fams, name, labels)
		if !ok || v < atLeast {
			t.Errorf("%s%v = %v (ada=%v), mau >= %v", name, labels, v, ok, atLeast)
		}
	}
	check("smsc_devices", map[string]string{"status": "online"}, 1)
	check("smsc_sims", map[string]string{"status": "active"}, 1)
	check("smsc_ws_connections", nil, 1)
	check("smsc_ws_connects_total", nil, 1)
	check("smsc_messages_created_total", map[string]string{"source": "dashboard"}, 1)
	check("smsc_message_transitions_total", map[string]string{"from": "sending", "to": "sent"}, 1)
	check("smsc_message_transitions_total", map[string]string{"from": "sent", "to": "delivered"}, 1)
	check("smsc_event_ingest_total", map[string]string{"state": "delivered"}, 1)
	check("smsc_dispatch_assignments_total", nil, 1)
	check("smsc_dispatch_wait_seconds", nil, 1)
	check("smsc_webhook_deliveries_total", map[string]string{"result": "delivered"}, 1)
	check("smsc_ws_messages_total", map[string]string{"type": "sms.event", "dir": "in"}, 1)
	check("smsc_ws_messages_total", map[string]string{"type": "sms.send", "dir": "out"}, 1)
	check("smsc_device_battery_percent", nil, 80)
	check("smsc_device_charging", nil, 1)
	check("smsc_queue_depth", map[string]string{"priority": "5"}, 0)
	if _, ok := metricValue(fams, "go_goroutines", nil); !ok {
		t.Error("metrik runtime Go harus ada")
	}
	// kardinalitas: 1 seri untuk pola rute walau 8 ID berbeda
	routes := 0
	for _, mt := range fams["smsc_http_requests_total"].Metric {
		for _, l := range mt.Label {
			if l.GetName() == "route" && l.GetValue() == "/api/v1/messages/{messageId}" {
				routes++
			}
			if l.GetName() == "route" && regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}`).MatchString(l.GetValue()) {
				t.Errorf("label route memuat UUID mentah: %s", l.GetValue())
			}
		}
	}
	if routes == 0 || routes > 2 { // maks. 2: status 200 dan 404
		t.Errorf("seri untuk pola rute = %d", routes)
	}
	// tanpa data pribadi/isi pesan di seluruh eksposisi
	for _, bad := range []string{"ISI-SANGAT-RAHASIA", "+628123456789", "8123456789", m.ID} {
		if strings.Contains(raw, bad) {
			t.Fatalf("eksposisi metrik memuat data sensitif: %q", bad)
		}
	}
	// seri berlabel terbatas ada sejak awal (alert tidak bergantung pada 'data belum ada')
	if _, ok := metricValue(fams, "smsc_webhook_deliveries_total", map[string]string{"result": "failed"}); !ok {
		t.Error("seri webhook failed harus ada (0) sejak awal")
	}
	if _, ok := metricValue(fams, "smsc_message_transitions_total", map[string]string{"from": "sending", "to": "unknown"}); !ok {
		t.Error("seri transisi unknown harus ada (0) sejak awal")
	}
}

func TestAlertRulesReferenceExistingMetrics(t *testing.T) {
	e := newEnv(t)
	raw, err := os.ReadFile("../../ops/prometheus/alerts.yml")
	if err != nil {
		t.Fatal(err)
	}
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	_ = id
	_, _ = e.svc.RetentionOnce(context.Background())
	m := admin.send(num, "x")[0]
	admin.waitStatus(m.ID, "delivered", 15*time.Second)
	// device dengan baterai agar seri baterai ada
	e.sql(`UPDATE devices SET battery_level=80, charging=true`)
	time.Sleep(5200 * time.Millisecond) // cache DB metrik 5 dtk
	fams, _ := scrape(t, e)
	names := map[string]bool{}
	for _, n := range regexp.MustCompile(`smsc_[a-z0-9_]+`).FindAllString(string(raw), -1) {
		names[n] = true
	}
	if len(names) < 15 {
		t.Fatalf("aturan alert harus mereferensikan banyak metrik (ditemukan %d)", len(names))
	}
	for n := range names {
		base := regexp.MustCompile(`_(bucket|sum|count)$`).ReplaceAllString(n, "")
		if fams[n] == nil && fams[base] == nil {
			t.Errorf("alert merujuk metrik yang TIDAK ada di /metrics: %s", n)
		}
	}
}
