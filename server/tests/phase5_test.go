package tests

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type secState struct {
	Require2FA string `json:"require_2fa"`
	Missing    int    `json:"users_missing_2fa"`
}

func getSec(t *testing.T, c *client) secState {
	t.Helper()
	var s secState
	if st := c.do("GET", "/api/v1/settings/security", nil, &s); st != 200 {
		t.Fatalf("GET security: %d", st)
	}
	return s
}

func TestRequire2FADefaultOffAndPolicyGuards(t *testing.T) {
	e := newEnv(t)
	a := e.newUser("admin")
	if s := getSec(t, a.c); s.Require2FA != "off" || s.Missing != 0 {
		t.Fatalf("default harus off: %+v", s)
	}
	// default nonaktif: admin tanpa 2FA bebas memakai dashboard
	if st := a.c.do("GET", "/api/v1/devices", nil, nil); st != 200 {
		t.Fatalf("default off tidak boleh menggerbang: %d", st)
	}
	// admin tanpa 2FA tidak boleh menyalakan kewajiban (mengunci diri sendiri)
	if st, ty := problemType(a.c, "PUT", "/api/v1/settings/security", map[string]string{"require_2fa": "admin"}); st != 409 || ty != "security.enroll_first" {
		t.Fatalf("harus 409 enroll_first: %d %s", st, ty)
	}
	if s := getSec(t, a.c); s.Require2FA != "off" {
		t.Fatal("penolakan tidak boleh mengubah kebijakan")
	}
	e.enableMFA(a)
	if st := a.c.do("PUT", "/api/v1/settings/security", map[string]string{"require_2fa": "semua"}, nil); st != 422 {
		t.Fatalf("nilai tak dikenal: %d", st)
	}
	op := e.newUser("operator")
	if st := op.c.do("PUT", "/api/v1/settings/security", map[string]string{"require_2fa": "off"}, nil); st != 403 {
		t.Fatalf("operator: %d", st)
	}
	if st := op.c.do("GET", "/api/v1/settings/security", nil, nil); st != 403 {
		t.Fatalf("operator GET: %d", st)
	}
	b := e.newUser("admin") // admin kedua tanpa 2FA
	var s secState
	if st := a.c.do("PUT", "/api/v1/settings/security", map[string]string{"require_2fa": "admin"}, &s); st != 200 || s.Require2FA != "admin" || s.Missing != 1 {
		t.Fatalf("nyalakan: %d %+v", st, s)
	}
	_ = b
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='security.update'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit security.update: %d", n)
	}
}

