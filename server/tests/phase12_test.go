package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/jipaw/smsc-app/server/internal/config"
)

type scimC struct {
	e     *env
	tok   string
	admin userCtx
}

func newSCIM(t *testing.T, mods ...func(*config.SCIMConfig)) *scimC {
	t.Helper()
	e := newEnvOpts(t, func(c *config.Config) {
		c.SCIM = config.SCIMConfig{Enabled: true, DefaultRole: "viewer"}
		for _, m := range mods {
			m(&c.SCIM)
		}
	})
	adm := e.newUser("admin")
	var out struct{ Token string }
	if st := adm.c.do("POST", "/api/v1/scim-tokens", map[string]string{"name": "okta-prod"}, &out); st != 201 {
		t.Fatalf("buat token: %d", st)
	}
	return &scimC{e: e, tok: out.Token, admin: adm}
}

func (c *scimC) req(token, method, path string, body any, ct string) (int, map[string]any, http.Header) {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+"/scim/v2"+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, resp.Header
}

func (c *scimC) do(method, path string, body any) (int, map[string]any, http.Header) {
	return c.req(c.tok, method, path, body, "application/scim+json")
}

const (
	uSchema = "urn:ietf:params:scim:schemas:core:2.0:User"
	pSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
)

func (c *scimC) create(userName string, extra map[string]any) (int, map[string]any) {
	b := map[string]any{"schemas": []string{uSchema}, "userName": userName}
	for k, v := range extra {
		b[k] = v
	}
	st, m, _ := c.do("POST", "/Users", b)
	return st, m
}

func patch(ops ...map[string]any) map[string]any {
	return map[string]any{"schemas": []string{pSchema}, "Operations": ops}
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func roleOf(m map[string]any) string {
	rs, _ := m["roles"].([]any)
	if len(rs) == 0 {
		return ""
	}
	r, _ := rs[0].(map[string]any)
	return str(r, "value")
}

func TestSCIMDisabledByDefaultAndTokenLifecycle(t *testing.T) {
	e := newEnv(t) // SCIM mati
	adm := e.newUser("admin")
	c := &scimC{e: e, tok: "smsc_scim_apasaja"}
	if st, _, _ := c.do("GET", "/Users", nil); st != 404 {
		t.Fatalf("SCIM mati harus 404: %d", st)
	}
	if st, ty := problemType(adm.c, "POST", "/api/v1/scim-tokens", map[string]string{"name": "x"}); st != 409 || ty != "scim.not_enabled" {
		t.Fatalf("buat token saat mati: %d %s", st, ty)
	}
	var v struct {
		Enabled bool `json:"enabled"`
	}
	adm.c.do("GET", "/api/v1/settings/scim", nil, &v)
	if v.Enabled {
		t.Fatal("default mati")
	}

	s := newSCIM(t)
	op := s.e.newUser("operator")
	if st := op.c.do("POST", "/api/v1/scim-tokens", map[string]string{"name": "x"}, nil); st != 403 {
		t.Fatalf("operator: %d", st)
	}
	if st := s.admin.c.do("POST", "/api/v1/scim-tokens", map[string]string{"name": ""}, nil); st != 422 {
		t.Fatalf("nama kosong: %d", st)
	}
	// token tampil sekali; hanya hash di DB
	var n int
	_ = s.e.pool.QueryRow(context.Background(), `SELECT count(*) FROM scim_tokens WHERE encode(token_hash,'escape') LIKE '%'||$1||'%'`, s.tok).Scan(&n)
	var list map[string]any
	s.admin.c.do("GET", "/api/v1/scim-tokens", nil, &list)
	if n != 0 || strings.Contains(toJSON(list), s.tok) || !strings.HasPrefix(s.tok, "smsc_scim_") {
		t.Fatal("token tidak boleh tersimpan polos / muncul di daftar")
	}
	// autentikasi: tanpa token, token ngawur, API key, cookie sesi, token di query — semuanya ditolak
	for name, tok := range map[string]string{"tanpa": "", "ngawur": "smsc_scim_ngawur", "bukan smsc_scim": "smsc_live_abc"} {
		st, m, h := s.req(tok, "GET", "/Users", nil, "")
		if st != 401 || h.Get("WWW-Authenticate") == "" || m["status"] != "401" {
			t.Errorf("%s: %d %v", name, st, m)
		}
	}
	var k struct{ Secret string }
	s.admin.c.do("POST", "/api/v1/api-keys", map[string]any{"name": "k"}, &k)
	if st, _, _ := s.req(k.Secret, "GET", "/Users", nil, ""); st != 401 {
		t.Fatalf("API key tidak boleh dipakai di SCIM: %d", st)
	}
	req, _ := http.NewRequest("GET", s.e.srv.URL+"/scim/v2/Users", nil)
	su, _ := url.Parse(s.e.srv.URL)
	for _, ck := range s.admin.c.http.Jar.Cookies(su) {
		req.AddCookie(ck)
	}
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("sesi dashboard tidak boleh dipakai di SCIM: %d", resp.StatusCode)
	}
	if st, _, _ := s.req("", "GET", "/Users?access_token="+s.tok, nil, ""); st != 401 {
		t.Fatalf("token di query tidak diterima: %d", st)
	}
	// sebaliknya: token SCIM tidak berlaku di /api/v1
	hc := &client{e: s.e, http: &http.Client{}, key: s.tok}
	if st := hc.do("GET", "/api/v1/messages?limit=1", nil, nil); st != 401 {
		t.Fatalf("token SCIM di API REST: %d", st)
	}
	// valid + last_used; cabut → 401
	if st, _, _ := s.do("GET", "/Users", nil); st != 200 {
		t.Fatalf("token valid: %d", st)
	}
	var used *string
	_ = s.e.pool.QueryRow(context.Background(), `SELECT last_used_at::text FROM scim_tokens`).Scan(&used)
	if used == nil {
		t.Fatal("last_used_at harus diperbarui")
	}
	var tl struct{ Items []struct{ ID string } }
	s.admin.c.do("GET", "/api/v1/scim-tokens", nil, &tl)
	if st := s.admin.c.do("DELETE", "/api/v1/scim-tokens/"+tl.Items[0].ID, nil, nil); st != 204 {
		t.Fatal(st)
	}
	if st, _, _ := s.do("GET", "/Users", nil); st != 401 {
		t.Fatalf("token dicabut: %d", st)
	}
	if st := s.admin.c.do("DELETE", "/api/v1/scim-tokens/"+uuid.NewString(), nil, nil); st != 404 {
		t.Fatalf("tak ada: %d", st)
	}
}

