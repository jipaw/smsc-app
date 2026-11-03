package tests

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/jipaw/smsc-app/server/internal/config"
)

const gSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"

func newSCIMGroups(t *testing.T, mods ...func(*config.SCIMConfig)) *scimC {
	t.Helper()
	return newSCIM(t, append([]func(*config.SCIMConfig){func(c *config.SCIMConfig) {
		c.GroupRoles = map[string]string{"gateway ops": "operator", "gateway viewers": "viewer"}
	}}, mods...)...)
}

func (c *scimC) group(name string, extra map[string]any) (int, map[string]any) {
	b := map[string]any{"schemas": []string{gSchema}, "displayName": name}
	for k, v := range extra {
		b[k] = v
	}
	st, m, _ := c.do("POST", "/Groups", b)
	return st, m
}

func members(ids ...string) []any {
	var out []any
	for _, id := range ids {
		out = append(out, map[string]any{"value": id})
	}
	return out
}

func (c *scimC) user(email string, extra map[string]any) string {
	st, m := c.create(email, extra)
	if st != 201 {
		c.e.t.Fatalf("buat %s: %d %v", email, st, m)
	}
	return str(m, "id")
}

func (c *scimC) roleOfUser(id string) string {
	st, m, _ := c.do("GET", "/Users/"+id, nil)
	if st != 200 {
		c.e.t.Fatalf("get user: %d", st)
	}
	return roleOf(m)
}

func gpatch(ops ...map[string]any) map[string]any { return patch(ops...) }

