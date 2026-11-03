package tests

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jipaw/smsc-app/server/internal/config"
)

type begun struct {
	CeremonyID string         `json:"ceremony_id"`
	Options    map[string]any `json:"options"`
}

type regOut struct {
	Credential struct {
		ID, Name string
	}
	Codes []string `json:"recovery_codes"`
}

func (u userCtx) waBegin(t *testing.T, pw, name string) (int, begun) {
	t.Helper()
	var b begun
	var raw json.RawMessage
	st := u.c.do("POST", "/api/v1/auth/webauthn/register/begin", map[string]string{"password": pw, "name": name}, &raw)
	if st == 200 {
		_ = json.Unmarshal(raw, &b)
	}
	return st, b
}

func (u userCtx) waFinish(ceremony string, cred json.RawMessage) (int, regOut) {
	var out regOut
	var raw json.RawMessage
	st := u.c.do("POST", "/api/v1/auth/webauthn/register/finish", map[string]any{"ceremony_id": ceremony, "credential": cred}, &raw)
	if st == 201 {
		_ = json.Unmarshal(raw, &out)
	}
	return st, out
}

// waRegister: begin + finish dengan authenticator lunak; mengembalikan kode pemulihan (jika faktor pertama).
func (e *env) waRegister(u userCtx, a *softAuth, name string) []string {
	e.t.Helper()
	st, b := u.waBegin(e.t, pw0, name)
	if st != 200 {
		e.t.Fatalf("begin: %d", st)
	}
	st, out := u.waFinish(b.CeremonyID, a.Create(b.Options))
	if st != 201 {
		e.t.Fatalf("finish: %d", st)
	}
	return out.Codes
}

type loginBegin struct {
	Required bool     `json:"mfa_required"`
	Token    string   `json:"mfa_token"`
	Methods  []string `json:"methods"`
}

func (e *env) waLoginStart(email string) (loginBegin, *client) {
	c := e.freshClient()
	var lb loginBegin
	if st := c.do("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": pw0}, &lb); st != 200 || !lb.Required {
		e.t.Fatalf("login: %d %+v", st, lb)
	}
	return lb, c
}

func (e *env) waLoginBegin(c *client, tok string) (int, begun) {
	var raw json.RawMessage
	st := c.do("POST", "/api/v1/auth/login/webauthn/begin", map[string]string{"mfa_token": tok}, &raw)
	var b begun
	if st == 200 {
		_ = json.Unmarshal(raw, &b)
	}
	return st, b
}

func (e *env) waLoginFinish(c *client, tok, ceremony string, cred json.RawMessage) (int, string) {
	var p struct {
		Type string
		CSRF string `json:"csrf_token"`
	}
	st := c.do("POST", "/api/v1/auth/login/webauthn/finish", map[string]any{"mfa_token": tok, "ceremony_id": ceremony, "credential": cred}, &p)
	if st == 200 {
		c.csrf = p.CSRF
	}
	return st, p.Type
}

func TestWebAuthnRegisterAsFirstFactor(t *testing.T) {
	e := newEnv(t)
	u := e.newUser("operator")
	if st, _ := u.waBegin(t, "salah-salah-123", "x"); st != 401 {
		t.Fatalf("password salah: %d", st)
	}
	if st, _ := u.waBegin(t, pw0, strings.Repeat("n", 41)); st != 422 {
		t.Fatalf("nama terlalu panjang: %d", st)
	}
	st, b := u.waBegin(t, pw0, "YubiKey kantor")
	if st != 200 {
		t.Fatal(st)
	}
	pk := b.Options["publicKey"].(map[string]any)
	rp := pk["rp"].(map[string]any)
	sel := pk["authenticatorSelection"].(map[string]any)
	if rp["id"] != "example.test" || len(challengeOf(b.Options)) < 20 || sel["residentKey"] != "discouraged" {
		t.Fatalf("opsi: %v", pk)
	}
	a := newSoft(t)
	st, out := u.waFinish(b.CeremonyID, a.Create(b.Options))
	if st != 201 || out.Credential.Name != "YubiKey kantor" || len(out.Codes) != 10 {
		t.Fatalf("finish: %d %+v", st, out)
	}
	var stt struct {
		Enabled bool `json:"enabled"`
		TOTP    bool `json:"totp_enabled"`
		Keys    int  `json:"webauthn_credentials"`
		Rec     int  `json:"recovery_codes_remaining"`
	}
	u.c.do("GET", "/api/v1/auth/2fa", nil, &stt)
	if !stt.Enabled || stt.TOTP || stt.Keys != 1 || stt.Rec != 10 {
		t.Fatalf("status: %+v", stt)
	}
	var me struct {
		MFA bool `json:"mfa_enabled"`
	}
	u.c.do("GET", "/api/v1/auth/me", nil, &me)
	if !me.MFA {
		t.Fatal("kunci keamanan harus dihitung sebagai 2FA aktif")
	}
	var list struct {
		Enabled bool `json:"enabled"`
		Items   []struct {
			ID, Name string
		} `json:"credentials"`
	}
	u.c.do("GET", "/api/v1/auth/webauthn", nil, &list)
	if !list.Enabled || len(list.Items) != 1 || list.Items[0].Name != "YubiKey kantor" {
		t.Fatalf("daftar: %+v", list)
	}
	var blob string
	_ = e.pool.QueryRow(context.Background(), `SELECT data::text FROM webauthn_credentials`).Scan(&blob)
	if !strings.Contains(blob, "publicKey") || strings.Contains(blob, "privateKey") {
		t.Fatalf("data kredensial: %s", blob)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='auth.webauthn_register'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit: %d", n)
	}
}

