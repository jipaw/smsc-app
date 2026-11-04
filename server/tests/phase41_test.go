package tests

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jipaw/smsc-app/server/internal/mailer/mailertest"
)

func shareView(t *testing.T, c *client, name string) sview {
	t.Helper()
	var v sview
	if st := c.do("POST", "/api/v1/saved-views", map[string]any{"name": name, "shared": true, "filter": map[string]any{"status": []string{"failed"}}}, &v); st != 201 {
		t.Fatalf("berbagi %s: %d", name, st)
	}
	return v
}

// moderasi => email ke pemilik (isi memuat nama tampilan dan admin tanpa email penuh; isi outbox dikosongkan setelah terkirim).
func TestModerationEmailsOwner(t *testing.T) {
	srv := mailertest.Start(mailertest.Options{})
	defer srv.Close()
	e := newEnvOpts(t, withMail(srv, "none"))
	owner, adm := e.newUser("operator"), e.newUser("admin")
	v := shareView(t, owner.c, "Terlalu luas")
	if st := adm.c.do("DELETE", "/api/v1/saved-views/"+v.ID, nil, nil); st != 204 {
		t.Fatal(st)
	}
	waitFor(t, 8*time.Second, "email moderasi terkirim", func() bool { return len(srv.Messages()) == 1 })
	m := srv.Messages()[0]
	if !strings.Contains(strings.ToLower(m.To), strings.ToLower(owner.email)) {
		t.Fatalf("penerima: %q", m.To)
	}
	if !strings.Contains(m.Data, "Terlalu luas") || !strings.Contains(m.Data, "Subject: Tampilan transaksi Anda dihapus oleh admin") {
		t.Fatalf("isi: %s", m.Data)
	}
	if strings.Contains(m.Data, adm.email) {
		t.Fatal("email penuh admin tidak boleh ada di isi")
	}
	adminLocal := adm.email[:strings.Index(adm.email, "@")]
	if !strings.Contains(m.Data, "admin "+adminLocal) {
		t.Fatalf("harus menyebut admin (bagian lokal): %s", m.Data)
	}
	waitFor(t, 5*time.Second, "outbox sent & isi dikosongkan", func() bool {
		var st string
		var nullBody bool
		_ = e.pool.QueryRow(context.Background(), `SELECT status, body IS NULL FROM email_outbox WHERE kind='saved_view_moderated'`).Scan(&st, &nullBody)
		return st == "sent" && nullBody
	})
	// notifikasi dalam aplikasi tetap ada
	if n := notifs(t, owner.c, "?unread=true"); len(n.Items) != 1 || n.Items[0].Kind != "saved_view.moderated" {
		t.Fatalf("notifikasi app: %+v", n)
	}
}