func TestSCIMDiscovery(t *testing.T) {
	s := newSCIM(t)
	st, m, h := s.do("GET", "/ServiceProviderConfig", nil)
	if st != 200 || !strings.HasPrefix(h.Get("Content-Type"), "application/scim+json") {
		t.Fatalf("SPC: %d %s", st, h.Get("Content-Type"))
	}
	patchOK := m["patch"].(map[string]any)["supported"] == true
	bulk := m["bulk"].(map[string]any)["supported"] == false
	filt := m["filter"].(map[string]any)
	if !patchOK || !bulk || filt["supported"] != true || filt["maxResults"].(float64) != 100 || m["sort"].(map[string]any)["supported"] != false || m["etag"].(map[string]any)["supported"] != false {
		t.Fatalf("SPC isi: %v", m)
	}
	if as := m["authenticationSchemes"].([]any); len(as) != 1 || as[0].(map[string]any)["type"] != "oauthbearertoken" {
		t.Fatalf("authenticationSchemes: %v", as)
	}
	st, m, _ = s.do("GET", "/ResourceTypes", nil)
	if st != 200 || m["totalResults"].(float64) != 2 || m["Resources"].([]any)[0].(map[string]any)["endpoint"] != "/Users" || m["Resources"].([]any)[1].(map[string]any)["endpoint"] != "/Groups" {
		t.Fatalf("ResourceTypes: %d %v", st, m)
	}
	if st, _, _ := s.do("GET", "/ResourceTypes/User", nil); st != 200 {
		t.Fatal(st)
	}
	if st, _, _ := s.do("GET", "/ResourceTypes/Group", nil); st != 200 {
		t.Fatalf("Group: %d", st)
	}
	if st, _, _ := s.do("GET", "/ResourceTypes/Tim", nil); st != 404 {
		t.Fatalf("resource type tak ada: %d", st)
	}
	st, m, _ = s.do("GET", "/Schemas", nil)
	if st != 200 || m["Resources"].([]any)[0].(map[string]any)["id"] != uSchema {
		t.Fatalf("Schemas: %d", st)
	}
	if st, _, _ := s.do("GET", "/Schemas/"+uSchema, nil); st != 200 {
		t.Fatal(st)
	}
	if st, m, _ := s.do("GET", "/Groups", nil); st != 200 || m["totalResults"].(float64) != 0 {
		t.Fatalf("Groups: %d %v", st, m)
	}
	if st, _, _ := s.do("GET", "/Schemas/urn:ietf:params:scim:schemas:core:2.0:Group", nil); st != 200 {
		t.Fatalf("schema Group: %d", st)
	}
}

