package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jipaw/smsc-app/server/internal/config"
	"github.com/jipaw/smsc-app/server/internal/proto"
)

type camp struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	PauseReason *string `json:"pause_reason"`
	Priority    int     `json:"priority"`
	StartedAt   *string `json:"started_at"`
	FinishedAt  *string `json:"finished_at"`
	Counters    struct {
		Total, Queued, Sent, Delivered, Failed, Cancelled, Expired, Unknown int
		InFlight                                                            int `json:"in_flight"`
	} `json:"counters"`
	Recipients struct {
		Count   int      `json:"count"`
		Columns []string `json:"columns"`
	} `json:"recipients"`
}

type importRes struct {
	Accepted, Duplicates, Suppressed int
	Invalid                          []struct {
		Row    int
		Value  string
		Reason string
	}
}

func (c *client) newCampaign(fields map[string]any) camp {
	c.e.t.Helper()
	var out camp
	if st := c.do("POST", "/api/v1/campaigns", fields, &out); st != 201 {
		c.e.t.Fatalf("POST /campaigns: %d", st)
	}
	return out
}

func (c *client) campaign(id string) camp {
	c.e.t.Helper()
	var out camp
	if st := c.do("GET", "/api/v1/campaigns/"+id, nil, &out); st != 200 {
		c.e.t.Fatalf("GET /campaigns/%s: %d", id, st)
	}
	return out
}

func (c *client) importJSON(id string, recs []map[string]any) importRes {
	c.e.t.Helper()
	var out importRes
	if st := c.do("POST", "/api/v1/campaigns/"+id+"/recipients", map[string]any{"recipients": recs}, &out); st != 200 {
		c.e.t.Fatalf("import: %d", st)
	}
	return out
}

func (c *client) action(id, act string, want int) camp {
	c.e.t.Helper()
	var out camp
	if st := c.do("POST", "/api/v1/campaigns/"+id+"/"+act, map[string]any{}, &out); st != want {
		c.e.t.Fatalf("%s: status %d, mau %d", act, st, want)
	}
	return out
}

func (c *client) waitCampaign(id, status string, d time.Duration) camp {
	c.e.t.Helper()
	var cm camp
	waitFor(c.e.t, d, fmt.Sprintf("campaign -> %s", status), func() bool { cm = c.campaign(id); return cm.Status == status })
	return cm
}

// invarian: total = jumlah semua status (tidak ada penerima yang hilang atau terhitung ganda)
func checkInvariant(t *testing.T, cm camp) {
	t.Helper()
	k := cm.Counters
	sum := k.Queued + k.InFlight + k.Sent + k.Delivered + k.Failed + k.Cancelled + k.Expired + k.Unknown
	if sum != k.Total || k.Total != cm.Recipients.Count {
		t.Fatalf("invarian counter rusak: total=%d recipients=%d jumlah=%d %+v", k.Total, cm.Recipients.Count, sum, k)
	}
}

func recs(n int, name string) []map[string]any {
	out := []map[string]any{}
	for i := 0; i < n; i++ {
		out = append(out, map[string]any{"to": fmt.Sprintf("0812%07d", 1000000+i), "vars": map[string]string{"name": fmt.Sprintf("%s%d", name, i+1)}})
	}
	return out
}

func (e *env) fastPolicy(minMs, maxMs int) {
	e.sql(`UPDATE dispatch_policies SET min_delay_ms=$1, max_delay_ms=$2 WHERE is_default`, minMs, maxMs)
}

func (c *client) messagesOf(campaignID string) []struct {
	ID, To, Body, Status, Source string
	Priority                     int
	ErrorCode                    *string `json:"error_code"`
	SimID                        *string `json:"sim_id"`
} {
	c.e.t.Helper()
	type m = struct {
		ID, To, Body, Status, Source string
		Priority                     int
		ErrorCode                    *string `json:"error_code"`
		SimID                        *string `json:"sim_id"`
	}
	var out []m
	cursor := ""
	for {
		var page struct {
			Items      []m
			NextCursor *string `json:"next_cursor"`
		}
		path := "/api/v1/messages?limit=200&campaign_id=" + campaignID
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		if st := c.do("GET", path, nil, &page); st != 200 {
			c.e.t.Fatalf("list messages: %d", st)
		}
		out = append(out, page.Items...)
		if page.NextCursor == nil {
			return out
		}
		cursor = *page.NextCursor
	}
}

