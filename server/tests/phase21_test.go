package tests

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jipaw/smsc-app/server/internal/config"
	"github.com/jipaw/smsc-app/server/internal/proto"
)

// craftAssigned membuat pesan 'assigned' seolah-olah leader lama mati sebelum mengirim ke device.
func craftAssigned(t *testing.T, e *env, admin *client, dev uuid.UUID, leaseOffset string) string {
	t.Helper()
	m := admin.send(num, "pesan "+uuid.NewString()[:6])[0]
	e.sql(`UPDATE messages SET status='assigned', sim_id=(SELECT id FROM sims WHERE device_id=$2 LIMIT 1), device_id=$2, attempt=1,
		assigned_at=now() - interval '10 seconds', lease_expires_at=now() + $3::interval WHERE id=$1`, m.ID, dev, leaseOffset)
	return m.ID
}

func newTakeoverEnv(t *testing.T) (*env, *client, uuid.UUID, *Sim) {
	t.Helper()
	e := newEnvOpts(t, func(c *config.Config) { c.ReaperInterval = time.Hour }) // reaper diam: lease uji tidak ikut diproses
	admin := e.loginAs("admin")
	if err := e.svc.SetPaused(context.Background(), true, "tes"); err != nil { // dispatcher tidak mengambil pesan 'queued'
		t.Fatal(err)
	}
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	// gerbang dispatcher yang tidak bergantung pada kill switch: SIM dinonaktifkan agar dispatcher harness tidak ikut
	// menugaskan pesan uji (pause saja rapuh di bawah beban); pemulihan/reaper tidak memeriksa sims.enabled
	e.sql(`UPDATE sims SET enabled=false WHERE device_id=$1`, id)
	return e, admin, id, s
}

func status(e *env, id string) string {
	var st string
	_ = e.pool.QueryRow(context.Background(), `SELECT status::text FROM messages WHERE id=$1`, id).Scan(&st)
	return st
}

// pesan assigned yang tak pernah terkirim dipulihkan segera: lease diperbarui, sms.send dikirim tepat sekali, lalu delivered.
func TestTakeoverRecoversUnsentAssignedMessage(t *testing.T) {
	e, admin, dev, s := newTakeoverEnv(t)
	s.OnSend = func(*Sim, proto.Envelope, proto.SmsSend) {} // device menerima tetapi belum membalas: lease tetap terlihat
	id := craftAssigned(t, e, admin, dev, "1 second")
	// kill switch aktif: pemulihan tidak boleh mengirim apa pun
	if n := e.svc.RecoverAssigned(context.Background(), time.Time{}, map[uuid.UUID]bool{}); n != 0 || s.SendCount() != 0 {
		t.Fatalf("saat pause tidak boleh memulihkan: n=%d sends=%d", n, s.SendCount())
	}
	if err := e.svc.SetPaused(context.Background(), false, "tes"); err != nil {
		t.Fatal(err)
	}
	skip := map[uuid.UUID]bool{}
	t0 := time.Now()
	if n := e.svc.RecoverAssigned(context.Background(), time.Time{}, skip); n != 1 {
		t.Fatalf("dipulihkan: %d", n)
	}
	waitFor(t, 5*time.Second, "sms.send diterima", func() bool { return s.SendCount() == 1 })
	var lease time.Time
	_ = e.pool.QueryRow(context.Background(), `SELECT lease_expires_at FROM messages WHERE id=$1`, id).Scan(&lease)
	if lease.Before(t0.Add(1400 * time.Millisecond)) { // ack_timeout_ms uji = 1500; lease awal hanya +1 dtk dari pembuatan
		t.Fatalf("lease harus diperbarui sepanjang ack timeout: %v (t0=%v)", lease, t0)
	}
	// idempoten pada pengambilalihan yang sama
	if n := e.svc.RecoverAssigned(context.Background(), time.Time{}, skip); n != 0 {
		t.Fatalf("pass kedua tidak boleh mengulang: %d", n)
	}
	if v := gauge(t, e, "smsc_lease_recovered_total", nil); v != 1 {
		t.Fatalf("metrik: %v", v)
	}
}

// lease yang habis selama tidak ada leader dipulihkan; yang habis sebelum leader terakhir terlihat dibiarkan untuk reaper.
func TestTakeoverRecoversLeaseExpiredDuringGapOnly(t *testing.T) {
	e, admin, dev, s := newTakeoverEnv(t)
	_ = e.svc.SetPaused(context.Background(), false, "tes")
	inGap := craftAssigned(t, e, admin, dev, "-10 seconds")  // habis 10 dtk lalu (setelah leader terakhir terlihat)
	before := craftAssigned(t, e, admin, dev, "-60 seconds") // habis 60 dtk lalu (sebelum leader terakhir terlihat)
	since := time.Now().Add(-30 * time.Second)
	if n := e.svc.RecoverAssigned(context.Background(), since, map[uuid.UUID]bool{}); n != 1 {
		t.Fatalf("hanya satu yang boleh dipulihkan: %d", n)
	}
	admin.waitStatus(inGap, "delivered", 10*time.Second)
	if status(e, before) != "assigned" || s.SendCount() != 1 {
		t.Fatalf("yang habis sebelum jeda tidak boleh disentuh: %s sends=%d", status(e, before), s.SendCount())
	}
	// reaper (manual) tetap menganggapnya ack_timeout
	e.svc.ReapOnceForTest(context.Background())
	if st := status(e, before); st != "queued" {
		t.Fatalf("reaper harus mengantre ulang: %s", st)
	}
}