func TestSCIMCreateAndRead(t *testing.T) {
	s := newSCIM(t)
	st, m := s.create("Budi.Santoso@Contoh.ID", map[string]any{"externalId": "okta-123", "displayName": "Budi S", "name": map[string]any{"givenName": "Budi", "familyName": "Santoso"}})
	if st != 201 {
		t.Fatalf("create: %d %v", st, m)
	}
	id := str(m, "id")
	if str(m, "userName") != "budi.santoso@contoh.id" || m["active"] != true || roleOf(m) != "viewer" || str(m, "externalId") != "okta-123" || str(m, "displayName") != "Budi S" ||
		m["name"].(map[string]any)["givenName"] != "Budi" || m["meta"].(map[string]any)["resourceType"] != "User" || !strings.HasSuffix(str(m["meta"].(map[string]any), "location"), "/scim/v2/Users/"+id) {
		t.Fatalf("representasi: %v", m)
	}
	if e := m["emails"].([]any)[0].(map[string]any); e["value"] != "budi.santoso@contoh.id" || e["primary"] != true {
		t.Fatalf("emails: %v", e)
	}
	// Location header
	_, _, h := s.do("POST", "/Users", map[string]any{"schemas": []string{uSchema}, "userName": "dua@contoh.id"})
	if !strings.Contains(h.Get("Location"), "/scim/v2/Users/") {
		t.Fatalf("Location: %q", h.Get("Location"))
	}
	if st, g, _ := s.do("GET", "/Users/"+id, nil); st != 200 || str(g, "id") != id {
		t.Fatalf("get: %d", st)
	}
	// pengguna SSO-saja: tanpa password, tak bisa login lokal; terlihat di dashboard sebagai SCIM
	var hasPW bool
	var scimManaged bool
	_ = s.e.pool.QueryRow(context.Background(), `SELECT password_hash IS NOT NULL, scim_managed FROM users WHERE id=$1`, id).Scan(&hasPW, &scimManaged)
	if hasPW || !scimManaged {
		t.Fatalf("pw=%v scim=%v", hasPW, scimManaged)
	}
	if st := s.e.freshClient().do("POST", "/api/v1/auth/login", map[string]string{"email": "budi.santoso@contoh.id", "password": "kuda-biru-melompati-pagar-9"}, nil); st != 401 {
		t.Fatalf("login lokal: %d", st)
	}
	var ul struct {
		Items []struct {
			Email string
			SCIM  bool `json:"scim_managed"`
		}
	}
	s.admin.c.do("GET", "/api/v1/users", nil, &ul)
	ok := false
	for _, it := range ul.Items {
		ok = ok || (it.Email == "budi.santoso@contoh.id" && it.SCIM)
	}
	if !ok {
		t.Fatal("dashboard harus menandai pengguna SCIM")
	}
	// peran dari roles; admin ditolak secara default
	if st, m := s.create("op@contoh.id", map[string]any{"roles": []any{map[string]any{"value": "operator"}}}); st != 201 || roleOf(m) != "operator" {
		t.Fatalf("operator: %d %v", st, m)
	}
	if st, m := s.create("adm@contoh.id", map[string]any{"roles": []any{map[string]any{"value": "admin"}}}); st != 400 || m["scimType"] != "invalidValue" {
		t.Fatalf("admin harus ditolak: %d %v", st, m)
	}
	if st, m := s.create("x@contoh.id", map[string]any{"roles": []any{map[string]any{"value": "dewa"}}}); st != 400 || m["scimType"] != "invalidValue" {
		t.Fatalf("peran buruk: %d %v", st, m)
	}
	// nonaktif sejak awal
	if st, m := s.create("off@contoh.id", map[string]any{"active": false}); st != 201 || m["active"] != false {
		t.Fatalf("active=false: %d %v", st, m)
	}
	// validasi
	for name, body := range map[string]map[string]any{"userName bukan email": {"userName": "bukan-email"}, "tanpa userName": {}, "active bukan bool": {"userName": "a@contoh.id", "active": "mungkin"}} {
		if st, m := s.create("", nil); st != 400 {
			_ = m
		}
		b := map[string]any{"schemas": []string{uSchema}}
		for k, v := range body {
			b[k] = v
		}
		if st, m, _ := s.do("POST", "/Users", b); st != 400 || m["schemas"].([]any)[0] != "urn:ietf:params:scim:api:messages:2.0:Error" {
			t.Errorf("%s: %d %v", name, st, m)
		}
	}
	// unik
	if st, m := s.create("BUDI.santoso@contoh.id", nil); st != 409 || m["scimType"] != "uniqueness" {
		t.Fatalf("userName ganda: %d %v", st, m)
	}
	if st, m := s.create("lain@contoh.id", map[string]any{"externalId": "okta-123"}); st != 409 || m["scimType"] != "uniqueness" {
		t.Fatalf("externalId ganda: %d %v", st, m)
	}
	if st, _, _ := s.do("POST", "/Users", "{bukan json"); st != 400 {
		t.Fatalf("JSON rusak: %d", st)
	}
	// application/json biasa juga diterima
	if st, _, _ := s.req(s.tok, "POST", "/Users", map[string]any{"userName": "biasa@contoh.id"}, "application/json"); st != 201 {
		t.Fatalf("content-type application/json: %d", st)
	}
	if st, _, _ := s.do("GET", "/Users/bukan-uuid", nil); st != 404 {
		t.Fatal(st)
	}
	if st, _, _ := s.do("GET", "/Users/"+uuid.NewString(), nil); st != 404 {
		t.Fatal(st)
	}
	var n int
	_ = s.e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE actor='scim:okta-prod' AND action='scim.user_create'`).Scan(&n)
	if n < 4 {
		t.Fatalf("audit scim.user_create: %d", n)
	}
}

func TestSCIMListFilterPagination(t *testing.T) {
	s := newSCIM(t)
	for i := 0; i < 7; i++ {
		s.create(fmt.Sprintf("u%d@contoh.id", i), map[string]any{"externalId": fmt.Sprintf("ext-%d", i), "active": i%2 == 0})
	}
	list := func(q string) (int, map[string]any) { st, m, _ := s.do("GET", "/Users"+q, nil); return st, m }
	_, m := list("")
	if m["totalResults"].(float64) != 7 || len(m["Resources"].([]any)) != 7 || m["startIndex"].(float64) != 1 {
		t.Fatalf("semua: %v", m)
	}
	_, m = list("?startIndex=3&count=2")
	rs := m["Resources"].([]any)
	if m["totalResults"].(float64) != 7 || len(rs) != 2 || m["itemsPerPage"].(float64) != 2 || str(rs[0].(map[string]any), "userName") != "u2@contoh.id" {
		t.Fatalf("halaman: %v", m)
	}
	_, m = list("?count=0")
	if m["totalResults"].(float64) != 7 || len(m["Resources"].([]any)) != 0 {
		t.Fatalf("count=0: %v", m)
	}
	_, m = list("?startIndex=100")
	if len(m["Resources"].([]any)) != 0 || m["totalResults"].(float64) != 7 {
		t.Fatalf("lewat akhir: %v", m)
	}
	f := func(expr string) (int, map[string]any) { return list("?filter=" + url.QueryEscape(expr)) }
	if _, m := f(`userName eq "U3@Contoh.ID"`); m["totalResults"].(float64) != 1 {
		t.Fatalf("userName (tak peka huruf): %v", m)
	}
	if _, m := f(`externalId eq "ext-4"`); m["totalResults"].(float64) != 1 {
		t.Fatalf("externalId: %v", m)
	}
	if _, m := f(`active eq false`); m["totalResults"].(float64) != 3 {
		t.Fatalf("active eq false: %v", m)
	}
	if _, m := f(`active eq true and userName eq "u0@contoh.id"`); m["totalResults"].(float64) != 1 {
		t.Fatalf("and: %v", m)
	}
	if _, m := f(`active eq false and userName eq "u0@contoh.id"`); m["totalResults"].(float64) != 0 {
		t.Fatalf("and tidak cocok: %v", m)
	}
	if _, m := f(`emails.value eq "u1@contoh.id"`); m["totalResults"].(float64) != 1 {
		t.Fatalf("emails.value: %v", m)
	}
	if _, m := f(`userName eq "tidak@ada.id"`); m["totalResults"].(float64) != 0 || len(m["Resources"].([]any)) != 0 {
		t.Fatalf("kosong harus 200 dengan 0 hasil: %v", m)
	}
	for _, bad := range []string{`userName co "u"`, `userName sw "u"`, `userName pr`, `userName eq "a" or userName eq "b"`, `(userName eq "a")`, `name.givenName eq "x"`, `userName eq "belum ditutup`, `userName eq`} {
		if st, m := f(bad); st != 400 || m["scimType"] != "invalidFilter" {
			t.Errorf("filter %q harus 400 invalidFilter: %d %v", bad, st, m)
		}
	}
	if st, _, _ := s.do("GET", "/Users?sortBy=userName", nil); st != 400 {
		t.Fatalf("sortBy: %d", st)
	}
	// count dibatasi 100
	_, m = list("?count=100000")
	if len(m["Resources"].([]any)) != 7 {
		t.Fatal("count besar harus dibatasi dan tetap valid")
	}
}

