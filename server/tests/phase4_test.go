package tests

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jipaw/smsc-app/server/internal/bus"
	"github.com/jipaw/smsc-app/server/internal/core"
	"github.com/jipaw/smsc-app/server/internal/proto"
)

// ---------- TOTP independen (bukan memakai kode server) ----------

func totp(secretB32 string, at time.Time) string {
	key, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretB32)
	var c [8]byte
	binary.BigEndian.PutUint64(c[:], uint64(at.Unix()/30))
	m := hmac.New(sha1.New, key)
	m.Write(c[:])
	s := m.Sum(nil)
	o := s[len(s)-1] & 0xf
	v := (uint32(s[o]&0x7f)<<24 | uint32(s[o+1])<<16 | uint32(s[o+2])<<8 | uint32(s[o+3])) % 1000000
	return fmt.Sprintf("%06d", v)
}

const pw0 = "password-panjang-123"

type userCtx struct {
	c     *client
	email string
	id    string
}

// newUser membuat pengguna dan login (tanpa 2FA).
func (e *env) newUser(role string) userCtx {
	e.t.Helper()
	email := role + "-" + uuid.NewString()[:6] + "@example.com"
	u, err := e.svc.CreateUser(context.Background(), email, pw0, role)
	if err != nil {
		e.t.Fatal(err)
	}
	return userCtx{c: e.loginEmail(email, pw0), email: email, id: u.ID.String()}
}

func (e *env) freshClient() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, http: &http.Client{Jar: jar}}
}

func (e *env) loginEmail(email, pw string) *client {
	e.t.Helper()
	c := e.freshClient()
	var out struct {
		CSRF string `json:"csrf_token"`
	}
	if st := c.do("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": pw}, &out); st != 200 || out.CSRF == "" {
		e.t.Fatalf("login %s: %d", email, st)
	}
	c.csrf = out.CSRF
	return c
}

type challenge struct {
	Required bool   `json:"mfa_required"`
	Token    string `json:"mfa_token"`
}

func (e *env) loginStep1(email, pw string) (challenge, int, http.Header, *client) {
	c := e.freshClient()
	var ch challenge
	st, h := c.doH("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": pw}, &ch, nil)
	return ch, st, h, c
}

func (e *env) verify(c *client, tok, code string) (int, map[string]any) {
	var out map[string]any
	st := c.do("POST", "/api/v1/auth/login/verify", map[string]string{"mfa_token": tok, "code": code}, &out)
	if st == 200 {
		c.csrf, _ = out["csrf_token"].(string)
	}
	return st, out
}

// enableMFA menyiapkan 2FA untuk pengguna; mengembalikan rahasia TOTP dan kode pemulihan.
func (e *env) enableMFA(u userCtx) (string, []string) {
	e.t.Helper()
	var setup struct {
		Secret string `json:"secret"`
		URI    string `json:"otpauth_uri"`
	}
	if st := u.c.do("POST", "/api/v1/auth/2fa/setup", map[string]string{"password": pw0}, &setup); st != 200 {
		e.t.Fatalf("setup: %d", st)
	}
	var en struct {
		Codes []string `json:"recovery_codes"`
	}
	if st := u.c.do("POST", "/api/v1/auth/2fa/enable", map[string]string{"code": totp(setup.Secret, time.Now())}, &en); st != 200 {
		e.t.Fatalf("enable: %d", st)
	}
	return setup.Secret, en.Codes
}

func problemType(c *client, method, path string, body any) (int, string) {
	var p struct{ Type string }
	st := c.do(method, path, body, &p)
	return st, p.Type
}

// ================= 2FA =================

