package tests

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func (e *env) setPasskeyLogin(adm userCtx, on bool) {
	e.t.Helper()
	if st := adm.c.do("PUT", "/api/v1/settings/security", map[string]any{"passkey_login": on}, nil); st != 200 {
		e.t.Fatalf("passkey_login=%v: %d", on, st)
	}
}

// pkRegister mendaftarkan passkey (resident + UV) dan mengembalikan kode pemulihan (bila faktor pertama).
func (e *env) pkRegister(u userCtx, a *softAuth, name string) (int, []string) {
	e.t.Helper()
	var raw json.RawMessage
	if st := u.c.do("POST", "/api/v1/auth/webauthn/register/begin", map[string]any{"password": pw0, "name": name, "passkey": true}, &raw); st != 200 {
		return st, nil
	}
	var b begun
	_ = json.Unmarshal(raw, &b)
	st, out := u.waFinish(b.CeremonyID, a.Create(b.Options))
	return st, out.Codes
}

func (e *env) pkBegin(c *client) (int, begun) {
	var raw json.RawMessage
	st := c.do("POST", "/api/v1/auth/passkey/begin", map[string]any{}, &raw)
	var b begun
	if st == 200 {
		_ = json.Unmarshal(raw, &b)
	}
	return st, b
}

func (e *env) pkFinish(c *client, ceremony string, cred json.RawMessage) (int, string) {
	var p struct {
		Type string
		CSRF string `json:"csrf_token"`
	}
	st := c.do("POST", "/api/v1/auth/passkey/finish", map[string]any{"ceremony_id": ceremony, "credential": cred}, &p)
	if st == 200 {
		c.csrf = p.CSRF
	}
	return st, p.Type
}

// pkLogin: login lengkap dengan passkey; mengembalikan status.
func (e *env) pkLogin(a *softAuth) (int, *client) {
	c := e.freshClient()
	_, b := e.pkBegin(c)
	st, _ := e.pkFinish(c, b.CeremonyID, a.Assert(b.Options))
	return st, c
}

func TestPasskeyLoginDisabledByDefaultAndAdminToggle(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	op := e.newUser("operator")
	var m struct {
		P bool `json:"passkey_login"`
	}
	e.freshClient().do("GET", "/api/v1/auth/methods", nil, &m)
	if m.P {
		t.Fatal("default harus mati")
	}
	var s struct {
		P   bool   `json:"passkey_login"`
		Req string `json:"require_2fa"`
	}
	adm.c.do("GET", "/api/v1/settings/security", nil, &s)
	if s.P {
		t.Fatal("pengaturan default harus mati")
	}
	if st, ty := problemType(op.c, "POST", "/api/v1/auth/webauthn/register/begin", map[string]any{"password": pw0, "passkey": true}); st != 409 || ty != "passkey.disabled" {
		t.Fatalf("daftar saat mati: %d %s", st, ty)
	}
	if st, ty := problemType(e.freshClient(), "POST", "/api/v1/auth/passkey/begin", map[string]any{}); st != 409 || ty != "passkey.disabled" {
		t.Fatalf("login saat mati: %d %s", st, ty)
	}
	if st := op.c.do("PUT", "/api/v1/settings/security", map[string]any{"passkey_login": true}, nil); st != 403 {
		t.Fatalf("operator: %d", st)
	}
	if st := adm.c.do("PUT", "/api/v1/settings/security", map[string]any{}, nil); st != 422 {
		t.Fatalf("tanpa field: %d", st)
	}
	e.setPasskeyLogin(adm, true)
	e.freshClient().do("GET", "/api/v1/auth/methods", nil, &m)
	if !m.P {
		t.Fatal("harus menyala")
	}
	adm.c.do("GET", "/api/v1/settings/security", nil, &s)
	if !s.P || s.Req != "off" {
		t.Fatalf("passkey_login tidak boleh mengubah require_2fa: %+v", s)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='security.update' AND data->>'passkey_login'='true'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit: %d", n)
	}
}

