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
	_ = e.svc.SetPaused(context.Background(), false, "tes")
	id := craftAssigned(t, e, admin, dev, "5 seconds")
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
	_ = e.svc.SetPaused(context.Background(), false, "tes")
	e.sql(`INSERT INTO settings(key,value) VALUES ('leader_seen', to_jsonb(now() - interval '20 seconds')) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value`)
	id := craftAssigned(t, e, admin, dev, "-5 seconds") // habis selama jeda
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