func TestMFASetupEnableAndLoginFlow(t *testing.T) {
	e := newEnv(t)
	u := e.newUser("admin")
	if st, _ := problemType(u.c, "POST", "/api/v1/auth/2fa/setup", map[string]string{"password": "salah-salah-123"}); st != 401 {
		t.Fatalf("setup dengan password salah harus 401: %d", st)
	}
	if st, ty := problemType(u.c, "POST", "/api/v1/auth/2fa/enable", map[string]string{"code": "123456"}); st != 409 || ty != "mfa.no_pending_setup" {
		t.Fatalf("enable tanpa setup: %d %s", st, ty)
	}
	var setup struct {
		Secret string `json:"secret"`
		URI    string `json:"otpauth_uri"`
	}
	if st := u.c.do("POST", "/api/v1/auth/2fa/setup", map[string]string{"password": pw0}, &setup); st != 200 {
		t.Fatalf("setup: %d", st)
	}
	if len(setup.Secret) != 32 || !strings.HasPrefix(setup.URI, "otpauth://totp/") || !strings.Contains(setup.URI, "secret="+setup.Secret) {
		t.Fatalf("setup: %+v", setup)
	}
	// 2FA belum aktif sampai dikonfirmasi: login masih tanpa tantangan
	if ch, st, _, _ := e.loginStep1(u.email, pw0); st != 200 || ch.Required {
		t.Fatalf("sebelum enable tidak boleh ada tantangan: %d %+v", st, ch)
	}
	if st, _ := problemType(u.c, "POST", "/api/v1/auth/2fa/enable", map[string]string{"code": "000000"}); st != 422 {
		t.Fatalf("kode salah harus 422: %d", st)
	}
	var en struct {
		Codes []string `json:"recovery_codes"`
	}
	if st := u.c.do("POST", "/api/v1/auth/2fa/enable", map[string]string{"code": totp(setup.Secret, time.Now())}, &en); st != 200 || len(en.Codes) != 10 {
		t.Fatalf("enable: %d %v", st, en.Codes)
	}
	var stt struct {
		Enabled   bool `json:"enabled"`
		Remaining int  `json:"recovery_codes_remaining"`
	}
	u.c.do("GET", "/api/v1/auth/2fa", nil, &stt)
	if !stt.Enabled || stt.Remaining != 10 {
		t.Fatalf("status: %+v", stt)
	}
	if st, ty := problemType(u.c, "POST", "/api/v1/auth/2fa/setup", map[string]string{"password": pw0}); st != 409 || ty != "mfa.already_enabled" {
		t.Fatalf("setup saat sudah aktif: %d %s", st, ty)
	}
	// rahasia terenkripsi di database
	var blob []byte
	_ = e.pool.QueryRow(context.Background(), `SELECT totp_secret FROM users WHERE id=$1`, u.id).Scan(&blob)
	if len(blob) == 0 || strings.Contains(string(blob), setup.Secret) {
		t.Fatal("rahasia TOTP tidak boleh tersimpan sebagai teks")
	}

	// login: password benar TIDAK memberi sesi
	ch, st, hdr, c2 := e.loginStep1(u.email, pw0)
	if st != 200 || !ch.Required || ch.Token == "" {
		t.Fatalf("harus ada tantangan: %d %+v", st, ch)
	}
	if strings.Contains(strings.Join(hdr.Values("Set-Cookie"), ";"), "smsc_session") {
		t.Fatal("tidak boleh ada cookie sesi sebelum kode diverifikasi")
	}
	if st := c2.do("GET", "/api/v1/auth/me", nil, nil); st != 401 {
		t.Fatalf("tanpa sesi harus 401: %d", st)
	}
	if st, _ := e.verify(c2, ch.Token, "000000"); st != 401 {
		t.Fatalf("kode salah: %d", st)
	}
	if st, _ := e.verify(c2, "sampah.sampah", totp(setup.Secret, time.Now())); st != 401 {
		t.Fatalf("token sampah: %d", st)
	}
	tampered := ch.Token[:len(ch.Token)-2] + "AA"
	if st, _ := e.verify(c2, tampered, totp(setup.Secret, time.Now())); st != 401 {
		t.Fatalf("token diubah: %d", st)
	}
	good := totp(setup.Secret, time.Now())
	if st, _ := e.verify(c2, ch.Token, good); st != 401 {
		t.Fatalf("kode yang dipakai saat enable tidak boleh dipakai lagi untuk login: %d", st)
	}
	e.sql(`UPDATE users SET totp_last_step=0 WHERE id=$1`, u.id) // simulasi waktu berlalu (langkah 30 dtk baru)
	st, out := e.verify(c2, ch.Token, good)
	if st != 200 || out["mfa_enabled"] != true {
		t.Fatalf("verify: %d %v", st, out)
	}
	var me struct {
		MFA bool `json:"mfa_enabled"`
	}
	if st := c2.do("GET", "/api/v1/auth/me", nil, &me); st != 200 || !me.MFA {
		t.Fatalf("me setelah verify: %d %+v", st, me)
	}
	// replay: kode yang sama tidak boleh dipakai lagi (login kedua)
	ch2, _, _, c3 := e.loginStep1(u.email, pw0)
	if st, _ := e.verify(c3, ch2.Token, good); st != 401 {
		t.Fatalf("replay kode TOTP harus ditolak: %d", st)
	}
	if st, _ := e.verify(c3, ch2.Token, totp(setup.Secret, time.Now().Add(-30*time.Second))); st != 401 {
		t.Fatalf("kode langkah lebih lama dari yang terakhir dipakai harus ditolak: %d", st)
	}
	if st, _ := e.verify(c3, ch2.Token, totp(setup.Secret, time.Now().Add(30*time.Second))); st != 200 {
		t.Fatalf("kode langkah berikutnya harus diterima: %d", st)
	}
	// admin melihat status 2FA di daftar pengguna
	adm := e.loginAs("admin")
	var list struct {
		Items []struct {
			Email string
			MFA   bool `json:"mfa_enabled"`
		}
	}
	adm.do("GET", "/api/v1/users", nil, &list)
	found := false
	for _, it := range list.Items {
		if it.Email == u.email {
			found = it.MFA
		}
	}
	if !found {
		t.Fatal("daftar pengguna harus menampilkan mfa_enabled")
	}
}