func TestSCIMGroupCRUDAndFilter(t *testing.T) {
	s := newSCIMGroups(t)
	u1, u2 := s.user("a@contoh.id", nil), s.user("b@contoh.id", nil)
	st, g := s.group("Tim Infra", map[string]any{"externalId": "grp-1", "members": members(u1, u2)})
	if st != 201 || str(g, "displayName") != "Tim Infra" || str(g, "externalId") != "grp-1" || len(g["members"].([]any)) != 2 || g["meta"].(map[string]any)["resourceType"] != "Group" {
		t.Fatalf("create: %d %v", st, g)
	}
	gid := str(g, "id")
	mm := g["members"].([]any)[0].(map[string]any)
	if mm["type"] != "User" || !strings.Contains(str(mm, "$ref"), "/scim/v2/Users/") || str(mm, "display") == "" {
		t.Fatalf("anggota: %v", mm)
	}
	_, _, h := s.do("POST", "/Groups", map[string]any{"schemas": []string{gSchema}, "displayName": "Tim Lain"})
	if !strings.Contains(h.Get("Location"), "/scim/v2/Groups/") {
		t.Fatalf("Location: %q", h.Get("Location"))
	}
	if st, g2, _ := s.do("GET", "/Groups/"+gid, nil); st != 200 || len(g2["members"].([]any)) != 2 {
		t.Fatalf("get: %d", st)
	}
	if st, g2, _ := s.do("GET", "/Groups/"+gid+"?excludedAttributes=members", nil); st != 200 || g2["members"] != nil {
		t.Fatalf("excludedAttributes: %d %v", st, g2)
	}
	// daftar + filter
	_, m, _ := s.do("GET", "/Groups", nil)
	if m["totalResults"].(float64) != 2 {
		t.Fatalf("daftar: %v", m)
	}
	f := func(expr string) map[string]any {
		st, m, _ := s.do("GET", "/Groups?filter="+url.QueryEscape(expr), nil)
		if st != 200 {
			t.Fatalf("filter %q: %d %v", expr, st, m)
		}
		return m
	}
	if f(`displayName eq "tim INFRA"`)["totalResults"].(float64) != 1 || f(`externalId eq "grp-1"`)["totalResults"].(float64) != 1 || f(`id eq "` + gid + `"`)["totalResults"].(float64) != 1 || f(`displayName eq "tidak ada"`)["totalResults"].(float64) != 0 {
		t.Fatal("filter grup")
	}
	if st, m, _ := s.do("GET", "/Groups?filter="+url.QueryEscape(`userName eq "a@contoh.id"`), nil); st != 200 {
		_ = m
	}
	if st, m, _ := s.do("GET", "/Groups?filter="+url.QueryEscape(`displayName co "x"`), nil); st != 400 || m["scimType"] != "invalidFilter" {
		t.Fatalf("operator co: %d %v", st, m)
	}
	if st, m, _ := s.do("GET", "/Users?filter="+url.QueryEscape(`displayName eq "x"`), nil); st != 400 || m["scimType"] != "invalidFilter" {
		t.Fatalf("displayName pada Users harus ditolak, bukan diabaikan: %d %v", st, m)
	}
	_, m, _ = s.do("GET", "/Groups?startIndex=2&count=1", nil)
	if m["totalResults"].(float64) != 2 || len(m["Resources"].([]any)) != 1 {
		t.Fatalf("halaman: %v", m)
	}
	// unik + validasi
	if st, m := s.group("TIM infra", nil); st != 409 || m["scimType"] != "uniqueness" {
		t.Fatalf("nama ganda: %d %v", st, m)
	}
	if st, m := s.group("Baru", map[string]any{"externalId": "grp-1"}); st != 409 {
		t.Fatalf("externalId ganda: %d %v", st, m)
	}
	for name, body := range map[string]map[string]any{
		"tanpa nama":          {},
		"nama kosong":         {"displayName": "   "},
		"anggota ngawur":      {"displayName": "X1", "members": members("bukan-uuid")},
		"anggota tak ada":     {"displayName": "X2", "members": members(uuid.NewString())},
		"anggota bukan array": {"displayName": "X3", "members": "u1"},
		"anggota grup":        {"displayName": "X4", "members": []any{map[string]any{"value": u1, "type": "Group"}}},
	} {
		if st, m := s.group(str(body, "displayName"), body); st != 400 {
			t.Errorf("%s: harus 400: %d %v", name, st, m)
		}
	}
	// grup gagal dibuat tidak meninggalkan baris
	var n int
	_ = s.e.pool.QueryRow(context.Background(), `SELECT count(*) FROM scim_groups WHERE display_name IN ('X1','X2','X3','X4')`).Scan(&n)
	if n != 0 {
		t.Fatalf("sisa grup: %d", n)
	}
	if st, _, _ := s.do("GET", "/Groups/"+uuid.NewString(), nil); st != 404 {
		t.Fatal(st)
	}
	// atribut groups pada User (read-only)
	if st, u, _ := s.do("GET", "/Users/"+u1, nil); st != 200 || len(u["groups"].([]any)) != 1 || u["groups"].([]any)[0].(map[string]any)["display"] != "Tim Infra" {
		t.Fatalf("User.groups: %v", u["groups"])
	}
	// hapus
	if st, _, _ := s.do("DELETE", "/Groups/"+gid, nil); st != 204 {
		t.Fatal(st)
	}
	if st, _, _ := s.do("GET", "/Groups/"+gid, nil); st != 404 {
		t.Fatal(st)
	}
	if st, _, _ := s.do("DELETE", "/Groups/"+gid, nil); st != 404 {
		t.Fatal(st)
	}
	if st, u, _ := s.do("GET", "/Users/"+u1, nil); st != 200 || len(u["groups"].([]any)) != 0 {
		t.Fatalf("anggota harus lepas: %v", u["groups"])
	}
	for _, p := range []string{"/Groups", "/Groups/" + gid} {
		if st, _, _ := s.req("", "GET", p, nil, ""); st != 401 {
			t.Errorf("tanpa token %s: %d", p, st)
		}
	}
}