func TestSCIMLocalUsersAreInvisibleAndImmutableByDefault(t *testing.T) {
	s := newSCIM(t)
	local := s.e.newUser("operator")
	if st, m, _ := s.do("GET", "/Users?filter="+url.QueryEscape(fmt.Sprintf(`userName eq "%s"`, local.email)), nil); st != 200 || m["totalResults"].(float64) != 0 {
		t.Fatalf("pengguna lokal tidak boleh terlihat: %d %v", st, m)
	}
	if st, _ := s.create(local.email, nil); st != 409 {
		t.Fatalf("POST userName milik pengguna lokal: %d", st)
	}
	for _, c := range []struct {
		m    string
		body any
	}{
		{"GET", nil}, {"PUT", map[string]any{"schemas": []string{uSchema}, "userName": "x@contoh.id"}},
		{"PATCH", patch(map[string]any{"op": "replace", "path": "active", "value": false})}, {"DELETE", nil},
	} {
		if st, _, _ := s.do(c.m, "/Users/"+local.id, c.body); st != 404 {
			t.Errorf("%s pengguna lokal harus 404: %d", c.m, st)
		}
	}
	var active bool
	_ = s.e.pool.QueryRow(context.Background(), `SELECT NOT disabled FROM users WHERE id=$1`, local.id).Scan(&active)
	if !active || local.c.sessionEmail() != local.email {
		t.Fatal("pengguna lokal tidak boleh tersentuh")
	}
}

