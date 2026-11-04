package tests

import (
	"context"
	"testing"
)

type sview struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Shared bool   `json:"shared"`
	Mine   bool   `json:"mine"`
	Owner  string `json:"owner"`
	Filter struct {
		To     string `json:"to"`
		Source string `json:"source"`
	} `json:"filter"`
}

func listViews(t *testing.T, c *client) []sview {
	t.Helper()
	var out struct{ Items []sview }
	if st := c.do("GET", "/api/v1/saved-views", nil, &out); st != 200 {
		t.Fatalf("daftar: %d", st)
	}
	return out.Items
}

// berbagi: terlihat hanya-baca oleh pengguna lain (dengan label pemilik tanpa email penuh); pribadi tetap tak terlihat.
func TestSharedViewsVisibilityAndReadOnly(t *testing.T) {
	e := newEnv(t)
	a, b, v := e.newUser("operator"), e.newUser("operator"), e.newUser("viewer")
	var shared, private sview
	if st := a.c.do("POST", "/api/v1/saved-views", map[string]any{"name": "Gagal tim", "shared": true, "filter": map[string]any{"status": []string{"failed"}}}, &shared); st != 201 || !shared.Shared || !shared.Mine {
		t.Fatalf("buat berbagi: %d %+v", st, shared)
	}
	if st := a.c.do("POST", "/api/v1/saved-views", map[string]any{"name": "Pribadi", "filter": map[string]any{}}, &private); st != 201 || private.Shared {
		t.Fatalf("buat pribadi: %d %+v", st, private)
	}
	for _, who := range []struct {
		name string
		c    *client
	}{{"operator B", b.c}, {"viewer", v.c}} {
		items := listViews(t, who.c)
		if len(items) != 1 || items[0].ID != shared.ID || items[0].Mine || !items[0].Shared {
			t.Fatalf("%s melihat: %+v", who.name, items)
		}
		if items[0].Owner == "" || containsAt(items[0].Owner) {
			t.Fatalf("label pemilik harus bagian lokal email saja: %q", items[0].Owner)
		}
		// hanya-baca
		if st := who.c.do("PUT", "/api/v1/saved-views/"+shared.ID, map[string]any{"name": "x", "filter": map[string]any{}}, nil); st != 404 {
			t.Fatalf("%s mengubah milik orang: %d", who.name, st)
		}
		if st := who.c.do("DELETE", "/api/v1/saved-views/"+shared.ID, nil, nil); st != 404 {
			t.Fatalf("%s menghapus milik orang: %d", who.name, st)
		}
	}
	// pemilik melihat miliknya dulu (mine=true) lalu tidak ada duplikat
	mine := listViews(t, a.c)
	if len(mine) != 2 || !mine[0].Mine || !mine[1].Mine {
		t.Fatalf("pemilik: %+v", mine)
	}
	// nama yang sama dengan tampilan berbagi orang lain boleh dipakai (unik per pengguna)
	if st := b.c.do("POST", "/api/v1/saved-views", map[string]any{"name": "Gagal tim", "filter": map[string]any{}}, nil); st != 201 {
		t.Fatalf("nama sama lintas pengguna: %d", st)
	}
	// berhenti berbagi => hilang dari orang lain
	if st := a.c.do("PUT", "/api/v1/saved-views/"+shared.ID, map[string]any{"name": "Gagal tim", "shared": false, "filter": map[string]any{"status": []string{"failed"}}}, &shared); st != 200 || shared.Shared {
		t.Fatalf("unshare: %d %+v", st, shared)
	}
	for _, it := range listViews(t, v.c) {
		t.Fatalf("setelah unshare viewer tidak boleh melihat apa pun: %+v", it)
	}
	// PUT tanpa field shared mempertahankan status berbagi
	if st := a.c.do("PUT", "/api/v1/saved-views/"+shared.ID, map[string]any{"name": "Gagal tim", "shared": true, "filter": map[string]any{}}, nil); st != 200 {
		t.Fatal(st)
	}
	if st := a.c.do("PUT", "/api/v1/saved-views/"+shared.ID, map[string]any{"name": "Gagal tim 2", "filter": map[string]any{}}, &shared); st != 200 || !shared.Shared {
		t.Fatalf("shared dipertahankan: %d %+v", st, shared)
	}
}

