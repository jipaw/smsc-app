package tests

import (
	"context"
	"strings"
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
		if st = s.e.svc.LastSCIMReconcile(ctx); st.Runs >= 4 {
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
	if st := s.e.svc.LastSCIMReconcile(context.Background()); st.Runs != 1 || st.Changed != 1 {
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

// status tersimpan di database: terbaca dari SQL langsung (jadi dari instance mana pun) dan runs bertambah atomik.
func TestSCIMReconcileStatusPersistedInDB(t *testing.T) {
	s := newSCIMGroups(t)
	s.e.svc.Cfg.SCIMReconcileInterval = 0
	ctx := context.Background()
	if st := s.e.svc.LastSCIMReconcile(ctx); st.Runs != 0 {
		t.Fatalf("awal: %+v", st)
	}
	var v struct {
		Last *struct{} `json:"last_reconcile"`
	}
	s.admin.c.do("GET", "/api/v1/settings/scim", nil, &v)
	if v.Last != nil {
		t.Fatal("belum ada run: last_reconcile harus kosong")
	}
	s.e.svc.RunSCIMReconcile(ctx)
	s.e.svc.RunSCIMReconcile(ctx)
	var runs int
	var errTxt string
	if err := s.e.pool.QueryRow(ctx, `SELECT (value->>'runs')::int, COALESCE(value->>'error','') FROM settings WHERE key='scim_reconcile_last'`).Scan(&runs, &errTxt); err != nil || runs != 2 || errTxt != "" {
		t.Fatalf("db: %d %q %v", runs, errTxt, err)
	}
	s.admin.c.do("GET", "/api/v1/settings/scim", nil, &v)
	if v.Last == nil {
		t.Fatal("settings harus memuat last_reconcile dari DB")
	}
}

// metrik: counter run/changed di leader, gauge dari DB (timestamp, ok); tanpa SCIM grup tidak ada run tercatat.
func TestSCIMReconcileMetrics(t *testing.T) {
	s := newSCIMGroups(t)
	s.e.svc.Cfg.SCIMReconcileInterval = 0
	ctx := context.Background()
	u := s.user("metrik@contoh.id", nil)
	s.group("Gateway Ops", map[string]any{"members": members(u)})
	_, _ = s.e.pool.Exec(ctx, `UPDATE users SET role='viewer' WHERE id=$1`, u)

	fams, _ := scrape(t, s.e)
	if v, ok := metricValue(fams, "smsc_scim_reconcile_runs_total", map[string]string{"result": "ok"}); !ok || v != 0 {
		t.Fatalf("seri awal harus ada (0): %v %v", v, ok)
	}
	if _, ok := metricValue(fams, "smsc_scim_reconcile_last_run_ok", nil); ok {
		t.Fatal("gauge tidak boleh ada sebelum run pertama")
	}
	s.e.svc.RunSCIMReconcile(ctx)
	time.Sleep(5200 * time.Millisecond) // cache DB metrik 5 dtk
	fams, body := scrape(t, s.e)
	if v, _ := metricValue(fams, "smsc_scim_reconcile_runs_total", map[string]string{"result": "ok"}); v != 1 {
		t.Fatalf("runs ok: %v", v)
	}
	if v, _ := metricValue(fams, "smsc_scim_reconcile_changed_total", nil); v != 1 {
		t.Fatalf("changed: %v", v)
	}
	if v, ok := metricValue(fams, "smsc_scim_reconcile_last_run_ok", nil); !ok || v != 1 {
		t.Fatalf("last_run_ok: %v %v", v, ok)
	}
	if v, _ := metricValue(fams, "smsc_scim_reconcile_last_run_timestamp_seconds", nil); time.Since(time.Unix(int64(v), 0)) > time.Minute {
		t.Fatalf("timestamp: %v", v)
	}
	if strings.Contains(body, "metrik@contoh.id") {
		t.Fatal("metrik tidak boleh memuat PII")
	}
	// run gagal tercatat: simulasikan dengan status error di DB → gauge ok=0
	_, _ = s.e.pool.Exec(ctx, `UPDATE settings SET value = value || '{"error":"x"}' WHERE key='scim_reconcile_last'`)
	time.Sleep(5200 * time.Millisecond)
	fams, _ = scrape(t, s.e)
	if v, _ := metricValue(fams, "smsc_scim_reconcile_last_run_ok", nil); v != 0 {
		t.Fatalf("ok harus 0 saat error: %v", v)
	}
	// tanpa pemetaan grup: tidak ada run baru tercatat
	s.e.svc.Cfg.SCIM.GroupRoles = map[string]string{}
	s.e.svc.RunSCIMReconcile(ctx)
	if st := s.e.svc.LastSCIMReconcile(ctx); st.Runs != 1 {
		t.Fatalf("tanpa pemetaan tidak boleh mencatat run: %+v", st)
	}
}