func TestSCIMAdoptExistingOptIn(t *testing.T) {
	s := newSCIM(t, func(c *config.SCIMConfig) { c.AdoptExisting = true })
	local := s.e.newUser("operator")
	st, m, _ := s.do("GET", "/Users/"+local.id, nil)
	if st != 200 || roleOf(m) != "operator" {
		t.Fatalf("mode adopt: pengguna lokal terlihat: %d", st)
	}
	st, m, _ = s.do("PATCH", "/Users/"+local.id, patch(map[string]any{"op": "replace", "path": "externalId", "value": "adopted-1"}))
	if st != 200 || str(m, "externalId") != "adopted-1" {
		t.Fatalf("adopsi: %d %v", st, m)
	}
	var managed bool
	_ = s.e.pool.QueryRow(context.Background(), `SELECT scim_managed FROM users WHERE id=$1`, local.id).Scan(&managed)
	var d string
	_ = s.e.pool.QueryRow(context.Background(), `SELECT data::text FROM audit_logs WHERE action='scim.user_update' ORDER BY at DESC LIMIT 1`).Scan(&d)
	if !managed || !strings.Contains(d, "adopted") {
		t.Fatalf("managed=%v audit=%s", managed, d)
	}
}

func TestSCIMReplaceAndPatch(t *testing.T) {
	s := newSCIM(t)
	_, m := s.create("ganti@contoh.id", map[string]any{"externalId": "e1", "displayName": "Lama", "name": map[string]any{"givenName": "A", "familyName": "B"}, "roles": []any{map[string]any{"value": "operator"}}})
	id := str(m, "id")
	// PUT: atribut yang tak disebut dikosongkan, active tetap, peran kembali ke bawaan
	st, m, _ := s.do("PUT", "/Users/"+id, map[string]any{"schemas": []string{uSchema}, "userName": "ganti2@contoh.id", "displayName": "Baru"})
	if st != 200 || str(m, "userName") != "ganti2@contoh.id" || str(m, "displayName") != "Baru" || m["externalId"] != nil || m["name"] != nil || roleOf(m) != "viewer" || m["active"] != true {
		t.Fatalf("PUT: %d %v", st, m)
	}
	// PUT tanpa userName → 400; userName bentrok → 409
	if st, _, _ := s.do("PUT", "/Users/"+id, map[string]any{"schemas": []string{uSchema}}); st != 400 {
		t.Fatalf("PUT tanpa userName: %d", st)
	}
	s.create("lain@contoh.id", nil)
	if st, m, _ := s.do("PUT", "/Users/"+id, map[string]any{"schemas": []string{uSchema}, "userName": "lain@contoh.id"}); st != 409 || m["scimType"] != "uniqueness" {
		t.Fatalf("PUT bentrok: %d %v", st, m)
	}
	// PATCH gaya Azure AD: op kapital, boolean string
	st, m, _ = s.do("PATCH", "/Users/"+id, patch(
		map[string]any{"op": "Replace", "path": "active", "value": "False"},
		map[string]any{"op": "Add", "path": "name.givenName", "value": "Budi"},
		map[string]any{"op": "Replace", "path": `emails[type eq "work"].value`, "value": "diabaikan@contoh.id"},
		map[string]any{"op": "Replace", "path": "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:department", "value": "TI"},
	))
	if st != 200 || m["active"] != false || m["name"].(map[string]any)["givenName"] != "Budi" || str(m, "userName") != "ganti2@contoh.id" {
		t.Fatalf("PATCH azure: %d %v", st, m)
	}
	// PATCH gaya Okta: tanpa path, value objek
	st, m, _ = s.do("PATCH", "/Users/"+id, patch(map[string]any{"op": "replace", "value": map[string]any{"active": true, "displayName": "Okta", "name": map[string]any{"familyName": "Santoso"}, "roles": []any{map[string]any{"value": "operator"}}}}))
	if st != 200 || m["active"] != true || str(m, "displayName") != "Okta" || m["name"].(map[string]any)["familyName"] != "Santoso" || roleOf(m) != "operator" {
		t.Fatalf("PATCH okta: %d %v", st, m)
	}
	// remove
	st, m, _ = s.do("PATCH", "/Users/"+id, patch(map[string]any{"op": "remove", "path": "roles"}, map[string]any{"op": "remove", "path": "displayName"}))
	if st != 200 || roleOf(m) != "viewer" || m["displayName"] != nil {
		t.Fatalf("PATCH remove: %d %v", st, m)
	}
	// userName & externalId lewat PATCH
	st, m, _ = s.do("PATCH", "/Users/"+id, patch(map[string]any{"op": "replace", "path": "userName", "value": "final@contoh.id"}, map[string]any{"op": "add", "path": "externalId", "value": "ext-final"}))
	if st != 200 || str(m, "userName") != "final@contoh.id" || str(m, "externalId") != "ext-final" {
		t.Fatalf("PATCH userName: %d %v", st, m)
	}
	// galat
	for name, body := range map[string]any{
		"op ngawur":           patch(map[string]any{"op": "move", "path": "active", "value": true}),
		"path tak dikenal":    patch(map[string]any{"op": "replace", "path": "passwordHash", "value": "x"}),
		"tanpa value":         patch(map[string]any{"op": "replace", "path": "active"}),
		"tanpa path & skalar": patch(map[string]any{"op": "replace", "value": "x"}),
		"active bukan bool":   patch(map[string]any{"op": "replace", "path": "active", "value": "mungkin"}),
		"hapus active":        patch(map[string]any{"op": "remove", "path": "active"}),
		"peran admin":         patch(map[string]any{"op": "replace", "path": "roles", "value": []any{map[string]any{"value": "admin"}}}),
		"skema salah":         map[string]any{"schemas": []string{"x"}, "Operations": []any{map[string]any{"op": "replace", "path": "active", "value": true}}},
		"tanpa Operations":    map[string]any{"schemas": []string{pSchema}},
	} {
		if st, m, _ := s.do("PATCH", "/Users/"+id, body); st != 400 || m["status"] != "400" {
			t.Errorf("%s: harus 400: %d %v", name, st, m)
		}
	}
	// PUT parsial tidak boleh mengaktifkan kembali akun yang dinonaktifkan
	s.do("PATCH", "/Users/"+id, patch(map[string]any{"op": "replace", "path": "active", "value": false}))
	st, m, _ = s.do("PUT", "/Users/"+id, map[string]any{"schemas": []string{uSchema}, "userName": "final@contoh.id"})
	if st != 200 || m["active"] != false {
		t.Fatalf("PUT tanpa active tidak boleh mengaktifkan ulang: %d %v", st, m)
	}
}

