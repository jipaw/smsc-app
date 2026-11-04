package tests

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jipaw/smsc-app/server/internal/config"
)

type quotaOut struct {
	DailyQuota *int   `json:"daily_quota"`
	Effective  *int   `json:"effective"`
	Used       int    `json:"used"`
	Remaining  *int   `json:"remaining"`
	ResetsAt   string `json:"resets_at"`
	Timezone   string `json:"timezone"`
	Unit       string `json:"unit"`
}

func sendN(c *client, n int, body string, hdr map[string]string) (int, string, string) {
	rs := []map[string]any{}
	for i := 0; i < n; i++ {
		rs = append(rs, map[string]any{"to": fmt.Sprintf("0813%07d", 2000000+i)})
	}
	var prob struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	st, h := c.doH("POST", "/api/v1/messages", map[string]any{"recipients": rs, "body": body}, &prob, hdr)
	return st, prob.Type, h.Get("Retry-After")
}

func countMsgs(e *env) (n int) {
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM messages WHERE source <> 'campaign'`).Scan(&n)
	return
}

// kuota API key dalam bagian SMS: pesan multi-bagian dihitung per bagian; permintaan yang melampaui ditolak utuh (429 + Retry-After).
func TestAPIKeyDailyQuota(t *testing.T) {
	e := newEnv(t)
	admin := e.loginAs("admin")
	var k struct {
		ID     string    `json:"id"`
		Secret string    `json:"secret"`
		Quota  *quotaOut `json:"quota"`
	}
	if st := admin.do("POST", "/api/v1/api-keys", map[string]any{"name": "kuota", "daily_quota": 4}, &k); st != 201 || k.Quota == nil || *k.Quota.Effective != 4 {
		t.Fatalf("buat key: %d %+v", st, k.Quota)
	}
	api := &client{e: e, http: admin.http, key: k.Secret}
	long := strings.Repeat("panjang ", 25) // 200 karakter GSM → 2 bagian
	if st, _, _ := sendN(api, 1, long, nil); st != 202 {
		t.Fatalf("2 bagian: %d", st)
	}
	before := countMsgs(e)
	st, typ, ra := sendN(api, 3, "pendek", nil) // 2 + 3 > 4
	if st != 429 || typ != "quota.exceeded" || ra == "" {
		t.Fatalf("melampaui: %d %s retry-after=%q", st, typ, ra)
	}
	if countMsgs(e) != before {
		t.Fatal("permintaan yang melampaui kuota tidak boleh membuat pesan sebagian")
	}
	if st, _, _ := sendN(api, 2, "pendek", nil); st != 202 {
		t.Fatalf("pas di batas: %d", st)
	}
	var q quotaOut
	if st := api.do("GET", "/api/v1/quota", nil, &q); st != 200 || q.Used != 4 || q.Remaining == nil || *q.Remaining != 0 || q.Unit != "sms_parts" || q.Timezone != "UTC" {
		t.Fatalf("GET /quota: %d %+v", st, q)
	}
	// duplikat idempoten tidak dibebankan ulang
	if st := admin.do("PUT", "/api/v1/api-keys/"+k.ID+"/quota", map[string]any{"daily_quota": 5}, nil); st != 200 {
		t.Fatalf("naikkan: %d", st)
	}
	idem := map[string]string{"Idempotency-Key": "q-1"}
	if st, _, _ := sendN(api, 1, "sekali", idem); st != 202 {
		t.Fatalf("idem pertama: %d", st)
	}
	if st, _, _ := sendN(api, 1, "sekali", idem); st != 202 {
		t.Fatalf("idem ulang harus tetap 202 meski kuota habis: %d", st)
	}
	// 0 = tanpa batas; daftar admin memuat pemakaian
	admin.do("PUT", "/api/v1/api-keys/"+k.ID+"/quota", map[string]any{"daily_quota": 0}, nil)
	if st, _, _ := sendN(api, 3, "bebas", nil); st != 202 {
		t.Fatalf("tanpa batas: %d", st)
	}
	var list struct {
		Items []struct {
			ID    string    `json:"id"`
			Quota *quotaOut `json:"quota"`
		} `json:"items"`
	}
	admin.do("GET", "/api/v1/api-keys", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Quota == nil || list.Items[0].Quota.Effective != nil || list.Items[0].Quota.Used != 8 { // tanpa batas tetap dicatat
		t.Fatalf("daftar key: %+v", list.Items[0].Quota)
	}
	for _, bad := range []any{-1, "x", 20_000_000} {
		if st := admin.do("PUT", "/api/v1/api-keys/"+k.ID+"/quota", map[string]any{"daily_quota": bad}, nil); st != 422 && st != 400 {
			t.Fatalf("nilai %v harus ditolak: %d", bad, st)
		}
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='quota.set' AND target_id=$1`, k.ID).Scan(&n)
	if n < 3 {
		t.Fatalf("audit quota.set: %d", n)
	}
}