func TestSCIMGroupMembershipDrivesRole(t *testing.T) {
	s := newSCIMGroups(t)
	u := s.user("peran@contoh.id", nil)
	if s.roleOfUser(u) != "viewer" {
		t.Fatal("awal viewer")
	}
	// grup terpetakan (nama tak peka huruf) → operator; diaudit via=group
	_, g := s.group("GATEWAY OPS", map[string]any{"members": members(u)})
	gid := str(g, "id")
	if s.roleOfUser(u) != "operator" {
		t.Fatalf("anggota grup ops harus operator: %s", s.roleOfUser(u))
	}
	var d string
	_ = s.e.pool.QueryRow(context.Background(), `SELECT data::text FROM audit_logs WHERE action='scim.user_update' AND data->>'via'='group' ORDER BY at DESC LIMIT 1`).Scan(&d)
	if !strings.Contains(d, `"to": "operator"`) {
		t.Fatalf("audit: %s", d)
	}
	// grup tak terpetakan tidak memengaruhi
	u2 := s.user("netral@contoh.id", nil)
	s.group("Tim Marketing", map[string]any{"members": members(u2)})
	if s.roleOfUser(u2) != "viewer" {
		t.Fatal("grup tak terpetakan tidak boleh mengubah peran")
	}
	// keluar dari grup (path filter, gaya Okta/Azure) → kembali ke peran bawaan
	st, _, _ := s.do("PATCH", "/Groups/"+gid, gpatch(map[string]any{"op": "remove", "path": `members[value eq "` + u + `"]`}))
	if st != 200 || s.roleOfUser(u) != "viewer" {
		t.Fatalf("keluar grup: %d %s", st, s.roleOfUser(u))
	}
	// masuk lagi gaya Azure (op kapital, array value)
	st, g2, _ := s.do("PATCH", "/Groups/"+gid, gpatch(map[string]any{"op": "Add", "path": "members", "value": members(u, u2)}))
	if st != 200 || len(g2["members"].([]any)) != 2 || s.roleOfUser(u) != "operator" || s.roleOfUser(u2) != "operator" {
		t.Fatalf("tambah anggota: %d %v", st, g2)
	}
	// remove dengan value array
	st, _, _ = s.do("PATCH", "/Groups/"+gid, gpatch(map[string]any{"op": "Remove", "path": "members", "value": members(u2)}))
	if st != 200 || s.roleOfUser(u2) != "viewer" || s.roleOfUser(u) != "operator" {
		t.Fatalf("remove array: %d", st)
	}
	// grup lain bermitra: tertinggi menang
	_, vg := s.group("Gateway Viewers", map[string]any{"members": members(u)})
	if s.roleOfUser(u) != "operator" {
		t.Fatal("viewer + operator → operator")
	}
	s.do("DELETE", "/Groups/"+str(vg, "id"), nil)
	// ganti nama grup mengubah pemetaan untuk SEMUA anggota
	st, _, _ = s.do("PATCH", "/Groups/"+gid, gpatch(map[string]any{"op": "replace", "path": "displayName", "value": "Tim Biasa"}))
	if st != 200 || s.roleOfUser(u) != "viewer" {
		t.Fatalf("rename ke tak terpetakan: %d %s", st, s.roleOfUser(u))
	}
	st, _, _ = s.do("PATCH", "/Groups/"+gid, gpatch(map[string]any{"op": "replace", "path": "displayName", "value": "Gateway Ops"}))
	if st != 200 || s.roleOfUser(u) != "operator" {
		t.Fatalf("rename ke terpetakan: %d", st)
	}
	// gaya Okta: tanpa path, value objek (replace anggota)
	st, g3, _ := s.do("PATCH", "/Groups/"+gid, gpatch(map[string]any{"op": "replace", "value": map[string]any{"displayName": "Gateway Ops", "members": members(u2)}}))
	if st != 200 || len(g3["members"].([]any)) != 1 || s.roleOfUser(u) != "viewer" || s.roleOfUser(u2) != "operator" {
		t.Fatalf("replace anggota: %d %v", st, g3)
	}
	// PUT: ganti seluruh anggota
	st, _, _ = s.do("PUT", "/Groups/"+gid, map[string]any{"schemas": []string{gSchema}, "displayName": "Gateway Ops", "members": members(u)})
	if st != 200 || s.roleOfUser(u) != "operator" || s.roleOfUser(u2) != "viewer" {
		t.Fatalf("PUT: %d", st)
	}
	if st, _, _ := s.do("PUT", "/Groups/"+gid, map[string]any{"schemas": []string{gSchema}}); st != 400 {
		t.Fatalf("PUT tanpa displayName: %d", st)
	}
	// hapus grup → anggota kembali ke bawaan
	s.do("DELETE", "/Groups/"+gid, nil)
	if s.roleOfUser(u) != "viewer" {
		t.Fatalf("hapus grup: %s", s.roleOfUser(u))
	}
	// kosongkan semua anggota (remove tanpa value)
	_, g4 := s.group("Gateway Ops", map[string]any{"members": members(u, u2)})
	st, g5, _ := s.do("PATCH", "/Groups/"+str(g4, "id"), gpatch(map[string]any{"op": "remove", "path": "members"}))
	if st != 200 || len(g5["members"].([]any)) != 0 || s.roleOfUser(u) != "viewer" {
		t.Fatalf("kosongkan: %d %v", st, g5)
	}
	// galat PATCH
	for name, body := range map[string]any{
		"path tak dikenal":   gpatch(map[string]any{"op": "replace", "path": "icon", "value": "x"}),
		"anggota bukan uuid": gpatch(map[string]any{"op": "add", "path": "members", "value": members("zzz")}),
		"anggota tak ada":    gpatch(map[string]any{"op": "add", "path": "members", "value": members(uuid.NewString())}),
		"hapus nama":         gpatch(map[string]any{"op": "remove", "path": "displayName"}),
		"op ngawur":          gpatch(map[string]any{"op": "move", "path": "members", "value": members(u)}),
	} {
		if st, m, _ := s.do("PATCH", "/Groups/"+str(g4, "id"), body); st != 400 {
			t.Errorf("%s: harus 400: %d %v", name, st, m)
		}
	}
}

