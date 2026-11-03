package tests

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jipaw/smsc-app/server/internal/api"
	"github.com/jipaw/smsc-app/server/internal/bus"
	"github.com/jipaw/smsc-app/server/internal/core"
	"github.com/jipaw/smsc-app/server/internal/hub"
	"github.com/jipaw/smsc-app/server/internal/proto"
)

// peer membuat instance kedua (proses lain) di database yang sama: hub + API + cluster, tanpa dispatcher/leader.
func (e *env) peer() *env {
	e.t.Helper()
	b := bus.New(5 * time.Minute)
	svc := core.New(e.pool, e.svc.Cfg, e.svc.Log, b)
	h := hub.New(svc, e.svc.Log)
	srv := httptest.NewServer(api.New(svc, h, b, e.svc.Log).Router())
	ctx, cancel := context.WithCancel(context.Background())
	go h.RunCluster(ctx)
	p := &env{t: e.t, pool: e.pool, svc: svc, hub: h, srv: srv, cancel: cancel}
	e.t.Cleanup(func() { cancel(); srv.Close() })
	return p
}

func remoteSeen(from *env, id uuid.UUID) bool { return from.hub.Connected(id) }

// device tersambung ke instance B, dispatcher (leader) di A: pesan tetap terkirim, event/ack dari B memperbarui status.
func TestMultiInstanceSendThroughOtherInstance(t *testing.T) {
	a := newEnv(t)
	b := a.peer()
	admin := a.loginAs("admin")
	id, secret := a.enroll(admin)
	s, _ := b.connect(id, secret, nil) // sesi ada di B
	s.OnSend = Happy
	waitFor(t, 5*time.Second, "A melihat sesi di B", func() bool { return remoteSeen(a, id) })
	if a.hub.Connected(id) && !remoteSeen(a, id) {
		t.Fatal("tidak konsisten")
	}
	m := admin.send(num, "lewat instance lain")[0]
	admin.waitStatus(m.ID, "delivered", 15*time.Second)
	if s.SendCount() != 1 {
		t.Fatalf("sms.send harus tepat sekali: %d", s.SendCount())
	}
	var n int
	_ = a.pool.QueryRow(context.Background(), `SELECT count(*) FROM cmd_refs`).Scan(&n)
	if n != 0 {
		t.Fatalf("cmd_refs harus kosong setelah ack: %d", n)
	}
}

