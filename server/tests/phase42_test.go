package tests

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jipaw/smsc-app/server/internal/mailer/mailertest"
)

type prefsOut struct {
	EmailNotifications bool `json:"email_notifications"`
	EmailSharedViews   bool `json:"email_shared_views"`
	EmailAvailable     bool `json:"email_available"`
}

func setPrefs(t *testing.T, c *client, body map[string]any) prefsOut {
	t.Helper()
	var p prefsOut
	if st := c.do("PUT", "/api/v1/auth/preferences", body, &p); st != 200 {
		t.Fatalf("PUT prefs: %d", st)
	}
	return p
}

// preferensi parsial: bidang yang tidak dikirim tidak berubah; default berbagi = mati; kosong = 422.
func TestPrefsPartialUpdateAndDefaults(t *testing.T) {
	e := newEnv(t)
	u := e.newUser("operator")
	var p prefsOut
	if st := u.c.do("GET", "/api/v1/auth/preferences", nil, &p); st != 200 || !p.EmailNotifications || p.EmailSharedViews {
		t.Fatalf("default: %d %+v", st, p)
	}
	if p = setPrefs(t, u.c, map[string]any{"email_shared_views": true}); !p.EmailSharedViews || !p.EmailNotifications {
		t.Fatalf("hanya shared berubah: %+v", p)
	}
	if p = setPrefs(t, u.c, map[string]any{"email_notifications": false}); p.EmailNotifications || !p.EmailSharedViews {
		t.Fatalf("hanya notifications berubah: %+v", p)
	}
	if st := u.c.do("PUT", "/api/v1/auth/preferences", map[string]any{}, nil); st != 422 {
		t.Fatalf("kosong: %d", st)
	}
	// pengguna baru yang hanya mengubah satu bidang tetap mendapat default bidang lain
	v := e.newUser("viewer")
	if p = setPrefs(t, v.c, map[string]any{"email_notifications": false}); p.EmailSharedViews {
		t.Fatalf("default shared harus mati: %+v", p)
	}
}

