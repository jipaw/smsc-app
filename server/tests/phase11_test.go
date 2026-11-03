package tests

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/jipaw/smsc-app/server/internal/config"
	"github.com/jipaw/smsc-app/server/internal/oidctest"
)

type ssoEnv struct {
	*env
	idp *oidctest.IdP
}

func newSSO(t *testing.T, mods ...func(*config.OIDCConfig)) *ssoEnv {
	t.Helper()
	idp := oidctest.New("smsc-client", "s3cret-klien")
	t.Cleanup(idp.Close)
	e := newEnvOpts(t, func(c *config.Config) {
		c.OIDC = config.OIDCConfig{Issuer: idp.Issuer, ClientID: "smsc-client", ClientSecret: "s3cret-klien", RedirectURL: "http://example.test/api/v1/auth/oidc/callback",
			Scopes: []string{"openid", "email", "profile"}, Name: "Contoh IdP", DefaultRole: "viewer", GroupsClaim: "groups", TrustIdPMFA: true}
		for _, m := range mods {
			m(&c.OIDC)
		}
	})
	return &ssoEnv{env: e, idp: idp}
}

// browser: klien dengan cookie jar yang TIDAK mengikuti redirect (agar Location terbaca).
func (e *env) browser() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, http: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *client) raw(method, path string) *http.Response {
	req, _ := http.NewRequest(method, c.e.srv.URL+path, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func claimsFor(sub, email string, extra map[string]any) map[string]any {
	m := map[string]any{"sub": sub, "email": email, "email_verified": true, "name": "Orang Uji"}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// login menjalankan alur penuh: /login → (IdP menyetujui) → /callback. Mengembalikan Location akhir.
func (s *ssoEnv) login(c *client, claims map[string]any, next string) string {
	s.t.Helper()
	path := "/api/v1/auth/oidc/login"
	if next != "" {
		path += "?next=" + url.QueryEscape(next)
	}
	r := c.raw("GET", path)
	if r.StatusCode != 302 {
		s.t.Fatalf("login mulai: %d", r.StatusCode)
	}
	loc := r.Header.Get("Location")
	code, q, err := s.idp.Authorize(loc, claims)
	if err != nil {
		s.t.Fatalf("authorize: %v (Location=%s)", err, loc)
	}
	cb := c.raw("GET", "/api/v1/auth/oidc/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(q.Get("state")))
	if cb.StatusCode != 302 {
		s.t.Fatalf("callback: %d", cb.StatusCode)
	}
	return cb.Header.Get("Location")
}

func (c *client) sessionEmail() string {
	var me struct{ Email string }
	if st := c.do("GET", "/api/v1/auth/me", nil, &me); st != 200 {
		return ""
	}
	return me.Email
}

func ssoErr(loc string) string {
	if i := strings.Index(loc, "sso_error="); i >= 0 {
		return loc[i+len("sso_error="):]
	}
	return ""
}

func TestSSODisabledByDefault(t *testing.T) {
	e := newEnv(t)
	var m struct {
		OIDC struct {
			Enabled bool `json:"enabled"`
		} `json:"oidc"`
	}
	e.freshClient().do("GET", "/api/v1/auth/methods", nil, &m)
	if m.OIDC.Enabled {
		t.Fatal("SSO harus mati tanpa konfigurasi")
	}
	if st := e.browser().raw("GET", "/api/v1/auth/oidc/login").StatusCode; st != 404 {
		t.Fatalf("login SSO tanpa konfigurasi: %d", st)
	}
	adm := e.newUser("admin")
	var v struct {
		Enabled bool `json:"enabled"`
	}
	if st := adm.c.do("GET", "/api/v1/settings/sso", nil, &v); st != 200 || v.Enabled {
		t.Fatalf("settings: %d %+v", st, v)
	}
	if st := adm.c.do("POST", "/api/v1/auth/oidc/link", map[string]any{}, nil); st != 404 {
		t.Fatalf("link tanpa konfigurasi: %d", st)
	}
}

func TestSSOProvisionAndLoginHappyPath(t *testing.T) {
	s := newSSO(t, func(o *config.OIDCConfig) { o.AutoProvision = true })
	b := s.browser()
	loc := s.login(b, claimsFor("sub-1", "Baru@Contoh.ID", nil), "/campaigns")
	if loc != "/campaigns" {
		t.Fatalf("redirect akhir: %q", loc)
	}
	if !s.idp.LastVerifierOK || s.idp.LastMethod != "S256" {
		t.Fatalf("PKCE S256 harus dipakai & diverifikasi IdP: ok=%v method=%q", s.idp.LastVerifierOK, s.idp.LastMethod)
	}
	var me struct {
		Email      string
		Role       string
		HasPW      bool `json:"has_password"`
		Linked     bool `json:"sso_linked"`
		SSOSession bool `json:"sso_session"`
	}
	if st := b.do("GET", "/api/v1/auth/me", nil, &me); st != 200 || me.Email != "baru@contoh.id" || me.Role != "viewer" || me.HasPW || !me.Linked || !me.SSOSession {
		t.Fatalf("sesi SSO: %d %+v", st, me)
	}
	// login kedua memakai akun yang sama (identitas = issuer+sub, bukan email)
	b2 := s.browser()
	if loc := s.login(b2, claimsFor("sub-1", "email-berubah@contoh.id", nil), ""); loc != "/" || b2.sessionEmail() != "baru@contoh.id" {
		t.Fatalf("login ulang: %q %q", loc, b2.sessionEmail())
	}
	var n int
	_ = s.pool.QueryRow(context.Background(), `SELECT count(*) FROM users WHERE oidc_subject='sub-1'`).Scan(&n)
	if n != 1 {
		t.Fatalf("tidak boleh membuat pengguna ganda: %d", n)
	}
	_ = s.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='auth.oidc_provision'`).Scan(&n)
	var logins int
	_ = s.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='auth.login' AND data->>'method'='oidc'`).Scan(&logins)
	if n != 1 || logins != 2 {
		t.Fatalf("audit: provision=%d login=%d", n, logins)
	}
	// login lokal tidak mungkin untuk akun SSO-saja
	if st := s.freshClient().do("POST", "/api/v1/auth/login", map[string]string{"email": "baru@contoh.id", "password": "apa-saja-12345678"}, nil); st != 401 {
		t.Fatalf("akun SSO-saja tidak punya password: %d", st)
	}
	// email_verified sebagai string "true" diterima
	s.idp.Mutate = func(c map[string]any) { c["email_verified"] = "true" }
	if loc := s.login(s.browser(), claimsFor("sub-2", "dua@contoh.id", nil), ""); ssoErr(loc) != "" {
		t.Fatalf("email_verified string: %q", loc)
	}
}

func TestSSONotLinkedWhenProvisioningOff(t *testing.T) {
	s := newSSO(t) // AutoProvision = false
	if loc := s.login(s.browser(), claimsFor("sub-x", "x@contoh.id", nil), ""); ssoErr(loc) != "not_linked" {
		t.Fatalf("harus not_linked: %q", loc)
	}
	var n int
	_ = s.pool.QueryRow(context.Background(), `SELECT count(*) FROM users WHERE oidc_subject IS NOT NULL`).Scan(&n)
	if n != 0 {
		t.Fatal("tidak boleh membuat pengguna")
	}
}

func TestSSOOpenRedirectNextSanitized(t *testing.T) {
	s := newSSO(t, func(o *config.OIDCConfig) { o.AutoProvision = true })
	for i, c := range []struct{ next, want string }{
		{"//evil.example/x", "/"}, {"https://evil.example", "/"}, {"/\\evil.example", "/"}, {"javascript:alert(1)", "/"},
		{"/ok/path?x=1", "/ok/path?x=1"}, {"", "/"}, {"/" + strings.Repeat("a", 400), "/"},
	} {
		sub := "redir-" + string(rune('a'+i))
		if loc := s.login(s.browser(), claimsFor(sub, sub+"@contoh.id", nil), c.next); loc != c.want {
			t.Errorf("next=%q → %q (mau %q)", c.next, loc, c.want)
		}
	}
}

func TestSSOStateBinderAndReplayProtection(t *testing.T) {
	s := newSSO(t, func(o *config.OIDCConfig) { o.AutoProvision = true })
	start := func(c *client) (state, code string) {
		r := c.raw("GET", "/api/v1/auth/oidc/login")
		cd, q, err := s.idp.Authorize(r.Header.Get("Location"), claimsFor("sub-r", "r@contoh.id", nil))
		if err != nil {
			t.Fatal(err)
		}
		return q.Get("state"), cd
	}
	cb := func(c *client, state, code string) string {
		return c.raw("GET", "/api/v1/auth/oidc/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(state)).Header.Get("Location")
	}
	// state ngawur / kosong
	if ssoErr(cb(s.browser(), "ngawur", "x")) != "invalid_state" || ssoErr(cb(s.browser(), "", "x")) != "invalid_state" {
		t.Fatal("state tak dikenal harus invalid_state")
	}
	// login-CSRF: URL callback korban dipakai di peramban LAIN (tanpa cookie binder) → ditolak
	victim, attacker := s.browser(), s.browser()
	state, code := start(attacker)
	if loc := cb(victim, state, code); ssoErr(loc) != "invalid_state" {
		t.Fatalf("tanpa binder: %q", loc)
	}
	if attacker.sessionEmail() != "" || victim.sessionEmail() != "" {
		t.Fatal("tidak boleh ada sesi")
	}
	// state sudah habis dipakai (percobaan di atas MENGHABISKAN alur) → pemilik asli pun tidak bisa memakainya lagi
	if loc := cb(attacker, state, code); ssoErr(loc) != "invalid_state" {
		t.Fatalf("alur sekali pakai: %q", loc)
	}
	// replay setelah sukses
	b := s.browser()
	state, code = start(b)
	if loc := cb(b, state, code); ssoErr(loc) != "" {
		t.Fatalf("alur normal: %q", loc)
	}
	if loc := cb(b, state, code); ssoErr(loc) != "invalid_state" {
		t.Fatalf("replay callback: %q", loc)
	}
	// kedaluwarsa
	b2 := s.browser()
	state, code = start(b2)
	s.sql(`UPDATE oidc_flows SET expires_at = now() - interval '1 second'`)
	if loc := cb(b2, state, code); ssoErr(loc) != "invalid_state" {
		t.Fatalf("kedaluwarsa: %q", loc)
	}
	// IdP menolak (user klik batal)
	b3 := s.browser()
	r := b3.raw("GET", "/api/v1/auth/oidc/login")
	_, q, _ := s.idp.Authorize(r.Header.Get("Location"), claimsFor("sub-r", "r@contoh.id", nil))
	if loc := b3.raw("GET", "/api/v1/auth/oidc/callback?error=access_denied&state="+url.QueryEscape(q.Get("state"))).Header.Get("Location"); ssoErr(loc) != "denied" {
		t.Fatalf("error dari IdP: %q", loc)
	}
	// code yang sudah dipakai di IdP tidak bisa dipakai lagi pada alur baru
	b4 := s.browser()
	r = b4.raw("GET", "/api/v1/auth/oidc/login")
	_, q, _ = s.idp.Authorize(r.Header.Get("Location"), claimsFor("sub-r", "r@contoh.id", nil))
	if loc := b4.raw("GET", "/api/v1/auth/oidc/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(q.Get("state"))).Header.Get("Location"); ssoErr(loc) != "token" {
		t.Fatalf("code lama: %q", loc)
	}
}

func TestSSORejectsInvalidIDTokens(t *testing.T) {
	s := newSSO(t, func(o *config.OIDCConfig) { o.AutoProvision = true })
	past := int64(1_000_000_000)
	for name, mut := range map[string]func(c map[string]any){
		"audience lain": func(c map[string]any) { c["aud"] = "klien-lain" },
		"penerbit lain": func(c map[string]any) { c["iss"] = "https://evil.example" },
		"kedaluwarsa":   func(c map[string]any) { c["exp"] = past },
		"nonce berbeda": func(c map[string]any) { c["nonce"] = "nonce-lain" },
		"tanpa nonce":   func(c map[string]any) { delete(c, "nonce") },
		"tanpa sub":     func(c map[string]any) { delete(c, "sub") },
	} {
		s.idp.Mutate = mut
		if loc := s.login(s.browser(), claimsFor("sub-t", "t@contoh.id", nil), ""); ssoErr(loc) != "token" {
			t.Errorf("%s: harus sso_error=token, dapat %q", name, loc)
		}
	}
	s.idp.Mutate = nil
	s.idp.SignWithOther = true
	if loc := s.login(s.browser(), claimsFor("sub-t", "t@contoh.id", nil), ""); ssoErr(loc) != "token" {
		t.Errorf("tanda tangan kunci asing: %q", loc)
	}
	s.idp.SignWithOther = false
	s.idp.NoIDToken = true
	if loc := s.login(s.browser(), claimsFor("sub-t", "t@contoh.id", nil), ""); ssoErr(loc) != "token" {
		t.Errorf("tanpa id_token: %q", loc)
	}
	s.idp.NoIDToken = false
	s.idp.TokenStatus = 500
	if loc := s.login(s.browser(), claimsFor("sub-t", "t@contoh.id", nil), ""); ssoErr(loc) != "token" {
		t.Errorf("endpoint token error: %q", loc)
	}
	s.idp.TokenStatus = 0
	var n int
	_ = s.pool.QueryRow(context.Background(), `SELECT count(*) FROM users WHERE oidc_subject IS NOT NULL`).Scan(&n)
	if n != 0 {
		t.Fatalf("tak ada pengguna boleh dibuat dari token invalid: %d", n)
	}
	// normal kembali berfungsi
	if loc := s.login(s.browser(), claimsFor("sub-t", "t@contoh.id", nil), ""); ssoErr(loc) != "" {
		t.Fatalf("setelah pulih: %q", loc)
	}
}

func TestSSOEmailAndDomainRules(t *testing.T) {
	s := newSSO(t, func(o *config.OIDCConfig) { o.AutoProvision = true; o.AllowedDomains = []string{"contoh.id"} })
	if loc := s.login(s.browser(), claimsFor("d1", "x@lain.example", nil), ""); ssoErr(loc) != "domain" {
		t.Fatalf("domain: %q", loc)
	}
	if loc := s.login(s.browser(), claimsFor("d2", "y@contoh.id", map[string]any{"email_verified": false}), ""); ssoErr(loc) != "unverified_email" {
		t.Fatalf("email belum terverifikasi: %q", loc)
	}
	noEmail := claimsFor("d3", "", nil)
	delete(noEmail, "email")
	if loc := s.login(s.browser(), noEmail, ""); ssoErr(loc) != "unverified_email" {
		t.Fatalf("tanpa email: %q", loc)
	}
	// email milik pengguna lokal TIDAK otomatis diambil alih (pencegahan pengambilalihan akun lewat IdP)
	if _, err := s.svc.CreateUser(context.Background(), "lokal@contoh.id", pw0, "operator"); err != nil {
		t.Fatal(err)
	}
	if loc := s.login(s.browser(), claimsFor("d4", "lokal@contoh.id", nil), ""); ssoErr(loc) != "not_linked" {
		t.Fatalf("email lokal: %q", loc)
	}
}

func TestSSOLinkByEmailOptIn(t *testing.T) {
	s := newSSO(t, func(o *config.OIDCConfig) { o.LinkByEmail = true })
	local := s.newUser("operator")
	if loc := s.login(s.browser(), claimsFor("l1", local.email, map[string]any{"email_verified": false}), ""); ssoErr(loc) != "unverified_email" {
		t.Fatalf("email tak terverifikasi tidak boleh menautkan: %q", loc)
	}
	b := s.browser()
	if loc := s.login(b, claimsFor("l1", local.email, nil), ""); ssoErr(loc) != "" || b.sessionEmail() != local.email {
		t.Fatalf("penautan otomatis: %q %q", loc, b.sessionEmail())
	}
	// sub lain dengan email yang sama tidak boleh menimpa tautan yang sudah ada
	if loc := s.login(s.browser(), claimsFor("l2-penyerang", local.email, nil), ""); ssoErr(loc) != "not_linked" {
		t.Fatalf("akun sudah tertaut ke sub lain: %q", loc)
	}
}

func TestSSOLinkAndUnlinkFromAccount(t *testing.T) {
	s := newSSO(t)
	u := s.newUser("operator")
	b := s.browser()
	// login lokal di klien browser (tanpa redirect)
	var out struct {
		CSRF string `json:"csrf_token"`
	}
	b.do("POST", "/api/v1/auth/login", map[string]string{"email": u.email, "password": pw0}, &out)
	b.csrf = out.CSRF

	link := func(c *client, claims map[string]any) string {
		var l struct{ URL string }
		if st := c.do("POST", "/api/v1/auth/oidc/link", map[string]any{}, &l); st != 200 {
			t.Fatalf("link begin: %d", st)
		}
		code, q, err := s.idp.Authorize(l.URL, claims)
		if err != nil {
			t.Fatal(err)
		}
		return c.raw("GET", "/api/v1/auth/oidc/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(q.Get("state"))).Header.Get("Location")
	}
	if loc := link(b, claimsFor("tautan-1", "terserah@contoh.id", nil)); loc != "/account?sso=linked" {
		t.Fatalf("link: %q", loc)
	}
	// menautkan lagi ke akun yang sudah tertaut → konflik
	if loc := link(b, claimsFor("tautan-2", "x@contoh.id", nil)); !strings.Contains(loc, "sso_error=link_conflict") {
		t.Fatalf("link ganda: %q", loc)
	}
	// identitas yang sama ke akun lain → konflik
	other := s.newUser("viewer")
	ob := s.browser()
	var oo struct {
		CSRF string `json:"csrf_token"`
	}
	ob.do("POST", "/api/v1/auth/login", map[string]string{"email": other.email, "password": pw0}, &oo)
	ob.csrf = oo.CSRF
	if loc := link(ob, claimsFor("tautan-1", "x@contoh.id", nil)); !strings.Contains(loc, "sso_error=link_conflict") {
		t.Fatalf("identitas sudah dipakai akun lain: %q", loc)
	}
	// login SSO sekarang masuk sebagai u
	sb := s.browser()
	if loc := s.login(sb, claimsFor("tautan-1", "terserah@contoh.id", nil), ""); ssoErr(loc) != "" || sb.sessionEmail() != u.email {
		t.Fatalf("login via tautan: %q %q", loc, sb.sessionEmail())
	}
	// callback link dengan sesi PENGGUNA LAIN ditolak
	var l struct{ URL string }
	b.do("POST", "/api/v1/auth/oidc/link", map[string]any{}, &l) // u sudah tertaut, tetapi alur tetap dibuat; yang diuji: pemilik sesi
	code, q, _ := s.idp.Authorize(l.URL, claimsFor("tautan-9", "x@contoh.id", nil))
	// peramban lain dengan binder yang sama tapi sesi pengguna lain: salin cookie binder ke ob
	su, _ := url.Parse(s.srv.URL + "/api/v1/auth/oidc/callback")
	for _, ck := range b.http.Jar.Cookies(su) {
		if ck.Name == "smsc_oidc" {
			ob.http.Jar.SetCookies(su, []*http.Cookie{ck})
		}
	}
	if loc := ob.raw("GET", "/api/v1/auth/oidc/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(q.Get("state"))).Header.Get("Location"); !strings.Contains(loc, "sso_error=session_mismatch") {
		t.Fatalf("sesi bukan pemilik alur: %q", loc)
	}
	// lepas tautan
	if st := b.do("DELETE", "/api/v1/auth/oidc/link", nil, nil); st != 204 {
		t.Fatalf("unlink: %d", st)
	}
	if st, ty := problemType(b, "DELETE", "/api/v1/auth/oidc/link", nil); st != 409 || ty != "sso.not_linked" {
		t.Fatalf("unlink ulang: %d %s", st, ty)
	}
	if loc := s.login(s.browser(), claimsFor("tautan-1", "terserah@contoh.id", nil), ""); ssoErr(loc) != "not_linked" {
		t.Fatalf("setelah unlink: %q", loc)
	}
}

func TestSSOOnlyAccountCannotUnlinkAndAdminCanSetPassword(t *testing.T) {
	s := newSSO(t, func(o *config.OIDCConfig) { o.AutoProvision = true })
	adm := s.newUser("admin")
	b := s.browser()
	s.login(b, claimsFor("only-1", "only@contoh.id", nil), "")
	var me struct {
		CSRF string `json:"csrf_token"`
		ID   string
	}
	b.do("GET", "/api/v1/auth/me", nil, &me)
	b.csrf = me.CSRF
	if st, ty := problemType(b, "DELETE", "/api/v1/auth/oidc/link", nil); st != 409 || ty != "sso.unlink_blocked" {
		t.Fatalf("akun tanpa password tidak boleh melepas SSO: %d %s", st, ty)
	}
	var list struct {
		Items []struct {
			Email  string
			HasPW  bool `json:"has_password"`
			Linked bool `json:"sso_linked"`
		}
	}
	adm.c.do("GET", "/api/v1/users", nil, &list)
	found := false
	for _, it := range list.Items {
		if it.Email == "only@contoh.id" {
			found = !it.HasPW && it.Linked
		}
	}
	if !found {
		t.Fatalf("daftar pengguna harus menampilkan SSO-saja: %+v", list.Items)
	}
	if st := adm.c.do("POST", "/api/v1/users/"+me.ID+"/password", map[string]string{"new_password": "kuda-biru-melompati-pagar-9"}, nil); st != 204 {
		t.Fatalf("admin set password: %d", st)
	}
	b2 := s.browser()
	var o struct {
		CSRF string `json:"csrf_token"`
	}
	if st := b2.do("POST", "/api/v1/auth/login", map[string]string{"email": "only@contoh.id", "password": "kuda-biru-melompati-pagar-9"}, &o); st != 200 {
		t.Fatalf("login lokal setelah diberi password: %d", st)
	}
	b2.csrf = o.CSRF
	if st := b2.do("DELETE", "/api/v1/auth/oidc/link", nil, nil); st != 204 {
		t.Fatalf("kini boleh melepas: %d", st)
	}
}

func TestSSORoleMappingProvisionAndSync(t *testing.T) {
	s := newSSO(t, func(o *config.OIDCConfig) {
		o.AutoProvision = true
		o.AdminGroups = []string{"smsc-admin"}
		o.OperatorGroups = []string{"smsc-ops"}
		o.SyncRoles = true
	})
	role := func(email string) string {
		var r string
		_ = s.pool.QueryRow(context.Background(), `SELECT role::text FROM users WHERE email=$1`, email).Scan(&r)
		return r
	}
	s.login(s.browser(), claimsFor("g1", "adm@contoh.id", map[string]any{"groups": []string{"x", "smsc-admin"}}), "")
	s.login(s.browser(), claimsFor("g2", "ops@contoh.id", map[string]any{"groups": []string{"smsc-ops"}}), "")
	s.login(s.browser(), claimsFor("g3", "biasa@contoh.id", map[string]any{"groups": []string{"lain"}}), "")
	s.login(s.browser(), claimsFor("g4", "str@contoh.id", map[string]any{"groups": "smsc-ops, lain"}), "") // klaim berupa string
	if role("adm@contoh.id") != "admin" || role("ops@contoh.id") != "operator" || role("biasa@contoh.id") != "viewer" || role("str@contoh.id") != "operator" {
		t.Fatalf("peran: %s %s %s %s", role("adm@contoh.id"), role("ops@contoh.id"), role("biasa@contoh.id"), role("str@contoh.id"))
	}
	// sinkronisasi: dicabut dari grup admin → turun peran pada login berikutnya (dan diaudit)
	s.login(s.browser(), claimsFor("g1", "adm@contoh.id", map[string]any{"groups": []string{}}), "")
	if role("adm@contoh.id") != "viewer" {
		t.Fatalf("sinkron peran turun: %s", role("adm@contoh.id"))
	}
	var n int
	_ = s.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='user.update' AND data->>'via'='oidc_sync'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit sinkron: %d", n)
	}
}

func TestSSODisabledUserDeniedAndExistingSessionsUnaffectedByIdP(t *testing.T) {
	s := newSSO(t, func(o *config.OIDCConfig) { o.AutoProvision = true })
	adm := s.newUser("admin")
	b := s.browser()
	s.login(b, claimsFor("dis-1", "dis@contoh.id", nil), "")
	var id string
	_ = s.pool.QueryRow(context.Background(), `SELECT id::text FROM users WHERE email='dis@contoh.id'`).Scan(&id)
	if st := adm.c.do("PATCH", "/api/v1/users/"+id, map[string]any{"disabled": true}, nil); st != 200 {
		t.Fatal(st)
	}
	if b.sessionEmail() != "" {
		t.Fatal("sesi harus dicabut saat dinonaktifkan")
	}
	if loc := s.login(s.browser(), claimsFor("dis-1", "dis@contoh.id", nil), ""); ssoErr(loc) != "disabled" {
		t.Fatalf("nonaktif: %q", loc)
	}
}

func TestSSOSatisfies2FAPolicyWhenTrusted(t *testing.T) {
	for _, trust := range []bool{true, false} {
		s := newSSO(t, func(o *config.OIDCConfig) {
			o.AutoProvision = true
			o.AdminGroups = []string{"adm"}
			o.TrustIdPMFA = trust
			o.LinkByEmail = true
		})
		a := s.newUser("admin")
		s.enableMFA(a)
		a.c.do("PUT", "/api/v1/settings/security", map[string]string{"require_2fa": "admin"}, nil)
		// admin SSO-saja (tanpa password): tidak mungkin menyiapkan 2FA lokal → tidak digerbang, apa pun TRUST
		b := s.browser()
		s.login(b, claimsFor("p1", "ssoadmin@contoh.id", map[string]any{"groups": []string{"adm"}}), "")
		if st := b.do("GET", "/api/v1/devices", nil, nil); st != 200 {
			t.Fatalf("trust=%v: admin SSO-saja tidak boleh digerbang: %d", trust, st)
		}
		// admin lokal (punya password) yang tertaut via email, TANPA 2FA lokal
		local := s.newUser("admin")
		lb := s.browser()
		s.login(lb, claimsFor("p2", local.email, nil), "")
		st := lb.do("GET", "/api/v1/devices", nil, nil)
		if trust && st != 200 {
			t.Fatalf("trust=true: sesi SSO memenuhi 2FA: %d", st)
		}
		if !trust && st != 403 {
			t.Fatalf("trust=false: harus digerbang hingga 2FA lokal aktif: %d", st)
		}
		// login password biasa untuk akun yang sama tetap digerbang (SSO tidak melonggarkan jalur lain)
		pc := s.freshClient()
		var o struct {
			CSRF string `json:"csrf_token"`
		}
		pc.do("POST", "/api/v1/auth/login", map[string]string{"email": local.email, "password": pw0}, &o)
		pc.csrf = o.CSRF
		if st := pc.do("GET", "/api/v1/devices", nil, nil); st != 403 {
			t.Fatalf("login password tetap digerbang: %d", st)
		}
	}
}

func TestSSOSettingsViewNoSecretAndDiscoveryProbe(t *testing.T) {
	s := newSSO(t)
	adm := s.newUser("admin")
	op := s.newUser("operator")
	var raw map[string]any
	if st := adm.c.do("GET", "/api/v1/settings/sso?probe=1", nil, &raw); st != 200 || raw["enabled"] != true || raw["reachable"] != true || raw["client_id"] != "smsc-client" {
		t.Fatalf("settings: %d %v", st, raw)
	}
	b, _ := json.Marshal(raw)
	if strings.Contains(string(b), "s3cret-klien") {
		t.Fatal("client secret tidak boleh bocor")
	}
	if st := op.c.do("GET", "/api/v1/settings/sso", nil, nil); st != 403 {
		t.Fatalf("operator: %d", st)
	}
	var m struct {
		OIDC struct {
			Enabled bool
			Name    string
		} `json:"oidc"`
	}
	s.freshClient().do("GET", "/api/v1/auth/methods", nil, &m)
	if !m.OIDC.Enabled || m.OIDC.Name != "Contoh IdP" {
		t.Fatalf("methods: %+v", m)
	}
}

func TestSSOIdPDownReportsUnavailable(t *testing.T) {
	s := newSSO(t, func(o *config.OIDCConfig) { o.AutoProvision = true })
	adm := s.newUser("admin")
	s.idp.Close() // sebelum discovery pertama
	var v struct {
		Reachable *bool  `json:"reachable"`
		Err       string `json:"reach_error"`
	}
	if st := adm.c.do("GET", "/api/v1/settings/sso?probe=1", nil, &v); st != 200 || v.Reachable == nil || *v.Reachable || v.Err == "" {
		t.Fatalf("probe: %d %+v", st, v)
	}
	r := s.browser().raw("GET", "/api/v1/auth/oidc/login")
	if r.StatusCode != 302 || ssoErr(r.Header.Get("Location")) != "unavailable" {
		t.Fatalf("IdP mati: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	// pesan kesalahan internal tidak bocor ke peramban
	if strings.Contains(r.Header.Get("Location"), "dial") || strings.Contains(r.Header.Get("Location"), "127.0.0.1") {
		t.Fatal("detail jaringan bocor ke URL")
	}
}