func TestCampaignHappyPathMultiSimToCompletion(t *testing.T) { // FR-08, FR-09, T-INT-13
	e := newEnv(t)
	e.fastPolicy(150, 150)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin, 0, 1)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	e.sql(`INSERT INTO suppressions(id,number_e164,reason) VALUES (gen_random_uuid(),'+628120009999','test')`)

	cm := admin.newCampaign(map[string]any{"name": "Tagihan November", "body": "Halo {{name}}, tagihan Anda jatuh tempo."})
	if cm.Status != "draft" || cm.Priority != 3 {
		t.Fatalf("draft default: %+v", cm)
	}
	in := append(recs(8, "Budi"),
		map[string]any{"to": "bukan-nomor", "vars": map[string]string{"name": "X"}},
		map[string]any{"to": "08120001000", "vars": map[string]string{"name": "Dup"}},   // duplikat dari recs[0]? (beda nomor) -> tidak dup
		map[string]any{"to": "0812 1000000", "vars": map[string]string{"name": "Dup2"}}, // normalisasi sama dgn recs[0] => duplikat
		map[string]any{"to": "08120009999", "vars": map[string]string{"name": "Supp"}},  // suppressed
		map[string]any{"to": "08129990000", "vars": map[string]string{}},                // variabel hilang
	)
	res := admin.importJSON(cm.ID, in)
	if res.Accepted != 9 || res.Duplicates != 1 || res.Suppressed != 1 || len(res.Invalid) != 2 {
		t.Fatalf("hasil impor: %+v", res)
	}
	reasons := map[string]bool{}
	for _, iv := range res.Invalid {
		reasons[iv.Reason] = true
	}
	if !reasons["invalid_number"] || !reasons["missing_variable:name"] {
		t.Fatalf("alasan invalid: %+v", res.Invalid)
	}
	got := admin.campaign(cm.ID)
	if got.Recipients.Count != 9 || len(got.Recipients.Columns) != 1 || got.Recipients.Columns[0] != "name" {
		t.Fatalf("ringkasan penerima: %+v", got.Recipients)
	}
	var pv struct {
		Recipients int
		Samples    []struct {
			To, Body string
			Parts    int
		}
		OnlineSims int  `json:"online_sims"`
		Est        *int `json:"estimated_duration_seconds"`
	}
	admin.do("POST", "/api/v1/campaigns/"+cm.ID+"/preview", map[string]any{"sample": 2}, &pv)
	if len(pv.Samples) != 2 || !strings.HasPrefix(pv.Samples[0].Body, "Halo Budi1,") || pv.OnlineSims != 2 || pv.Est == nil {
		t.Fatalf("preview: %+v", pv)
	}

	started := admin.action(cm.ID, "start", 200)
	if started.Status != "running" || started.StartedAt == nil {
		t.Fatalf("start: %+v", started)
	}
	done := admin.waitCampaign(cm.ID, "completed", 40*time.Second)
	checkInvariant(t, done)
	if done.Counters.Delivered != 9 || done.Counters.Total != 9 || done.FinishedAt == nil {
		t.Fatalf("hasil akhir: %+v", done.Counters)
	}
	ms := admin.messagesOf(cm.ID)
	slots := map[int]int{}
	s.mu.Lock()
	for _, r := range s.Sends {
		slots[r.Cmd.Sim.SlotIndex]++
	}
	s.mu.Unlock()
	if len(ms) != 9 || len(slots) != 2 {
		t.Fatalf("pesan=%d slot dipakai=%v", len(ms), slots)
	}
	for _, m := range ms {
		if m.Source != "campaign" || m.Priority != 3 || m.Status != "delivered" || !strings.HasPrefix(m.Body, "Halo ") || strings.Contains(m.Body, "{{") {
			t.Fatalf("pesan campaign: %+v", m)
		}
	}
	// FR-09 KP: tidak bisa diubah/impor setelah selesai
	var pr struct{ Type string }
	if st := admin.do("PATCH", "/api/v1/campaigns/"+cm.ID, map[string]any{"name": "x"}, &pr); st != 409 || pr.Type != "campaign.not_editable" {
		t.Fatalf("patch completed: %d %s", st, pr.Type)
	}
}

