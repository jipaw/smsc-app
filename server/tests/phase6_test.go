package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type invResp struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	Token     string `json:"token"`
	InviteURL string `json:"invite_url"`
	CreatedBy string `json:"created_by"`
}

func (c *client) invite(email, role string, ttl int) (int, invResp) {
	var v invResp
	var raw json.RawMessage
	body := map[string]any{"email": email, "role": role}
	if ttl > 0 {
		body["ttl_hours"] = ttl
	}
	st := c.do("POST", "/api/v1/invitations", body, &raw)
	if st == 201 {
		_ = json.Unmarshal(raw, &v)
	}
	return st, v
}

func (c *client) invStatus(id string) string {
	var l struct{ Items []invResp }
	c.do("GET", "/api/v1/invitations", nil, &l)
	for _, it := range l.Items {
		if it.ID == id {
			return it.Status
		}
	}
	return ""
}

func anonPost(e *env, path string, body any, out any) int {
	return e.freshClient().do("POST", path, body, out)
}

func TestInvitationCreateValidationAndSecrecy(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	op := e.newUser("operator")
	if st, _ := op.c.invite("x@example.com", "viewer", 0); st != 403 {
		t.Fatalf("operator: %d", st)
	}
	for name, b := range map[string]map[string]any{
		"email buruk": {"email": "bukan-email", "role": "viewer"},
		"peran buruk": {"email": "a@example.com", "role": "dewa"},
		"ttl nol-neg": {"email": "a@example.com", "role": "viewer", "ttl_hours": -3},
		"ttl besar":   {"email": "a@example.com", "role": "viewer", "ttl_hours": 9999},
	} {
		if st := adm.c.do("POST", "/api/v1/invitations", b, nil); st != 422 {
			t.Errorf("%s: harus 422, dapat %d", name, st)
		}
	}
	if st, _ := adm.c.invite(op.email, "viewer", 0); st != 409 {
		t.Fatalf("email sudah menjadi pengguna: %d", st)
	}
	st, v := adm.c.invite("Baru@Example.com", "operator", 0)
	if st != 201 || v.Status != "pending" || v.Email != "baru@example.com" || v.CreatedBy != adm.email {
		t.Fatalf("buat: %d %+v", st, v)
	}
	if !strings.HasPrefix(v.Token, "inv_") || v.InviteURL != "http://example.test/invite/"+v.Token {
		t.Fatalf("token/url: %+v", v)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM invitations WHERE encode(token_hash,'escape') LIKE '%'||$1||'%'`, v.Token).Scan(&n)
	var raw string
	_ = e.pool.QueryRow(context.Background(), `SELECT string_agg(i::text,'') FROM invitations i`).Scan(&raw)
	if n != 0 || strings.Contains(raw, v.Token) {
		t.Fatal("token tidak boleh tersimpan polos")
	}
	var l map[string]any
	adm.c.do("GET", "/api/v1/invitations", nil, &l)
	if strings.Contains(strings.ToLower(toJSON(l)), v.Token) || strings.Contains(toJSON(l), "\"token\"") {
		t.Fatal("daftar tidak boleh memuat token")
	}
	if adm.c.invStatus(v.ID) != "pending" {
		t.Fatal("harus pending")
	}
}

func TestInvitationAcceptFlow(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	_, v := adm.c.invite("tamu@example.com", "operator", 0)
	anon := e.freshClient()

	var info struct {
		Email, Role string
	}
	if st := anon.do("POST", "/api/v1/invitations/lookup", map[string]string{"token": v.Token}, &info); st != 200 || info.Email != "tamu@example.com" || info.Role != "operator" {
		t.Fatalf("lookup: %d %+v", st, info)
	}
	if st, ty := problemType(anon, "POST", "/api/v1/invitations/lookup", map[string]string{"token": "inv_ngawur"}); st != 404 || ty != "invitation.invalid" {
		t.Fatalf("token salah: %d %s", st, ty)
	}
	// password pendek: ditolak dan undangan TIDAK terpakai
	if st := anon.do("POST", "/api/v1/invitations/accept", map[string]string{"token": v.Token, "password": "pendek"}, nil); st != 422 {
		t.Fatalf("password pendek: %d", st)
	}
	if st := anon.do("POST", "/api/v1/invitations/lookup", map[string]string{"token": v.Token}, nil); st != 200 {
		t.Fatalf("undangan harus masih berlaku: %d", st)
	}
	// konflik email muncul saat menerima (pengguna dibuat di sela-sela): 409 dan undangan tidak habis
	if _, err := e.svc.CreateUser(context.Background(), "tamu@example.com", "password-panjang-123", "viewer"); err != nil {
		t.Fatal(err)
	}
	if st := anon.do("POST", "/api/v1/invitations/accept", map[string]string{"token": v.Token, "password": "password-tamu-1234"}, nil); st != 409 {
		t.Fatalf("konflik email: %d", st)
	}
	if adm.c.invStatus(v.ID) != "pending" {
		t.Fatal("konflik tidak boleh menghabiskan undangan")
	}
	e.sql(`DELETE FROM users WHERE email='tamu@example.com'`)

	var out struct{ ID, Email, Role string }
	if st := anon.do("POST", "/api/v1/invitations/accept", map[string]string{"token": v.Token, "password": "password-tamu-1234"}, &out); st != 201 || out.Role != "operator" {
		t.Fatalf("accept: %d %+v", st, out)
	}
	if st := anon.do("POST", "/api/v1/invitations/accept", map[string]string{"token": v.Token, "password": "password-tamu-1234"}, nil); st != 404 {
		t.Fatalf("undangan sekali pakai: %d", st)
	}
	if st := anon.do("POST", "/api/v1/invitations/lookup", map[string]string{"token": v.Token}, nil); st != 404 {
		t.Fatalf("lookup setelah dipakai: %d", st)
	}
	if adm.c.invStatus(v.ID) != "accepted" {
		t.Fatal("status harus accepted")
	}
	guest := e.loginEmail("tamu@example.com", "password-tamu-1234") // password pilihan penerima
	if st := guest.do("POST", "/api/v1/templates", map[string]any{"name": "t1", "body": "x"}, nil); st != 201 {
		t.Fatalf("peran operator berlaku: %d", st)
	}
	if st := guest.do("GET", "/api/v1/users", nil, nil); st != 403 {
		t.Fatalf("bukan admin: %d", st)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action IN ('invitation.create','invitation.accept')`).Scan(&n)
	if n != 2 {
		t.Fatalf("audit: %d", n)
	}
}

