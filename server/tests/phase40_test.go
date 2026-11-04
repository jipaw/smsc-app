package tests

import (
	"context"
	"testing"
	"time"
)

type notif struct {
	ID     string         `json:"id"`
	Kind   string         `json:"kind"`
	Data   map[string]any `json:"data"`
	ReadAt *string        `json:"read_at"`
}

type notifList struct {
	Items       []notif `json:"items"`
	UnreadCount int     `json:"unread_count"`
}

func notifs(t *testing.T, c *client, q string) notifList {
	t.Helper()
	var out notifList
	if st := c.do("GET", "/api/v1/notifications"+q, nil, &out); st != 200 {
		t.Fatalf("notifikasi: %d", st)
	}
	return out
}

// berbagi => semua pengguna aktif lain diberi tahu (bukan pemilik, bukan yang dinonaktifkan); tidak berulang dalam 1 jam.
func TestSharingNotifiesOthersOnceAndSkipsOwnerAndDisabled(t *testing.T) {
	e := newEnv(t)
	owner, b, v, off := e.newUser("operator"), e.newUser("operator"), e.newUser("viewer"), e.newUser("viewer")
	e.sql(`UPDATE users SET disabled=true WHERE id=$1`, off.id)
	var sv sview
	if st := owner.c.do("POST", "/api/v1/saved-views", map[string]any{"name": "Tim gagal", "shared": true, "filter": map[string]any{"status": []string{"failed"}}}, &sv); st != 201 {
		t.Fatalf("buat: %d", st)
	}
	for name, c := range map[string]*client{"operator B": b.c, "viewer": v.c} {
		n := notifs(t, c, "")
		if n.UnreadCount != 1 || len(n.Items) != 1 || n.Items[0].Kind != "saved_view.shared" || n.Items[0].Data["view_id"] != sv.ID || n.Items[0].Data["name"] != "Tim gagal" || n.Items[0].ReadAt != nil {
			t.Fatalf("%s: %+v", name, n)
		}
		if o, _ := n.Items[0].Data["owner"].(string); o == "" || containsAt(o) {
			t.Fatalf("%s: owner harus bagian lokal email: %q", name, o)
		}
	}
	if n := notifs(t, owner.c, ""); n.UnreadCount != 0 || len(n.Items) != 0 {
		t.Fatalf("pemilik tidak diberi tahu: %+v", n)
	}
	var cnt int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications WHERE user_id=$1`, off.id).Scan(&cnt)
	if cnt != 0 {
		t.Fatalf("pengguna nonaktif tidak diberi tahu: %d", cnt)
	}
	// bolak-balik berbagi dalam 1 jam tidak membanjiri
	owner.c.do("PUT", "/api/v1/saved-views/"+sv.ID, map[string]any{"name": "Tim gagal", "shared": false, "filter": map[string]any{}}, nil)
	owner.c.do("PUT", "/api/v1/saved-views/"+sv.ID, map[string]any{"name": "Tim gagal", "shared": true, "filter": map[string]any{}}, nil)
	if n := notifs(t, b.c, ""); len(n.Items) != 1 {
		t.Fatalf("dedupe 1 jam: %+v", n)
	}
	// setelah lewat 1 jam, berbagi ulang boleh memberi tahu lagi
	e.sql(`UPDATE notifications SET created_at = now() - interval '2 hours'`)
	owner.c.do("PUT", "/api/v1/saved-views/"+sv.ID, map[string]any{"name": "Tim gagal", "shared": false, "filter": map[string]any{}}, nil)
	owner.c.do("PUT", "/api/v1/saved-views/"+sv.ID, map[string]any{"name": "Tim gagal", "shared": true, "filter": map[string]any{}}, nil)
	if n := notifs(t, b.c, ""); len(n.Items) != 2 {
		t.Fatalf("setelah 1 jam: %+v", n)
	}
	// tampilan pribadi / hanya disunting (sudah dibagikan) tidak memberi tahu siapa pun
	before := notifs(t, v.c, "").UnreadCount
	var priv sview
	owner.c.do("POST", "/api/v1/saved-views", map[string]any{"name": "pribadi", "filter": map[string]any{}}, &priv)
	owner.c.do("PUT", "/api/v1/saved-views/"+sv.ID, map[string]any{"name": "Tim gagal 2", "filter": map[string]any{}}, nil)
	if notifs(t, v.c, "").UnreadCount != before {
		t.Fatal("pribadi/sunting tidak boleh memberi notifikasi")
	}
}

// moderasi admin => pemilik diberi tahu (siapa & apa); tindakan tidak diam-diam.
func TestModerationNotifiesOwner(t *testing.T) {
	e := newEnv(t)
	owner, adm := e.newUser("operator"), e.newUser("admin")
	var sv sview
	owner.c.do("POST", "/api/v1/saved-views", map[string]any{"name": "Terlalu luas", "shared": true, "filter": map[string]any{}}, &sv)
	if st := adm.c.do("DELETE", "/api/v1/saved-views/"+sv.ID, nil, nil); st != 204 {
		t.Fatal(st)
	}
	n := notifs(t, owner.c, "?unread=true")
	if len(n.Items) != 1 || n.Items[0].Kind != "saved_view.moderated" || n.Items[0].Data["name"] != "Terlalu luas" {
		t.Fatalf("pemilik: %+v", n)
	}
	if by, _ := n.Items[0].Data["by"].(string); by == "" || containsAt(by) {
		t.Fatalf("by harus bagian lokal email admin: %q", by)
	}
}

// tandai dibaca: milik sendiri saja (orang lain = 404), read-all, filter unread, batas limit.
func TestNotificationsReadStateAndIsolation(t *testing.T) {
	e := newEnv(t)
	owner, b, c := e.newUser("operator"), e.newUser("operator"), e.newUser("viewer")
	for _, nm := range []string{"a", "b", "c"} {
		owner.c.do("POST", "/api/v1/saved-views", map[string]any{"name": nm, "shared": true, "filter": map[string]any{}}, nil)
	}
	nb := notifs(t, b.c, "")
	if nb.UnreadCount != 3 || len(nb.Items) != 3 {
		t.Fatalf("B: %+v", nb)
	}
	if st := c.c.do("POST", "/api/v1/notifications/"+nb.Items[0].ID+"/read", nil, nil); st != 404 {
		t.Fatalf("membaca milik orang lain harus 404: %d", st)
	}
	if st := b.c.do("POST", "/api/v1/notifications/"+nb.Items[0].ID+"/read", nil, nil); st != 204 {
		t.Fatalf("tandai: %d", st)
	}
	if st := b.c.do("POST", "/api/v1/notifications/"+nb.Items[0].ID+"/read", nil, nil); st != 204 {
		t.Fatalf("idempoten: %d", st)
	}
	if n := notifs(t, b.c, "?unread=true"); n.UnreadCount != 2 || len(n.Items) != 2 {
		t.Fatalf("unread: %+v", n)
	}
	if n := notifs(t, b.c, "?limit=1"); len(n.Items) != 1 || n.UnreadCount != 2 {
		t.Fatalf("limit tidak memengaruhi unread_count: %+v", n)
	}
	if st := b.c.do("POST", "/api/v1/notifications/read-all", nil, nil); st != 204 {
		t.Fatal(st)
	}
	if n := notifs(t, b.c, ""); n.UnreadCount != 0 || len(n.Items) != 3 {
		t.Fatalf("read-all: %+v", n)
	}
	if n := notifs(t, c.c, ""); n.UnreadCount != 3 {
		t.Fatalf("C tidak terpengaruh: %+v", n)
	}
	// retensi: yang sudah dibaca > 30 hari dan semua > 90 hari dihapus
	e.sql(`UPDATE notifications SET read_at = now() - interval '40 days' WHERE user_id=$1`, b.id)
	e.sql(`UPDATE notifications SET created_at = now() - interval '100 days' WHERE user_id=$1`, c.id)
	if _, err := e.svc.RetentionOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications`).Scan(&n)
	if n != 0 {
		t.Fatalf("retensi: tersisa %d", n)
	}
}

// sinyal SSE terbit tanpa muatan saat ada notifikasi baru.
func TestNotificationSignalHasNoPayload(t *testing.T) {
	e := newEnv(t)
	owner := e.newUser("operator")
	_ = e.newUser("viewer")
	ch, _, _, stop := e.svc.Bus.Subscribe(0, false)
	defer stop()
	owner.c.do("POST", "/api/v1/saved-views", map[string]any{"name": "s", "shared": true, "filter": map[string]any{}}, nil)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Name == "notification.created" {
				if string(ev.Data) != "{}" {
					t.Fatalf("muatan harus kosong: %s", ev.Data)
				}
				return
			}
		case <-deadline:
			t.Fatal("tidak ada sinyal notification.created")
		}
	}
}