func TestRequire2FAGatesOnlyInScopeUsersWithout2FA(t *testing.T) {
	e := newEnv(t)
	a := e.newUser("admin")
	e.enableMFA(a)
	b := e.newUser("admin") // belum punya 2FA
	op := e.newUser("operator")
	vw := e.newUser("viewer")
	adm := e.loginAs("admin") // sesi admin lain tanpa 2FA (dibuat sebelum kebijakan)
	var k struct{ Secret string }
	a.c.do("POST", "/api/v1/api-keys", map[string]any{"name": "k", "scopes": []string{"messages:read"}}, &k)

	if st := a.c.do("PUT", "/api/v1/settings/security", map[string]string{"require_2fa": "admin"}, nil); st != 200 {
		t.Fatal(st)
	}
	// admin tanpa 2FA: semua endpoint non-akun diblok, sesi yang SUDAH ada pun
	for _, c := range []*client{b.c, adm} {
		if st, ty := problemType(c, "GET", "/api/v1/devices", nil); st != 403 || ty != "auth.mfa_setup_required" {
			t.Fatalf("admin tanpa 2FA harus digerbang: %d %s", st, ty)
		}
		if st, _ := problemType(c, "GET", "/api/v1/settings/security", nil); st != 403 {
			t.Fatalf("termasuk pengaturan: %d", st)
		}
	}
	var me struct {
		Req bool `json:"mfa_setup_required"`
	}
	if st := b.c.do("GET", "/api/v1/auth/me", nil, &me); st != 200 || !me.Req {
		t.Fatalf("me harus tetap bisa dan memberi tahu: %d %+v", st, me)
	}
	// operator/viewer di luar cakupan "admin" tidak terpengaruh; API key juga tidak
	if st := op.c.do("GET", "/api/v1/devices", nil, nil); st != 200 {
		t.Fatalf("operator: %d", st)
	}
	if st := vw.c.do("GET", "/api/v1/devices", nil, nil); st != 200 {
		t.Fatalf("viewer: %d", st)
	}
	kc := &client{e: e, http: &http.Client{}, key: k.Secret}
	if st := kc.do("GET", "/api/v1/messages?limit=1", nil, nil); st != 200 {
		t.Fatalf("API key tidak boleh terpengaruh: %d", st)
	}
	// login baru admin tanpa 2FA: berhasil tetapi ditandai
	var lg struct {
		Req  bool   `json:"mfa_setup_required"`
		CSRF string `json:"csrf_token"`
	}
	c2 := e.freshClient()
	if st := c2.do("POST", "/api/v1/auth/login", map[string]string{"email": b.email, "password": pw0}, &lg); st != 200 || !lg.Req {
		t.Fatalf("login: %d %+v", st, lg)
	}
	// endpoint akun terbuka: ganti password & penyiapan 2FA; setelah aktif, gerbang terbuka tanpa login ulang
	if st := b.c.do("POST", "/api/v1/auth/password", map[string]string{"current_password": pw0, "new_password": pw0 + "x"}, nil); st != 204 {
		t.Fatalf("ganti password harus boleh: %d", st)
	}
	pwNow := pw0 + "x"
	var setup struct{ Secret string }
	if st := b.c.do("POST", "/api/v1/auth/2fa/setup", map[string]string{"password": pwNow}, &setup); st != 200 {
		t.Fatalf("setup harus boleh: %d", st)
	}
	if st := b.c.do("POST", "/api/v1/auth/2fa/enable", map[string]string{"code": totp(setup.Secret, time.Now())}, nil); st != 200 {
		t.Fatalf("enable harus boleh: %d", st)
	}
	if st := b.c.do("GET", "/api/v1/devices", nil, nil); st != 200 {
		t.Fatalf("setelah 2FA aktif gerbang harus terbuka: %d", st)
	}
	if st := b.c.do("GET", "/api/v1/auth/me", nil, &me); st != 200 || me.Req {
		t.Fatalf("flag harus padam: %+v", me)
	}
	// "all": operator ikut digerbang
	if st := a.c.do("PUT", "/api/v1/settings/security", map[string]string{"require_2fa": "all"}, nil); st != 200 {
		t.Fatal(st)
	}
	if st, ty := problemType(op.c, "GET", "/api/v1/devices", nil); st != 403 || ty != "auth.mfa_setup_required" {
		t.Fatalf("operator di bawah 'all': %d %s", st, ty)
	}
	if st, ty := problemType(vw.c, "GET", "/api/v1/devices", nil); st != 403 || ty != "auth.mfa_setup_required" {
		t.Fatalf("viewer di bawah 'all': %d %s", st, ty)
	}
	// pengguna baru ikut digerbang; kembali ke off membuka semuanya
	nu := e.newUser("viewer")
	if st := nu.c.do("GET", "/api/v1/devices", nil, nil); st != 403 {
		t.Fatalf("pengguna baru: %d", st)
	}
	if st := a.c.do("PUT", "/api/v1/settings/security", map[string]string{"require_2fa": "off"}, nil); st != 200 {
		t.Fatal(st)
	}
	for _, c := range []*client{op.c, vw.c, nu.c, adm} {
		if st := c.do("GET", "/api/v1/devices", nil, nil); st != 200 {
			t.Fatalf("setelah off semua terbuka: %d", st)
		}
	}
}