// default env per pengguna; admin menimpa (0 = tanpa batas, null = kembali ke default); key tidak memakai kuota pengguna.
func TestUserDailyQuotaDefaultAndOverride(t *testing.T) {
	e := newEnvOpts(t, func(c *config.Config) { c.DailyQuotaUser = 2 })
	admin := e.loginAs("admin")
	op := e.newUser("operator")
	if st, _, _ := sendN(op.c, 2, "a", nil); st != 202 {
		t.Fatalf("dalam default: %d", st)
	}
	if st, typ, _ := sendN(op.c, 1, "a", nil); st != 429 || typ != "quota.exceeded" {
		t.Fatalf("default 2 habis: %d %s", st, typ)
	}
	if st := admin.do("PUT", "/api/v1/users/"+op.id+"/quota", map[string]any{"daily_quota": 0}, nil); st != 200 {
		t.Fatalf("set 0: %d", st)
	}
	if st, _, _ := sendN(op.c, 3, "a", nil); st != 202 {
		t.Fatalf("tanpa batas: %d", st)
	}
	var q quotaOut
	if st := admin.do("PUT", "/api/v1/users/"+op.id+"/quota", map[string]any{"daily_quota": nil}, &q); st != 200 || q.DailyQuota != nil || q.Effective == nil || *q.Effective != 2 || q.Used != 5 {
		t.Fatalf("kembali ke default: %d %+v", st, q)
	}
	if st, _, _ := sendN(op.c, 1, "a", nil); st != 429 {
		t.Fatalf("default berlaku lagi: %d", st)
	}
	if st := admin.do("PUT", "/api/v1/users/00000000-0000-0000-0000-000000000000/quota", map[string]any{"daily_quota": 1}, nil); st != 404 {
		t.Fatalf("pengguna tak ada: %d", st)
	}
	// API key tanpa nilai sendiri dan DAILY_QUOTA_PER_KEY=0 → tidak dibatasi walau dibuat oleh pengguna yang kuotanya habis
	var k struct{ Secret string }
	admin.do("POST", "/api/v1/api-keys", map[string]any{"name": "bebas"}, &k)
	if st, _, _ := sendN(&client{e: e, http: admin.http, key: k.Secret}, 3, "k", nil); st != 202 {
		t.Fatalf("key tanpa kuota: %d", st)
	}
}

// permintaan bersamaan tidak dapat melampaui batas (baris kuota terkunci dalam transaksi).
func TestQuotaConcurrentRequestsNeverExceed(t *testing.T) {
	e := newEnvOpts(t, func(c *config.Config) { c.DailyQuotaKey = 5 })
	admin := e.loginAs("admin")
	var k struct{ Secret string }
	admin.do("POST", "/api/v1/api-keys", map[string]any{"name": "paralel"}, &k)
	api := &client{e: e, http: admin.http, key: k.Secret}
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st, _, _ := sendN(api, 1, fmt.Sprintf("p%d", i), nil)
			mu.Lock()
			codes[st]++
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if codes[202] != 5 || codes[429] != 7 || countMsgs(e) != 5 {
		t.Fatalf("paralel: %v pesan=%d", codes, countMsgs(e))
	}
}

// campaign: materialisasi berhenti di sisa kuota pemilik, campaign dijeda "quota", lalu lanjut otomatis saat kuota dinaikkan.
func TestCampaignPausesOnOwnerQuotaAndAutoResumes(t *testing.T) {
	e := newEnv(t)
	admin := e.loginAs("admin")
	e.enroll(admin, 0)
	owner := e.newUser("operator")
	admin.do("PUT", "/api/v1/users/"+owner.id+"/quota", map[string]any{"daily_quota": 3}, nil)
	cm := owner.c.newCampaign(map[string]any{"name": "kuota", "body": "Halo {{name}}"})
	owner.c.importJSON(cm.ID, recs(5, "Q"))
	owner.c.action(cm.ID, "start", 200)
	msgs := func() (n int) {
		_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM messages WHERE campaign_id=$1`, cm.ID).Scan(&n)
		return
	}
	p := owner.c.waitCampaign(cm.ID, "paused", 10*time.Second)
	if p.PauseReason == nil || *p.PauseReason != "quota" || msgs() != 3 {
		t.Fatalf("jeda kuota: reason=%v pesan=%d", p.PauseReason, msgs())
	}
	var q quotaOut
	owner.c.do("GET", "/api/v1/quota", nil, &q)
	if q.Used != 3 {
		t.Fatalf("kuota terpakai campaign: %+v", q)
	}
	// melanjutkan manual saat kuota masih habis → dijeda lagi
	owner.c.action(cm.ID, "resume", 200)
	if owner.c.waitCampaign(cm.ID, "paused", 10*time.Second); msgs() != 3 {
		t.Fatalf("resume manual tanpa kuota: pesan=%d", msgs())
	}
	admin.do("PUT", "/api/v1/users/"+owner.id+"/quota", map[string]any{"daily_quota": 10}, nil)
	waitFor(t, 10*time.Second, "campaign lanjut otomatis", func() bool {
		c := owner.c.campaign(cm.ID)
		return c.Status == "running" && msgs() == 5
	})
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE action='campaign.auto_resume' AND target_id=$1`, cm.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("audit auto_resume: %d", n)
	}
}