func TestCampaignCSVImport(t *testing.T) { // FR-08 KP1 (BOM, ;, nomor baris, sel kosong, client_ref)
	e := newEnv(t)
	admin := e.loginAs("admin")
	cm := admin.newCampaign(map[string]any{"name": "CSV", "body": "Halo {{name}}, {{amount}}"})
	csv := "\xef\xbb\xbfphone;name;amount;client_ref\r\n08123456789;Budi;150000;inv-1\r\n08121112222;Sari;;inv-2\r\nbukan;Eko;90000;inv-3\r\n\r\n08123334444;Dewi;70000;inv-4\r\n"
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "penerima.csv")
	_, _ = fw.Write([]byte(csv))
	_ = mw.Close()
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/campaigns/"+cm.ID+"/recipients", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", admin.csrf)
	resp, err := admin.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var res importRes
	_ = json.Unmarshal(raw, &res)
	if resp.StatusCode != 200 || res.Accepted != 2 || len(res.Invalid) != 2 {
		t.Fatalf("impor CSV: %d %s", resp.StatusCode, raw)
	}
	rows := map[string]int{}
	for _, iv := range res.Invalid {
		rows[iv.Reason] = iv.Row
	}
	if rows["missing_variable:amount"] != 3 || rows["invalid_number"] != 4 { // header = baris 1
		t.Fatalf("nomor baris laporan: %+v", res.Invalid)
	}
	// header tanpa kolom phone -> 422
	buf.Reset()
	mw = multipart.NewWriter(&buf)
	fw, _ = mw.CreateFormFile("file", "x.csv")
	_, _ = fw.Write([]byte("nama,umur\nA,1\n"))
	_ = mw.Close()
	req, _ = http.NewRequest("POST", e.srv.URL+"/api/v1/campaigns/"+cm.ID+"/recipients", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", admin.csrf)
	resp, _ = admin.http.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 422 {
		t.Fatalf("CSV tanpa kolom phone harus 422, dapat %d", resp.StatusCode)
	}
	// hapus semua penerima untuk impor ulang
	var rm struct{ Removed int }
	if st := admin.do("DELETE", "/api/v1/campaigns/"+cm.ID+"/recipients", nil, &rm); st != 200 || rm.Removed != 2 {
		t.Fatalf("clear: %d %+v", st, rm)
	}
}

func TestCampaignPauseResumeCancel(t *testing.T) { // FR-09 KP2/KP3
	e := newEnv(t)
	e.fastPolicy(700, 700)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	cm := admin.newCampaign(map[string]any{"name": "PRC", "body": "Halo {{name}}"})
	admin.importJSON(cm.ID, recs(10, "A"))
	admin.action(cm.ID, "start", 200)
	waitFor(t, 10*time.Second, "pesan pertama terkirim", func() bool { return s.SendCount() >= 1 })

	p := admin.action(cm.ID, "pause", 200)
	if p.Status != "paused" || p.PauseReason == nil || *p.PauseReason != "user" {
		t.Fatalf("pause: %+v", p)
	}
	time.Sleep(900 * time.Millisecond) // pesan yang sudah di-lease boleh selesai
	frozen := s.SendCount()
	time.Sleep(2500 * time.Millisecond)
	if s.SendCount() != frozen {
		t.Fatalf("saat paused tidak boleh ada lease baru: %d -> %d", frozen, s.SendCount())
	}
	var pr struct{ Type string }
	if st := admin.do("POST", "/api/v1/campaigns/"+cm.ID+"/pause", map[string]any{}, &pr); st != 409 || pr.Type != "campaign.invalid_transition" {
		t.Fatalf("pause ganda: %d %s", st, pr.Type)
	}
	r := admin.action(cm.ID, "resume", 200)
	if r.Status != "running" || r.PauseReason != nil {
		t.Fatalf("resume: %+v", r)
	}
	waitFor(t, 10*time.Second, "kirim berlanjut", func() bool { return s.SendCount() > frozen })

	c := admin.action(cm.ID, "cancel", 200)
	if c.Status != "cancelled" || c.FinishedAt == nil {
		t.Fatalf("cancel: %+v", c)
	}
	time.Sleep(1500 * time.Millisecond)
	final := admin.campaign(cm.ID)
	checkInvariant(t, final)
	if final.Counters.Cancelled == 0 || final.Counters.Cancelled+final.Counters.Delivered+final.Counters.InFlight+final.Counters.Sent != 10 {
		t.Fatalf("sisa harus cancelled: %+v", final.Counters)
	}
	before := s.SendCount()
	time.Sleep(1500 * time.Millisecond)
	if s.SendCount() != before {
		t.Fatal("setelah cancel tidak boleh ada pengiriman baru")
	}
}

