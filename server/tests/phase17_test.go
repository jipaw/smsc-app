package tests

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Dashboard Grafana: JSON valid, id panel unik, setiap metrik yang dirujuk ada di /metrics,
// dan filter route /scim/v2 benar-benar cocok dengan label route yang dihasilkan server.
func TestGrafanaSCIMDashboard(t *testing.T) {
	raw, err := os.ReadFile("../../ops/grafana/smsc-scim.json")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		UID         string `json:"uid"`
		Annotations struct {
			List []struct {
				Name string `json:"name"`
				Expr string `json:"expr"`
			} `json:"list"`
		} `json:"annotations"`
		Panels []struct {
			ID      int    `json:"id"`
			Type    string `json:"type"`
			Title   string `json:"title"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.UID == "" || len(d.Panels) < 10 {
		t.Fatalf("dashboard tidak lengkap: uid=%q panel=%d", d.UID, len(d.Panels))
	}
	s := newSCIMGroups(t)
	u := s.user("grafana@contoh.id", nil)
	s.group("Gateway Ops", map[string]any{"members": members(u)})
	s.do("GET", "/Users", nil)
	s.do("GET", "/Groups", nil)
	s.do("GET", "/Users/00000000-0000-0000-0000-000000000000", nil)
	s.e.sql(`INSERT INTO settings(key,value) VALUES ('scim_reconcile_last', '{"at":"2026-01-01T00:00:00Z","changed":0,"runs":1}') ON CONFLICT (key) DO NOTHING`)
	time.Sleep(5200 * time.Millisecond) // cache DB metrik
	fams, _ := scrape(t, s.e)

	ids := map[int]bool{}
	names := map[string]bool{}
	re := regexp.MustCompile(`smsc_[a-z0-9_]+`)
	_ = re
	routeRe := regexp.MustCompile(`route=~"([^"]+)"`)
	var routeFilter string
	if len(d.Annotations.List) < 3 {
		t.Fatalf("anotasi restart/deploy/leader wajib ada: %d", len(d.Annotations.List))
	}
	for _, a := range d.Annotations.List {
		for _, n := range regexp.MustCompile(`(?:smsc|process)_[a-z0-9_]+`).FindAllString(a.Expr, -1) {
			names[n] = true
		}
	}
	for _, p := range d.Panels {
		if ids[p.ID] {
			t.Errorf("id panel ganda: %d", p.ID)
		}
		ids[p.ID] = true
		if p.Type != "row" && len(p.Targets) == 0 {
			t.Errorf("panel %q tanpa query", p.Title)
		}
		for _, tg := range p.Targets {
			if strings.Count(tg.Expr, "(") != strings.Count(tg.Expr, ")") || strings.Count(tg.Expr, "{") != strings.Count(tg.Expr, "}") {
				t.Errorf("kurung tidak seimbang: %s", tg.Expr)
			}
			for _, n := range re.FindAllString(tg.Expr, -1) {
				names[n] = true
			}
			if m := routeRe.FindStringSubmatch(tg.Expr); m != nil {
				routeFilter = m[1]
			}
		}
	}
	if !names["smsc_scim_reconcile_last_run_ok"] || !names["smsc_scim_reconcile_runs_total"] || !names["smsc_scim_reconcile_changed_total"] {
		t.Fatalf("dashboard harus memakai metrik rekonsiliasi: %v", names)
	}
	for n := range names {
		base := regexp.MustCompile(`_(bucket|sum|count)$`).ReplaceAllString(n, "")
		if fams[n] == nil && fams[base] == nil {
			t.Errorf("dashboard merujuk metrik yang tidak ada: %s", n)
		}
	}
	// PromQL `=~` berjangkar penuh: samakan perilakunya
	rr := regexp.MustCompile("^(?:" + routeFilter + ")$")
	matched := 0
	for _, m := range fams["smsc_http_requests_total"].GetMetric() {
		for _, l := range m.GetLabel() {
			if l.GetName() == "route" && rr.MatchString(l.GetValue()) {
				matched++
			}
		}
	}
	if routeFilter == "" || matched == 0 {
		t.Fatalf("filter route %q tidak cocok dengan seri nyata", routeFilter)
	}
}

// metrik untuk anotasi: build_info (versi/revisi), leader, dan process_start_time_seconds (restart).
func TestBuildInfoLeaderAndStartTimeMetrics(t *testing.T) {
	e := newEnv(t)
	fams, body := scrape(t, e)
	bi := fams["smsc_build_info"]
	if bi == nil || len(bi.GetMetric()) != 1 || bi.GetMetric()[0].GetGauge().GetValue() != 1 {
		t.Fatalf("smsc_build_info harus 1 seri bernilai 1: %v", bi)
	}
	lbl := map[string]string{}
	for _, l := range bi.GetMetric()[0].GetLabel() {
		lbl[l.GetName()] = l.GetValue()
	}
	if lbl["version"] == "" || lbl["go_version"] == "" {
		t.Fatalf("label build_info: %v", lbl)
	}
	if v, ok := metricValue(fams, "smsc_leader", nil); !ok || v != 0 {
		t.Fatalf("smsc_leader awal harus 0: %v %v", v, ok)
	}
	e.svc.M.Leader.Set(1)
	fams, _ = scrape(t, e)
	if v, _ := metricValue(fams, "smsc_leader", nil); v != 1 {
		t.Fatalf("smsc_leader: %v", v)
	}
	if v, ok := metricValue(fams, "process_start_time_seconds", nil); !ok || time.Since(time.Unix(int64(v), 0)) > time.Hour {
		t.Fatalf("process_start_time_seconds: %v %v", v, ok)
	}
	if strings.Contains(body, "password") {
		t.Fatal("metrik tidak boleh memuat rahasia")
	}
}