func TestMFARecoveryCodesSingleUse(t *testing.T) {
	e := newEnv(t)
	u := e.newUser("operator")
	_, codes := e.enableMFA(u)
	if len(codes) != 10 {
		t.Fatal(codes)
	}
	use := func(code string) int {
		ch, _, _, c := e.loginStep1(u.email, pw0)
		st, _ := e.verify(c, ch.Token, code)
		return st
	}
	if st := use(strings.ToLower(strings.ReplaceAll(codes[0], "-", ""))); st != 200 {
		t.Fatalf("kode pemulihan (huruf kecil tanpa tanda hubung) harus diterima: %d", st)
	}
	if st := use(codes[0]); st != 401 {
		t.Fatalf("kode pemulihan hanya sekali pakai: %d", st)
	}
	if st := use(codes[1]); st != 200 {
		t.Fatalf("kode lain harus tetap sah: %d", st)
	}
	var stt struct {
		Remaining int `json:"recovery_codes_remaining"`
	}
	u.c.do("GET", "/api/v1/auth/2fa", nil, &stt)
	if stt.Remaining != 8 {
		t.Fatalf("sisa kode: %d", stt.Remaining)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='auth.recovery_code_used'`).Scan(&n)
	if n != 2 {
		t.Fatalf("pemakaian kode pemulihan harus diaudit: %d", n)
	}
	// kode tersimpan sebagai hash
	var raw string
	_ = e.pool.QueryRow(context.Background(), `SELECT string_agg(encode(code_hash,'hex'),',') FROM user_recovery_codes`).Scan(&raw)
	if strings.Contains(strings.ToUpper(raw), strings.ReplaceAll(codes[2], "-", "")) {
		t.Fatal("kode pemulihan tidak boleh tersimpan polos")
	}
	// terbitkan ulang: kode lama hangus
	secret := "" // butuh kode TOTP aktual: ambil dari DB lewat mekanisme tes ini dengan kode pemulihan sisa
	_ = secret
	var out struct {
		Codes []string `json:"recovery_codes"`
	}
	if st := u.c.do("POST", "/api/v1/auth/2fa/recovery-codes", map[string]string{"password": pw0, "code": codes[2]}, &out); st != 200 || len(out.Codes) != 10 {
		t.Fatalf("terbitkan ulang: %d", st)
	}
	if st := use(codes[3]); st != 401 {
		t.Fatalf("kode lama harus hangus setelah diterbitkan ulang: %d", st)
	}
	if st := use(out.Codes[0]); st != 200 {
		t.Fatalf("kode baru harus sah: %d", st)
	}
	if st, _ := problemType(u.c, "POST", "/api/v1/auth/2fa/recovery-codes", map[string]string{"password": "salah-salah-123", "code": out.Codes[1]}); st != 401 {
		t.Fatalf("password salah: %d", st)
	}
}

func TestMFAWrongCodesLockAccount(t *testing.T) {
	e := newEnv(t)
	u := e.newUser("admin")
	secret, _ := e.enableMFA(u)
	ch, _, _, c := e.loginStep1(u.email, pw0)
	for i := 0; i < 5; i++ {
		if st, _ := e.verify(c, ch.Token, "000000"); st != 401 {
			t.Fatalf("percobaan %d: %d", i, st)
		}
	}
	// kode BENAR pun ditolak saat terkunci; password pun tidak lagi diproses
	if st, _ := e.verify(c, ch.Token, totp(secret, time.Now())); st != 423 {
		t.Fatalf("akun harus terkunci (423): %d", st)
	}
	if _, st, _, _ := e.loginStep1(u.email, pw0); st != 423 {
		t.Fatalf("login harus 423 saat terkunci: %d", st)
	}
	// menebak kode berulang lewat login ulang tidak mereset penghitung
	e.sql(`UPDATE users SET locked_until=NULL, failed_logins=3 WHERE id=$1`, u.id)
	ch, _, _, c = e.loginStep1(u.email, pw0)
	var f int
	_ = e.pool.QueryRow(context.Background(), `SELECT failed_logins FROM users WHERE id=$1`, u.id).Scan(&f)
	if f != 3 {
		t.Fatalf("password benar tidak boleh mereset penghitung saat 2FA aktif: %d", f)
	}
	if st, _ := e.verify(c, ch.Token, totp(secret, time.Now().Add(30*time.Second))); st != 200 {
		t.Fatalf("kode benar: %d", st)
	}
	_ = e.pool.QueryRow(context.Background(), `SELECT failed_logins FROM users WHERE id=$1`, u.id).Scan(&f)
	if f != 0 {
		t.Fatalf("keberhasilan penuh harus mereset: %d", f)
	}
}

func TestMFAChallengeInvalidatedByPasswordChange(t *testing.T) {
	e := newEnv(t)
	u := e.newUser("admin")
	secret, _ := e.enableMFA(u)
	ch, _, _, c := e.loginStep1(u.email, pw0)
	if st := u.c.do("POST", "/api/v1/auth/password", map[string]string{"current_password": pw0, "new_password": "password-baru-456"}, nil); st != 204 {
		t.Fatalf("ganti password: %d", st)
	}
	if st, _ := e.verify(c, ch.Token, totp(secret, time.Now().Add(30*time.Second))); st != 401 {
		t.Fatalf("tantangan lama harus tidak berlaku setelah ganti password: %d", st)
	}
	// kedaluwarsa
	e.sql(`UPDATE users SET locked_until=NULL WHERE id=$1`, u.id)
	ch, _, _, c = e.loginStep1(u.email, "password-baru-456")
	exp := forgeExpired(ch.Token)
	if st, _ := e.verify(c, exp, totp(secret, time.Now().Add(60*time.Second))); st != 401 {
		t.Fatalf("token kedaluwarsa/dipalsukan: %d", st)
	}
}

// forgeExpired mengubah payload (tanpa kunci) — tanda tangan tidak lagi cocok, jadi harus ditolak.
func forgeExpired(tok string) string {
	a, b, _ := strings.Cut(tok, ".")
	return a + "x." + b
}

func TestMFADisableAndAdminReset(t *testing.T) {
	e := newEnv(t)
	u := e.newUser("operator")
	secret, _ := e.enableMFA(u)
	if st, ty := problemType(u.c, "POST", "/api/v1/auth/2fa/disable", map[string]string{"password": pw0, "code": "000000"}); st != 401 || ty != "auth.invalid_code" {
		t.Fatalf("disable dengan kode salah: %d %s", st, ty)
	}
	if st := u.c.do("POST", "/api/v1/auth/2fa/disable", map[string]string{"password": pw0, "code": totp(secret, time.Now().Add(30*time.Second))}, nil); st != 204 {
		t.Fatalf("disable: %d", st)
	}
	if ch, st, _, _ := e.loginStep1(u.email, pw0); st != 200 || ch.Required {
		t.Fatal("setelah dinonaktifkan login tanpa tantangan")
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM user_recovery_codes WHERE user_id=$1`, u.id).Scan(&n)
	if n != 0 {
		t.Fatal("kode pemulihan harus dihapus")
	}
	if st, ty := problemType(u.c, "POST", "/api/v1/auth/2fa/disable", map[string]string{"password": pw0, "code": "123456"}); st != 409 || ty != "mfa.not_enabled" {
		t.Fatalf("disable saat tidak aktif: %d %s", st, ty)
	}

	// admin mereset 2FA pengguna yang kehilangan perangkat: sesi pengguna dicabut
	v := e.newUser("viewer")
	e.enableMFA(v)
	ch, _, _, vc := e.loginStep1(v.email, pw0) // sesi hidup lewat pemulihan tak perlu: pakai sesi awal `v.c` yang dibuat sebelum 2FA
	_ = ch
	_ = vc
	adm := e.loginAs("admin")
	op := e.loginAs("operator")
	if st := op.do("DELETE", "/api/v1/users/"+v.id+"/2fa", nil, nil); st != 403 {
		t.Fatalf("operator tidak boleh mereset 2FA: %d", st)
	}
	if st := v.c.do("GET", "/api/v1/auth/me", nil, nil); st != 200 {
		t.Fatalf("sesi pengguna sebelum reset: %d", st)
	}
	if st := adm.do("DELETE", "/api/v1/users/"+v.id+"/2fa", nil, nil); st != 204 {
		t.Fatalf("reset: %d", st)
	}
	if st := adm.do("DELETE", "/api/v1/users/"+uuid.NewString()+"/2fa", nil, nil); st != 404 {
		t.Fatalf("reset pengguna tak ada: %d", st)
	}
	if st := v.c.do("GET", "/api/v1/auth/me", nil, nil); st != 401 {
		t.Fatalf("sesi pengguna harus dicabut setelah reset: %d", st)
	}
	if ch, st, _, _ := e.loginStep1(v.email, pw0); st != 200 || ch.Required {
		t.Fatal("setelah reset login tanpa tantangan")
	}
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action IN ('user.2fa_reset','auth.2fa_enable','auth.2fa_disable')`).Scan(&n)
	if n != 4 { // 2 enable + 1 disable + 1 reset
		t.Fatalf("audit 2FA: %d", n)
	}
}

func TestAccountEndpointsRejectAPIKeys(t *testing.T) {
	e := newEnv(t)
	adm := e.loginAs("admin")
	var k struct {
		Secret string `json:"secret"`
	}
	adm.do("POST", "/api/v1/api-keys", map[string]any{"name": "k"}, &k)
	kc := &client{e: e, http: &http.Client{}, key: k.Secret}
	for _, p := range [][2]string{{"GET", "/api/v1/auth/2fa"}, {"POST", "/api/v1/auth/password"}, {"POST", "/api/v1/auth/2fa/setup"}} {
		if st := kc.do(p[0], p[1], map[string]string{}, nil); st != 403 {
			t.Errorf("%s %s dengan API key harus 403: %d", p[0], p[1], st)
		}
	}
}

// ================= ganti password =================

func TestChangePassword(t *testing.T) {
	e := newEnv(t)
	u := e.newUser("admin")
	other := e.loginEmail(u.email, pw0) // sesi kedua pengguna yang sama
	if st := u.c.do("POST", "/api/v1/auth/password", map[string]string{"current_password": "salah-salah-123", "new_password": "password-baru-456"}, nil); st != 401 {
		t.Fatalf("password saat ini salah: %d", st)
	}
	if st := u.c.do("POST", "/api/v1/auth/password", map[string]string{"current_password": pw0, "new_password": "pendek"}, nil); st != 422 {
		t.Fatalf("terlalu pendek: %d", st)
	}
	if st := u.c.do("POST", "/api/v1/auth/password", map[string]string{"current_password": pw0, "new_password": pw0}, nil); st != 422 {
		t.Fatalf("sama dengan lama: %d", st)
	}
	if st := u.c.do("POST", "/api/v1/auth/password", map[string]string{"current_password": pw0, "new_password": "password-baru-456"}, nil); st != 204 {
		t.Fatalf("ganti: %d", st)
	}
	if st := u.c.do("GET", "/api/v1/auth/me", nil, nil); st != 200 {
		t.Fatalf("sesi saat ini harus tetap: %d", st)
	}
	if st := other.do("GET", "/api/v1/auth/me", nil, nil); st != 401 {
		t.Fatalf("sesi lain harus dicabut: %d", st)
	}
	c := e.freshClient()
	if st := c.do("POST", "/api/v1/auth/login", map[string]string{"email": u.email, "password": pw0}, nil); st != 401 {
		t.Fatalf("password lama harus gagal: %d", st)
	}
	e.loginEmail(u.email, "password-baru-456")
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='auth.password_change'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit: %d", n)
	}
}

// ================= rotasi API key =================

func TestAPIKeyRotationWithGrace(t *testing.T) {
	e := newEnv(t)
	adm := e.loginAs("admin")
	op := e.loginAs("operator")
	var k struct {
		ID, Secret, Prefix string
	}
	adm.do("POST", "/api/v1/api-keys", map[string]any{"name": "billing", "scopes": []string{"messages:read"}}, &k)
	use := func(secret string) int {
		kc := &client{e: e, http: &http.Client{}, key: secret}
		return kc.do("GET", "/api/v1/messages?limit=1", nil, nil)
	}
	if use(k.Secret) != 200 {
		t.Fatal("key awal harus bekerja")
	}
	if st := op.do("POST", "/api/v1/api-keys/"+k.ID+"/rotate", map[string]any{}, nil); st != 403 {
		t.Fatalf("operator: %d", st)
	}
	if st := adm.do("POST", "/api/v1/api-keys/"+k.ID+"/rotate", map[string]any{"grace_seconds": -1}, nil); st != 422 {
		t.Fatalf("grace negatif: %d", st)
	}
	if st := adm.do("POST", "/api/v1/api-keys/"+k.ID+"/rotate", map[string]any{"grace_seconds": 999999}, nil); st != 422 {
		t.Fatalf("grace terlalu besar: %d", st)
	}
	if st := adm.do("POST", "/api/v1/api-keys/"+uuid.NewString()+"/rotate", map[string]any{}, nil); st != 404 {
		t.Fatalf("tak ada: %d", st)
	}
	// dengan masa tumpang tindih: keduanya sah, lalu yang lama mati
	var r1 struct {
		ID, Secret, Prefix string
		PrevUntil          *time.Time `json:"previous_valid_until"`
		RotatedAt          *time.Time `json:"rotated_at"`
		Scopes             []string
	}
	if st := adm.do("POST", "/api/v1/api-keys/"+k.ID+"/rotate", map[string]any{"grace_seconds": 2}, &r1); st != 200 {
		t.Fatalf("rotate: %d", st)
	}
	if r1.ID != k.ID || r1.Secret == k.Secret || r1.Prefix == k.Prefix || !strings.HasPrefix(r1.Secret, "smsc_live_") || r1.PrevUntil == nil || r1.RotatedAt == nil || len(r1.Scopes) != 1 {
		t.Fatalf("hasil rotasi: %+v", r1)
	}
	if use(k.Secret) != 200 || use(r1.Secret) != 200 {
		t.Fatal("selama grace kedua secret harus sah")
	}
	var list struct {
		Items []struct {
			ID        string
			PrevUntil *time.Time `json:"previous_valid_until"`
		}
	}
	adm.do("GET", "/api/v1/api-keys", nil, &list)
	if len(list.Items) != 1 || list.Items[0].PrevUntil == nil {
		t.Fatalf("daftar harus menampilkan masa tumpang tindih: %+v", list)
	}
	time.Sleep(2300 * time.Millisecond)
	if use(k.Secret) != 401 {
		t.Fatal("secret lama harus mati setelah grace")
	}
	if use(r1.Secret) != 200 {
		t.Fatal("secret baru harus tetap sah")
	}
	// tanpa grace: lama langsung mati
	var r2 struct{ Secret string }
	adm.do("POST", "/api/v1/api-keys/"+k.ID+"/rotate", map[string]any{}, &r2)
	if use(r1.Secret) != 401 || use(r2.Secret) != 200 {
		t.Fatal("tanpa grace secret lama langsung mati")
	}
	var raw map[string]any
	adm.do("GET", "/api/v1/api-keys", nil, &raw)
	if strings.Contains(fmt.Sprint(raw), r2.Secret) {
		t.Fatal("daftar tidak boleh memuat secret")
	}
	// key yang sudah dicabut tidak bisa diputar
	adm.do("DELETE", "/api/v1/api-keys/"+k.ID, nil, nil)
	if st := adm.do("POST", "/api/v1/api-keys/"+k.ID+"/rotate", map[string]any{}, nil); st != 404 {
		t.Fatalf("key tercabut: %d", st)
	}
	if use(r2.Secret) != 401 {
		t.Fatal("key tercabut harus mati")
	}
}

// ================= rotasi secret webhook =================

func TestWebhookSecretRotationDualSignature(t *testing.T) {
	e := newEnv(t)
	adm := e.loginAs("admin")
	rcv := newReceiver(t)
	w := adm.newWebhook(rcv.srv.URL, "message.failed")
	if st := adm.do("POST", "/api/v1/webhooks/"+uuid.NewString()+"/rotate-secret", map[string]any{}, nil); st != 404 {
		t.Fatalf("tak ada: %d", st)
	}
	if st := adm.do("POST", "/api/v1/webhooks/"+w.ID+"/rotate-secret", map[string]any{"grace_seconds": 99999999}, nil); st != 422 {
		t.Fatalf("grace: %d", st)
	}
	var r struct {
		SigningSecret string     `json:"signing_secret"`
		PrevUntil     *time.Time `json:"previous_secret_valid_until"`
		RotatedAt     *time.Time `json:"secret_rotated_at"`
	}
	if st := adm.do("POST", "/api/v1/webhooks/"+w.ID+"/rotate-secret", map[string]any{"grace_seconds": 3}, &r); st != 200 {
		t.Fatalf("rotate: %d", st)
	}
	if r.SigningSecret == w.SigningSecret || !strings.HasPrefix(r.SigningSecret, "whsec_") || r.PrevUntil == nil || r.RotatedAt == nil {
		t.Fatalf("hasil: %+v", r)
	}
	var got struct {
		SigningSecret *string `json:"signing_secret"`
	}
	adm.do("GET", "/api/v1/webhooks/"+w.ID, nil, &got)
	if got.SigningSecret != nil {
		t.Fatal("GET tidak boleh membocorkan secret")
	}
	post := func() hit {
		before := rcv.count()
		adm.do("POST", "/api/v1/webhooks/"+w.ID+"/test", map[string]any{}, nil)
		waitFor(t, 8*time.Second, "event uji", func() bool { return rcv.count() > before })
		return rcv.all()[before]
	}
	anyValid := func(h hit, secret string) bool {
		for _, sig := range strings.Split(h.Header.Get("X-SMSC-Signature"), ",") {
			if hmac.Equal([]byte(core.Sign(secret, h.Header.Get("X-SMSC-Timestamp"), h.Body)), []byte(strings.TrimSpace(sig))) {
				return true
			}
		}
		return false
	}
	h := post()
	if n := len(strings.Split(h.Header.Get("X-SMSC-Signature"), ",")); n != 2 || !anyValid(h, r.SigningSecret) || !anyValid(h, w.SigningSecret) {
		t.Fatalf("selama grace harus ada dua tanda tangan (baru+lama): %q", h.Header.Get("X-SMSC-Signature"))
	}
	time.Sleep(3300 * time.Millisecond)
	h = post()
	if strings.Contains(h.Header.Get("X-SMSC-Signature"), ",") || !anyValid(h, r.SigningSecret) || anyValid(h, w.SigningSecret) {
		t.Fatalf("setelah grace hanya secret baru: %q", h.Header.Get("X-SMSC-Signature"))
	}
	// tanpa grace
	var r2 struct {
		SigningSecret string `json:"signing_secret"`
	}
	adm.do("POST", "/api/v1/webhooks/"+w.ID+"/rotate-secret", map[string]any{"grace_seconds": 0}, &r2)
	h = post()
	if strings.Contains(h.Header.Get("X-SMSC-Signature"), ",") || !anyValid(h, r2.SigningSecret) || anyValid(h, r.SigningSecret) {
		t.Fatal("grace 0: hanya secret terbaru")
	}
}

// ================= rotasi kredensial device =================

func (e *env) deviceAuth(secret string) int {
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/device/config", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func rotate(c *client, id string) (int, string) {
	var out struct {
		CommandID string `json:"command_id"`
	}
	st := c.do("POST", "/api/v1/devices/"+id+"/commands", map[string]string{"action": "rotate_credential"}, &out)
	return st, out.CommandID
}

func TestDeviceCredentialRotationHappyPath(t *testing.T) {
	e := newEnv(t)
	adm := e.loginAs("admin")
	id, old := e.enroll(adm)
	s, _ := e.connect(id, old, nil)
	if st, _ := rotate(adm, uuid.NewString()); st != 409 && st != 404 {
		t.Fatalf("device tak dikenal: %d", st)
	}
	if st, _ := rotate(adm, id.String()); st != 202 {
		t.Fatalf("rotate: %d", st)
	}
	var cmd proto.DeviceCommand
	waitFor(t, 5*time.Second, "device menerima perintah", func() bool { c, ok := s.LastCommand(); cmd = c; return ok })
	if cmd.Action != "rotate_credential" || cmd.NewSecret == nil || len(*cmd.NewSecret) < 32 || !strings.HasPrefix(*cmd.NewSecret, "smsd_") || *cmd.NewSecret == old {
		t.Fatalf("perintah: %+v", cmd)
	}
	fresh := *cmd.NewSecret
	// tahap 1: sebelum konfirmasi, secret lama masih satu-satunya yang sah; yang baru belum aktif tapi tertunda
	if e.deviceAuth(old) != 200 {
		t.Fatal("secret lama harus tetap sah sebelum konfirmasi")
	}
	s.Result(cmd, true, "")
	waitFor(t, 5*time.Second, "secret baru dipromosikan", func() bool {
		var n int
		_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM device_credentials WHERE device_id=$1 AND pending_secret_hash IS NULL AND prev_secret_hash IS NOT NULL`, id).Scan(&n)
		return n == 1
	})
	if e.deviceAuth(fresh) != 200 {
		t.Fatal("secret baru harus sah")
	}
	if e.deviceAuth(old) != 200 {
		t.Fatal("secret lama harus sah selama grace")
	}
	time.Sleep(1800 * time.Millisecond) // grace uji 1,5 dtk
	if e.deviceAuth(old) != 401 {
		t.Fatal("secret lama harus mati setelah grace")
	}
	if e.deviceAuth(fresh) != 200 {
		t.Fatal("secret baru tetap sah")
	}
	// secret baru tidak bocor ke audit
	var leaked int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE data::text LIKE '%'||$1||'%' OR actor LIKE '%'||$1||'%'`, fresh).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("secret tidak boleh muncul di audit")
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='device.credential_rotated'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit rotasi: %d", n)
	}
	// device boleh menyambung ulang dengan secret baru
	s2, _ := e.connect(id, fresh, nil)
	_ = s2
}

func TestDeviceCredentialRotationFailureAndEdgeCases(t *testing.T) {
	e := newEnv(t)
	adm := e.loginAs("admin")
	id, old := e.enroll(adm)

	// device offline
	if st, _ := rotate(adm, id.String()); st != 409 {
		t.Fatalf("offline harus 409: %d", st)
	}
	var pend int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM device_credentials WHERE pending_secret_hash IS NOT NULL`).Scan(&pend)
	if pend != 0 {
		t.Fatal("tidak boleh ada secret tertunda bila perintah tak terkirim")
	}
	s, _ := e.connect(id, old, nil)

	// device gagal menyimpan → ok=false → secret baru dibuang, lama tetap
	if st, _ := rotate(adm, id.String()); st != 202 {
		t.Fatal(st)
	}
	var cmd proto.DeviceCommand
	waitFor(t, 5*time.Second, "perintah 1", func() bool { c, ok := s.LastCommand(); cmd = c; return ok })
	s.Result(cmd, false, "keystore gagal")
	waitFor(t, 5*time.Second, "pending dibuang", func() bool {
		_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM device_credentials WHERE pending_secret_hash IS NOT NULL`).Scan(&pend)
		return pend == 0
	})
	if e.deviceAuth(*cmd.NewSecret) != 401 || e.deviceAuth(old) != 200 {
		t.Fatal("setelah gagal: secret baru ditolak, lama tetap sah")
	}

	// konfirmasi hilang di jalan, device langsung memakai secret baru → dipromosikan saat pemakaian pertama
	s.Commands = nil
	if st, _ := rotate(adm, id.String()); st != 202 {
		t.Fatal(st)
	}
	waitFor(t, 5*time.Second, "perintah 2", func() bool { c, ok := s.LastCommand(); cmd = c; return ok })
	fresh := *cmd.NewSecret
	if e.deviceAuth(fresh) != 200 {
		t.Fatal("secret baru yang dipakai sebelum konfirmasi harus diterima (anti-terkunci)")
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM device_credentials WHERE pending_secret_hash IS NULL AND secret_hash=$1`, sha256Bytes(fresh)).Scan(&n)
	if n != 1 {
		t.Fatal("secret baru harus menjadi aktif")
	}
	if e.deviceAuth(old) != 200 {
		t.Fatal("lama masih dalam grace")
	}
	// result ok terlambat untuk perintah yang sudah dipromosikan: tidak merusak
	s.Result(cmd, true, "")
	time.Sleep(300 * time.Millisecond)
	if e.deviceAuth(fresh) != 200 {
		t.Fatal("secret baru harus tetap sah setelah konfirmasi terlambat")
	}

	// secret tertunda kedaluwarsa
	time.Sleep(1700 * time.Millisecond)
	s.Commands = nil
	if st, _ := rotate(adm, id.String()); st != 202 {
		t.Fatal(st)
	}
	waitFor(t, 5*time.Second, "perintah 3", func() bool { c, ok := s.LastCommand(); cmd = c; return ok })
	e.sql(`UPDATE device_credentials SET pending_expires_at = now() - interval '1 second' WHERE device_id=$1`, id)
	if e.deviceAuth(*cmd.NewSecret) != 401 {
		t.Fatal("secret tertunda kedaluwarsa harus ditolak")
	}
	// konfirmasi dengan command_id lain tidak mempromosikan
	s.Commands = nil
	rotate(adm, id.String())
	waitFor(t, 5*time.Second, "perintah 4", func() bool { c, ok := s.LastCommand(); cmd = c; return ok })
	bogus := cmd
	bogus.CommandID = uuid.NewString()
	s.Result(bogus, true, "")
	time.Sleep(400 * time.Millisecond)
	if e.deviceAuth(*cmd.NewSecret) != 200 { // promosi karena dipakai langsung (bukan karena result palsu)
		t.Fatal("pemakaian langsung tetap mempromosikan")
	}

	// device dicabut: secret tertunda tidak berlaku
	id2, old2 := e.enroll(adm)
	s2, _ := e.connect(id2, old2, nil)
	rotate(adm, id2.String())
	var c2 proto.DeviceCommand
	waitFor(t, 5*time.Second, "perintah device 2", func() bool { c, ok := s2.LastCommand(); c2 = c; return ok })
	adm.do("DELETE", "/api/v1/devices/"+id2.String(), nil, nil)
	if e.deviceAuth(*c2.NewSecret) != 401 || e.deviceAuth(old2) != 401 {
		t.Fatal("device dicabut: tidak ada secret yang berlaku")
	}
}