func TestRequire2FARecoveryPathStillWorks(t *testing.T) {
	e := newEnv(t)
	a := e.newUser("admin")
	e.enableMFA(a)
	b := e.newUser("admin")
	e.enableMFA(b)
	a.c.do("PUT", "/api/v1/settings/security", map[string]string{"require_2fa": "admin"}, nil)
	// admin lain mereset 2FA B: B kembali digerbang (harus menyiapkan ulang) tapi bisa login
	if st := a.c.do("DELETE", "/api/v1/users/"+b.id+"/2fa", nil, nil); st != 204 {
		t.Fatalf("reset: %d", st)
	}
	c := e.loginEmail(b.email, pw0)
	if st := c.do("GET", "/api/v1/devices", nil, nil); st != 403 {
		t.Fatalf("setelah reset B wajib menyiapkan ulang: %d", st)
	}
	// memastikan A (yang punya 2FA) masih leluasa
	if st := a.c.do("GET", "/api/v1/devices", nil, nil); st != 200 {
		t.Fatalf("A: %d", st)
	}
	if s := getSec(t, a.c); s.Missing != 1 {
		t.Fatalf("hitungan: %+v", s)
	}
}

// ================= manajemen pengguna =================

func TestUserManagementRoleAndDisable(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	u := e.newUser("viewer")
	op := e.newUser("operator")

	tpl := map[string]any{"name": "t-" + uuid.NewString()[:4], "body": "Halo"}
	if st := u.c.do("POST", "/api/v1/templates", tpl, nil); st != 403 {
		t.Fatalf("viewer tidak boleh membuat template: %d", st)
	}
	if st := op.c.do("PATCH", "/api/v1/users/"+u.id, map[string]any{"role": "operator"}, nil); st != 403 {
		t.Fatalf("operator tidak boleh mengelola pengguna: %d", st)
	}
	var out struct {
		Role      string `json:"role"`
		Disabled  bool   `json:"disabled"`
		CreatedAt string `json:"created_at"`
	}
	if st := adm.c.do("PATCH", "/api/v1/users/"+u.id, map[string]any{"role": "operator"}, &out); st != 200 || out.Role != "operator" || out.CreatedAt == "" {
		t.Fatalf("ubah peran: %d %+v", st, out)
	}
	if st := u.c.do("POST", "/api/v1/templates", tpl, nil); st != 201 && st != 200 {
		t.Fatalf("peran baru harus berlaku seketika tanpa login ulang: %d", st)
	}
	if st := adm.c.do("PATCH", "/api/v1/users/"+u.id, map[string]any{"role": "viewer"}, &out); st != 200 {
		t.Fatal(st)
	}
	// validasi
	if st := adm.c.do("PATCH", "/api/v1/users/"+u.id, map[string]any{"role": "dewa"}, nil); st != 422 {
		t.Fatalf("peran tak dikenal: %d", st)
	}
	if st := adm.c.do("PATCH", "/api/v1/users/"+u.id, map[string]any{}, nil); st != 422 {
		t.Fatalf("body kosong: %d", st)
	}
	if st := adm.c.do("PATCH", "/api/v1/users/"+uuid.NewString(), map[string]any{"role": "viewer"}, nil); st != 404 {
		t.Fatalf("tak ada: %d", st)
	}
	if st, ty := problemType(adm.c, "PATCH", "/api/v1/users/"+adm.id, map[string]any{"role": "viewer"}); st != 409 || ty != "user.self_change" {
		t.Fatalf("ubah diri sendiri: %d %s", st, ty)
	}
	if st, _ := problemType(adm.c, "PATCH", "/api/v1/users/"+adm.id, map[string]any{"disabled": true}); st != 409 {
		t.Fatalf("nonaktifkan diri sendiri: %d", st)
	}
	// nonaktifkan: sesi dicabut, tidak bisa login; aktifkan lagi: bisa
	if st := u.c.do("GET", "/api/v1/auth/me", nil, nil); st != 200 {
		t.Fatal(st)
	}
	if st := adm.c.do("PATCH", "/api/v1/users/"+u.id, map[string]any{"disabled": true}, &out); st != 200 || !out.Disabled {
		t.Fatalf("nonaktifkan: %d %+v", st, out)
	}
	if st := u.c.do("GET", "/api/v1/auth/me", nil, nil); st != 401 {
		t.Fatalf("sesi harus dicabut: %d", st)
	}
	if st := e.freshClient().do("POST", "/api/v1/auth/login", map[string]string{"email": u.email, "password": pw0}, nil); st != 401 {
		t.Fatalf("pengguna nonaktif tidak boleh login: %d", st)
	}
	var list struct {
		Items []struct {
			Email    string
			Disabled bool `json:"disabled"`
		}
	}
	adm.c.do("GET", "/api/v1/users", nil, &list)
	found := false
	for _, it := range list.Items {
		if it.Email == u.email {
			found = it.Disabled
		}
	}
	if !found {
		t.Fatal("daftar harus menampilkan disabled")
	}
	if st := adm.c.do("PATCH", "/api/v1/users/"+u.id, map[string]any{"disabled": false}, nil); st != 200 {
		t.Fatal(st)
	}
	e.sql(`UPDATE users SET failed_logins=0, locked_until=NULL WHERE id=$1`, u.id) // percobaan login saat nonaktif menaikkan penghitung
	e.loginEmail(u.email, pw0)
	// audit memuat perubahan sebelum→sesudah
	var d string
	_ = e.pool.QueryRow(context.Background(), `SELECT data::text FROM audit_logs WHERE action='user.update' ORDER BY at ASC LIMIT 1`).Scan(&d)
	if !strings.Contains(d, `"from": "viewer"`) || !strings.Contains(d, `"to": "operator"`) {
		t.Fatalf("audit perubahan peran: %s", d)
	}
}

