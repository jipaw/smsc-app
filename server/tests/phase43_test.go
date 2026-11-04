package tests

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"testing"

	"github.com/jipaw/smsc-app/server/internal/config"
)

func cidrs(t *testing.T, vs ...string) []*net.IPNet {
	var out []*net.IPNet
	for _, v := range vs {
		_, n, err := net.ParseCIDR(v)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func anonClient(e *env) *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, http: &http.Client{Jar: jar}}
}

func loginH(c *client, email, pw string, hdr map[string]string) int {
	st, _ := c.doH("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": pw}, nil, hdr)
	return st
}

// di belakang proxy tepercaya (loopback), rate limit login dihitung per klien asli (X-Forwarded-For), bukan global.
func TestLoginRateLimitPerForwardedClient(t *testing.T) {
	e := newEnvOpts(t, func(c *config.Config) {
		c.LoginRatePerMin, c.LoginBurst = 1, 3
		c.TrustedProxies = cidrs(t, "127.0.0.0/8", "::1/128")
	})
	c := anonClient(e)
	a := map[string]string{"X-Forwarded-For": "100.64.0.1"}
	for i := 0; i < 3; i++ {
		if st := loginH(c, "tak@ada.id", "salah-salah-123", a); st != 401 {
			t.Fatalf("percobaan %d: %d", i, st)
		}
	}
	if st := loginH(c, "tak@ada.id", "salah-salah-123", a); st != 429 {
		t.Fatalf("A harus 429: %d", st)
	}
	// klien lain lewat proxy yang sama tidak terdampak
	if st := loginH(c, "tak@ada.id", "salah-salah-123", map[string]string{"X-Forwarded-For": "100.64.0.2"}); st != 401 {
		t.Fatalf("B tidak boleh kena limit A: %d", st)
	}
	// nilai paling kiri dikendalikan klien: hop paling kanan yang tak tepercaya yang dipakai
	if st := loginH(c, "tak@ada.id", "salah-salah-123", map[string]string{"X-Forwarded-For": "1.2.3.4, 100.64.0.1"}); st != 429 {
		t.Fatalf("pemalsuan hop kiri harus tetap 429: %d", st)
	}
	// hop proxy tepercaya di kanan dilewati
	if st := loginH(c, "tak@ada.id", "salah-salah-123", map[string]string{"X-Forwarded-For": "100.64.0.1, 127.0.0.1"}); st != 429 {
		t.Fatalf("hop tepercaya harus dilewati: %d", st)
	}
}

// dari alamat yang bukan proxy tepercaya, X-Forwarded-For dan header Tailscale diabaikan (tidak bisa dipakai untuk mengelak limit).
func TestForwardedHeadersIgnoredFromUntrustedRemote(t *testing.T) {
	e := newEnvOpts(t, func(c *config.Config) {
		c.LoginRatePerMin, c.LoginBurst = 1, 2
		c.TrustTailscale = true
		c.TrustedProxies = cidrs(t, "10.99.0.0/16")
	})
	c := anonClient(e)
	for i := 0; i < 2; i++ {
		hdr := map[string]string{"X-Forwarded-For": fmt.Sprintf("100.64.1.%d", i), "Tailscale-User-Login": fmt.Sprintf("u%d@ts", i)}
		if st := loginH(c, "tak@ada.id", "salah-salah-123", hdr); st != 401 {
			t.Fatalf("percobaan %d: %d", i, st)
		}
	}
	if st := loginH(c, "tak@ada.id", "salah-salah-123", map[string]string{"X-Forwarded-For": "100.64.1.9", "Tailscale-User-Login": "lain@ts"}); st != 429 {
		t.Fatalf("header palsu dari klien tak tepercaya harus diabaikan: %d", st)
	}
}