// device offline: tidak disentuh (reaper yang menangani ack_timeout).
func TestTakeoverSkipsOfflineDevice(t *testing.T) {
	e, admin, dev, s := newTakeoverEnv(t)
	id := craftAssigned(t, e, admin, dev, "5 seconds")
	_ = e.svc.SetPaused(context.Background(), false, "tes")
	s.cancel()
	waitFor(t, 5*time.Second, "device offline", func() bool { return !e.hub.Connected(dev) })
	if n := e.svc.RecoverAssigned(context.Background(), time.Time{}, map[uuid.UUID]bool{}); n != 0 {
		t.Fatalf("device offline: %d", n)
	}
	if status(e, id) != "assigned" {
		t.Fatalf("status harus tetap assigned: %s", status(e, id))
	}
}

// LeaderWorkers pada instance lain (tanpa sesi lokal): memulihkan lewat relay hub, lalu worker jalan; leader_seen dicatat RunLeader.
func TestLeaderWorkersRecoverThroughOtherInstance(t *testing.T) {
	e, admin, dev, s := newTakeoverEnv(t)
	e.sql(`INSERT INTO settings(key,value) VALUES ('leader_seen', to_jsonb(now() - interval '20 seconds')) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value`)
	id := craftAssigned(t, e, admin, dev, "-5 seconds") // habis selama jeda
	_ = e.svc.SetPaused(context.Background(), false, "tes")
	b := e.peer()
	waitFor(t, 5*time.Second, "B melihat sesi di A", func() bool { return b.hub.Connected(dev) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.svc.RunLeader(ctx, 50*time.Millisecond, b.svc.LeaderWorkers); close(done) }()
	defer func() { cancel(); <-done }()
	admin.waitStatus(id, "delivered", 15*time.Second)
	if s.SendCount() != 1 {
		t.Fatalf("sms.send tepat sekali: %d", s.SendCount())
	}
	var recent bool
	_ = e.pool.QueryRow(context.Background(), `SELECT (value #>> '{}')::timestamptz > now() - interval '5 seconds' FROM settings WHERE key='leader_seen'`).Scan(&recent)
	if !recent {
		t.Fatal("leader_seen harus diperbarui oleh leader")
	}
	waitFor(t, 5*time.Second, "metrik pemulihan di B", func() bool { return gauge(t, b, "smsc_lease_recovered_total", nil) == 1 })
}

// ---------- fase 22: pesan 'sending' saat leader berganti ----------

// craftSending membuat pesan 'sending' yang dikirim `age` lalu (sent_timeout uji = 3 dtk).
func craftSending(t *testing.T, e *env, admin *client, dev uuid.UUID, age string) string {
	t.Helper()
	m := admin.send(num, "sending "+uuid.NewString()[:6])[0]
	e.sql(`UPDATE messages SET status='sending', sim_id=(SELECT id FROM sims WHERE device_id=$2 LIMIT 1), device_id=$2, attempt=1,
		assigned_at=now() - $3::interval, lease_expires_at=NULL WHERE id=$1`, m.ID, dev, age)
	return m.ID
}

func graceOf(e *env, id string) int {
	var g int
	_ = e.pool.QueryRow(context.Background(), `SELECT sent_grace_ms FROM messages WHERE id=$1`, id).Scan(&g)
	return g
}

// batas sent_timeout yang jatuh di jeda tanpa leader diperpanjang; yang sudah lewat sebelum leader terakhir terlihat tidak.
func TestTakeoverExtendsSendingDeadlineAcrossGap(t *testing.T) {
	e, admin, dev, _ := newTakeoverEnv(t)
	ctx := context.Background()
	inGap := craftSending(t, e, admin, dev, "4 seconds")              // batas (3 dtk) lewat 1 dtk lalu: setelah leader terakhir terlihat
	before := craftSending(t, e, admin, dev, "20 seconds")            // batas lewat 17 dtk lalu: sebelum leader terakhir terlihat
	e.sql(`UPDATE messages SET status_queries=2 WHERE id=$1`, before) // kuota sms.status habis: uji jalur alur lama (fase 23)
	fresh := craftSending(t, e, admin, dev, "1 second")               // batas masih 2 dtk lagi
	since := time.Now().Add(-5 * time.Second)
	if n := e.svc.ExtendSendingGrace(ctx, since); n != 2 {
		t.Fatalf("dua pesan harus diperpanjang (inGap, fresh): %d", n)
	}
	if g := graceOf(e, inGap); g < 2900 || g > 3000 { // jeda 5 dtk dibatasi satu sent_timeout (3 dtk)
		t.Fatalf("grace inGap: %d", g)
	}
	if graceOf(e, before) != 0 {
		t.Fatal("pesan yang batasnya lewat sebelum jeda tidak boleh diperpanjang")
	}
	if graceOf(e, fresh) == 0 {
		t.Fatal("pesan yang batasnya masih di depan juga mengalami jeda")
	}
	e.svc.ReapOnceForTest(ctx)
	if status(e, inGap) != "sending" || status(e, fresh) != "sending" {
		t.Fatalf("reaper tidak boleh menandai yang diperpanjang: %s %s", status(e, inGap), status(e, fresh))
	}
	if status(e, before) != "unknown" {
		t.Fatalf("yang tak diperpanjang tetap unknown: %s", status(e, before))
	}
	// kumulatif untuk pengambilalihan berikutnya
	e.svc.ExtendSendingGrace(ctx, time.Now().Add(-1*time.Second))
	if g := graceOf(e, inGap); g < 3900 {
		t.Fatalf("grace harus kumulatif: %d", g)
	}
	if v := gauge(t, e, "smsc_sending_grace_total", nil); v < 3 {
		t.Fatalf("metrik: %v", v)
	}
	// tanpa info leader sebelumnya tidak ada perpanjangan
	if n := e.svc.ExtendSendingGrace(ctx, time.Time{}); n != 0 {
		t.Fatalf("since nol: %d", n)
	}
}

// dengan retry_on_unknown, tanpa perpanjangan reaper mengantre ulang pesan yang mungkin sudah terkirim (kirim ganda).
func TestTakeoverGraceAvoidsDuplicateRetryOnUnknown(t *testing.T) {
	e, admin, dev, _ := newTakeoverEnv(t)
	ctx := context.Background()
	e.sql(`UPDATE dispatch_policies SET retry_on_unknown=true WHERE is_default`)
	protected := craftSending(t, e, admin, dev, "4 seconds")
	unprotected := craftSending(t, e, admin, dev, "4 seconds")
	e.sql(`UPDATE messages SET sent_grace_ms=0 WHERE id=$1`, unprotected)
	e.sql(`UPDATE messages SET status_queries=2 WHERE id=$1`, unprotected) // kuota sms.status habis: jalur alur lama
	// hanya `protected` yang melewati jalur pengambilalihan
	e.sql(`UPDATE messages SET sent_grace_ms=3000 WHERE id=$1`, protected)
	e.svc.ReapOnceForTest(ctx)
	if status(e, protected) != "sending" {
		t.Fatalf("yang diperpanjang harus tetap sending: %s", status(e, protected))
	}
	if st := status(e, unprotected); st != "queued" {
		t.Fatalf("kontrol: tanpa perpanjangan reaper mengantre ulang: %s", st)
	}
}

// penugasan baru menghapus grace lama (grace hanya untuk satu percobaan kirim).
func TestAssignmentResetsSendingGrace(t *testing.T) {
	e, admin, dev, s := newTakeoverEnv(t)
	s.OnSend = func(*Sim, proto.Envelope, proto.SmsSend) {}
	m := admin.send(num, "reset grace")[0]
	e.sql(`UPDATE messages SET sent_grace_ms=5000 WHERE id=$1`, m.ID)
	e.sql(`UPDATE sims SET enabled=true WHERE device_id=$1`, dev) // dispatcher boleh bekerja untuk tes ini
	_ = e.svc.SetPaused(context.Background(), false, "tes")
	admin.waitStatus(m.ID, "assigned", 10*time.Second)
	if g := graceOf(e, m.ID); g != 0 {
		t.Fatalf("grace harus direset saat penugasan: %d", g)
	}
	_ = dev
}

// LeaderWorkers memperpanjang batas sending sebelum reaper leader baru berjalan.
func TestLeaderWorkersExtendSendingBeforeReaper(t *testing.T) {
	e, admin, dev, _ := newTakeoverEnv(t)
	e.sql(`INSERT INTO settings(key,value) VALUES ('leader_seen', to_jsonb(now() - interval '4 seconds')) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value`)
	id := craftSending(t, e, admin, dev, "4 seconds")
	b := e.peer()
	b.svc.Cfg.ReaperInterval = 100 * time.Millisecond // reaper di B benar-benar berjalan
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.svc.RunLeader(ctx, 50*time.Millisecond, b.svc.LeaderWorkers); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, 5*time.Second, "grace terpasang", func() bool { return graceOf(e, id) > 0 })
	time.Sleep(600 * time.Millisecond) // beri reaper B beberapa putaran
	if status(e, id) != "sending" {
		t.Fatalf("reaper leader baru tidak boleh langsung menandai: %s", status(e, id))
	}
}