// berbagi => email hanya ke pengguna aktif yang opt-in (bukan pemilik, bukan nonaktif); isi memuat nama, pemilik (tanpa email penuh), dan tautan.
func TestSharedViewEmailsOnlyOptedInUsers(t *testing.T) {
	srv := mailertest.Start(mailertest.Options{})
	defer srv.Close()
	e := newEnvOpts(t, withMail(srv, "none"))
	owner, yes, no, off, selfIn := e.newUser("operator"), e.newUser("viewer"), e.newUser("viewer"), e.newUser("operator"), e.newUser("operator")
	setPrefs(t, yes.c, map[string]any{"email_shared_views": true})
	setPrefs(t, off.c, map[string]any{"email_shared_views": true})
	e.sql(`UPDATE users SET disabled=true WHERE id=$1`, off.id)
	setPrefs(t, no.c, map[string]any{"email_shared_views": false})      // eksplisit mati
	setPrefs(t, selfIn.c, map[string]any{"email_notifications": false}) // hanya mengubah pref lain: shared tetap default mati
	// pemilik juga opt-in, tetapi tidak boleh diemail tentang tampilannya sendiri
	setPrefs(t, owner.c, map[string]any{"email_shared_views": true})
	v := shareView(t, owner.c, "Gagal minggu ini")
	waitFor(t, 8*time.Second, "email berbagi terkirim", func() bool { return len(srv.Messages()) == 1 })
	time.Sleep(600 * time.Millisecond)
	if len(srv.Messages()) != 1 {
		t.Fatalf("hanya satu penerima: %d", len(srv.Messages()))
	}
	m := srv.Messages()[0]
	if !strings.Contains(strings.ToLower(m.To), strings.ToLower(yes.email)) {
		t.Fatalf("penerima: %q", m.To)
	}
	ownerLocal := owner.email[:strings.Index(owner.email, "@")]
	if !strings.Contains(m.Data, "Gagal minggu ini") || !strings.Contains(m.Data, ownerLocal) || strings.Contains(m.Data, owner.email) || !strings.Contains(m.Data, "http://example.test/transactions") {
		t.Fatalf("isi: %s", m.Data)
	}
	// pemicu: hanya saat BARU dibagikan; menyunting yang sudah dibagikan tidak mengirim email baru
	e.sql(`UPDATE email_outbox SET created_at = now() - interval '11 minutes'`)
	owner.c.do("PUT", "/api/v1/saved-views/"+v.ID, map[string]any{"name": "Gagal minggu ini 2", "filter": map[string]any{}}, nil)
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM email_outbox WHERE kind='saved_view_shared'`).Scan(&n)
	if n != 1 {
		t.Fatalf("sunting tidak boleh mengirim email: %d", n)
	}
}

// dedupe 10 menit per alamat: tampilan kedua yang dibagikan dalam jendela itu tidak diemail (lonceng tetap), boleh lagi sesudahnya;
// tanpa SMTP tidak ada outbox; isi outbox dikosongkan setelah terkirim.
func TestSharedViewEmailDedupeAndNoSMTP(t *testing.T) {
	srv := mailertest.Start(mailertest.Options{})
	defer srv.Close()
	e := newEnvOpts(t, withMail(srv, "none"))
	owner, yes := e.newUser("operator"), e.newUser("viewer")
	setPrefs(t, yes.c, map[string]any{"email_shared_views": true})
	count := func() int {
		var n int
		_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM email_outbox WHERE kind='saved_view_shared'`).Scan(&n)
		return n
	}
	shareView(t, owner.c, "a1")
	shareView(t, owner.c, "a2")
	if count() != 1 {
		t.Fatalf("dedupe 10 menit: %d", count())
	}
	if nn := notifs(t, yes.c, ""); len(nn.Items) != 2 {
		t.Fatalf("lonceng tetap 2: %+v", nn)
	}
	e.sql(`UPDATE email_outbox SET created_at = now() - interval '11 minutes'`)
	shareView(t, owner.c, "a3")
	if count() != 2 {
		t.Fatalf("setelah 10 menit: %d", count())
	}
	waitFor(t, 8*time.Second, "terkirim & dikosongkan", func() bool {
		var n int
		_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM email_outbox WHERE kind='saved_view_shared' AND status='sent' AND body IS NULL`).Scan(&n)
		return n == 2
	})
	// tanpa SMTP
	e2 := newEnv(t)
	o2, y2 := e2.newUser("operator"), e2.newUser("viewer")
	setPrefs(t, y2.c, map[string]any{"email_shared_views": true})
	shareView(t, o2.c, "b1")
	var n int
	_ = e2.pool.QueryRow(context.Background(), `SELECT count(*) FROM email_outbox`).Scan(&n)
	if n != 0 {
		t.Fatalf("tanpa SMTP: %d", n)
	}
}

// ledakan permintaan SPA (puluhan permintaan paralel saat memuat halaman) tidak boleh 429, dan sambungan SSE tidak ikut tergilas
// oleh pembatas umum per pengguna (EventSource tidak mencoba ulang setelah respons 429).
func TestSPABurstNotRateLimitedAndStreamIndependent(t *testing.T) {
	e := newEnv(t)
	u := e.newUser("operator")
	limited := 0
	for i := 0; i < 80; i++ { // 80 permintaan beruntun ≈ beberapa kali muat ulang halaman
		if st := u.c.do("GET", "/api/v1/notifications", nil, nil); st == 429 {
			limited++
		}
	}
	if limited > 0 {
		t.Fatalf("ledakan 80 permintaan: %d kena 429 (burst per pengguna terlalu kecil untuk SPA)", limited)
	}
	// habiskan pembatas umum dengan ledakan paralel sampai benar-benar 429, lalu sambungan SSE tetap diterima
	var mu sync.Mutex
	hit := 0
	var wg sync.WaitGroup
	for i := 0; i < 400; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if u.c.do("GET", "/api/v1/notifications", nil, nil) == 429 {
				mu.Lock()
				hit++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if hit == 0 {
		t.Fatal("ledakan paralel 400 permintaan seharusnya memicu pembatas umum")
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/events/stream", nil)
	resp, err := u.c.http.Do(req.WithContext(sseCtx(t)))
	if err != nil {
		t.Fatalf("SSE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("SSE harus 200 walau pembatas umum habis: %d", resp.StatusCode)
	}
}

func sseCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}
