package tests

import (
	"context"
	"testing"
	"time"

	"github.com/jipaw/smsc-app/server/internal/core"
)

type SCIMReconcileState = core.SCIMReconcileState

// drift (peran diubah langsung di DB / pemetaan berubah) dikoreksi oleh worker berkala tanpa restart.
func TestSCIMPeriodicReconcileFixesDrift(t *testing.T) {
	s := newSCIMGroups(t)
	s.e.svc.Cfg.SCIMReconcileInterval = 150 * time.Millisecond
	u := s.user("drift@contoh.id", nil)
	s.group("Gateway Ops", map[string]any{"members": members(u)})
	if s.roleOfUser(u) != "operator" {
		t.Fatalf("awal: %s", s.roleOfUser(u))
	}
	start := func() func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { s.e.svc.RunSCIMReconcile(ctx); close(done) }()
		return func() { cancel(); <-done }
	}
	stop := start()
	ctx := context.Background()

	waitRole := func(want string) {
		t.Helper()
		for i := 0; i < 60; i++ {
			if s.roleOfUser(u) == want {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("peran %s != %s", s.roleOfUser(u), want)
	}
	// drift langsung di DB
	if _, err := s.e.pool.Exec(ctx, `UPDATE users SET role='viewer' WHERE id=$1`, u); err != nil {
		t.Fatal(err)
	}
	waitRole("operator")
	stop() // pemetaan berubah (mutasi map hanya saat worker berhenti: Config tidak dirancang berubah saat berjalan)
	s.e.svc.Cfg.SCIM.GroupRoles["gateway ops"] = "viewer"
	stop = start()
	defer func() { stop() }()
	waitRole("viewer")
	// tunggu satu tick berkala tambahan (bukan hanya run awal)
	var st SCIMReconcileState
	for i := 0; i < 60; i++ {
		if st = s.e.svc.LastSCIMReconcile(); st.Runs >= 4 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st.Runs < 4 || st.Error != "" || st.At.IsZero() {
		t.Fatalf("state: %+v", st)
	}
	var v struct {
		Interval int `json:"reconcile_interval_seconds"`
		Last     *struct {
			Runs int `json:"runs"`
		} `json:"last_reconcile"`
	}
	s.admin.c.do("GET", "/api/v1/settings/scim", nil, &v)
	if v.Last == nil || v.Last.Runs < 4 {
		t.Fatalf("settings: %+v", v)
	}
	var n int
	_ = s.e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action='scim.user_update' AND data->>'via'='reconcile' AND actor='system'`).Scan(&n)
	if n < 2 {
		t.Fatalf("audit reconcile: %d", n)
	}
}

// kegagalan (guard admin terakhir / error) tidak menghentikan worker; interval 0 = hanya sekali; ctx batal menghentikan.
func TestSCIMReconcileRunOnceAndStop(t *testing.T) {
	s := newSCIMGroups(t)
	s.e.svc.Cfg.SCIMReconcileInterval = 0
	u := s.user("sekali@contoh.id", nil)
	s.group("Gateway Ops", map[string]any{"members": members(u)})
	_, _ = s.e.pool.Exec(context.Background(), `UPDATE users SET role='viewer' WHERE id=$1`, u)
	done := make(chan struct{})
	go func() { s.e.svc.RunSCIMReconcile(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("interval 0 harus selesai setelah satu kali")
	}
	if s.roleOfUser(u) != "operator" {
		t.Fatalf("peran: %s", s.roleOfUser(u))
	}
	if st := s.e.svc.LastSCIMReconcile(); st.Runs != 1 || st.Changed != 1 {
		t.Fatalf("state: %+v", st)
	}
	// konteks sudah batal: tidak mencatat state baru dan kembali
	s.e.svc.Cfg.SCIMReconcileInterval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done = make(chan struct{})
	go func() { s.e.svc.RunSCIMReconcile(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("harus berhenti saat ctx batal")
	}
}