func TestSCIMGroupRoleCombinesWithRolesAttribute(t *testing.T) {
	s := newSCIMGroups(t)
	op := []any{map[string]any{"value": "operator"}}
	// roles langsung operator + grup viewer → operator (tertinggi)
	u := s.user("gabung@contoh.id", map[string]any{"roles": op})
	s.group("Gateway Viewers", map[string]any{"members": members(u)})
	if s.roleOfUser(u) != "operator" {
		t.Fatalf("operator+viewer: %s", s.roleOfUser(u))
	}
	// hapus atribut roles → jatuh ke peran dari grup (viewer)
	st, m, _ := s.do("PATCH", "/Users/"+u, patch(map[string]any{"op": "remove", "path": "roles"}))
	if st != 200 || roleOf(m) != "viewer" {
		t.Fatalf("roles dihapus: %d %v", st, roleOf(m))
	}
	// pengguna viewer langsung + grup ops → operator; lalu PUT tanpa roles tetap operator (dari grup)
	v := s.user("naik@contoh.id", nil)
	_, og := s.group("Gateway Ops", map[string]any{"members": members(v)})
	if s.roleOfUser(v) != "operator" {
		t.Fatal("grup ops")
	}
	st, m, _ = s.do("PUT", "/Users/"+v, map[string]any{"schemas": []string{uSchema}, "userName": "naik@contoh.id"})
	if st != 200 || roleOf(m) != "operator" {
		t.Fatalf("PUT tanpa roles: %d %s", st, roleOf(m))
	}
	// roles eksplisit viewer tidak menurunkan di bawah grup
	st, m, _ = s.do("PATCH", "/Users/"+v, patch(map[string]any{"op": "replace", "path": "roles", "value": []any{map[string]any{"value": "viewer"}}}))
	if st != 200 || roleOf(m) != "operator" {
		t.Fatalf("viewer langsung + grup ops: %d %s", st, roleOf(m))
	}
	// keluar dari grup → peran langsung (viewer)
	s.do("DELETE", "/Groups/"+str(og, "id"), nil)
	if s.roleOfUser(v) != "viewer" {
		t.Fatalf("setelah grup hilang: %s", s.roleOfUser(v))
	}
	// pengguna dihapus → keanggotaan ikut hilang
	g2c, g2 := s.group("Gateway Ops", map[string]any{"members": members(u, v)})
	if g2c != 201 {
		t.Fatal(g2c)
	}
	s.do("DELETE", "/Users/"+v, nil)
	_, g3, _ := s.do("GET", "/Groups/"+str(g2, "id"), nil)
	if len(g3["members"].([]any)) != 1 {
		t.Fatalf("anggota terhapus harus lepas: %v", g3["members"])
	}
}

