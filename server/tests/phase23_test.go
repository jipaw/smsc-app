package tests

import (
	"context"
	"testing"
	"time"

	"github.com/jipaw/smsc-app/server/internal/proto"
)

func answer(state string, code *string) func(*Sim, proto.Envelope, proto.SmsStatus) {
	return func(s *Sim, env proto.Envelope, q proto.SmsStatus) { s.State(env, q, state, code) }
}

func queriesOf(e *env, id string) int {
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT status_queries FROM messages WHERE id=$1`, id).Scan(&n)
	return n
}

func waitStatusIs(t *testing.T, e *env, id, want string) {
	t.Helper()
	waitFor(t, 5*time.Second, "status "+want, func() bool { return status(e, id) == want })
}

// jawaban device menentukan nasib pesan sending yang lewat batas waktu, bukan tebakan reaper.
func TestStatusQueryAnswerDecidesOutcome(t *testing.T) {
	gen := "invalid_destination" // non-retryable: generic_failure akan diantre ulang oleh logika retry biasa
	cases := []struct {
		reply string
		code  *string
		want  string
	}{{"sent", nil, "sent"}, {"delivered", nil, "delivered"}, {"failed", &gen, "failed"}, {"unknown", nil, "unknown"}}
	for _, c := range cases {
		t.Run(c.reply, func(t *testing.T) {
			e, admin, dev, s := newTakeoverEnv(t)
			s.OnStatus = answer(c.reply, c.code)
			id := craftSending(t, e, admin, dev, "4 seconds")
			e.svc.ReapOnceForTest(context.Background())
			waitStatusIs(t, e, id, c.want)
			if s.StatusCount() != 1 || s.FirstStatus().MessageID != id || s.FirstStatus().Attempt != 1 {
				t.Fatalf("query: %d", s.StatusCount())
			}
			if c.want == "unknown" {
				var code string
				_ = e.pool.QueryRow(context.Background(), `SELECT error_code FROM messages WHERE id=$1`, id).Scan(&code)
				if code != "device_lost" {
					t.Fatalf("error_code: %s", code)
				}
			}
			if v := gauge(t, e, "smsc_status_queries_total", map[string]string{"result": "queried"}); v != 1 {
				t.Fatalf("queried: %v", v)
			}
		})
	}
}

// device menjawab "masih mengirim": batas diperpanjang, tidak ditanya lagi sebelum batas baru.
func TestStatusQueryAliveExtendsDeadline(t *testing.T) {
	e, admin, dev, s := newTakeoverEnv(t)
	s.OnStatus = answer("sending", nil)
	id := craftSending(t, e, admin, dev, "4 seconds")
	e.svc.ReapOnceForTest(context.Background())
	waitFor(t, 5*time.Second, "grace bertambah oleh jawaban", func() bool { return graceOf(e, id) >= 3000+400 })
	e.svc.ReapOnceForTest(context.Background())
	if status(e, id) != "sending" || s.StatusCount() != 1 || queriesOf(e, id) != 1 {
		t.Fatalf("tidak boleh ditanya lagi / berubah: %s q=%d n=%d", status(e, id), queriesOf(e, id), s.StatusCount())
	}
}

// device lama yang diam: ditanya sampai kuota, lalu alur lama (unknown). Dengan retry_on_unknown: diantre ulang.
func TestStatusQueryNoAnswerFallsBackAfterQuota(t *testing.T) {
	e, admin, dev, s := newTakeoverEnv(t)
	ctx := context.Background()
	id := craftSending(t, e, admin, dev, "4 seconds")
	for i := 1; i <= 2; i++ {
		e.svc.ReapOnceForTest(ctx)
		if status(e, id) != "sending" || queriesOf(e, id) != i {
			t.Fatalf("putaran %d: %s q=%d", i, status(e, id), queriesOf(e, id))
		}
		time.Sleep(500 * time.Millisecond) // lewati StatusQueryWait (400 ms)
	}
	e.svc.ReapOnceForTest(ctx)
	if status(e, id) != "unknown" || s.StatusCount() != 2 {
		t.Fatalf("setelah kuota: %s queries=%d", status(e, id), s.StatusCount())
	}
}

// inti manfaat: dengan retry_on_unknown, pesan yang sebenarnya sudah terkirim tidak diantre ulang (tanpa SMS ganda).
func TestStatusQueryPreventsDuplicateWithRetryOnUnknown(t *testing.T) {
	e, admin, dev, s := newTakeoverEnv(t)
	e.sql(`UPDATE dispatch_policies SET retry_on_unknown=true WHERE is_default`)
	s.OnStatus = answer("sent", nil)
	id := craftSending(t, e, admin, dev, "4 seconds")
	e.svc.ReapOnceForTest(context.Background())
	waitStatusIs(t, e, id, "sent")
	var attempt int
	_ = e.pool.QueryRow(context.Background(), `SELECT attempt FROM messages WHERE id=$1`, id).Scan(&attempt)
	if attempt != 1 || s.SendCount() != 0 {
		t.Fatalf("tidak boleh dikirim ulang: attempt=%d sends=%d", attempt, s.SendCount())
	}
}

// device offline: tidak ada yang ditanya, alur lama langsung berlaku. Jawaban basi (attempt lain) diabaikan.
func TestStatusQueryOfflineAndStaleAnswer(t *testing.T) {
	e, admin, dev, s := newTakeoverEnv(t)
	ctx := context.Background()
	s.OnStatus = func(s *Sim, env proto.Envelope, q proto.SmsStatus) {
		s.State(env, proto.SmsStatus{MessageID: q.MessageID, Attempt: 99}, "sent", nil)
	}
	stale := craftSending(t, e, admin, dev, "4 seconds")
	e.svc.ReapOnceForTest(ctx)
	waitFor(t, 5*time.Second, "pertanyaan terkirim", func() bool { return s.StatusCount() == 1 })
	time.Sleep(300 * time.Millisecond)
	if status(e, stale) != "sending" {
		t.Fatalf("jawaban dengan attempt lain harus diabaikan: %s", status(e, stale))
	}
	if v := gauge(t, e, "smsc_status_queries_total", map[string]string{"result": "stale"}); v != 1 {
		t.Fatalf("stale: %v", v)
	}
	s.cancel()
	waitFor(t, 5*time.Second, "device offline", func() bool { return !e.hub.Connected(dev) })
	off := craftSending(t, e, admin, dev, "4 seconds")
	e.svc.ReapOnceForTest(ctx)
	if status(e, off) != "unknown" {
		t.Fatalf("device offline: alur lama: %s", status(e, off))
	}
}

// reaper di instance A bertanya ke device yang tersambung di instance B; jawaban diproses di B.
func TestStatusQueryAcrossInstances(t *testing.T) {
	e, admin, dev, _ := newTakeoverEnv(t) // pause + reaper manual di e (A); sesi device awal di A
	b := e.peer()
	id2, secret2 := e.enroll(admin)
	s2, _ := b.connect(id2, secret2, nil) // device kedua di B
	s2.OnStatus = answer("delivered", nil)
	waitFor(t, 5*time.Second, "A melihat sesi di B", func() bool { return e.hub.Connected(id2) })
	id := craftSending(t, e, admin, id2, "4 seconds")
	_ = dev
	e.svc.ReapOnceForTest(context.Background())
	waitStatusIs(t, e, id, "delivered")
	if s2.StatusCount() != 1 {
		t.Fatalf("pertanyaan harus sampai ke device di B: %d", s2.StatusCount())
	}
}