func TestCampaignAutoPauseOnInfraFailuresOnly(t *testing.T) { // FR-10
	e := newEnv(t)
	e.fastPolicy(250, 250)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = func(s *Sim, env proto.Envelope, cmd proto.SmsSend) {
		s.Ack(env, true, "")
		s.Event(cmd, "sending", "")
		s.Event(cmd, "failed", "no_service") // kegagalan infrastruktur terus-menerus
	}
	cm := admin.newCampaign(map[string]any{"name": "AP", "body": "Halo {{name}}", "auto_pause": map[string]any{"enabled": true, "window": 10, "max_failure_ratio": 0.5}})
	admin.importJSON(cm.ID, recs(30, "A"))
	admin.action(cm.ID, "start", 200)
	paused := admin.waitCampaign(cm.ID, "paused", 40*time.Second)
	if paused.PauseReason == nil || *paused.PauseReason != "auto_failure_ratio" {
		t.Fatalf("alasan pause: %+v", paused.PauseReason)
	}
	checkInvariant(t, paused)
	// resume memulai baseline baru: tidak langsung dijeda ulang oleh kegagalan lama
	admin.action(cm.ID, "resume", 200)
	time.Sleep(400 * time.Millisecond)
	if st := admin.campaign(cm.ID).Status; st != "running" {
		t.Fatalf("setelah resume harus running (baseline baru), dapat %s", st)
	}
	admin.action(cm.ID, "cancel", 200)
}

func TestCampaignNonInfraFailuresDoNotAutoPause(t *testing.T) { // invalid_destination tidak mencerminkan kesehatan SIM
	e := newEnv(t)
	e.fastPolicy(60, 60)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = func(s *Sim, env proto.Envelope, cmd proto.SmsSend) {
		s.Ack(env, true, "")
		s.Event(cmd, "sending", "")
		s.Event(cmd, "failed", "invalid_destination")
	}
	cm := admin.newCampaign(map[string]any{"name": "NI", "body": "Halo {{name}}", "auto_pause": map[string]any{"window": 10, "max_failure_ratio": 0.2}})
	admin.importJSON(cm.ID, recs(20, "A"))
	admin.action(cm.ID, "start", 200)
	done := admin.waitCampaign(cm.ID, "completed", 40*time.Second)
	checkInvariant(t, done)
	if done.Counters.Failed != 20 || done.PauseReason != nil {
		t.Fatalf("semua gagal tanpa pause: %+v", done.Counters)
	}
}

func TestCampaignNoCapacityPausesThenRecovers(t *testing.T) { // FR-10 KP, no_capacity
	e := newEnvOpts(t, func(c *config.Config) { c.NoCapacityAfter = 1200 * time.Millisecond })
	e.fastPolicy(100, 100)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin) // terdaftar tetapi tidak tersambung -> SIM ada, tidak online
	cm := admin.newCampaign(map[string]any{"name": "NC", "body": "Halo {{name}}"})
	admin.importJSON(cm.ID, recs(3, "A"))
	admin.action(cm.ID, "start", 200)
	p := admin.waitCampaign(cm.ID, "paused", 15*time.Second)
	if p.PauseReason == nil || *p.PauseReason != "no_capacity" {
		t.Fatalf("alasan: %+v", p.PauseReason)
	}
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	admin.action(cm.ID, "resume", 200)
	done := admin.waitCampaign(cm.ID, "completed", 25*time.Second)
	if done.Counters.Delivered != 3 {
		t.Fatalf("setelah pulih: %+v", done.Counters)
	}
}