func TestSCIMDeactivateRevokesSessionsAndDelete(t *testing.T) {
	s := newSCIM(t)
	_, m := s.create("sesi@contoh.id", nil)
	id := str(m, "id")
	// beri password lewat admin agar bisa membuat sesi; SCIM tidak menyentuh password
	if st := s.admin.c.do("POST", "/api/v1/users/"+id+"/password", map[string]string{"new_password": "kuda-biru-melompati-pagar-9"}, nil); st != 204 {
		t.Fatal(st)
	}
	c := s.e.loginEmail("sesi@contoh.id", "kuda-biru-melompati-pagar-9")
	if c.sessionEmail() == "" {
		t.Fatal("sesi harus hidup")
	}
	if st, _, _ := s.do("PATCH", "/Users/"+id, patch(map[string]any{"op": "replace", "path": "active", "value": false})); st != 200 {
		t.Fatal(st)
	}
	if c.sessionEmail() != "" {
		t.Fatal("deaktivasi harus mencabut sesi seketika")
	}
	if st := s.e.freshClient().do("POST", "/api/v1/auth/login", map[string]string{"email": "sesi@contoh.id", "password": "kuda-biru-melompati-pagar-9"}, nil); st != 401 {
		t.Fatalf("login nonaktif: %d", st)
	}
	var n int
	_ = s.e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='scim.user_deactivate'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit deaktivasi: %d", n)
	}
	// aktifkan kembali
	if st, m, _ := s.do("PATCH", "/Users/"+id, patch(map[string]any{"op": "replace", "path": "active", "value": true})); st != 200 || m["active"] != true {
		t.Fatal(st)
	}
	// delete
	if st, _, _ := s.do("DELETE", "/Users/"+id, nil); st != 204 {
		t.Fatalf("delete: %d", st)
	}
	if st, _, _ := s.do("GET", "/Users/"+id, nil); st != 404 {
		t.Fatalf("setelah delete: %d", st)
	}
	if st, _, _ := s.do("DELETE", "/Users/"+id, nil); st != 404 {
		t.Fatalf("delete ulang: %d", st)
	}
	_ = s.e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='scim.user_delete'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit delete: %d", n)
	}
	// userName bisa dipakai lagi setelah dihapus
	if st, _ := s.create("sesi@contoh.id", nil); st != 201 {
		t.Fatalf("buat ulang: %d", st)
	}
}