func TestInvitationRevokeSupersedeExpire(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	anon := e.freshClient()
	look := func(tok string) int {
		return anon.do("POST", "/api/v1/invitations/lookup", map[string]string{"token": tok}, nil)
	}
	_, a := adm.c.invite("x@example.com", "viewer", 0)
	_, b := adm.c.invite("x@example.com", "admin", 0) // menggantikan a
	if look(a.Token) != 404 || look(b.Token) != 200 {
		t.Fatal("undangan baru untuk email yang sama harus mencabut yang lama")
	}
	if adm.c.invStatus(a.ID) != "revoked" || adm.c.invStatus(b.ID) != "pending" {
		t.Fatal("status")
	}
	if st := adm.c.do("DELETE", "/api/v1/invitations/"+b.ID, nil, nil); st != 204 {
		t.Fatalf("revoke: %d", st)
	}
	if look(b.Token) != 404 || anon.do("POST", "/api/v1/invitations/accept", map[string]string{"token": b.Token, "password": "password-panjang-123"}, nil) != 404 {
		t.Fatal("undangan dicabut tidak boleh bisa dipakai")
	}
	if st, ty := problemType(adm.c, "DELETE", "/api/v1/invitations/"+b.ID, nil); st != 409 || ty != "invitation.not_pending" {
		t.Fatalf("revoke ulang: %d %s", st, ty)
	}
	if st := adm.c.do("DELETE", "/api/v1/invitations/"+uuid.NewString(), nil, nil); st != 404 {
		t.Fatalf("tak ada: %d", st)
	}
	// kedaluwarsa
	_, c := adm.c.invite("y@example.com", "viewer", 1)
	e.sql(`UPDATE invitations SET expires_at = now() - interval '1 minute' WHERE id=$1`, c.ID)
	if look(c.Token) != 404 || adm.c.invStatus(c.ID) != "expired" {
		t.Fatal("kedaluwarsa")
	}
	if anon.do("POST", "/api/v1/invitations/accept", map[string]string{"token": c.Token, "password": "password-panjang-123"}, nil) != 404 {
		t.Fatal("tidak boleh menerima yang kedaluwarsa")
	}
	if st := adm.c.do("DELETE", "/api/v1/invitations/"+c.ID, nil, nil); st != 204 {
		t.Fatalf("yang kedaluwarsa boleh dicabut: %d", st)
	}
	// yang sudah diterima tidak bisa dicabut
	_, d := adm.c.invite("z@example.com", "viewer", 0)
	anon.do("POST", "/api/v1/invitations/accept", map[string]string{"token": d.Token, "password": "password-panjang-123"}, nil)
	if st, ty := problemType(adm.c, "DELETE", "/api/v1/invitations/"+d.ID, nil); st != 409 || ty != "invitation.not_pending" {
		t.Fatalf("revoke accepted: %d %s", st, ty)
	}
}