func TestSCIMGroupAdminMappingGuards(t *testing.T) {
	s := newSCIMGroups(t, func(c *config.SCIMConfig) {
		c.AllowAdminRole = true
		c.GroupRoles["gateway admins"] = "admin"
	})
	// admin lokal dinonaktifkan agar admin SCIM menjadi satu-satunya
	a := s.user("kepala@contoh.id", nil)
	_, ag := s.group("Gateway Admins", map[string]any{"members": members(a)})
	if s.roleOfUser(a) != "admin" {
		t.Fatalf("grup admin: %s", s.roleOfUser(a))
	}
	s.e.sql(`UPDATE users SET disabled=true WHERE role='admin' AND id<>$1`, a)
	for name, fn := range map[string]func() int{
		"keluarkan anggota": func() int {
			st, _, _ := s.do("PATCH", "/Groups/"+str(ag, "id"), gpatch(map[string]any{"op": "remove", "path": "members", "value": members(a)}))
			return st
		},
		"hapus grup": func() int { st, _, _ := s.do("DELETE", "/Groups/"+str(ag, "id"), nil); return st },
		"ganti nama": func() int {
			st, _, _ := s.do("PATCH", "/Groups/"+str(ag, "id"), gpatch(map[string]any{"op": "replace", "path": "displayName", "value": "Biasa"}))
			return st
		},
		"PUT kosong": func() int {
			st, _, _ := s.do("PUT", "/Groups/"+str(ag, "id"), map[string]any{"schemas": []string{gSchema}, "displayName": "Gateway Admins", "members": []any{}})
			return st
		},
	} {
		if st := fn(); st != 409 {
			t.Errorf("%s: admin aktif terakhir harus 409: %d", name, st)
		}
	}
	// semuanya dibatalkan utuh (transaksi): grup, nama, anggota, peran tidak berubah
	var role string
	var members int
	var gname string
	_ = s.e.pool.QueryRow(context.Background(), `SELECT role::text FROM users WHERE id=$1`, a).Scan(&role)
	_ = s.e.pool.QueryRow(context.Background(), `SELECT count(*), max(g.display_name) FROM scim_group_members m JOIN scim_groups g ON g.id=m.group_id WHERE m.user_id=$1`, a).Scan(&members, &gname)
	if role != "admin" || members != 1 || gname != "Gateway Admins" {
		t.Fatalf("rollback: role=%s members=%d nama=%s", role, members, gname)
	}
	// ada admin lain → boleh
	b := s.user("wakil@contoh.id", nil)
	s.do("PATCH", "/Groups/"+str(ag, "id"), gpatch(map[string]any{"op": "add", "path": "members", "value": members2(b)}))
	if s.roleOfUser(b) != "admin" {
		t.Fatal("anggota kedua jadi admin")
	}
	if st, _, _ := s.do("PATCH", "/Groups/"+str(ag, "id"), gpatch(map[string]any{"op": "remove", "path": "members", "value": members2(a)})); st != 200 {
		t.Fatalf("masih ada admin lain: %d", st)
	}
}

func members2(ids ...string) []any { return members(ids...) }

func TestSCIMGroupAdminNotAllowedLeavesAdminsUntouched(t *testing.T) {
	// pemetaan admin dengan AllowAdminRole=false tidak mungkin lewat konfigurasi (gagal start); di sini pastikan
	// admin yang sudah ada (diangkat manual) tidak disentuh perubahan grup dan tidak ada eskalasi.
	s := newSCIMGroups(t)
	u := s.user("manual@contoh.id", nil)
	s.e.sql(`UPDATE users SET role='admin' WHERE id=$1`, u)
	other := s.e.newUser("admin") // admin lain
	_ = other
	_, g := s.group("Gateway Viewers", map[string]any{"members": members(u)})
	var role string
	_ = s.e.pool.QueryRow(context.Background(), `SELECT role::text FROM users WHERE id=$1`, u).Scan(&role)
	if role != "admin" {
		t.Fatalf("admin tidak boleh diturunkan oleh grup saat SCIM tidak boleh mengubah admin: %s", role)
	}
	s.do("DELETE", "/Groups/"+str(g, "id"), nil)
	_ = s.e.pool.QueryRow(context.Background(), `SELECT role::text FROM users WHERE id=$1`, u).Scan(&role)
	if role != "admin" {
		t.Fatalf("setelah hapus grup: %s", role)
	}
}