func TestSCIMAdminGuards(t *testing.T) {
	s := newSCIM(t, func(c *config.SCIMConfig) { c.AllowAdminRole = true })
	adminRole := []any{map[string]any{"value": "admin"}}
	st, m := s.create("scimadmin@contoh.id", map[string]any{"roles": adminRole})
	if st != 201 || roleOf(m) != "admin" {
		t.Fatalf("admin diizinkan: %d %v", st, m)
	}
	id := str(m, "id")
	// masih ada admin lokal aktif → boleh dinonaktifkan
	if st, _, _ := s.do("PATCH", "/Users/"+id, patch(map[string]any{"op": "replace", "path": "active", "value": false})); st != 200 {
		t.Fatalf("masih ada admin lain: %d", st)
	}
	s.do("PATCH", "/Users/"+id, patch(map[string]any{"op": "replace", "path": "active", "value": true}))
	// nonaktifkan semua admin lain → admin SCIM adalah yang terakhir
	s.e.sql(`UPDATE users SET disabled=true WHERE role='admin' AND id<>$1`, id)
	for name, fn := range map[string]func() (int, map[string]any){
		"nonaktifkan": func() (int, map[string]any) {
			st, m, _ := s.do("PATCH", "/Users/"+id, patch(map[string]any{"op": "replace", "path": "active", "value": false}))
			return st, m
		},
		"turunkan peran": func() (int, map[string]any) {
			st, m, _ := s.do("PATCH", "/Users/"+id, patch(map[string]any{"op": "replace", "path": "roles", "value": []any{map[string]any{"value": "viewer"}}}))
			return st, m
		},
		"PUT tanpa roles": func() (int, map[string]any) {
			st, m, _ := s.do("PUT", "/Users/"+id, map[string]any{"schemas": []string{uSchema}, "userName": "scimadmin@contoh.id"})
			return st, m
		},
		"hapus": func() (int, map[string]any) { st, m, _ := s.do("DELETE", "/Users/"+id, nil); return st, m },
	} {
		if st, m := fn(); st != 409 || m["scimType"] != "mutability" {
			t.Errorf("%s admin terakhir harus 409 mutability: %d %v", name, st, m)
		}
	}
	var act bool
	var role string
	_ = s.e.pool.QueryRow(context.Background(), `SELECT NOT disabled, role::text FROM users WHERE id=$1`, id).Scan(&act, &role)
	if !act || role != "admin" {
		t.Fatal("admin terakhir harus tetap utuh")
	}

	// default (AllowAdminRole=false): admin tak boleh diubah SCIM sama sekali, walau terkelola (mis. diangkat manual lewat dashboard)
	s2 := newSCIM(t)
	_, m2 := s2.create("promosi@contoh.id", nil)
	s2.e.sql(`UPDATE users SET role='admin' WHERE id=$1`, str(m2, "id"))
	if st, m, _ := s2.do("PATCH", "/Users/"+str(m2, "id"), patch(map[string]any{"op": "replace", "path": "active", "value": false})); st != 403 || m["scimType"] != "mutability" {
		t.Fatalf("admin tak boleh diubah SCIM: %d %v", st, m)
	}
	if st, _, _ := s2.do("DELETE", "/Users/"+str(m2, "id"), nil); st != 403 {
		t.Fatalf("admin tak boleh dihapus SCIM: %d", st)
	}
}