// pembatalan (Request) ke device di instance lain, dan revoke menutup soket di instance lain.
func TestMultiInstanceRequestAndClose(t *testing.T) {
	a := newEnv(t)
	b := a.peer()
	admin := a.loginAs("admin")
	id, secret := a.enroll(admin)
	s, _ := b.connect(id, secret, nil)
	waitFor(t, 5*time.Second, "A melihat sesi di B", func() bool { return remoteSeen(a, id) })
	// Request lintas instance: sms.cancel dijawab ack oleh device di B
	env, _ := proto.New(proto.TypeSmsCancel, proto.SmsCancel{MessageID: uuid.NewString()}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := a.hub.Request(ctx, id, env, 4*time.Second)
	if err != nil || resp.Type != proto.TypeCommandAck || resp.Ref == nil || *resp.Ref != env.ID {
		t.Fatalf("request lintas instance: %v %+v", err, resp)
	}
	if len(s.Cancels) != 1 {
		t.Fatalf("device di B harus menerima sms.cancel: %d", len(s.Cancels))
	}
	// revoke lewat API di A menutup soket di B dengan 4401
	if st := admin.do("DELETE", "/api/v1/devices/"+id.String(), nil, nil); st != 204 {
		t.Fatal(st)
	}
	select {
	case ce := <-s.Closed:
		if ce.Code != 4401 {
			t.Fatalf("kode close %d", ce.Code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("soket di instance lain tidak ditutup")
	}
}

// sesi baru di instance lain menggantikan yang lama (4409) dan device tidak ditandai offline oleh sesi lama.
func TestMultiInstanceSupersedeKeepsDeviceOnline(t *testing.T) {
	a := newEnv(t)
	b := a.peer()
	admin := a.loginAs("admin")
	id, secret := a.enroll(admin)
	s1, _ := a.connect(id, secret, nil)
	s2, _ := b.connect(id, secret, nil)
	_ = s2
	select {
	case ce := <-s1.Closed:
		if ce.Code != 4409 {
			t.Fatalf("mau 4409: %d", ce.Code)
		}
	case <-time.After(5 * time.Second):
		var n int
		var inst string
		_ = a.pool.QueryRow(context.Background(), `SELECT count(*) FROM hub_commands`).Scan(&n)
		_ = a.pool.QueryRow(context.Background(), `SELECT instance_id::text FROM device_sessions`).Scan(&inst)
		t.Fatalf("sesi lama di instance lain tidak ditutup (hub_commands=%d, sesi di %s; A=%s B=%s)", n, inst, a.hub.InstanceID(), b.hub.InstanceID())
	}
	time.Sleep(500 * time.Millisecond) // beri waktu defer sesi lama berjalan
	var st string
	_ = a.pool.QueryRow(context.Background(), `SELECT status::text FROM devices WHERE id=$1`, id).Scan(&st)
	if st != "online" {
		t.Fatalf("device harus tetap online (sesi baru di B), dapat %s", st)
	}
	var inst uuid.UUID
	_ = a.pool.QueryRow(context.Background(), `SELECT instance_id FROM device_sessions WHERE device_id=$1`, id).Scan(&inst)
	if inst != b.hub.InstanceID() {
		t.Fatal("device_sessions harus menunjuk B")
	}
	// putus normal di B: baris dihapus dan device offline
	s2.cancel()
	waitFor(t, 5*time.Second, "sesi dihapus", func() bool {
		var n int
		_ = a.pool.QueryRow(context.Background(), `SELECT count(*) FROM device_sessions WHERE device_id=$1`, id).Scan(&n)
		return n == 0
	})
	waitFor(t, 5*time.Second, "device offline", func() bool {
		_ = a.pool.QueryRow(context.Background(), `SELECT status::text FROM devices WHERE id=$1`, id).Scan(&st)
		return st == "offline"
	})
}

// event bus dari instance lain sampai ke pelanggan SSE lokal (tanpa gema ke asalnya); sesi basi dianggap hilang.
func TestMultiInstanceBusRelayAndStaleSession(t *testing.T) {
	a := newEnv(t)
	b := a.peer()
	chA, _, _, stopA := a.svc.Bus.Subscribe(0, false)
	defer stopA()
	chB, _, _, stopB := b.svc.Bus.Subscribe(0, false)
	defer stopB()
	time.Sleep(300 * time.Millisecond) // LISTEN aktif
	b.svc.Bus.Publish("tes.event", map[string]any{"x": 1})
	select {
	case ev := <-chA:
		if ev.Name != "tes.event" {
			t.Fatalf("event: %s", ev.Name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event dari B tidak sampai ke A")
	}
	got := 0
	for {
		select {
		case <-chB:
			got++
			continue
		case <-time.After(500 * time.Millisecond):
		}
		break
	}
	if got != 1 {
		t.Fatalf("B harus menerima event lokalnya tepat sekali (tanpa gema): %d", got)
	}
	// sesi basi (heartbeat instance mati) tidak dihitung terhubung
	admin := a.loginAs("admin")
	id, secret := a.enroll(admin)
	b.connect(id, secret, nil)
	waitFor(t, 5*time.Second, "A melihat sesi", func() bool { return remoteSeen(a, id) })
	a.sql(`UPDATE device_sessions SET heartbeat_at = now() - interval '5 minutes' WHERE device_id=$1`, id)
	waitFor(t, 5*time.Second, "sesi basi diabaikan", func() bool { return !remoteSeen(a, id) })
	if err := a.hub.Send(id, proto.Envelope{}); err != core.ErrOffline {
		t.Fatalf("kirim ke sesi basi harus ErrOffline: %v", err)
	}
}