func TestWebAuthnLoginAndAttacks(t *testing.T) {
	e := newEnv(t)
	u := e.newUser("admin")
	a := newSoft(t)
	e.waRegister(u, a, "Kunci A")

	lb, c := e.waLoginStart(u.email)
	if strings.Join(lb.Methods, ",") != "webauthn,recovery" {
		t.Fatalf("metode: %v", lb.Methods)
	}
	st, b := e.waLoginBegin(c, lb.Token)
	if st != 200 {
		t.Fatal(st)
	}
	pk := b.Options["publicKey"].(map[string]any)
	allow := pk["allowCredentials"].([]any)
	if len(allow) != 1 || allow[0].(map[string]any)["id"] != b64.EncodeToString(a.CredID) || pk["rpId"] != "example.test" {
		t.Fatalf("allowCredentials: %v", pk)
	}
	good := a.Assert(b.Options)
	// serangan: tanda tangan dirusak / origin lain / challenge lain / rpId lain — tiap percobaan menghabiskan upacara
	for name, mk := range map[string]func(map[string]any) json.RawMessage{
		"tanda tangan rusak": func(o map[string]any) json.RawMessage {
			a.SignCount++
			return a.AssertWith(challengeOf(o), a.Origin, true)
		},
		"origin lain": func(o map[string]any) json.RawMessage {
			a.SignCount++
			return a.AssertWith(challengeOf(o), "https://evil.example", false)
		},
		"challenge lain": func(o map[string]any) json.RawMessage {
			a.SignCount++
			return a.AssertWith("Y2hhbGxlbmdlLWxhaW4", a.Origin, false)
		},
		"rpId lain": func(o map[string]any) json.RawMessage {
			x := *a
			x.RPID = "evil.example"
			x.SignCount++
			return x.AssertWith(challengeOf(o), a.Origin, false)
		},
	} {
		_, bb := e.waLoginBegin(c, lb.Token)
		if st, ty := e.waLoginFinish(c, lb.Token, bb.CeremonyID, mk(bb.Options)); st != 401 || ty != "auth.webauthn_failed" {
			t.Errorf("%s: harus 401: %d %s", name, st, ty)
		}
		a.SignCount-- // kembalikan agar skenario berikut tidak kebetulan gagal karena sign count
		a.SignCount--
		if a.SignCount > 1<<30 {
			a.SignCount = 0
		}
	}
	// 4 kegagalan tercatat; yang valid (untuk upacara pertama) kini kedaluwarsa karena kita membuat upacara baru? tidak: upacara
	// pertama masih ada. Gunakan assertion benar pada upacara pertama.
	if st, _ := e.waLoginFinish(c, lb.Token, b.CeremonyID, good); st != 200 {
		t.Fatalf("assertion benar harus 200: %d", st)
	}
	var me struct {
		Email string
	}
	if st := c.do("GET", "/api/v1/auth/me", nil, &me); st != 200 || me.Email != u.email {
		t.Fatalf("sesi: %d %+v", st, me)
	}
	var cnt int
	var used *time.Time
	_ = e.pool.QueryRow(context.Background(), `SELECT (data->'authenticator'->>'signCount')::int, last_used_at FROM webauthn_credentials`).Scan(&cnt, &used)
	if cnt < 1 || used == nil {
		t.Fatalf("sign count & last_used harus diperbarui: %d %v", cnt, used)
	}
	// upacara sekali pakai: mengulang finish dengan upacara yang sama → 400
	lb2, c2 := e.waLoginStart(u.email)
	if st, _ := e.waLoginFinish(c2, lb2.Token, b.CeremonyID, good); st != 400 {
		t.Fatalf("upacara orang lain / terpakai harus 400: %d", st)
	}
	// replay tanda tangan lama pada challenge baru → gagal
	_, bb := e.waLoginBegin(c2, lb2.Token)
	if st, _ := e.waLoginFinish(c2, lb2.Token, bb.CeremonyID, good); st != 401 {
		t.Fatalf("replay assertion lama: %d", st)
	}
	var f int
	_ = e.pool.QueryRow(context.Background(), `SELECT failed_logins FROM users WHERE id=$1`, u.id).Scan(&f)
	if f == 0 {
		t.Fatal("kegagalan WebAuthn harus dihitung ke lockout")
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='auth.login' AND data->>'method'='webauthn'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit login webauthn: %d", n)
	}
}

