package tests

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type partialView struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Partial   bool   `json:"partial"`
	PartsSent *int   `json:"parts_sent"`
	Parts     int    `json:"parts"`
	Attempt   int    `json:"attempt"`
}

// membuat pesan 3 bagian yang gagal parsial lewat jalur sms.state (bagian ke-1 terkirim, lalu gagal).
func makePartial(t *testing.T) (*env, *client, string, *Sim) {
	t.Helper()
	gen := "generic_failure"
	e, admin, dev, s := newTakeoverEnv(t)
	s.OnStatus = stateWith("failed", 3, 1, 0, &gen)
	id := craftSending(t, e, admin, dev, "4 seconds")
	e.sql(`UPDATE messages SET parts=3 WHERE id=$1`, id)
	e.svc.ReapOnceForTest(context.Background())
	waitStatusIs(t, e, id, "failed")
	return e, admin, id, s
}

// API, filter, CSV, webhook, metrik, dan retry dengan konfirmasi.
func TestPartialFailureVisibleInAPIFilterCSVWebhookAndRetryNeedsConfirm(t *testing.T) {
	// webhook harus ada SEBELUM transisi agar outbox terisi: daftarkan lebih dulu
	gen := "generic_failure"
	e, admin, dev, s := newTakeoverEnv(t)
	wh := admin.newWebhook("http://127.0.0.1:9/ignored", "message.failed")
	s.OnStatus = stateWith("failed", 3, 1, 0, &gen)
	id := craftSending(t, e, admin, dev, "4 seconds")
	e.sql(`UPDATE messages SET parts=3 WHERE id=$1`, id)
	other := craftSending(t, e, admin, dev, "1 second") // pesan lain yang bukan parsial (masih sending)
	e.svc.ReapOnceForTest(context.Background())
	waitStatusIs(t, e, id, "failed")

	var m partialView
	if st := admin.do("GET", "/api/v1/messages/"+id, nil, &m); st != 200 || !m.Partial || m.PartsSent == nil || *m.PartsSent != 1 || m.Parts != 3 || m.Status != "failed" {
		t.Fatalf("detail: %d %+v", st, m)
	}
	var o partialView
	admin.do("GET", "/api/v1/messages/"+other, nil, &o)
	if o.Partial || o.PartsSent != nil {
		t.Fatalf("pesan biasa tidak boleh parsial: %+v", o)
	}
	list := func(q string) []partialView {
		var out struct{ Items []partialView }
		if st := admin.do("GET", "/api/v1/messages?"+q, nil, &out); st != 200 {
			t.Fatalf("list %s: %d", q, st)
		}
		return out.Items
	}
	if p := list("partial=true"); len(p) != 1 || p[0].ID != id {
		t.Fatalf("partial=true: %+v", p)
	}
	for _, x := range list("partial=false") {
		if x.ID == id {
			t.Fatal("partial=false tidak boleh memuat pesan parsial")
		}
	}
	if st := admin.do("GET", "/api/v1/messages?partial=mungkin", nil, nil); st != 422 {
		t.Fatalf("partial tidak valid harus 422: %d", st)
	}
	// ekspor CSV: kolom partial & parts_sent, ikut filter
	resp, err := admin.http.Get(e.srv.URL + "/api/v1/messages/export?partial=true")
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := csv.NewReader(resp.Body).ReadAll()
	resp.Body.Close()
	if len(rows) != 2 || rows[0][len(rows[0])-2] != "partial" || rows[1][len(rows[1])-2] != "true" || rows[1][len(rows[1])-1] != "1" {
		t.Fatalf("csv: %v", rows)
	}
	// webhook message.failed memuat penanda parsial
	var payload string
	if err := e.pool.QueryRow(context.Background(), `SELECT payload::text FROM webhook_deliveries WHERE webhook_id=$1 AND event_type='message.failed'`, wh.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"partial": true`) || !strings.Contains(payload, `"parts_sent": 1`) || !strings.Contains(payload, `"parts_total": 3`) {
		t.Fatalf("payload webhook: %s", payload)
	}
	if v := gauge(t, e, "smsc_messages_partial_total", nil); v != 1 {
		t.Fatalf("metrik: %v", v)
	}
	// retry tanpa konfirmasi ditolak (bagian ganda); dengan konfirmasi berjalan dan menghapus penanda
	if st := admin.do("POST", "/api/v1/messages/"+id+"/retry", map[string]any{}, nil); st != 422 {
		t.Fatalf("retry parsial tanpa konfirmasi harus 422: %d", st)
	}
	if st := admin.do("POST", "/api/v1/messages/"+id+"/retry", map[string]any{"confirm_duplicate_risk": true}, &m); st != 200 || m.Partial || m.Status != "queued" || m.Attempt != 2 {
		t.Fatalf("retry dikonfirmasi: %d %+v", st, m)
	}
}

// kegagalan biasa (bukan parsial) tetap bisa di-retry tanpa konfirmasi; webhook failed biasa memuat partial=false.
func TestNonPartialFailureRetryUnchanged(t *testing.T) {
	e, admin, dev, _ := newTakeoverEnv(t)
	id := craftSending(t, e, admin, dev, "1 second")
	e.sql(`UPDATE messages SET status='failed', error_code='generic_failure', finished_at=now() WHERE id=$1`, id)
	var m partialView
	if st := admin.do("GET", "/api/v1/messages/"+id, nil, &m); st != 200 || m.Partial {
		t.Fatalf("%d %+v", st, m)
	}
	if st := admin.do("POST", "/api/v1/messages/"+id+"/retry", map[string]any{}, &m); st != 200 || m.Status != "queued" {
		t.Fatalf("retry biasa: %d %+v", st, m)
	}
	_ = time.Second
}

// tes tidak menyentuh makePartial secara langsung; pastikan helper tetap dipakai agar jalur terpisah (tanpa webhook) teruji.
func TestPartialFlagSurvivesWithoutWebhook(t *testing.T) {
	e, admin, id, _ := makePartial(t)
	var m partialView
	if st := admin.do("GET", "/api/v1/messages/"+id, nil, &m); st != 200 || !m.Partial {
		t.Fatalf("%d %+v", st, m)
	}
	_ = e
}

// jalur event biasa (bukan sms.state): failed multi-bagian dengan parts_sent => parsial, tidak diantre ulang walau retryable.
func newUUID() string { return uuid.NewString() }

func TestFailedEventWithPartsSentIsPartialAndNotRetried(t *testing.T) {
	e, admin, dev, s := newTakeoverEnv(t)
	cases := []struct {
		name         string
		total, sent  int
		wantStatus   string
		wantPartial  bool
		wantAttempts int
	}{
		{"parsial-1-dari-3", 3, 1, "failed", true, 1},
		{"semua-bagian-terkirim-lalu-gagal", 2, 2, "failed", false, 1}, // tidak parsial, tetap tidak diantre ulang
		{"tak-ada-bagian-terkirim", 3, 0, "queued", false, 2},          // gagal total retryable => retry biasa
	}
	for _, c := range cases {
		id := craftSending(t, e, admin, dev, "1 second")
		e.sql(`UPDATE messages SET parts=$2 WHERE id=$1`, id, c.total)
		sent := c.sent
		ev := map[string]any{"event_id": newUUID(), "message_id": id, "attempt": 1, "state": "failed", "parts_total": c.total, "error_code": "generic_failure", "at": time.Now().UTC().Format(time.RFC3339)}
		if sent > 0 {
			ev["parts_sent"] = sent
		}
		raw, _ := json.Marshal(ev)
		if err := e.svc.OnSmsEventRaw(context.Background(), dev, raw); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var m partialView
		if st := admin.do("GET", "/api/v1/messages/"+id, nil, &m); st != 200 || m.Status != c.wantStatus || m.Partial != c.wantPartial || m.Attempt != c.wantAttempts {
			t.Fatalf("%s: %d %+v", c.name, st, m)
		}
		if c.wantPartial && (m.PartsSent == nil || *m.PartsSent != c.sent) {
			t.Fatalf("%s: parts_sent %v", c.name, m.PartsSent)
		}
	}
	_ = s
}

// filter all_sent: gagal tetapi semua bagian terkirim; kombinasi dengan partial memberi parsial sejati.
func TestListFilterAllSent(t *testing.T) {
	e, admin, dev, _ := newTakeoverEnv(t)
	mk := func(parts, sent int) string {
		id := craftSending(t, e, admin, dev, "1 second")
		if sent < 0 {
			e.sql(`UPDATE messages SET status='failed', error_code='generic_failure', finished_at=now(), parts=$2 WHERE id=$1`, id, parts)
		} else {
			e.sql(`UPDATE messages SET status='failed', error_code='generic_failure', finished_at=now(), parts=$2, partial_sent=$3 WHERE id=$1`, id, parts, sent)
		}
		return id
	}
	strict, all, plain := mk(3, 1), mk(3, 3), mk(2, -1)
	ids := func(q string) map[string]bool {
		var out struct{ Items []partialView }
		if st := admin.do("GET", "/api/v1/messages?"+q, nil, &out); st != 200 {
			t.Fatalf("list %s: %d", q, st)
		}
		m := map[string]bool{}
		for _, x := range out.Items {
			m[x.ID] = true
		}
		return m
	}
	eq := func(q string, want ...string) {
		t.Helper()
		got := ids(q)
		if len(got) != len(want) {
			t.Fatalf("%s: %v, mau %v", q, got, want)
		}
		for _, w := range want {
			if !got[w] {
				t.Fatalf("%s: %v tanpa %s", q, got, w)
			}
		}
	}
	eq("all_sent=true", all)
	eq("partial=true", strict, all)           // partial tetap mencakup keduanya
	eq("partial=true&all_sent=false", strict) // parsial sejati
	eq("partial=true&all_sent=true", all)
	eq("partial=false&all_sent=true") // kontradiksi => kosong
	if got := ids("all_sent=false&status=failed"); got[all] || !got[strict] || !got[plain] {
		t.Fatalf("all_sent=false: %v", got)
	}
	if st := admin.do("GET", "/api/v1/messages?all_sent=entah", nil, nil); st != 422 {
		t.Fatalf("nilai tidak valid harus 422: %d", st)
	}
	resp, err := admin.http.Get(e.srv.URL + "/api/v1/messages/export?all_sent=true")
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := csv.NewReader(resp.Body).ReadAll()
	resp.Body.Close()
	if len(rows) != 2 || rows[1][0] != all {
		t.Fatalf("csv: %v", rows)
	}
}
