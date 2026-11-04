package tests

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

type viewOut struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Filter struct {
		Status   []string `json:"status"`
		Source   string   `json:"source"`
		To       string   `json:"to"`
		Since    string   `json:"since"`
		Partial  string   `json:"partial"`
		Campaign string   `json:"campaign"`
	} `json:"filter"`
}

// CRUD tampilan tersimpan, normalisasi, validasi ketat, nama unik (tanpa membedakan huruf), batas 20.
func TestSavedViewsCRUDValidationAndLimit(t *testing.T) {
	e := newEnv(t)
	u := e.newUser("operator")
	camp := uuid.NewString()
	var v viewOut
	body := map[string]any{"name": "  Gagal parsial hari ini ", "filter": map[string]any{"status": []string{"failed", "failed"}, "partial": "partial", "to": "+62812", "campaign": camp}}
	if st := u.c.do("POST", "/api/v1/saved-views", body, &v); st != 201 || v.Name != "Gagal parsial hari ini" || len(v.Filter.Status) != 1 || v.Filter.Partial != "partial" || v.Filter.Campaign != camp {
		t.Fatalf("buat: %d %+v", st, v)
	}
	var list struct{ Items []viewOut }
	if st := u.c.do("GET", "/api/v1/saved-views", nil, &list); st != 200 || len(list.Items) != 1 || list.Items[0].ID != v.ID {
		t.Fatalf("daftar: %d %+v", st, list)
	}
	// nama unik tanpa membedakan huruf besar/kecil
	if st := u.c.do("POST", "/api/v1/saved-views", map[string]any{"name": "GAGAL PARSIAL HARI INI", "filter": map[string]any{}}, nil); st != 409 {
		t.Fatalf("duplikat harus 409: %d", st)
	}
	// validasi ketat
	bad := []map[string]any{
		{"name": "", "filter": map[string]any{}},
		{"name": "x\u0007y", "filter": map[string]any{}},
		{"name": "ok1", "filter": map[string]any{"status": []string{"hack"}}},
		{"name": "ok2", "filter": map[string]any{"source": "ftp"}},
		{"name": "ok3", "filter": map[string]any{"to": "+62abc"}},
		{"name": "ok4", "filter": map[string]any{"since": "besok"}},
		{"name": "ok5", "filter": map[string]any{"partial": "semua"}},
		{"name": "ok6", "filter": map[string]any{"campaign": "bukan-uuid"}},
	}
	for i, b := range bad {
		if st := u.c.do("POST", "/api/v1/saved-views", b, nil); st != 422 {
			t.Fatalf("kasus %d harus 422: %d", i, st)
		}
	}
	// ganti nama + filter
	if st := u.c.do("PUT", "/api/v1/saved-views/"+v.ID, map[string]any{"name": "Parsial", "filter": map[string]any{"partial": "allsent"}}, &v); st != 200 || v.Name != "Parsial" || v.Filter.Partial != "allsent" || v.Filter.Campaign != "" {
		t.Fatalf("ganti: %d %+v", st, v)
	}
	// batas 20
	for i := 1; i < 20; i++ {
		if st := u.c.do("POST", "/api/v1/saved-views", map[string]any{"name": fmt.Sprintf("v%02d", i), "filter": map[string]any{}}, nil); st != 201 {
			t.Fatalf("buat %d: %d", i, st)
		}
	}
	var pr struct{ Type string }
	if st := u.c.do("POST", "/api/v1/saved-views", map[string]any{"name": "ke-21", "filter": map[string]any{}}, &pr); st != 409 || pr.Type != "saved_view.limit" {
		t.Fatalf("batas 20: %d %s", st, pr.Type)
	}
	if st := u.c.do("DELETE", "/api/v1/saved-views/"+v.ID, nil, nil); st != 204 {
		t.Fatalf("hapus: %d", st)
	}
	if st := u.c.do("DELETE", "/api/v1/saved-views/"+v.ID, nil, nil); st != 404 {
		t.Fatalf("hapus lagi harus 404: %d", st)
	}
	// setelah dihapus ada ruang lagi
	if st := u.c.do("POST", "/api/v1/saved-views", map[string]any{"name": "ke-21", "filter": map[string]any{}}, nil); st != 201 {
		t.Fatalf("ruang kembali: %d", st)
	}
}

// tampilan terisolasi per pengguna; viewer boleh punya tampilan; API key tidak; pengguna dihapus => tampilannya ikut terhapus.
func TestSavedViewsIsolationAndAccess(t *testing.T) {
	e := newEnv(t)
	a, b := e.newUser("operator"), e.newUser("viewer")
	var v viewOut
	if st := a.c.do("POST", "/api/v1/saved-views", map[string]any{"name": "milik A", "filter": map[string]any{"source": "api"}}, &v); st != 201 {
		t.Fatal(st)
	}
	var list struct{ Items []viewOut }
	b.c.do("GET", "/api/v1/saved-views", nil, &list)
	if len(list.Items) != 0 {
		t.Fatalf("B tidak boleh melihat milik A: %+v", list)
	}
	if st := b.c.do("PUT", "/api/v1/saved-views/"+v.ID, map[string]any{"name": "dibajak", "filter": map[string]any{}}, nil); st != 404 {
		t.Fatalf("ubah milik orang lain harus 404: %d", st)
	}
	if st := b.c.do("DELETE", "/api/v1/saved-views/"+v.ID, nil, nil); st != 404 {
		t.Fatalf("hapus milik orang lain harus 404: %d", st)
	}
	// nama yang sama boleh dipakai pengguna berbeda; viewer boleh membuat
	if st := b.c.do("POST", "/api/v1/saved-views", map[string]any{"name": "milik A", "filter": map[string]any{}}, nil); st != 201 {
		t.Fatalf("viewer membuat: %d", st)
	}
	a.c.do("GET", "/api/v1/saved-views", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Name != "milik A" || list.Items[0].Filter.Source != "api" {
		t.Fatalf("milik A utuh: %+v", list)
	}
	// API key: tidak punya tampilan
	admin := e.loginAs("admin")
	var key struct{ Secret string }
	if st := admin.do("POST", "/api/v1/api-keys", map[string]any{"name": "k", "scopes": []string{"messages:read"}}, &key); st != 201 {
		t.Fatalf("api key: %d", st)
	}
	if st, _ := admin.doH("GET", "/api/v1/saved-views", nil, nil, map[string]string{"Authorization": "Bearer " + key.Secret}); st != 403 && st != 401 {
		t.Fatalf("API key harus ditolak: %d", st)
	}
	// hapus pengguna => ikut terhapus
	e.sql(`DELETE FROM users WHERE id=$1`, a.id)
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM saved_views WHERE user_id=$1`, a.id).Scan(&n)
	if n != 0 {
		t.Fatalf("cascade: %d", n)
	}
}
