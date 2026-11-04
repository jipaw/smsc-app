package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
)

// Semua dashboard di ops/grafana: JSON valid, uid & id panel unik, kurung seimbang, setiap metrik ada di /metrics, dan nilai
// label yang dipilih (result="lost", to="failed", dst.) benar-benar dihasilkan server — salah ketik label membuat panel kosong diam-diam.
func TestGrafanaDashboardsMetricsAndLabelValues(t *testing.T) {
	files, _ := filepath.Glob("../../ops/grafana/*.json")
	if len(files) < 2 {
		t.Fatalf("dashboard tidak ditemukan: %v", files)
	}
	e := newEnv(t)
	admin := e.loginAs("admin")
	_ = admin
	e.sql(`INSERT INTO settings(key,value) VALUES ('scim_reconcile_last', '{"at":"2026-01-01T00:00:00Z","changed":0,"runs":1}') ON CONFLICT (key) DO NOTHING`) // gauge SCIM baru ada setelah run pertama
	time.Sleep(5200 * time.Millisecond)                                                                                                                        // cache gauge berbasis DB
	fams, _ := scrape(t, e)

	metricRe := regexp.MustCompile(`\b(?:smsc|process)_[a-z0-9_]+`)
	selRe := regexp.MustCompile(`\b(smsc_[a-z0-9_]+)\{([^}]*)\}`)
	matcherRe := regexp.MustCompile(`([a-z_]+)\s*(=~|!~|!=|=)\s*"([^"]*)"`)
	dynamic := map[string]bool{"smsc_http_requests_total": true, "smsc_http_request_duration_seconds_bucket": true} // label rute/status muncul saat ada lalu lintas

	hasLabel := func(f *dto.MetricFamily, name string, re *regexp.Regexp) bool {
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == name && re.MatchString(l.GetValue()) {
					return true
				}
			}
		}
		return false
	}
	uids := map[string]string{}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var d struct {
			UID         string `json:"uid"`
			Annotations struct {
				List []struct{ Expr string } `json:"list"`
			} `json:"annotations"`
			Panels []struct {
				ID      int                     `json:"id"`
				Type    string                  `json:"type"`
				Title   string                  `json:"title"`
				Targets []struct{ Expr string } `json:"targets"`
			} `json:"panels"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if d.UID == "" || uids[d.UID] != "" {
			t.Errorf("%s: uid kosong/ganda (%q, juga di %s)", file, d.UID, uids[d.UID])
		}
		uids[d.UID] = file
		var exprs []string
		ids := map[int]bool{}
		for _, p := range d.Panels {
			if ids[p.ID] {
				t.Errorf("%s: id panel ganda %d", file, p.ID)
			}
			ids[p.ID] = true
			if p.Type != "row" && len(p.Targets) == 0 {
				t.Errorf("%s: panel %q tanpa query", file, p.Title)
			}
			for _, tg := range p.Targets {
				exprs = append(exprs, tg.Expr)
			}
		}
		for _, a := range d.Annotations.List {
			exprs = append(exprs, a.Expr)
		}
		for _, ex := range exprs {
			for _, pair := range [][2]string{{"(", ")"}, {"{", "}"}, {"[", "]"}} {
				if strings.Count(ex, pair[0]) != strings.Count(ex, pair[1]) {
					t.Errorf("%s: kurung %s tidak seimbang: %s", file, pair[0], ex)
				}
			}
			for _, n := range metricRe.FindAllString(ex, -1) {
				base := regexp.MustCompile(`_(bucket|sum|count)$`).ReplaceAllString(n, "")
				if fams[n] == nil && fams[base] == nil {
					t.Errorf("%s: metrik tidak ada di /metrics: %s", file, n)
				}
			}
			for _, sm := range selRe.FindAllStringSubmatch(ex, -1) {
				if dynamic[sm[1]] {
					continue
				}
				f := fams[sm[1]]
				if f == nil {
					continue // sudah dilaporkan di atas
				}
				for _, mm := range matcherRe.FindAllStringSubmatch(sm[2], -1) {
					if mm[2] == "!~" || mm[2] == "!=" {
						continue
					}
					pat := mm[3]
					if mm[2] == "=" {
						pat = regexp.QuoteMeta(mm[3])
					}
					if !hasLabel(f, mm[1], regexp.MustCompile("^(?:"+pat+")$")) {
						t.Errorf("%s: %s{%s%s%q} tidak cocok dengan seri apa pun yang dihasilkan server", file, sm[1], mm[1], mm[2], mm[3])
					}
				}
			}
		}
	}
	if uids["smsc-delivery"] == "" {
		t.Error("dashboard smsc-delivery tidak ada")
	}
}