func TestSCIMGroupVisibilityAndAdopt(t *testing.T) {
	s := newSCIMGroups(t)
	local := s.e.newUser("operator")
	if st, m := s.group("Gateway Viewers", map[string]any{"members": members(local.id)}); st != 400 {
		t.Fatalf("pengguna lokal tidak boleh jadi anggota: %d %v", st, m)
	}
	sa := newSCIMGroups(t, func(c *config.SCIMConfig) { c.AdoptExisting = true })
	l2 := sa.e.newUser("operator") // operator lokal
	l3 := sa.e.newUser("viewer")
	// adopsi lewat grup: operator lokal di grup viewers tetap operator (peran langsung = peran saat ini); viewer lokal di grup ops → operator
	sa.group("Gateway Viewers", map[string]any{"members": members(l2.id)})
	_, og := sa.group("Gateway Ops", map[string]any{"members": members(l3.id)})
	var r2, r3 string
	var m2, m3 bool
	_ = sa.e.pool.QueryRow(context.Background(), `SELECT role::text, scim_managed FROM users WHERE id=$1`, l2.id).Scan(&r2, &m2)
	_ = sa.e.pool.QueryRow(context.Background(), `SELECT role::text, scim_managed FROM users WHERE id=$1`, l3.id).Scan(&r3, &m3)
	if r2 != "operator" || r3 != "operator" || m2 || !m3 {
		t.Fatalf("adopsi: l2=%s/%v l3=%s/%v", r2, m2, r3, m3)
	}
	// keluar dari grup → kembali ke peran sebelum adopsi (peran langsung), bukan ke default
	sa.do("DELETE", "/Groups/"+str(og, "id"), nil)
	_ = sa.e.pool.QueryRow(context.Background(), `SELECT role::text FROM users WHERE id=$1`, l3.id).Scan(&r3)
	if r3 != "viewer" {
		t.Fatalf("setelah keluar: %s", r3)
	}
}

func TestSCIMReconcileAppliesNewMappingAndSettingsView(t *testing.T) {
	s := newSCIMGroups(t)
	u := s.user("rekon@contoh.id", nil)
	s.group("Tim Baru", map[string]any{"members": members(u)})
	if s.roleOfUser(u) != "viewer" {
		t.Fatal("belum terpetakan")
	}
	n, err := s.e.svc.ReconcileSCIMRoles(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("tidak ada perubahan: %d %v", n, err)
	}
	// konfigurasi berubah (restart dengan SCIM_OPERATOR_GROUPS baru) lalu rekonsiliasi
	s.e.svc.Cfg.SCIM.GroupRoles["tim baru"] = "operator"
	if n, err = s.e.svc.ReconcileSCIMRoles(context.Background()); err != nil || n != 1 {
		t.Fatalf("rekonsiliasi: %d %v", n, err)
	}
	if s.roleOfUser(u) != "operator" {
		t.Fatalf("peran: %s", s.roleOfUser(u))
	}
	if n, _ = s.e.svc.ReconcileSCIMRoles(context.Background()); n != 0 {
		t.Fatalf("idempoten: %d", n)
	}
	// ringkasan admin
	var gl struct {
		Items []struct {
			Name    string `json:"display_name"`
			Members int
			Role    *string `json:"mapped_role"`
		}
	}
	if st := s.admin.c.do("GET", "/api/v1/scim-groups", nil, &gl); st != 200 || len(gl.Items) != 1 || gl.Items[0].Members != 1 || gl.Items[0].Role == nil || *gl.Items[0].Role != "operator" {
		t.Fatalf("ringkasan: %d %+v", st, gl)
	}
	op := s.e.newUser("operator")
	if st := op.c.do("GET", "/api/v1/scim-groups", nil, nil); st != 403 {
		t.Fatalf("operator: %d", st)
	}
	var v struct {
		Groups     int               `json:"groups"`
		GroupRoles map[string]string `json:"group_roles"`
	}
	s.admin.c.do("GET", "/api/v1/settings/scim", nil, &v)
	if v.Groups != 1 || v.GroupRoles["gateway ops"] != "operator" {
		t.Fatalf("settings: %+v", v)
	}
}