// TRUST_TAILSCALE_HEADERS=true: identitas klien = login Tailscale (lebih stabil daripada IP), hanya dari proxy tepercaya.
func TestTailscaleLoginKeysRateLimit(t *testing.T) {
	e := newEnvOpts(t, func(c *config.Config) {
		c.LoginRatePerMin, c.LoginBurst = 1, 2
		c.TrustTailscale = true
		c.TrustedProxies = cidrs(t, "127.0.0.0/8", "::1/128")
	})
	c := anonClient(e)
	alice := func(ip string) map[string]string {
		return map[string]string{"Tailscale-User-Login": "Alice@Example.com", "X-Forwarded-For": ip}
	}
	loginH(c, "tak@ada.id", "x-salah-12345", alice("100.64.2.1"))
	loginH(c, "tak@ada.id", "x-salah-12345", alice("100.64.2.2"))
	// IP berbeda, login Tailscale sama (beda huruf besar) → tetap satu kunci
	if st := loginH(c, "tak@ada.id", "x-salah-12345", map[string]string{"Tailscale-User-Login": "alice@example.com", "X-Forwarded-For": "100.64.2.3"}); st != 429 {
		t.Fatalf("login Tailscale sama harus berbagi limit: %d", st)
	}
	if st := loginH(c, "tak@ada.id", "x-salah-12345", map[string]string{"Tailscale-User-Login": "bob@example.com"}); st != 401 {
		t.Fatalf("pengguna Tailscale lain tidak terdampak: %d", st)
	}
}