// tanpa SMTP, preferensi mati, pengguna nonaktif, atau email sejenis < 10 menit: tidak ada email baru (moderasi + notifikasi app tetap jalan).
func TestModerationEmailSkippedWhenNotApplicable(t *testing.T) {
	srv := mailertest.Start(mailertest.Options{})
	defer srv.Close()
	e := newEnvOpts(t, withMail(srv, "none"))
	adm := e.newUser("admin")
	outbox := func() int {
		var n int
		_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM email_outbox WHERE kind='saved_view_moderated'`).Scan(&n)
		return n
	}
	moderate := func(o userCtx, name string) {
		v := shareView(t, o.c, name)
		if st := adm.c.do("DELETE", "/api/v1/saved-views/"+v.ID, nil, nil); st != 204 {
			t.Fatal(st)
		}
	}
	// preferensi mati
	off := e.newUser("operator")
	var pr struct {
		EmailNotifications bool `json:"email_notifications"`
		EmailAvailable     bool `json:"email_available"`
	}
	if st := off.c.do("GET", "/api/v1/auth/preferences", nil, &pr); st != 200 || !pr.EmailNotifications || !pr.EmailAvailable {
		t.Fatalf("default: %d %+v", st, pr)
	}
	if st := off.c.do("PUT", "/api/v1/auth/preferences", map[string]any{"email_notifications": false}, &pr); st != 200 || pr.EmailNotifications {
		t.Fatalf("matikan: %d %+v", st, pr)
	}
	moderate(off, "p1")
	if outbox() != 0 {
		t.Fatal("preferensi mati: tidak boleh ada email")
	}
	if n := notifs(t, off.c, ""); len(n.Items) != 1 {
		t.Fatalf("notifikasi app tetap: %+v", n)
	}
	// pengguna nonaktif
	dis := e.newUser("operator")
	v := shareView(t, dis.c, "p2")
	e.sql(`UPDATE users SET disabled=true WHERE id=$1`, dis.id)
	adm.c.do("DELETE", "/api/v1/saved-views/"+v.ID, nil, nil)
	if outbox() != 0 {
		t.Fatal("nonaktif: tidak boleh ada email")
	}
	// dedupe 10 menit per alamat
	on := e.newUser("operator")
	moderate(on, "p3")
	moderate(on, "p4")
	if outbox() != 1 {
		t.Fatalf("dedupe 10 menit: %d", outbox())
	}
	if n := notifs(t, on.c, ""); len(n.Items) != 2 {
		t.Fatalf("dua notifikasi app: %+v", n)
	}
	e.sql(`UPDATE email_outbox SET created_at = now() - interval '11 minutes'`)
	moderate(on, "p5")
	if outbox() != 2 {
		t.Fatalf("setelah 10 menit boleh lagi: %d", outbox())
	}
	// pulihkan preferensi => email lagi
	off.c.do("PUT", "/api/v1/auth/preferences", map[string]any{"email_notifications": true}, nil)
	moderate(off, "p6")
	if outbox() != 3 {
		t.Fatalf("setelah dinyalakan: %d", outbox())
	}
}

// server tanpa SMTP: email_available=false dan tidak ada email; validasi PUT; API key ditolak.
func TestPreferencesWithoutSMTPAndValidation(t *testing.T) {
	e := newEnv(t) // tanpa Mail
	owner, adm := e.newUser("operator"), e.newUser("admin")
	var pr struct {
		EmailNotifications bool `json:"email_notifications"`
		EmailAvailable     bool `json:"email_available"`
	}
	if st := owner.c.do("GET", "/api/v1/auth/preferences", nil, &pr); st != 200 || pr.EmailAvailable {
		t.Fatalf("tanpa SMTP: %d %+v", st, pr)
	}
	if st := owner.c.do("PUT", "/api/v1/auth/preferences", map[string]any{}, nil); st != 422 {
		t.Fatalf("minimal satu bidang: %d", st)
	}
	v := shareView(t, owner.c, "x")
	adm.c.do("DELETE", "/api/v1/saved-views/"+v.ID, nil, nil)
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM email_outbox WHERE kind='saved_view_moderated'`).Scan(&n)
	if n != 0 {
		t.Fatalf("tanpa SMTP tidak boleh ada outbox: %d", n)
	}
	// isolasi: preferensi pengguna lain tidak terpengaruh
	owner.c.do("PUT", "/api/v1/auth/preferences", map[string]any{"email_notifications": false}, nil)
	adm.c.do("GET", "/api/v1/auth/preferences", nil, &pr)
	if !pr.EmailNotifications {
		t.Fatal("preferensi admin tidak boleh ikut berubah")
	}
	var key struct{ Secret string }
	adm.c.do("POST", "/api/v1/api-keys", map[string]any{"name": "k", "scopes": []string{"messages:read"}}, &key)
	if st, _ := adm.c.doH("GET", "/api/v1/auth/preferences", nil, nil, map[string]string{"Authorization": "Bearer " + key.Secret}); st != 403 && st != 401 {
		t.Fatalf("API key: %d", st)
	}
}