func TestInvitationConcurrentAcceptOnlyOneWins(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	_, v := adm.c.invite("race@example.com", "viewer", 0)
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- anonPost(e, "/api/v1/invitations/accept", map[string]string{"token": v.Token, "password": "password-panjang-123"}, nil)
		}()
	}
	wg.Wait()
	close(codes)
	ok := 0
	for c := range codes {
		if c == http.StatusCreated {
			ok++
		} else if c != 404 && c != 429 {
			t.Errorf("kode tak terduga: %d", c)
		}
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM users WHERE email='race@example.com'`).Scan(&n)
	if ok != 1 || n != 1 {
		t.Fatalf("tepat satu yang menang: ok=%d users=%d", ok, n)
	}
}

func TestInvitedUserSubjectToRequire2FA(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	e.enableMFA(adm)
	adm.c.do("PUT", "/api/v1/settings/security", map[string]string{"require_2fa": "admin"}, nil)
	_, v := adm.c.invite("admin3@example.com", "admin", 0)
	anonPost(e, "/api/v1/invitations/accept", map[string]string{"token": v.Token, "password": "password-panjang-123"}, nil)
	c := e.loginEmail("admin3@example.com", "password-panjang-123")
	if st, ty := problemType(c, "GET", "/api/v1/devices", nil); st != 403 || ty != "auth.mfa_setup_required" {
		t.Fatalf("admin undangan harus digerbang: %d %s", st, ty)
	}
}

// ================= hapus pengguna =================

func TestDeleteUserRequiresDisableAndKeepsAudit(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	op := e.newUser("operator")
	victim := e.newUser("admin")
	// objek yang dibuat korban harus tetap ada (created_by menjadi NULL)
	var k struct{ ID, Secret string }
	victim.c.do("POST", "/api/v1/api-keys", map[string]any{"name": "milik-korban", "scopes": []string{"messages:read"}}, &k)
	_, inv := victim.c.invite("anak@example.com", "viewer", 0)

	if st := op.c.do("DELETE", "/api/v1/users/"+victim.id, nil, nil); st != 403 {
		t.Fatalf("operator: %d", st)
	}
	if st, ty := problemType(adm.c, "DELETE", "/api/v1/users/"+adm.id, nil); st != 409 || ty != "user.self_change" {
		t.Fatalf("diri sendiri: %d %s", st, ty)
	}
	if st := adm.c.do("DELETE", "/api/v1/users/"+uuid.NewString(), nil, nil); st != 404 {
		t.Fatalf("tak ada: %d", st)
	}
	if st, ty := problemType(adm.c, "DELETE", "/api/v1/users/"+victim.id, nil); st != 409 || ty != "user.must_disable_first" {
		t.Fatalf("harus nonaktif dulu: %d %s", st, ty)
	}
	adm.c.do("PATCH", "/api/v1/users/"+victim.id, map[string]any{"disabled": true}, nil)
	if st := adm.c.do("DELETE", "/api/v1/users/"+victim.id, nil, nil); st != 204 {
		t.Fatalf("hapus: %d", st)
	}
	var list struct{ Items []struct{ Email string } }
	adm.c.do("GET", "/api/v1/users", nil, &list)
	for _, it := range list.Items {
		if it.Email == victim.email {
			t.Fatal("pengguna harus hilang dari daftar")
		}
	}
	if st := e.freshClient().do("POST", "/api/v1/auth/login", map[string]string{"email": victim.email, "password": pw0}, nil); st != 401 {
		t.Fatalf("login pengguna terhapus: %d", st)
	}
	kc := &client{e: e, http: &http.Client{}, key: k.Secret}
	if st := kc.do("GET", "/api/v1/messages?limit=1", nil, nil); st != 200 {
		t.Fatalf("API key milik pengguna terhapus tetap berfungsi: %d", st)
	}
	var cb *string
	_ = e.pool.QueryRow(context.Background(), `SELECT created_by::text FROM api_keys WHERE id=$1`, k.ID).Scan(&cb)
	if cb != nil {
		t.Fatal("created_by harus NULL")
	}
	if adm.c.invStatus(inv.ID) != "pending" {
		t.Fatal("undangan buatan pengguna terhapus tetap ada")
	}
	// audit tetap menyimpan email pelaku
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE actor LIKE '%'||$1||'%'`, victim.email).Scan(&n)
	var d string
	_ = e.pool.QueryRow(context.Background(), `SELECT data::text FROM audit_logs WHERE action='user.delete'`).Scan(&d)
	if n == 0 || !strings.Contains(d, victim.email) {
		t.Fatalf("audit harus tetap memuat email: n=%d data=%s", n, d)
	}
	// email dapat diundang dan didaftarkan ulang
	_, again := adm.c.invite(victim.email, "viewer", 0)
	if st := anonPost(e, "/api/v1/invitations/accept", map[string]string{"token": again.Token, "password": "password-panjang-123"}, nil); st != 201 {
		t.Fatalf("daftar ulang: %d", st)
	}
	_ = time.Now
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