func TestWebAuthnFailuresLockAccountAndTokenChecks(t *testing.T) {
	e := newEnv(t)
	u := e.newUser("operator")
	a := newSoft(t)
	e.waRegister(u, a, "K")
	lb, c := e.waLoginStart(u.email)
	if st, _ := e.waLoginBegin(c, "sampah.sampah"); st != 401 {
		t.Fatalf("token sampah: %d", st)
	}
	for i := 0; i < 5; i++ {
		_, b := e.waLoginBegin(c, lb.Token)
		a.SignCount++
		if st, _ := e.waLoginFinish(c, lb.Token, b.CeremonyID, a.AssertWith(challengeOf(b.Options), a.Origin, true)); st != 401 {
			t.Fatalf("percobaan %d: %d", i, st)
		}
	}
	if st, _ := e.waLoginBegin(c, lb.Token); st != 423 {
		t.Fatalf("setelah 5 kegagalan akun harus terkunci: %d", st)
	}
}

func TestWebAuthnCrossUserAndWrongKeyRejected(t *testing.T) {
	e := newEnv(t)
	u1, u2 := e.newUser("operator"), e.newUser("operator")
	a1, a2 := newSoft(t), newSoft(t)
	e.waRegister(u1, a1, "K1")
	e.waRegister(u2, a2, "K2")
	// kunci milik u2 dipakai pada upacara u1 → ditolak (credential bukan milik pengguna ini)
	lb, c := e.waLoginStart(u1.email)
	_, b := e.waLoginBegin(c, lb.Token)
	a2.SignCount++
	if st, _ := e.waLoginFinish(c, lb.Token, b.CeremonyID, a2.AssertWith(challengeOf(b.Options), a2.Origin, false)); st != 401 {
		t.Fatalf("kunci pengguna lain: %d", st)
	}
	// ceremony milik pengguna lain tidak bisa dipakai
	lb2, c2 := e.waLoginStart(u2.email)
	_, b2 := e.waLoginBegin(c2, lb2.Token)
	a1.SignCount++
	if st, _ := e.waLoginFinish(c, lb.Token, b2.CeremonyID, a1.AssertWith(challengeOf(b2.Options), a1.Origin, false)); st != 400 {
		t.Fatalf("ceremony pengguna lain: %d", st)
	}
	// ceremony register tidak bisa dipakai sebagai login dan sebaliknya
	stR, br := u1.waBegin(t, pw0, "x")
	if stR != 200 {
		t.Fatal(stR)
	}
	lb3, c3 := e.waLoginStart(u1.email)
	a1.SignCount++
	if st, _ := e.waLoginFinish(c3, lb3.Token, br.CeremonyID, a1.AssertWith(challengeOf(br.Options), a1.Origin, false)); st != 400 {
		t.Fatalf("ceremony register dipakai login: %d", st)
	}
}