func TestCampaignScheduledStartsAutomatically(t *testing.T) { // FR-09 jadwal
	e := newEnv(t)
	e.fastPolicy(100, 100)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	at := time.Now().Add(2500 * time.Millisecond).UTC().Format(time.RFC3339Nano)
	cm := admin.newCampaign(map[string]any{"name": "Jadwal", "body": "Halo {{name}}", "schedule": map[string]any{"start_at": at}})
	admin.importJSON(cm.ID, recs(2, "A"))
	sc := admin.action(cm.ID, "start", 200)
	if sc.Status != "scheduled" || sc.StartedAt != nil {
		t.Fatalf("harus scheduled: %+v", sc)
	}
	time.Sleep(1200 * time.Millisecond)
	if s.SendCount() != 0 {
		t.Fatal("sebelum waktunya tidak boleh ada pengiriman")
	}
	done := admin.waitCampaign(cm.ID, "completed", 25*time.Second)
	if done.Counters.Delivered != 2 || done.StartedAt == nil {
		t.Fatalf("terjadwal selesai: %+v", done)
	}
}

func TestCampaignBackpressureCapsQueuedMessages(t *testing.T) { // materializer tidak membengkakkan antrean
	e := newEnvOpts(t, func(c *config.Config) { c.CampaignQueueCap = 4 })
	e.fastPolicy(400, 400)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	cm := admin.newCampaign(map[string]any{"name": "BP", "body": "Halo {{name}}"})
	admin.importJSON(cm.ID, recs(12, "A"))
	admin.action(cm.ID, "start", 200)
	maxQueued := 0
	waitFor(t, 40*time.Second, "selesai", func() bool {
		var q int
		_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM messages WHERE campaign_id=$1 AND status='queued'`, cm.ID).Scan(&q)
		if q > maxQueued {
			maxQueued = q
		}
		return admin.campaign(cm.ID).Status == "completed"
	})
	if maxQueued > 4 || maxQueued == 0 {
		t.Fatalf("antrean campaign harus <= 4, teramati maks %d", maxQueued)
	}
}

func TestCampaignValidationAndEditRules(t *testing.T) { // FR-08/09 aturan, error terstruktur
	e := newEnv(t)
	e.fastPolicy(5000, 5000)
	admin := e.loginAs("admin")
	var pr struct {
		Type   string
		Errors []struct{ Field string }
	}
	if st := admin.do("POST", "/api/v1/campaigns", map[string]any{"body": "x"}, &pr); st != 422 || pr.Type != "validation.failed" {
		t.Fatalf("tanpa nama: %d %s", st, pr.Type)
	}
	if st := admin.do("POST", "/api/v1/campaigns", map[string]any{"name": "x"}, nil); st != 422 {
		t.Fatalf("tanpa isi: %d", st)
	}
	if st := admin.do("POST", "/api/v1/campaigns", map[string]any{"name": "x", "body": "a", "template_id": uuid.NewString()}, nil); st != 422 {
		t.Fatalf("body+template: %d", st)
	}
	if st := admin.do("POST", "/api/v1/campaigns", map[string]any{"name": "x", "body": "a", "priority": 12}, nil); st != 422 {
		t.Fatalf("prioritas 12: %d", st)
	}
	// idempotensi pembuatan
	h := map[string]string{"Idempotency-Key": "camp-1"}
	var a, b camp
	admin.doH("POST", "/api/v1/campaigns", map[string]any{"name": "Idem", "body": "Halo"}, &a, h)
	admin.doH("POST", "/api/v1/campaigns", map[string]any{"name": "Idem", "body": "Halo"}, &b, h)
	if a.ID == "" || a.ID != b.ID {
		t.Fatalf("Idempotency-Key harus mengembalikan campaign yang sama: %s vs %s", a.ID, b.ID)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM campaigns`).Scan(&n)
	if n != 1 {
		t.Fatalf("mau 1 campaign, dapat %d", n)
	}

	// draft dapat diubah penuh; ganti body -> template
	var tpl struct{ ID string }
	admin.do("POST", "/api/v1/templates", map[string]any{"name": "t1", "body": "Halo {{name}} {{kode}}"}, &tpl)
	var up camp
	if st := admin.do("PATCH", "/api/v1/campaigns/"+a.ID, map[string]any{"name": "Baru", "template_id": tpl.ID}, &up); st != 200 || up.Name != "Baru" {
		t.Fatalf("patch draft: %d %+v", st, up)
	}
	// start tanpa penerima, lalu dengan variabel hilang -> 422 dengan penjelasan
	if st := admin.do("POST", "/api/v1/campaigns/"+a.ID+"/start", map[string]any{}, &pr); st != 422 {
		t.Fatalf("start tanpa penerima: %d", st)
	}
	admin.importJSON(a.ID, []map[string]any{{"to": "08123456789", "vars": map[string]string{"name": "A", "kode": "1"}}})
	// template diganti (variabel baru) SETELAH impor: start harus menolak
	var tpl2 struct{ ID string }
	admin.do("POST", "/api/v1/templates", map[string]any{"name": "t2", "body": "{{name}} {{lokasi}}"}, &tpl2)
	admin.do("PATCH", "/api/v1/campaigns/"+a.ID, map[string]any{"template_id": tpl2.ID}, nil)
	var raw map[string]any
	if st := admin.do("POST", "/api/v1/campaigns/"+a.ID+"/start", map[string]any{}, &raw); st != 422 || !strings.Contains(fmt.Sprint(raw["errors"]), "lokasi") {
		t.Fatalf("start dengan variabel hilang: %d %v", st, raw)
	}
	// SIM tidak ada -> no_capacity; dedupe=false ditolak
	admin.do("PATCH", "/api/v1/campaigns/"+a.ID, map[string]any{"body": "Halo {{name}}"}, nil)
	if st := admin.do("POST", "/api/v1/campaigns/"+a.ID+"/start", map[string]any{}, &pr); st != 422 || pr.Type != "campaign.no_capacity" {
		t.Fatalf("tanpa SIM: %d %s", st, pr.Type)
	}
	if st := admin.do("POST", "/api/v1/campaigns/"+a.ID+"/recipients", map[string]any{"dedupe": false, "recipients": []map[string]any{{"to": "08123456780"}}}, nil); st != 422 {
		t.Fatalf("dedupe=false: %d", st)
	}
	// dengan device: start ok, lalu aturan edit untuk status berjalan
	id, secret := e.enroll(admin)
	e.connect(id, secret, nil)
	admin.action(a.ID, "start", 200)
	if st := admin.do("PATCH", "/api/v1/campaigns/"+a.ID, map[string]any{"name": "Ganti"}, &pr); st != 409 || pr.Type != "campaign.not_editable" {
		t.Fatalf("patch nama saat berjalan: %d %s", st, pr.Type)
	}
	var pol struct{ ID string }
	admin.do("POST", "/api/v1/dispatch-policies", map[string]any{"name": "Pelan", "min_delay_ms": 20000, "max_delay_ms": 20000}, &pol)
	if st := admin.do("PATCH", "/api/v1/campaigns/"+a.ID, map[string]any{"policy_id": pol.ID}, nil); st != 200 {
		t.Fatalf("patch policy saat berjalan harus boleh: %d", st)
	}
	if st := admin.do("POST", "/api/v1/campaigns/"+a.ID+"/recipients", map[string]any{"recipients": []map[string]any{{"to": "08123456780"}}}, &pr); st != 409 {
		t.Fatalf("impor saat berjalan: %d", st)
	}
	if st := admin.do("DELETE", "/api/v1/campaigns/"+a.ID, nil, &pr); st != 409 {
		t.Fatalf("hapus saat berjalan: %d", st)
	}
	if st := admin.do("POST", "/api/v1/campaigns/"+a.ID+"/start", map[string]any{}, &pr); st != 409 || pr.Type != "campaign.invalid_transition" {
		t.Fatalf("start ganda: %d %s", st, pr.Type)
	}
	admin.action(a.ID, "cancel", 200)
	if st := admin.do("POST", "/api/v1/campaigns/"+uuid.NewString()+"/start", map[string]any{}, nil); st != 404 {
		t.Fatalf("id tak dikenal: %d", st)
	}
	d := admin.newCampaign(map[string]any{"name": "Hapus", "body": "x"})
	if st := admin.do("DELETE", "/api/v1/campaigns/"+d.ID, nil, nil); st != 204 {
		t.Fatalf("hapus draft: %d", st)
	}
}

func TestCampaignPriorityYieldsToQuickSend(t *testing.T) { // T-INT-02: kirim cepat (5) mendahului antrean campaign (3)
	e := newEnv(t)
	e.fastPolicy(600, 600)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	cm := admin.newCampaign(map[string]any{"name": "Prio", "body": "Campaign {{name}}"})
	admin.importJSON(cm.ID, recs(8, "A"))
	admin.action(cm.ID, "start", 200)
	waitFor(t, 10*time.Second, "campaign mulai terkirim", func() bool { return s.SendCount() >= 1 })
	quick := admin.send("08129998877", "KIRIM-CEPAT")[0]
	admin.waitStatus(quick.ID, "delivered", 20*time.Second)
	idx := -1
	s.mu.Lock()
	for i, r := range s.Sends {
		if r.Cmd.Body == "KIRIM-CEPAT" {
			idx = i
		}
	}
	s.mu.Unlock()
	if idx < 0 || idx > 3 { // paling lambat setelah pesan campaign yang sedang berjalan + beberapa tick
		t.Fatalf("kirim cepat harus dilayani lebih dulu; urutan ke-%d dari 9", idx)
	}
	admin.action(cm.ID, "cancel", 200)
}

func TestCampaignSimAllowList(t *testing.T) { // sim_ids membatasi SIM yang dipakai
	e := newEnv(t)
	e.fastPolicy(80, 80)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin, 0, 1)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	var sims struct {
		Items []struct {
			ID        string
			SlotIndex int `json:"slot_index"`
		}
	}
	admin.do("GET", "/api/v1/sims?device_id="+id.String(), nil, &sims)
	var slot1 string
	for _, x := range sims.Items {
		if x.SlotIndex == 1 {
			slot1 = x.ID
		}
	}
	cm := admin.newCampaign(map[string]any{"name": "Allow", "body": "Halo {{name}}", "sim_ids": []string{slot1}})
	admin.importJSON(cm.ID, recs(5, "A"))
	admin.action(cm.ID, "start", 200)
	admin.waitCampaign(cm.ID, "completed", 25*time.Second)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.Sends {
		if r.Cmd.Sim.SlotIndex != 1 {
			t.Fatalf("hanya slot 1 yang boleh dipakai, dapat slot %d", r.Cmd.Sim.SlotIndex)
		}
	}
	if len(s.Sends) != 5 {
		t.Fatalf("mau 5 pengiriman, dapat %d", len(s.Sends))
	}
}