func TestSCIMUserCanSignInViaOIDCByVerifiedEmail(t *testing.T) {
	idpS := newSSO(t, func(o *config.OIDCConfig) { o.AutoProvision = false })
	// aktifkan SCIM di env yang sama: bangun ulang konfigurasi lewat Cfg (Service memakai salinan nilai)
	idpS.svc.Cfg.SCIM = config.SCIMConfig{Enabled: true, DefaultRole: "viewer"}
	adm := idpS.newUser("admin")
	var tok struct{ Token string }
	if st := adm.c.do("POST", "/api/v1/scim-tokens", map[string]string{"name": "idp"}, &tok); st != 201 {
		t.Fatalf("token: %d", st)
	}
	sc := &scimC{e: idpS.env, tok: tok.Token, admin: adm}
	_, m := sc.create("provisi@contoh.id", map[string]any{"externalId": "x1"})
	id := str(m, "id")
	// email tidak terverifikasi → tidak ditautkan
	if loc := idpS.login(idpS.browser(), claimsFor("sub-scim", "provisi@contoh.id", map[string]any{"email_verified": false}), ""); ssoErr(loc) != "unverified_email" {
		t.Fatalf("tak terverifikasi: %q", loc)
	}
	b := idpS.browser()
	if loc := idpS.login(b, claimsFor("sub-scim", "provisi@contoh.id", nil), ""); ssoErr(loc) != "" || b.sessionEmail() != "provisi@contoh.id" {
		t.Fatalf("login SSO pengguna SCIM: %q %q", loc, b.sessionEmail())
	}
	var linked bool
	var d string
	_ = idpS.pool.QueryRow(context.Background(), `SELECT oidc_subject='sub-scim' FROM users WHERE id=$1`, id).Scan(&linked)
	_ = idpS.pool.QueryRow(context.Background(), `SELECT data->>'via' FROM audit_logs WHERE action='auth.oidc_link'`).Scan(&d)
	if !linked || d != "scim" {
		t.Fatalf("tertaut=%v via=%q", linked, d)
	}
	// dinonaktifkan oleh SCIM → sesi SSO dicabut dan login SSO berikutnya ditolak
	if st, _, _ := sc.do("PATCH", "/Users/"+id, patch(map[string]any{"op": "replace", "path": "active", "value": false})); st != 200 {
		t.Fatal(st)
	}
	if b.sessionEmail() != "" {
		t.Fatal("sesi SSO harus dicabut oleh deprovisioning SCIM")
	}
	if loc := idpS.login(idpS.browser(), claimsFor("sub-scim", "provisi@contoh.id", nil), ""); ssoErr(loc) != "disabled" {
		t.Fatalf("login SSO setelah deprovisioning: %q", loc)
	}
	// pengguna lokal biasa (bukan SCIM) tetap tidak ditautkan otomatis
	local := idpS.newUser("operator")
	if loc := idpS.login(idpS.browser(), claimsFor("sub-lokal", local.email, nil), ""); ssoErr(loc) != "not_linked" {
		t.Fatalf("pengguna lokal: %q", loc)
	}
}
