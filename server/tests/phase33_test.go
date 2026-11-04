package tests

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

// kegagalan parsial lewat event failed (cuplikan parts_sent=sentSnap dari total).
func failPartialByEvent(t *testing.T, e *env, admin *client, dev uuid.UUID, total, sentSnap int) string {
	t.Helper()
	id := craftSending(t, e, admin, dev, "1 second")
	e.sql(`UPDATE messages SET parts=$2 WHERE id=$1`, id, total)
	ev := map[string]any{"event_id": uuid.NewString(), "message_id": id, "attempt": 1, "state": "failed", "parts_total": total, "parts_sent": sentSnap, "error_code": "generic_failure", "at": time.Now().UTC().Format(time.RFC3339)}
	raw, _ := json.Marshal(ev)
	if err := e.svc.OnSmsEventRaw(context.Background(), dev, raw); err != nil {
		t.Fatal(err)
	}
	return id
}

func partsSentOf(e *env, id string) int {
	var n *int
	_ = e.pool.QueryRow(context.Background(), `SELECT partial_sent FROM messages WHERE id=$1`, id).Scan(&n)
	if n == nil {
		return -1
	}
	return *n
}

func checkedOf(e *env, id string) bool {
	var ok bool
	_ = e.pool.QueryRow(context.Background(), `SELECT partial_checked_at IS NOT NULL FROM messages WHERE id=$1`, id).Scan(&ok)
	return ok
}

// hitungan bagian terkirim disegarkan dari HP: cuplikan 1 dari 4 menjadi 3 dari 4; hanya ditanya sekali; bertahan monoton.
func TestPartialCountRefreshedFromDeviceOnce(t *testing.T) {
	gen := "generic_failure"
	e, admin, dev, s := newTakeoverEnv(t)
	s.OnStatus = stateWith("failed", 4, 3, 0, &gen)
	id := failPartialByEvent(t, e, admin, dev, 4, 1)
	if partsSentOf(e, id) != 1 {
		t.Fatalf("cuplikan awal: %d", partsSentOf(e, id))
	}
	e.svc.ReapOnceForTest(context.Background()) // belum lewat PartialRefreshAfter (300 ms): tidak ditanya
	if s.StatusCount() != 0 {
		t.Fatalf("terlalu dini: %d", s.StatusCount())
	}
	time.Sleep(450 * time.Millisecond)
	e.svc.ReapOnceForTest(context.Background())
	waitFor(t, 5*time.Second, "hitungan disegarkan", func() bool { return partsSentOf(e, id) == 3 })
	e.svc.ReapOnceForTest(context.Background()) // sudah dicek: tidak ditanya lagi
	time.Sleep(300 * time.Millisecond)
	if s.StatusCount() != 1 || !checkedOf(e, id) {
		t.Fatalf("hanya sekali: n=%d checked=%v", s.StatusCount(), checkedOf(e, id))
	}
	var m partialView
	admin.do("GET", "/api/v1/messages/"+id, nil, &m)
	if !m.Partial || m.PartsSent == nil || *m.PartsSent != 3 {
		t.Fatalf("API: %+v", m)
	}
	var n int
	var prev int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*), COALESCE(max((data->>'previous')::int),0) FROM message_events WHERE message_id=$1 AND type='parts_refreshed'`, id).Scan(&n, &prev)
	if n != 1 || prev != 1 {
		t.Fatalf("event parts_refreshed: n=%d previous=%d", n, prev)
	}
	if v := gauge(t, e, "smsc_status_queries_total", map[string]string{"result": "refined"}); v != 1 {
		t.Fatalf("refined: %v", v)
	}
}

// jawaban lebih kecil, device tak punya job, atau attempt lain tidak mengubah hitungan; device offline ditanya nanti.
func TestPartialCountRefreshMonotonicAndRobust(t *testing.T) {
	gen := "generic_failure"
	e, admin, dev, s := newTakeoverEnv(t)
	// 1) jawaban lebih kecil dari cuplikan
	s.OnStatus = stateWith("failed", 4, 0, 0, &gen)
	a := failPartialByEvent(t, e, admin, dev, 4, 2)
	time.Sleep(450 * time.Millisecond)
	e.svc.ReapOnceForTest(context.Background())
	waitFor(t, 5*time.Second, "dijawab", func() bool { return s.StatusCount() == 1 })
	time.Sleep(300 * time.Millisecond)
	if partsSentOf(e, a) != 2 {
		t.Fatalf("tidak boleh turun: %d", partsSentOf(e, a))
	}
	// 2) device menjawab unknown
	s.OnStatus = answer("unknown", nil)
	b := failPartialByEvent(t, e, admin, dev, 4, 1)
	time.Sleep(450 * time.Millisecond)
	e.svc.ReapOnceForTest(context.Background())
	waitFor(t, 5*time.Second, "dijawab unknown", func() bool { return s.StatusCount() == 2 })
	time.Sleep(300 * time.Millisecond)
	if partsSentOf(e, b) != 1 || !checkedOf(e, b) {
		t.Fatalf("unknown: %d checked=%v", partsSentOf(e, b), checkedOf(e, b))
	}
	// 3) hitungan mustahil (terkirim > total) ditolak sebagai invalid
	s.OnStatus = stateWith("failed", 4, 9, 0, &gen)
	c := failPartialByEvent(t, e, admin, dev, 4, 1)
	time.Sleep(450 * time.Millisecond)
	e.svc.ReapOnceForTest(context.Background())
	waitFor(t, 5*time.Second, "dijawab invalid", func() bool { return s.StatusCount() == 3 })
	time.Sleep(300 * time.Millisecond)
	if partsSentOf(e, c) != 1 {
		t.Fatalf("invalid tidak boleh mengubah: %d", partsSentOf(e, c))
	}
	// 4) device offline: tidak ditandai dicek; ditanya setelah tersambung lagi
	s.cancel()
	waitFor(t, 5*time.Second, "device offline", func() bool { return !e.hub.Connected(dev) })
	d := failPartialByEvent(t, e, admin, dev, 4, 1)
	time.Sleep(450 * time.Millisecond)
	e.svc.ReapOnceForTest(context.Background())
	if checkedOf(e, d) {
		t.Fatal("offline: belum boleh ditandai dicek")
	}
}

// retry terkonfirmasi menghapus penanda dan status dicek (percobaan baru mulai bersih).
func TestPartialRetryResetsRefreshState(t *testing.T) {
	gen := "generic_failure"
	e, admin, dev, s := newTakeoverEnv(t)
	s.OnStatus = stateWith("failed", 4, 3, 0, &gen)
	id := failPartialByEvent(t, e, admin, dev, 4, 1)
	time.Sleep(450 * time.Millisecond)
	e.svc.ReapOnceForTest(context.Background())
	waitFor(t, 5*time.Second, "disegarkan", func() bool { return partsSentOf(e, id) == 3 })
	if st := admin.do("POST", "/api/v1/messages/"+id+"/retry", map[string]any{"confirm_duplicate_risk": true}, nil); st != 200 {
		t.Fatalf("retry: %d", st)
	}
	if partsSentOf(e, id) != -1 || checkedOf(e, id) {
		t.Fatalf("setelah retry: partial_sent=%d checked=%v", partsSentOf(e, id), checkedOf(e, id))
	}
}