func sha256Bytes(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }

// ================= rotasi kunci server =================

func TestSessionSecretRotationKeepsSessions(t *testing.T) {
	e := newEnv(t)
	u := e.newUser("admin")
	su, _ := url.Parse(e.srv.URL)
	var tok string
	for _, ck := range u.c.http.Jar.Cookies(su) {
		if ck.Name == "smsc_session" {
			tok = ck.Value
		}
	}
	if tok == "" {
		t.Fatal("cookie sesi tidak ditemukan")
	}
	mk := func(cur string, prev ...string) *core.Service {
		cfg := e.svc.Cfg
		cfg.SessionSecret, cfg.SessionSecretPrev = cur, prev
		return core.New(e.pool, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), bus.New(time.Minute))
	}
	newSecret := strings.Repeat("N", 40)
	if _, _, err := mk(newSecret).SessionUser(context.Background(), tok); err == nil {
		t.Fatal("tanpa SESSION_SECRET_PREVIOUS sesi lama harus tidak sah (logout massal)")
	}
	if usr, _, err := mk(newSecret, e.svc.Cfg.SessionSecret).SessionUser(context.Background(), tok); err != nil || usr.Email != u.email {
		t.Fatalf("dengan SESSION_SECRET_PREVIOUS sesi lama harus tetap sah: %v", err)
	}
}