func TestCampaignSuppressionAfterImportIsHonored(t *testing.T) { // opt-out yang masuk setelah impor
	e := newEnv(t)
	e.fastPolicy(60, 60)
	admin := e.loginAs("admin")
	id, secret := e.enroll(admin)
	s, _ := e.connect(id, secret, nil)
	s.OnSend = Happy
	cm := admin.newCampaign(map[string]any{"name": "Opt", "body": "Halo {{name}}"})
	admin.importJSON(cm.ID, recs(3, "A"))
	if st := admin.do("POST", "/api/v1/suppressions", map[string]any{"numbers": []string{"08121000001"}}, nil); st != 201 {
		t.Fatalf("suppress: %d", st)
	}
	admin.action(cm.ID, "start", 200)
	done := admin.waitCampaign(cm.ID, "completed", 20*time.Second)
	checkInvariant(t, done)
	if done.Counters.Failed != 1 || done.Counters.Delivered != 2 || s.SendCount() != 2 {
		t.Fatalf("1 penerima opt-out tidak boleh dikirim: %+v sends=%d", done.Counters, s.SendCount())
	}
	for _, m := range admin.messagesOf(cm.ID) {
		if m.Status == "failed" && (m.ErrorCode == nil || *m.ErrorCode != "suppressed") {
			t.Fatalf("alasan harus suppressed: %+v", m)
		}
	}
}

func TestCampaignRBAC(t *testing.T) {
	e := newEnv(t)
	admin := e.loginAs("admin")
	viewer := e.loginAs("viewer")
	operator := e.loginAs("operator")
	cm := operator.newCampaign(map[string]any{"name": "Op", "body": "x"})
	if st := viewer.do("POST", "/api/v1/campaigns", map[string]any{"name": "V", "body": "x"}, nil); st != 403 {
		t.Fatalf("viewer buat: %d", st)
	}
	if st := viewer.do("GET", "/api/v1/campaigns/"+cm.ID, nil, nil); st != 200 {
		t.Fatalf("viewer baca: %d", st)
	}
	if st := viewer.do("POST", "/api/v1/campaigns/"+cm.ID+"/start", map[string]any{}, nil); st != 403 {
		t.Fatalf("viewer start: %d", st)
	}
	var list struct{ Items []camp }
	if st := admin.do("GET", "/api/v1/campaigns?status=draft", nil, &list); st != 200 || len(list.Items) != 1 {
		t.Fatalf("list: %d %+v", st, list)
	}
}
