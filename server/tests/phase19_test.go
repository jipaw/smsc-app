package tests

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jipaw/smsc-app/server/internal/bus"
	"github.com/jipaw/smsc-app/server/internal/core"
)

type leadProbe struct {
	mu   sync.Mutex
	ctxs []context.Context
}

func (p *leadProbe) lead(c context.Context) { p.mu.Lock(); p.ctxs = append(p.ctxs, c); p.mu.Unlock() }
func (p *leadProbe) n() int                 { p.mu.Lock(); defer p.mu.Unlock(); return len(p.ctxs) }
func (p *leadProbe) at(i int) context.Context {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ctxs[i]
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if f() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func gauge(t *testing.T, e *env, name string, l map[string]string) float64 {
	t.Helper()
	fams, _ := scrape(t, e)
	v, _ := metricValue(fams, name, l)
	return v
}

// lock hilang (backend di-terminate) → worker dihentikan, smsc_leader turun, lalu pemilihan ulang berhasil.
func TestLeaderLockLossIsDetectedAndReelected(t *testing.T) {
	e := newEnv(t)
	p := &leadProbe{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.svc.RunLeader(ctx, 50*time.Millisecond, p.lead); close(done) }()
	defer func() { cancel(); <-done }()

	eventually(t, "menjadi leader", func() bool { return p.n() == 1 })
	if gauge(t, e, "smsc_leader", nil) != 1 {
		t.Fatal("smsc_leader harus 1")
	}
	first := p.at(0)
	// putuskan koneksi pemegang lock (mensimulasikan jaringan/DB bermasalah)
	e.sql(`SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND classid=0 AND objid=727002`)
	eventually(t, "worker lama dihentikan", func() bool { return first.Err() != nil })
	eventually(t, "terpilih ulang dengan ctx baru", func() bool { return p.n() == 2 })
	if p.at(1).Err() != nil {
		t.Fatal("ctx baru harus aktif")
	}
	if v := gauge(t, e, "smsc_leader_changes_total", map[string]string{"event": "lost"}); v != 1 {
		t.Fatalf("lost: %v", v)
	}
	if v := gauge(t, e, "smsc_leader_changes_total", map[string]string{"event": "acquired"}); v != 2 {
		t.Fatalf("acquired: %v", v)
	}
	eventually(t, "smsc_leader kembali 1", func() bool { return gauge(t, e, "smsc_leader", nil) == 1 })
}

// standby tidak memimpin selama ada leader dan mengambil alih saat leader berhenti; lock dilepas bersih saat shutdown.
func TestStandbyTakesOverWhenLeaderStops(t *testing.T) {
	e := newEnv(t)
	other := core.New(e.pool, e.svc.Cfg, e.svc.Log, bus.New(time.Minute))
	pa, pb := &leadProbe{}, &leadProbe{}
	ca, cancelA := context.WithCancel(context.Background())
	cb, cancelB := context.WithCancel(context.Background())
	doneA, doneB := make(chan struct{}), make(chan struct{})
	go func() { e.svc.RunLeader(ca, 50*time.Millisecond, pa.lead); close(doneA) }()
	eventually(t, "A menjadi leader", func() bool { return pa.n() == 1 })
	go func() { other.RunLeader(cb, 50*time.Millisecond, pb.lead); close(doneB) }()
	defer func() { cancelB(); <-doneB }()
	time.Sleep(400 * time.Millisecond)
	if pb.n() != 0 {
		t.Fatal("standby tidak boleh memimpin saat leader ada")
	}
	cancelA()
	<-doneA
	if pa.at(0).Err() == nil {
		t.Fatal("worker A harus berhenti")
	}
	eventually(t, "B mengambil alih", func() bool { return pb.n() == 1 })
	if v := gauge(t, e, "smsc_leader", nil); v != 0 {
		t.Fatalf("A tidak lagi leader: %v", v)
	}
	if p, _ := other.M.Reg.Gather(); len(p) == 0 {
		t.Fatal("registry B kosong")
	}
}