func TestWebAuthnCloneDetectionRejectsRegressedCounter(t *testing.T) {
	e := newEnv(t)
	u := e.newUser("operator")
	a := newSoft(t)
	e.waRegister(u, a, "K")
	login := func(count uint32) int {
		lb, c := e.waLoginStart(u.email)
		_, b := e.waLoginBegin(c, lb.Token)
		a.SignCount = count
		st, _ := e.waLoginFinish(c, lb.Token, b.CeremonyID, a.AssertWith(challengeOf(b.Options), a.Origin, false))
		return st
	}
	if st := login(5); st != 200 {
		t.Fatalf("hitung 5: %d", st)
	}
	if st := login(6); st != 200 {
		t.Fatalf("hitung 6: %d", st)
	}
	if st := login(3); st != 401 {
		t.Fatalf("hitung mundur harus ditolak (klon): %d", st)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='auth.webauthn_clone_warning'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit klon: %d", n)
	}
}

func TestWebAuthnCeremonyExpiryDuplicateAndLimit(t *testing.T) {
	e := newEnv(t)
	u := e.newUser("operator")
	a := newSoft(t)
	// kedaluwarsa
	_, b := u.waBegin(t, pw0, "x")
	e.sql(`UPDATE webauthn_ceremonies SET expires_at = now() - interval '1 second'`)
	if st, _ := u.waFinish(b.CeremonyID, a.Create(b.Options)); st != 400 {
		t.Fatalf("kedaluwarsa: %d", st)
	}
	// ceremony tidak ada
	if st, _ := u.waFinish(uuid.NewString(), a.CreateWith("abc", a.Origin)); st != 400 {
		t.Fatalf("tak ada: %d", st)
	}
	// attestation dengan origin salah / challenge salah ditolak
	_, b = u.waBegin(t, pw0, "x")
	if st, _ := u.waFinish(b.CeremonyID, a.CreateWith(challengeOf(b.Options), "https://evil.example")); st != 422 {
		t.Fatalf("origin salah: %d", st)
	}
	_, b = u.waBegin(t, pw0, "x")
	if st, _ := u.waFinish(b.CeremonyID, a.CreateWith("Y2hhbGxlbmdlLWxhaW4", a.Origin)); st != 422 {
		t.Fatalf("challenge salah: %d", st)
	}
	e.waRegister(u, a, "ok")
	// kunci yang sama didaftarkan lagi: opsi memuat excludeCredentials, dan finish ditolak
	_, b = u.waBegin(t, pw0, "dup")
	ex := b.Options["publicKey"].(map[string]any)["excludeCredentials"].([]any)
	if len(ex) != 1 {
		t.Fatalf("excludeCredentials: %v", ex)
	}
	if st, _ := u.waFinish(b.CeremonyID, a.Create(b.Options)); st != 409 {
		t.Fatalf("duplikat: %d", st)
	}
	// batas 10 kunci
	for i := 0; i < 9; i++ {
		e.waRegister(u, newSoft(t), "k")
	}
	if st, _ := u.waBegin(t, pw0, "kelebihan"); st != 409 {
		t.Fatalf("batas 10: %d", st)
	}
}