// penguncian per (akun, sumber): penyerang dari satu sumber tidak dapat mengunci pemilik akun di sumber lain; jaring pengaman global tetap ada.
func TestLockoutPerSourceAndGlobalBackstop(t *testing.T) {
	e := newEnvOpts(t, func(c *config.Config) { c.TrustedProxies = cidrs(t, "127.0.0.0/8", "::1/128") })
	email := "korban@contoh.id"
	if _, err := e.svc.CreateUser(context.Background(), email, "password-panjang-123", "admin"); err != nil {
		t.Fatal(err)
	}
	att := map[string]string{"X-Forwarded-For": "100.64.9.9"}
	c := anonClient(e)
	for i := 0; i < 5; i++ {
		if st := loginH(c, email, "tebakan-salah-1", att); st != 401 {
			t.Fatalf("gagal %d: %d", i, st)
		}
	}
	if st := loginH(c, email, "password-panjang-123", att); st != 423 {
		t.Fatalf("sumber penyerang harus terkunci walau password benar: %d", st)
	}
	if st := loginH(anonClient(e), email, "password-panjang-123", map[string]string{"X-Forwarded-For": "100.64.0.7"}); st != 200 {
		t.Fatalf("pemilik dari sumber lain harus tetap bisa login: %d", st)
	}
	// login sukses tidak membuka kunci sumber penyerang
	if st := loginH(c, email, "password-panjang-123", att); st != 423 {
		t.Fatalf("sumber penyerang tetap terkunci: %d", st)
	}
	// reset password lewat CLI membuka semua kunci per sumber
	if _, err := e.svc.SetPasswordByEmail(context.Background(), email, "password-panjang-456"); err != nil {
		t.Fatal(err)
	}
	if st := loginH(c, email, "password-panjang-456", att); st != 200 {
		t.Fatalf("setelah reset harus bisa login: %d", st)
	}
	// serangan terdistribusi: 4 kegagalan dari tiap sumber (di bawah ambang per sumber) → ambang global 50 mengunci akun
	n := 0
	for s := 0; n < 50; s++ {
		for i := 0; i < 4 && n < 50; i++ {
			loginH(c, email, "tebakan-salah-1", map[string]string{"X-Forwarded-For": fmt.Sprintf("100.65.%d.%d", s/250, s%250+1)})
			n++
		}
	}
	if st := loginH(anonClient(e), email, "password-panjang-456", map[string]string{"X-Forwarded-For": "100.64.0.8"}); st != 423 {
		t.Fatalf("jaring pengaman global harus mengunci: %d", st)
	}
	// retensi membersihkan penghitung per sumber yang basi
	e.sql(`UPDATE login_failures SET updated_at = now() - interval '2 days'`)
	if _, err := e.svc.RetentionOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var left int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM login_failures`).Scan(&left)
	if left != 0 {
		t.Fatalf("login_failures basi tersisa %d", left)
	}
}

// ALLOWED_DESTINATION_PREFIXES: tujuan di luar awalan ditolak pada kirim cepat dan impor campaign; materialisasi menghormati allow-list yang diperketat.
func TestDestinationAllowList(t *testing.T) {
	e := newEnvOpts(t, func(c *config.Config) { c.AllowedDestPrefixes = []string{"+62"} })
	admin := e.loginAs("admin")
	var out struct {
		Messages []struct{ ID string } `json:"messages"`
		Rejected []struct {
			Index  int    `json:"index"`
			To     string `json:"to"`
			Reason string `json:"reason"`
		} `json:"rejected"`
	}
	req := map[string]any{"recipients": []map[string]any{{"to": "+6281234567890"}, {"to": "+14155552671"}}, "body": "uji"}
	if st := admin.do("POST", "/api/v1/messages", req, &out); st != 202 || len(out.Messages) != 1 || len(out.Rejected) != 1 ||
		out.Rejected[0].Index != 1 || out.Rejected[0].Reason != "destination_not_allowed" || out.Rejected[0].To != "+14155552671" {
		t.Fatalf("kirim: %d %+v", st, out)
	}
	cm := admin.newCampaign(map[string]any{"name": "allow", "body": "halo"})
	imp := admin.importJSON(cm.ID, []map[string]any{{"to": "081234567891"}, {"to": "+6591234567"}})
	if imp.Accepted != 1 || len(imp.Invalid) != 1 || imp.Invalid[0].Reason != "destination_not_allowed" {
		t.Fatalf("impor: %+v", imp)
	}
}

// AUDIT_RETENTION_DAYS > 0 menghapus audit lama; 0 menyimpan selamanya. Outbox email selesai > 30 hari dihapus, yang tertunda tidak.
func TestAuditAndEmailOutboxRetention(t *testing.T) {
	count := func(e *env, q string) (n int) {
		_ = e.pool.QueryRow(context.Background(), q).Scan(&n)
		return
	}
	seed := func(e *env) {
		e.sql(`INSERT INTO audit_logs(id, at, actor, action) VALUES (gen_random_uuid(), now() - interval '400 days', 'uji', 'lama'), (gen_random_uuid(), now(), 'uji', 'baru')`)
	}
	keep := newEnv(t)
	seed(keep)
	if _, err := keep.svc.RetentionOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := count(keep, `SELECT count(*) FROM audit_logs WHERE actor='uji'`); n != 2 {
		t.Fatalf("default 0 harus menyimpan semua audit: %d", n)
	}
	e := newEnvOpts(t, func(c *config.Config) { c.AuditRetentionDays = 365 })
	seed(e)
	e.sql(`INSERT INTO email_outbox(id, kind, to_email, subject, status, created_at, sent_at) VALUES
		(gen_random_uuid(), 'invitation', 'a@x.id', 's', 'sent', now() - interval '40 days', now() - interval '40 days'),
		(gen_random_uuid(), 'invitation', 'b@x.id', 's', 'failed', now() - interval '40 days', NULL),
		(gen_random_uuid(), 'invitation', 'c@x.id', 's', 'sent', now() - interval '2 days', now() - interval '2 days'),
		(gen_random_uuid(), 'invitation', 'd@x.id', 's', 'pending', now() - interval '40 days', NULL)`)
	if _, err := e.svc.RetentionOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := count(e, `SELECT count(*) FROM audit_logs WHERE actor='uji'`); n != 1 {
		t.Fatalf("audit > 365 hari harus terhapus: %d", n)
	}
	if n := count(e, `SELECT count(*) FROM email_outbox`); n != 2 {
		t.Fatalf("outbox tersisa %d (mau 2: baru + pending)", n)
	}
}