func TestAdminSetsAnotherUsersPassword(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	u := e.newUser("operator")
	if st := u.c.do("POST", "/api/v1/users/"+adm.id+"/password", map[string]string{"new_password": "password-baru-456"}, nil); st != 403 {
		t.Fatalf("operator: %d", st)
	}
	if st := adm.c.do("POST", "/api/v1/users/"+u.id+"/password", map[string]string{"new_password": "pendek"}, nil); st != 422 {
		t.Fatalf("pendek: %d", st)
	}
	if st := adm.c.do("POST", "/api/v1/users/"+uuid.NewString()+"/password", map[string]string{"new_password": "password-baru-456"}, nil); st != 404 {
		t.Fatalf("tak ada: %d", st)
	}
	if st, ty := problemType(adm.c, "POST", "/api/v1/users/"+adm.id+"/password", map[string]string{"new_password": "password-baru-456"}); st != 409 || ty != "user.self_change" {
		t.Fatalf("diri sendiri: %d %s", st, ty)
	}
	e.sql(`UPDATE users SET failed_logins=5, locked_until=now()+interval '15 minutes' WHERE id=$1`, u.id) // terkunci
	if st := adm.c.do("POST", "/api/v1/users/"+u.id+"/password", map[string]string{"new_password": "password-baru-456"}, nil); st != 204 {
		t.Fatalf("set: %d", st)
	}
	if st := u.c.do("GET", "/api/v1/auth/me", nil, nil); st != 401 {
		t.Fatalf("sesi pengguna harus dicabut: %d", st)
	}
	if st := e.freshClient().do("POST", "/api/v1/auth/login", map[string]string{"email": u.email, "password": pw0}, nil); st != 401 {
		t.Fatalf("password lama: %d", st)
	}
	e.loginEmail(u.email, "password-baru-456") // juga membuktikan kunci akun direset
	var n int
	var leaked string
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*), coalesce(string_agg(data::text,''),'') FROM audit_logs WHERE action='user.password_reset'`).Scan(&n, &leaked)
	if n != 1 || strings.Contains(leaked, "password-baru") {
		t.Fatalf("audit: %d %s", n, leaked)
	}
}

// Regresi: template tanpa variabel dulu gagal 500 (array NULL melanggar NOT NULL).
func TestTemplateWithoutVariablesCanBeSaved(t *testing.T) {
	e := newEnv(t)
	a := e.newUser("admin")
	var tpl struct {
		Variables []string `json:"variables"`
	}
	if st := a.c.do("POST", "/api/v1/templates", map[string]any{"name": "polos", "body": "Halo semuanya"}, &tpl); st != 201 || tpl.Variables == nil || len(tpl.Variables) != 0 {
		t.Fatalf("template tanpa variabel: %d %+v", st, tpl)
	}
}