func TestWebAuthnMixedWithTOTPAndRecoveryCodes(t *testing.T) {
	e := newEnv(t)
	// (1) WebAuthn dulu, TOTP menyusul: TOTP boleh ditambah, kode pemulihan TIDAK diterbitkan ulang
	u := e.newUser("operator")
	a := newSoft(t)
	codes := e.waRegister(u, a, "K")
	var setup struct{ Secret string }
	if st := u.c.do("POST", "/api/v1/auth/2fa/setup", map[string]string{"password": pw0}, &setup); st != 200 {
		t.Fatalf("setup TOTP saat sudah ada kunci: %d", st)
	}
	var en struct {
		Codes []string `json:"recovery_codes"`
	}
	if st := u.c.do("POST", "/api/v1/auth/2fa/enable", map[string]string{"code": totp(setup.Secret, time.Now())}, &en); st != 200 || len(en.Codes) != 0 {
		t.Fatalf("enable TOTP: %d %v (tidak boleh menerbitkan kode baru)", st, en.Codes)
	}
	lb, c := e.waLoginStart(u.email)
	if strings.Join(lb.Methods, ",") != "webauthn,totp,recovery" {
		t.Fatalf("metode: %v", lb.Methods)
	}
	// kode pemulihan lama tetap sah
	if st, _ := e.verify(c, lb.Token, codes[0]); st != 200 {
		t.Fatalf("kode pemulihan lama: %d", st)
	}
	// TOTP juga bisa untuk login
	e.sql(`UPDATE users SET totp_last_step=0 WHERE id=$1`, u.id)
	lb, c = e.waLoginStart(u.email)
	if st, _ := e.verify(c, lb.Token, totp(setup.Secret, time.Now().Add(30*time.Second))); st != 200 {
		t.Fatalf("login TOTP: %d", st)
	}
	var stt struct {
		TOTP bool `json:"totp_enabled"`
		Keys int  `json:"webauthn_credentials"`
	}
	u.c.do("GET", "/api/v1/auth/2fa", nil, &stt)
	if !stt.TOTP || stt.Keys != 1 {
		t.Fatalf("status: %+v", stt)
	}
	// TOTP sudah ada: setup lagi 409
	if st, ty := problemType(u.c, "POST", "/api/v1/auth/2fa/setup", map[string]string{"password": pw0}); st != 409 || ty != "mfa.already_enabled" {
		t.Fatalf("setup ulang: %d %s", st, ty)
	}

	// (2) TOTP dulu, kunci menyusul: kode pemulihan dari TOTP tetap, tidak diterbitkan ulang
	v := e.newUser("operator")
	_, rc := e.enableMFA(v)
	b := newSoft(t)
	if got := e.waRegister(v, b, "K"); len(got) != 0 {
		t.Fatalf("kunci tambahan tidak boleh menerbitkan kode baru: %v", got)
	}
	var rem struct {
		Rec int `json:"recovery_codes_remaining"`
	}
	v.c.do("GET", "/api/v1/auth/2fa", nil, &rem)
	if rem.Rec != 10 {
		t.Fatalf("sisa kode: %d", rem.Rec)
	}
	_ = rc
}

func TestWebAuthnRemoveRequiresReauthAndDisablesWhenLast(t *testing.T) {
	e := newEnv(t)
	u := e.newUser("operator")
	a1, a2 := newSoft(t), newSoft(t)
	codes := e.waRegister(u, a1, "K1")
	e.waRegister(u, a2, "K2")
	var list struct {
		Items []struct{ ID, Name string } `json:"credentials"`
	}
	u.c.do("GET", "/api/v1/auth/webauthn", nil, &list)
	if len(list.Items) != 2 {
		t.Fatal(list)
	}
	path := func(id string) string { return "/api/v1/auth/webauthn/credentials/" + id }
	if st := u.c.do("DELETE", path(list.Items[0].ID), map[string]string{"password": pw0, "code": "000000"}, nil); st != 401 {
		t.Fatalf("kode salah: %d", st)
	}
	if st := u.c.do("DELETE", path(list.Items[0].ID), map[string]string{"password": "salah-salah-123", "code": codes[0]}, nil); st != 401 {
		t.Fatalf("password salah: %d", st)
	}
	if st := u.c.do("DELETE", path(uuid.NewString()), map[string]string{"password": pw0, "code": codes[0]}, nil); st != 404 {
		t.Fatalf("tak ada: %d", st)
	}
	other := e.newUser("operator")
	e.waRegister(other, newSoft(t), "O")
	var ol struct {
		Items []struct{ ID string } `json:"credentials"`
	}
	other.c.do("GET", "/api/v1/auth/webauthn", nil, &ol)
	if st := u.c.do("DELETE", path(ol.Items[0].ID), map[string]string{"password": pw0, "code": codes[1]}, nil); st != 404 {
		t.Fatalf("kunci milik orang lain: %d", st)
	}
	if st := u.c.do("DELETE", path(list.Items[0].ID), map[string]string{"password": pw0, "code": codes[1]}, nil); st != 204 {
		t.Fatalf("hapus satu: %d", st)
	}
	var s1 struct {
		Enabled bool `json:"enabled"`
		Rec     int  `json:"recovery_codes_remaining"`
	}
	u.c.do("GET", "/api/v1/auth/2fa", nil, &s1)
	if !s1.Enabled || s1.Rec != 9 {
		t.Fatalf("masih ada satu kunci: %+v", s1)
	}
	// menghapus kunci terakhir (tanpa TOTP) = 2FA mati, kode pemulihan dibuang
	if st := u.c.do("DELETE", path(list.Items[1].ID), map[string]string{"password": pw0, "code": codes[2]}, nil); st != 204 {
		t.Fatalf("hapus terakhir: %d", st)
	}
	u.c.do("GET", "/api/v1/auth/2fa", nil, &s1)
	if s1.Enabled || s1.Rec != 0 {
		t.Fatalf("2FA harus mati: %+v", s1)
	}
	if ch, st, _, _ := e.loginStep1(u.email, pw0); st != 200 || ch.Required {
		t.Fatal("login kembali tanpa tantangan")
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='auth.webauthn_remove'`).Scan(&n)
	if n != 2 {
		t.Fatalf("audit: %d", n)
	}
}

func TestWebAuthnClearedByDisableAdminResetAndUserDelete(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	// nonaktifkan 2FA menghapus kunci juga
	u := e.newUser("operator")
	codes := e.waRegister(u, newSoft(t), "K")
	if st := u.c.do("POST", "/api/v1/auth/2fa/disable", map[string]string{"password": pw0, "code": codes[0]}, nil); st != 204 {
		t.Fatalf("disable: %d", st)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM webauthn_credentials WHERE user_id=$1`, u.id).Scan(&n)
	if n != 0 {
		t.Fatal("disable harus menghapus kunci")
	}
	// reset admin
	v := e.newUser("operator")
	e.waRegister(v, newSoft(t), "K")
	_, _ = v.waBegin(t, pw0, "x") // ceremony menggantung
	if st := adm.c.do("DELETE", "/api/v1/users/"+v.id+"/2fa", nil, nil); st != 204 {
		t.Fatalf("reset: %d", st)
	}
	_ = e.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM webauthn_credentials WHERE user_id=$1) + (SELECT count(*) FROM webauthn_ceremonies WHERE user_id=$1)`, v.id).Scan(&n)
	if n != 0 {
		t.Fatalf("reset harus membersihkan kunci & ceremony: %d", n)
	}
	// hapus pengguna (nonaktif lalu hapus) → cascade
	w := e.newUser("viewer")
	e.waRegister(w, newSoft(t), "K")
	adm.c.do("PATCH", "/api/v1/users/"+w.id, map[string]any{"disabled": true}, nil)
	if st := adm.c.do("DELETE", "/api/v1/users/"+w.id, nil, nil); st != 204 {
		t.Fatalf("hapus: %d", st)
	}
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM webauthn_credentials`).Scan(&n)
	if n != 0 {
		t.Fatalf("cascade: %d", n)
	}
}