func containsAt(s string) bool {
	for _, r := range s {
		if r == '@' {
			return true
		}
	}
	return false
}

// tampilan berbagi tidak boleh memuat nomor (viewer melihat nomor termasking); aturan dijaga API dan constraint DB.
func TestSharedViewRejectsNumberFilter(t *testing.T) {
	e := newEnv(t)
	a := e.newUser("operator")
	if st := a.c.do("POST", "/api/v1/saved-views", map[string]any{"name": "n1", "shared": true, "filter": map[string]any{"to": "+62812"}}, nil); st != 422 {
		t.Fatalf("buat berbagi dengan nomor harus 422: %d", st)
	}
	var v sview
	if st := a.c.do("POST", "/api/v1/saved-views", map[string]any{"name": "n2", "filter": map[string]any{"to": "+62812"}}, &v); st != 201 {
		t.Fatalf("pribadi dengan nomor boleh: %d", st)
	}
	if st := a.c.do("PUT", "/api/v1/saved-views/"+v.ID, map[string]any{"name": "n2", "shared": true, "filter": map[string]any{"to": "+62812"}}, nil); st != 422 {
		t.Fatalf("membagikan yang bernomor harus 422: %d", st)
	}
	// berbagi tanpa nomor lalu menambah nomor lewat PUT (shared dipertahankan) juga ditolak
	if st := a.c.do("PUT", "/api/v1/saved-views/"+v.ID, map[string]any{"name": "n2", "shared": true, "filter": map[string]any{}}, nil); st != 200 {
		t.Fatalf("bagikan tanpa nomor: %d", st)
	}
	if st := a.c.do("PUT", "/api/v1/saved-views/"+v.ID, map[string]any{"name": "n2", "filter": map[string]any{"to": "+62"}}, nil); st != 422 {
		t.Fatalf("menambah nomor pada tampilan berbagi harus 422: %d", st)
	}
	// jaring pengaman DB
	if _, err := e.pool.Exec(context.Background(), `UPDATE saved_views SET filter = jsonb_set(filter, '{to}', '"+62"') WHERE id=$1`, v.ID); err == nil {
		t.Fatal("constraint DB harus menolak")
	}
}

// admin dapat memoderasi (hapus) tampilan BERBAGI orang lain dengan audit; yang pribadi tetap 404; non-admin tidak.
func TestAdminModeratesSharedViews(t *testing.T) {
	e := newEnv(t)
	a, adm := e.newUser("operator"), e.newUser("admin")
	var shared, private sview
	a.c.do("POST", "/api/v1/saved-views", map[string]any{"name": "tim", "shared": true, "filter": map[string]any{}}, &shared)
	a.c.do("POST", "/api/v1/saved-views", map[string]any{"name": "rahasia", "filter": map[string]any{}}, &private)
	if st := adm.c.do("DELETE", "/api/v1/saved-views/"+private.ID, nil, nil); st != 404 {
		t.Fatalf("pribadi orang lain harus 404 bahkan untuk admin: %d", st)
	}
	if st := adm.c.do("DELETE", "/api/v1/saved-views/"+shared.ID, nil, nil); st != 204 {
		t.Fatalf("admin menghapus berbagi: %d", st)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='saved_view.moderate_delete' AND target_id=$1`, shared.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("audit moderasi: %d", n)
	}
	if got := listViews(t, a.c); len(got) != 1 || got[0].ID != private.ID {
		t.Fatalf("sisa milik A: %+v", got)
	}
}