func TestPasskeyRegistrationRequirements(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	e.setPasskeyLogin(adm, true)
	u := e.newUser("operator")
	var raw json.RawMessage
	u.c.do("POST", "/api/v1/auth/webauthn/register/begin", map[string]any{"password": pw0, "name": "HP", "passkey": true}, &raw)
	var b begun
	_ = json.Unmarshal(raw, &b)
	pk := b.Options["publicKey"].(map[string]any)
	sel := pk["authenticatorSelection"].(map[string]any)
	ext := pk["extensions"].(map[string]any)
	if sel["residentKey"] != "required" || sel["userVerification"] != "required" || ext["credProps"] != true {
		t.Fatalf("opsi passkey: sel=%v ext=%v", sel, ext)
	}
	// tanpa verifikasi pengguna → ditolak
	noUV := newSoft(t)
	noUV.NoUV = true
	if st, _ := u.waFinish(b.CeremonyID, noUV.Create(b.Options)); st != 422 {
		t.Fatalf("tanpa UV: %d", st)
	}
	// klien melaporkan rk=false → ditolak
	u.c.do("POST", "/api/v1/auth/webauthn/register/begin", map[string]any{"password": pw0, "passkey": true}, &raw)
	_ = json.Unmarshal(raw, &b)
	f := false
	nonRes := newSoft(t)
	nonRes.RKProp = &f
	if st, _ := u.waFinish(b.CeremonyID, nonRes.Create(b.Options)); st != 422 {
		t.Fatalf("rk=false: %d", st)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM webauthn_credentials`).Scan(&n)
	if n != 0 {
		t.Fatal("tidak boleh ada yang tersimpan dari percobaan gagal")
	}
	// sukses
	good := newSoft(t)
	t1 := true
	good.RKProp = &t1
	st, codes := e.pkRegister(u, good, "HP")
	if st != 201 || len(codes) != 10 {
		t.Fatalf("daftar passkey: %d %v", st, codes)
	}
	var list struct {
		Items []struct {
			Name    string
			Passkey bool
		} `json:"credentials"`
	}
	u.c.do("GET", "/api/v1/auth/webauthn", nil, &list)
	if len(list.Items) != 1 || !list.Items[0].Passkey {
		t.Fatalf("daftar: %+v", list)
	}
	// kunci keamanan biasa (bukan passkey) tidak ditandai passkey
	plain := newSoft(t)
	e.waRegister(u, plain, "Kunci biasa")
	u.c.do("GET", "/api/v1/auth/webauthn", nil, &list)
	pks := 0
	for _, it := range list.Items {
		if it.Passkey {
			pks++
		}
	}
	if pks != 1 {
		t.Fatalf("hanya satu passkey: %d", pks)
	}
}

func TestPasskeyLoginHappyPathSkipsSecondFactor(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	e.setPasskeyLogin(adm, true)
	u := e.newUser("operator")
	e.enableMFA(u) // punya TOTP juga: passkey tetap tidak meminta faktor kedua
	a := newSoft(t)
	if st, _ := e.pkRegister(u, a, "HP"); st != 201 {
		t.Fatalf("daftar: %d", st)
	}
	c := e.freshClient()
	st, b := e.pkBegin(c)
	if st != 200 {
		t.Fatal(st)
	}
	pk := b.Options["publicKey"].(map[string]any)
	if _, has := pk["allowCredentials"]; has && len(pk["allowCredentials"].([]any)) > 0 {
		t.Fatalf("login discoverable tidak boleh membocorkan daftar kredensial: %v", pk["allowCredentials"])
	}
	if pk["userVerification"] != "required" || pk["rpId"] != "example.test" {
		t.Fatalf("opsi: %v", pk)
	}
	if st, _ := e.pkFinish(c, b.CeremonyID, a.Assert(b.Options)); st != 200 {
		t.Fatalf("finish: %d", st)
	}
	var me struct {
		Email string
		MFA   bool `json:"mfa_enabled"`
	}
	if st := c.do("GET", "/api/v1/auth/me", nil, &me); st != 200 || me.Email != u.email || !me.MFA {
		t.Fatalf("sesi: %d %+v", st, me)
	}
	var cnt int
	var used *time.Time
	_ = e.pool.QueryRow(context.Background(), `SELECT (data->'authenticator'->>'signCount')::int, last_used_at FROM webauthn_credentials`).Scan(&cnt, &used)
	if cnt < 1 || used == nil {
		t.Fatalf("sign count/last_used: %d %v", cnt, used)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='auth.login' AND data->>'method'='passkey'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit: %d", n)
	}
	// kebijakan wajib-2FA: passkey dihitung sebagai 2FA, jadi akun dengan passkey saja tidak digerbang
	adm2 := e.newUser("admin")
	e.enableMFA(adm2)
	if st := adm2.c.do("PUT", "/api/v1/settings/security", map[string]string{"require_2fa": "all"}, nil); st != 200 {
		t.Fatal(st)
	}
	p := e.newUser("viewer")
	if st := p.c.do("GET", "/api/v1/devices", nil, nil); st != 403 {
		t.Fatalf("tanpa 2FA harus digerbang: %d", st)
	}
	pa := newSoft(t)
	if st, _ := e.pkRegister(p, pa, "HP"); st != 201 { // pendaftaran lewat endpoint akun tetap terbuka bagi akun yang digerbang
		t.Fatalf("daftar passkey saat digerbang: %d", st)
	}
	st2, pc := e.pkLogin(pa)
	if st2 != 200 {
		t.Fatalf("login passkey: %d", st2)
	}
	if st := pc.do("GET", "/api/v1/devices", nil, nil); st != 200 {
		t.Fatalf("akun dengan passkey saja tidak boleh digerbang: %d", st)
	}
}

func TestPasskeyLoginRejectsAttacksAndWrongCredentials(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	e.setPasskeyLogin(adm, true)
	u, other := e.newUser("operator"), e.newUser("operator")
	a, oa := newSoft(t), newSoft(t)
	e.pkRegister(u, a, "HP")
	e.pkRegister(other, oa, "HP2")

	try := func(name string, mk func(o map[string]any) json.RawMessage, want int) {
		t.Helper()
		c := e.freshClient()
		_, b := e.pkBegin(c)
		if st, _ := e.pkFinish(c, b.CeremonyID, mk(b.Options)); st != want {
			t.Errorf("%s: harus %d, dapat %d", name, want, st)
		}
	}
	try("tanda tangan rusak", func(o map[string]any) json.RawMessage {
		a.SignCount++
		return a.AssertWith(challengeOf(o), a.Origin, true)
	}, 401)
	try("origin lain", func(o map[string]any) json.RawMessage {
		a.SignCount++
		return a.AssertWith(challengeOf(o), "https://evil.example", false)
	}, 401)
	try("challenge lain", func(o map[string]any) json.RawMessage {
		a.SignCount++
		return a.AssertWith("Y2hhbGxlbmdlLWxhaW4", a.Origin, false)
	}, 401)
	try("tanpa verifikasi pengguna", func(o map[string]any) json.RawMessage {
		x := *a
		x.NoUV = true
		x.SignCount = a.SignCount + 10
		return x.AssertWith(challengeOf(o), x.Origin, false)
	}, 401)
	try("user handle milik orang lain", func(o map[string]any) json.RawMessage {
		x := *a
		x.UserHandle = oa.UserHandle
		x.SignCount = a.SignCount + 20
		return x.AssertWith(challengeOf(o), x.Origin, false)
	}, 401)
	try("tanpa user handle", func(o map[string]any) json.RawMessage {
		x := *a
		x.UserHandle = nil
		x.SignCount = a.SignCount + 30
		return x.AssertWith(challengeOf(o), x.Origin, false)
	}, 401)
	try("kredensial tak dikenal", func(o map[string]any) json.RawMessage {
		z := newSoft(t)
		z.UserHandle = a.UserHandle
		z.SignCount = 1
		return z.AssertWith(challengeOf(o), z.Origin, false)
	}, 401)

	// kunci keamanan BIASA (bukan passkey) tidak boleh dipakai login tanpa password, walau user handle benar
	plain := newSoft(t)
	e.waRegister(u, plain, "Kunci biasa")
	plain.UserHandle = a.UserHandle
	try("kunci biasa bukan passkey", func(o map[string]any) json.RawMessage {
		plain.SignCount++
		return plain.AssertWith(challengeOf(o), plain.Origin, false)
	}, 401)

	// upacara: sekali pakai, dan jenisnya harus cocok
	c := e.freshClient()
	_, b := e.pkBegin(c)
	good := a.Assert(b.Options)
	if st, _ := e.pkFinish(c, b.CeremonyID, good); st != 200 {
		t.Fatalf("yang benar harus lolos: %d", st)
	}
	c2 := e.freshClient()
	if st, _ := e.pkFinish(c2, b.CeremonyID, good); st != 400 {
		t.Fatalf("upacara terpakai: %d", st)
	}
	// upacara login faktor-kedua tidak boleh dipakai di endpoint passkey, dan sebaliknya
	lb, c3 := e.waLoginStart(u.email)
	_, sb := e.waLoginBegin(c3, lb.Token)
	a.SignCount++
	if st, _ := e.pkFinish(e.freshClient(), sb.CeremonyID, a.AssertWith(challengeOf(sb.Options), a.Origin, false)); st != 400 {
		t.Fatalf("upacara login-2FA di endpoint passkey: %d", st)
	}
	c4 := e.freshClient()
	_, pb := e.pkBegin(c4)
	lb2, c5 := e.waLoginStart(u.email)
	a.SignCount++
	if st, _ := e.waLoginFinish(c5, lb2.Token, pb.CeremonyID, a.AssertWith(challengeOf(pb.Options), a.Origin, false)); st != 400 {
		t.Fatalf("upacara passkey di endpoint login-2FA: %d", st)
	}
	// kedaluwarsa
	c6 := e.freshClient()
	_, eb := e.pkBegin(c6)
	e.sql(`UPDATE webauthn_ceremonies SET expires_at = now() - interval '1 second'`)
	a.SignCount++
	if st, _ := e.pkFinish(c6, eb.CeremonyID, a.AssertWith(challengeOf(eb.Options), a.Origin, false)); st != 400 {
		t.Fatalf("kedaluwarsa: %d", st)
	}
}

func TestPasskeyLoginAccountStates(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	e.setPasskeyLogin(adm, true)
	u := e.newUser("operator")
	a := newSoft(t)
	e.pkRegister(u, a, "HP")
	if st, _ := e.pkLogin(a); st != 200 {
		t.Fatalf("dasar: %d", st)
	}
	// terkunci → 423
	e.sql(`UPDATE users SET locked_until = now() + interval '10 minutes' WHERE id=$1`, u.id)
	if st, _ := e.pkLogin(a); st != 423 {
		t.Fatalf("terkunci: %d", st)
	}
	e.sql(`UPDATE users SET locked_until = NULL WHERE id=$1`, u.id)
	// dinonaktifkan → ditolak, sesi lama tidak relevan
	adm.c.do("PATCH", "/api/v1/users/"+u.id, map[string]any{"disabled": true}, nil)
	if st, _ := e.pkLogin(a); st != 401 {
		t.Fatalf("nonaktif: %d", st)
	}
	adm.c.do("PATCH", "/api/v1/users/"+u.id, map[string]any{"disabled": false}, nil)
	if st, _ := e.pkLogin(a); st != 200 {
		t.Fatalf("aktif lagi: %d", st)
	}
	// admin mematikan fitur → semua passkey tak bisa dipakai, bahkan dengan upacara yang sudah dibuka
	c := e.freshClient()
	_, b := e.pkBegin(c)
	e.setPasskeyLogin(adm, false)
	a.SignCount++
	if st, ty := e.pkFinish(c, b.CeremonyID, a.AssertWith(challengeOf(b.Options), a.Origin, false)); st != 409 || ty != "passkey.disabled" {
		t.Fatalf("fitur dimatikan: %d %s", st, ty)
	}
	// ...namun kunci tersimpan dan tetap berfungsi sebagai faktor kedua
	lb, lc := e.waLoginStart(u.email)
	_, sb := e.waLoginBegin(lc, lb.Token)
	if st, _ := e.waLoginFinish(lc, lb.Token, sb.CeremonyID, a.Assert(sb.Options)); st != 200 {
		t.Fatalf("passkey tetap sah sebagai faktor kedua: %d", st)
	}
	e.setPasskeyLogin(adm, true)
	// reset admin (menghapus semua kredensial) → passkey tidak bisa dipakai lagi
	if st := adm.c.do("DELETE", "/api/v1/users/"+u.id+"/2fa", nil, nil); st != 204 {
		t.Fatalf("reset: %d", st)
	}
	if st, _ := e.pkLogin(a); st != 401 {
		t.Fatalf("setelah reset: %d", st)
	}
}

func TestPasskeyCloneDetection(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	e.setPasskeyLogin(adm, true)
	u := e.newUser("operator")
	a := newSoft(t)
	e.pkRegister(u, a, "HP")
	login := func(count uint32) int {
		c := e.freshClient()
		_, b := e.pkBegin(c)
		a.SignCount = count
		st, _ := e.pkFinish(c, b.CeremonyID, a.AssertWith(challengeOf(b.Options), a.Origin, false))
		return st
	}
	if login(5) != 200 || login(6) != 200 {
		t.Fatal("hitung naik harus lolos")
	}
	if login(2) != 401 {
		t.Fatal("hitung mundur harus ditolak")
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='auth.webauthn_clone_warning'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit klon: %d", n)
	}
	_ = uuid.Nil
}