func TestWebAuthnCountsForRequire2FAAndGateAllowsRegistration(t *testing.T) {
	e := newEnv(t)
	a := e.newUser("admin")
	e.enableMFA(a)
	a.c.do("PUT", "/api/v1/settings/security", map[string]string{"require_2fa": "admin"}, nil)
	// admin kedua tanpa 2FA: digerbang, tetapi endpoint pendaftaran WebAuthn terbuka
	b := e.newUser("admin")
	if st := b.c.do("GET", "/api/v1/devices", nil, nil); st != 403 {
		t.Fatalf("harus digerbang: %d", st)
	}
	key := newSoft(t)
	codes := e.waRegister(b, key, "K")
	if len(codes) != 10 {
		t.Fatalf("faktor pertama menerbitkan kode: %d", len(codes))
	}
	if st := b.c.do("GET", "/api/v1/devices", nil, nil); st != 200 {
		t.Fatalf("setelah mendaftarkan kunci gerbang harus terbuka: %d", st)
	}
	var s struct {
		Missing int `json:"users_missing_2fa"`
	}
	a.c.do("GET", "/api/v1/settings/security", nil, &s)
	if s.Missing != 0 {
		t.Fatalf("hitungan harus menganggap kunci sebagai 2FA: %d", s.Missing)
	}
}

func TestWebAuthnUnavailableWhenRPIDIsIP(t *testing.T) {
	e := newEnvOpts(t, func(c *config.Config) { c.WebAuthnRPID = "127.0.0.1" })
	u := e.newUser("operator")
	var l struct {
		Enabled bool `json:"enabled"`
	}
	if st := u.c.do("GET", "/api/v1/auth/webauthn", nil, &l); st != 200 || l.Enabled {
		t.Fatalf("daftar: %d %+v", st, l)
	}
	if st, ty := problemType(u.c, "POST", "/api/v1/auth/webauthn/register/begin", map[string]string{"password": pw0}); st != 409 || ty != "webauthn.not_available" {
		t.Fatalf("begin: %d %s", st, ty)
	}
}