func TestEncryptionKeyRotationAndReencrypt(t *testing.T) {
	e := newEnv(t)
	adm := e.loginAs("admin")
	u := e.newUser("operator")
	secret, _ := e.enableMFA(u)
	rcv := newReceiver(t)
	w := adm.newWebhook(rcv.srv.URL, "message.failed")
	adm.do("POST", "/api/v1/webhooks/"+w.ID+"/rotate-secret", map[string]any{"grace_seconds": 3600}, nil) // juga ada secret lama terenkripsi

	mk := func(key string, prev ...string) *core.Service {
		cfg := e.svc.Cfg
		cfg.WebhookKey, cfg.WebhookKeyPrev = key, prev
		return core.New(e.pool, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), bus.New(time.Minute))
	}
	ctx := context.Background()
	oldKey, newKey := e.svc.Cfg.WebhookKey, strings.Repeat("B", 40)

	// kunci baru TANPA kunci lama: tidak dapat membaca (rotasi salah dideteksi, bukan diam-diam)
	bad := mk(newKey)
	res, err := bad.Login(ctx, u.email, pw0)
	if err != nil || !res.MFARequired {
		t.Fatal(err)
	}
	if _, err := bad.LoginVerify(ctx, res.MFAToken, totp(secret, time.Now().Add(30*time.Second))); err == nil {
		t.Fatal("tanpa kunci lama rahasia TOTP tidak boleh terbaca")
	}
	// kunci baru + kunci lama: terbaca; re-encrypt menulis ulang
	mid := mk(newKey, oldKey)
	if _, err := mid.LoginVerify(ctx, res.MFAToken, totp(secret, time.Now().Add(30*time.Second))); err != nil {
		t.Fatalf("dengan kunci lama sebagai cadangan harus terbaca: %v", err)
	}
	wh, tp, err := mid.ReencryptSecrets(ctx)
	if err != nil || wh != 1 || tp != 1 {
		t.Fatalf("reencrypt: wh=%d tp=%d err=%v", wh, tp, err)
	}
	if wh, tp, _ := mid.ReencryptSecrets(ctx); wh != 0 || tp != 0 {
		t.Fatalf("idempoten: %d %d", wh, tp)
	}
	// kunci lama dibuang: tetap terbaca
	done := mk(newKey)
	e.sql(`UPDATE users SET totp_last_step=0 WHERE id=$1`, u.id)
	res2, _ := done.Login(ctx, u.email, pw0)
	if _, err := done.LoginVerify(ctx, res2.MFAToken, totp(secret, time.Now().Add(30*time.Second))); err != nil {
		t.Fatalf("setelah reencrypt kunci lama tidak lagi dibutuhkan: %v", err)
	}
	if _, err := done.TestWebhook(ctx, uuid.MustParse(w.ID)); err != nil {
		t.Fatal(err)
	}
	// kunci lama sendirian kini TIDAK bisa membuka (data sudah memakai kunci baru)
	var blob []byte
	_ = e.pool.QueryRow(ctx, `SELECT totp_secret FROM users WHERE id=$1`, u.id).Scan(&blob)
	e.sql(`UPDATE users SET totp_last_step=0 WHERE id=$1`, u.id)
	if _, err := mk(oldKey).LoginVerify(ctx, res2.MFAToken, totp(secret, time.Now().Add(30*time.Second))); err == nil {
		t.Fatal("kunci lama saja tidak boleh membuka data yang sudah ditulis ulang")
	}
}
