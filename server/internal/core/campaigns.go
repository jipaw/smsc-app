package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jipaw/smsc-app/server/internal/policy"
	"github.com/jipaw/smsc-app/server/internal/sms"
)

const maxRecipients = 50000

type AutoPause struct {
	Enabled         bool    `json:"enabled"`
	Window          int     `json:"window"`
	MaxFailureRatio float64 `json:"max_failure_ratio"`
}

type CampaignCounters struct {
	Total     int `json:"total"`
	Queued    int `json:"queued"`
	InFlight  int `json:"in_flight"`
	Sent      int `json:"sent"`
	Delivered int `json:"delivered"`
	Failed    int `json:"failed"`
	Partial   int `json:"partial"`  // bagian dari failed (bukan tambahan): gagal setelah sebagian bagian SMS terkirim
	AllSent   int `json:"all_sent"` // bagian dari partial: ternyata semua bagian terkirim
	Cancelled int `json:"cancelled"`
	Expired   int `json:"expired"`
	Unknown   int `json:"unknown"`
}

type Campaign struct {
	ID          uuid.UUID   `json:"id"`
	Name        string      `json:"name"`
	Description *string     `json:"description"`
	Status      string      `json:"status"`
	PauseReason *string     `json:"pause_reason"`
	Body        *string     `json:"body"`
	TemplateID  *uuid.UUID  `json:"template_id"`
	SimIDs      []uuid.UUID `json:"sim_ids"`
	PolicyID    *uuid.UUID  `json:"policy_id"`
	Priority    int         `json:"priority"`
	Schedule    struct {
		StartAt *time.Time `json:"start_at"`
	} `json:"schedule"`
	AutoPause  AutoPause        `json:"auto_pause"`
	Counters   CampaignCounters `json:"counters"`
	Recipients struct {
		Count   int      `json:"count"`
		Columns []string `json:"columns"`
	} `json:"recipients"`
	EstimatedFinishAt *time.Time `json:"estimated_finish_at"`
	CreatedBy         *uuid.UUID `json:"created_by"`
	CreatedAt         time.Time  `json:"created_at"`
	StartedAt         *time.Time `json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at"`

	evalSince *time.Time
}

const campCols = `id,name,description,status,pause_reason,body,template_id,sim_ids,policy_id,priority,start_at,auto_pause,created_by,created_at,started_at,finished_at,eval_since`

func scanCampaign(r pgx.Row) (Campaign, error) {
	var c Campaign
	err := r.Scan(&c.ID, &c.Name, &c.Description, &c.Status, &c.PauseReason, &c.Body, &c.TemplateID, &c.SimIDs, &c.PolicyID, &c.Priority,
		&c.Schedule.StartAt, &c.AutoPause, &c.CreatedBy, &c.CreatedAt, &c.StartedAt, &c.FinishedAt, &c.evalSince)
	if c.SimIDs == nil {
		c.SimIDs = []uuid.UUID{}
	}
	c.Recipients.Columns = []string{}
	return c, err
}

func problem(status int, typ, title, detail string) *Problem {
	return NewProblem(status, typ, title, detail)
}

func notEditable(detail string) *Problem {
	return problem(http.StatusConflict, "campaign.not_editable", "Campaign tidak dapat diubah", detail)
}
func badTransition(detail string) *Problem {
	return problem(http.StatusConflict, "campaign.invalid_transition", "Transisi status tidak valid", detail)
}

// ---------- counters, ringkasan penerima, estimasi ----------

// hydrate mengisi counters + ringkasan penerima + estimasi untuk sekumpulan campaign (query teragregasi, bukan per baris).
func (s *Service) hydrate(ctx context.Context, cs []*Campaign) error {
	if len(cs) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(cs))
	byID := map[uuid.UUID]*Campaign{}
	for i, c := range cs {
		ids[i] = c.ID
		byID[c.ID] = c
	}
	rows, err := s.Pool.Query(ctx, `SELECT campaign_id, status::text, count(*), count(*) FILTER (WHERE partial_sent IS NOT NULL), count(*) FILTER (WHERE partial_sent IS NOT NULL AND partial_sent >= parts) FROM messages WHERE campaign_id = ANY($1) GROUP BY 1,2`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id uuid.UUID
		var st string
		var n, np, na int
		if err := rows.Scan(&id, &st, &n, &np, &na); err != nil {
			rows.Close()
			return err
		}
		c := byID[id]
		switch st {
		case "queued":
			c.Counters.Queued += n
		case "assigned", "sending":
			c.Counters.InFlight += n
		case "sent":
			c.Counters.Sent += n
		case "delivered":
			c.Counters.Delivered += n
		case "failed":
			c.Counters.Failed += n
			c.Counters.Partial += np
			c.Counters.AllSent += na
		case "cancelled":
			c.Counters.Cancelled += n
		case "expired":
			c.Counters.Expired += n
		case "unknown":
			c.Counters.Unknown += n
		}
	}
	rows.Close()
	rows, err = s.Pool.Query(ctx, `SELECT campaign_id, count(*), count(*) FILTER (WHERE materialized_at IS NULL) FROM campaign_recipients WHERE campaign_id = ANY($1) GROUP BY 1`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id uuid.UUID
		var total, pending int
		if err := rows.Scan(&id, &total, &pending); err != nil {
			rows.Close()
			return err
		}
		c := byID[id]
		c.Recipients.Count, c.Counters.Total = total, total
		if c.Status == "cancelled" {
			c.Counters.Cancelled += pending
		} else {
			c.Counters.Queued += pending
		}
	}
	rows.Close()
	for _, c := range cs {
		cols, err := s.pool1000Columns(ctx, c.ID)
		if err != nil {
			return err
		}
		c.Recipients.Columns = cols
		if c.Status == "running" {
			remaining := c.Counters.Queued + c.Counters.InFlight
			if remaining > 0 {
				if _, secs := s.estimate(ctx, c.PolicyID, c.SimIDs, remaining); secs > 0 {
					t := time.Now().Add(time.Duration(math.Ceil(secs)) * time.Second)
					c.EstimatedFinishAt = &t
				}
			}
		}
	}
	return nil
}

// pool1000Columns: nama variabel dari 1.000 penerima pertama (cukup untuk validasi UI).
func (s *Service) pool1000Columns(ctx context.Context, id uuid.UUID) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT k FROM (SELECT vars FROM campaign_recipients WHERE campaign_id=$1 LIMIT 1000) r, jsonb_object_keys(r.vars) k ORDER BY k LIMIT 100`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// eligibleSims: jumlah SIM yang dapat mengirim sekarang (online) untuk allow-list sim_ids; total = tanpa syarat online.
func (s *Service) eligibleSims(ctx context.Context, simIDs []uuid.UUID, onlineOnly bool) int {
	q := `SELECT count(*) FROM sims s JOIN devices d ON d.id=s.device_id
		WHERE s.enabled AND s.status='active' AND d.enabled AND d.status <> 'revoked' AND (cardinality($1::uuid[])=0 OR s.id = ANY($1))`
	if onlineOnly {
		q += ` AND d.status IN ('online','degraded')`
	}
	var n int
	if err := s.Pool.QueryRow(ctx, q, nonNilUUIDs(simIDs)).Scan(&n); err != nil {
		return 0
	}
	return n
}

// estimateDuration: perkiraan kasar detik untuk mengirim n pesan lewat `online` SIM paralel.
// base = jeda rata-rata + ±2 dtk waktu kirim (+ jeda burst yang diamortisasi); batas per menit/jam/hari
// hanya memperlambat bila n per-SIM MELAMPAUI batas itu: (ceil(nSim/batas)-1) jendela penuh harus berlalu.
// Tidak memperhitungkan jendela waktu kirim maupun retry.
func estimateDuration(eff policy.Effective, online, n int) float64 {
	if online <= 0 || n <= 0 {
		return 0
	}
	per := float64(eff.MinDelayMs+eff.MaxDelayMs)/2000 + 2.0
	if eff.BurstSize > 0 {
		per += float64(eff.BurstPauseMs) / 1000 / float64(eff.BurstSize)
	}
	nSim := math.Ceil(float64(n) / float64(online))
	secs := nSim * per
	for _, w := range []struct {
		limit int
		win   float64
	}{{eff.RatePerMinute, 60}, {eff.RatePerHour, 3600}, {eff.RatePerDay, 86400}} {
		if w.limit > 0 {
			if need := (math.Ceil(nSim/float64(w.limit)) - 1) * w.win; need > secs {
				secs = need
			}
		}
	}
	return secs
}

// estimate: (SIM online, detik) untuk n pesan berdasarkan policy global + campaign. 0 detik bila tak ada SIM online.
func (s *Service) estimate(ctx context.Context, policyID *uuid.UUID, simIDs []uuid.UUID, n int) (online int, secs float64) {
	online = s.eligibleSims(ctx, simIDs, true)
	eff, err := s.Effective(ctx, s.Pool, nil, nil, policyID)
	if err != nil || online == 0 {
		return online, 0
	}
	return online, estimateDuration(eff, online, n)
}

// ---------- CRUD ----------

type campaignFields struct {
	name, description, body, templateID, simIDs, policyID, priority, startAt, autoPause bool
	v                                                                                   struct {
		name, desc, body *string
		template         *uuid.UUID
		sims             []uuid.UUID
		policy           *uuid.UUID
		priority         int
		startAt          *time.Time
		ap               AutoPause
	}
}

func (s *Service) parseCampaignFields(ctx context.Context, f map[string]json.RawMessage, base AutoPause) (campaignFields, []FieldError) {
	var cf campaignFields
	var errs []FieldError
	bad := func(field, msg string) { errs = append(errs, FieldError{Field: field, Message: msg}) }
	cf.v.ap = base
	for k, raw := range f {
		switch k {
		case "name":
			cf.name = true
			var v string
			if json.Unmarshal(raw, &v) != nil || strings.TrimSpace(v) == "" || len([]rune(v)) > 120 {
				bad("name", "wajib diisi, maksimal 120 karakter")
			}
			cf.v.name = &v
		case "description":
			cf.description = true
			_ = json.Unmarshal(raw, &cf.v.desc)
		case "body":
			cf.body = true
			if string(raw) != "null" {
				var v string
				if json.Unmarshal(raw, &v) != nil || strings.TrimSpace(v) == "" || len([]rune(v)) > 1600 {
					bad("body", "tidak boleh kosong, maksimal 1600 karakter")
				}
				cf.v.body = &v
			}
		case "template_id":
			cf.templateID = true
			if string(raw) != "null" {
				var id uuid.UUID
				if json.Unmarshal(raw, &id) != nil {
					bad("template_id", "bukan UUID")
				} else {
					var ok bool
					_ = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM templates WHERE id=$1)`, id).Scan(&ok)
					if !ok {
						bad("template_id", "template tidak ditemukan")
					}
					cf.v.template = &id
				}
			}
		case "sim_ids":
			cf.simIDs = true
			var ids []uuid.UUID
			if json.Unmarshal(raw, &ids) != nil {
				bad("sim_ids", "harus array UUID")
				break
			}
			if len(ids) > 0 {
				var n int
				_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM sims WHERE id = ANY($1)`, ids).Scan(&n)
				if n != len(uniqueUUIDs(ids)) {
					bad("sim_ids", "ada SIM yang tidak dikenal")
				}
			}
			cf.v.sims = uniqueUUIDs(ids)
		case "policy_id":
			cf.policyID = true
			if string(raw) != "null" {
				var id uuid.UUID
				if json.Unmarshal(raw, &id) != nil {
					bad("policy_id", "bukan UUID")
				} else if _, err := s.GetPolicy(ctx, id); err != nil {
					bad("policy_id", "policy tidak ditemukan")
				} else {
					cf.v.policy = &id
				}
			}
		case "priority":
			cf.priority = true
			if json.Unmarshal(raw, &cf.v.priority) != nil || cf.v.priority < 0 || cf.v.priority > 9 {
				bad("priority", "0..9")
			}
		case "schedule":
			cf.startAt = true
			var sch struct {
				StartAt *time.Time `json:"start_at"`
			}
			if json.Unmarshal(raw, &sch) != nil {
				bad("schedule", "format tidak valid")
			}
			cf.v.startAt = sch.StartAt
		case "auto_pause":
			cf.autoPause = true
			var in struct {
				Enabled *bool    `json:"enabled"`
				Window  *int     `json:"window"`
				Ratio   *float64 `json:"max_failure_ratio"`
			}
			if json.Unmarshal(raw, &in) != nil {
				bad("auto_pause", "format tidak valid")
				break
			}
			if in.Enabled != nil {
				cf.v.ap.Enabled = *in.Enabled
			}
			if in.Window != nil {
				if *in.Window < 1 || *in.Window > 1000 {
					bad("auto_pause.window", "1..1000")
				}
				cf.v.ap.Window = *in.Window
			}
			if in.Ratio != nil {
				if *in.Ratio < 0 || *in.Ratio > 1 {
					bad("auto_pause.max_failure_ratio", "0..1")
				}
				cf.v.ap.MaxFailureRatio = *in.Ratio
			}
		}
	}
	return cf, errs
}

func uniqueUUIDs(in []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	out := []uuid.UUID{}
	for _, x := range in {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

var defaultAutoPause = AutoPause{Enabled: true, Window: 50, MaxFailureRatio: 0.5}

func (s *Service) CreateCampaign(ctx context.Context, by uuid.UUID, idemKey string, f map[string]json.RawMessage) (Campaign, error) {
	cf, errs := s.parseCampaignFields(ctx, f, defaultAutoPause)
	if !cf.name {
		errs = append(errs, FieldError{Field: "name", Message: "wajib diisi"})
	}
	if cf.v.body != nil && cf.v.template != nil {
		errs = append(errs, FieldError{Field: "body", Message: "isi salah satu: body atau template_id"})
	}
	if cf.v.body == nil && cf.v.template == nil {
		errs = append(errs, FieldError{Field: "body", Message: "isi salah satu: body atau template_id"})
	}
	if cf.startAt && cf.v.startAt != nil && cf.v.startAt.Before(time.Now().Add(-time.Minute)) {
		errs = append(errs, FieldError{Field: "schedule.start_at", Message: "tidak boleh di masa lalu"})
	}
	if len(errs) > 0 {
		return Campaign{}, Validation(errs...)
	}
	prio := 3
	if cf.priority {
		prio = cf.v.priority
	}
	id := newID()
	tag, err := s.Pool.Exec(ctx, `INSERT INTO campaigns(id,name,description,body,template_id,sim_ids,policy_id,priority,start_at,auto_pause,created_by,idempotency_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT (created_by, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING`,
		id, *cf.v.name, cf.v.desc, cf.v.body, cf.v.template, nonNilUUIDs(cf.v.sims), cf.v.policy, prio, cf.v.startAt, cf.v.ap, by, nullStr(idemKey))
	if err != nil {
		return Campaign{}, err
	}
	if tag.RowsAffected() == 0 { // idempoten: kembalikan campaign yang sudah dibuat dengan kunci yang sama
		if err := s.Pool.QueryRow(ctx, `SELECT id FROM campaigns WHERE created_by=$1 AND idempotency_key=$2`, by, idemKey).Scan(&id); err != nil {
			return Campaign{}, err
		}
	}
	return s.GetCampaign(ctx, id)
}

func (s *Service) GetCampaign(ctx context.Context, id uuid.UUID) (Campaign, error) {
	c, err := scanCampaign(s.Pool.QueryRow(ctx, `SELECT `+campCols+` FROM campaigns WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, NotFound("campaign")
	}
	if err != nil {
		return c, err
	}
	return c, s.hydrate(ctx, []*Campaign{&c})
}

func (s *Service) ListCampaigns(ctx context.Context, status string, limit int, cursor string) ([]Campaign, string, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	args := []any{status, limit + 1}
	q := `SELECT ` + campCols + ` FROM campaigns WHERE ($1='' OR status::text=$1)`
	if cursor != "" {
		t, id, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", Validation(FieldError{Field: "cursor", Message: "tidak valid"})
		}
		args = append(args, t, id)
		q += ` AND (created_at, id) < ($3, $4)`
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT $2`
	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	var cs []Campaign
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			rows.Close()
			return nil, "", err
		}
		cs = append(cs, c)
	}
	rows.Close()
	next := ""
	if len(cs) > limit {
		cs = cs[:limit]
		next = encodeCursor(cs[len(cs)-1].CreatedAt, cs[len(cs)-1].ID)
	}
	ptrs := make([]*Campaign, len(cs))
	for i := range cs {
		ptrs[i] = &cs[i]
	}
	if err := s.hydrate(ctx, ptrs); err != nil {
		return nil, "", err
	}
	if cs == nil {
		cs = []Campaign{}
	}
	return cs, next, nil
}

// PatchCampaign: draft = semua field; scheduled/running/paused = hanya policy_id dan sim_ids; lainnya 409.
func (s *Service) PatchCampaign(ctx context.Context, id uuid.UUID, f map[string]json.RawMessage, actor string) (Campaign, error) {
	cur, err := scanCampaign(s.Pool.QueryRow(ctx, `SELECT `+campCols+` FROM campaigns WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, NotFound("campaign")
	}
	if err != nil {
		return Campaign{}, err
	}
	cf, errs := s.parseCampaignFields(ctx, f, cur.AutoPause)
	switch cur.Status {
	case "draft":
	case "scheduled", "running", "paused":
		var forbidden []string
		for k := range f {
			if k != "policy_id" && k != "sim_ids" {
				forbidden = append(forbidden, k)
			}
		}
		if len(forbidden) > 0 {
			return Campaign{}, notEditable(fmt.Sprintf("pada status %q hanya policy_id dan sim_ids yang dapat diubah (ditolak: %s)", cur.Status, strings.Join(forbidden, ", ")))
		}
	default:
		return Campaign{}, notEditable(fmt.Sprintf("campaign berstatus %q tidak dapat diubah", cur.Status))
	}
	// aturan body/template saling eksklusif (draft)
	body, tpl := cur.Body, cur.TemplateID
	if cf.body {
		body = cf.v.body
		if cf.v.body != nil && !cf.templateID {
			tpl = nil
		}
	}
	if cf.templateID {
		tpl = cf.v.template
		if cf.v.template != nil && !cf.body {
			body = nil
		}
	}
	if (body == nil) == (tpl == nil) {
		errs = append(errs, FieldError{Field: "body", Message: "isi salah satu: body atau template_id"})
	}
	if cf.startAt && cf.v.startAt != nil && cf.v.startAt.Before(time.Now().Add(-time.Minute)) {
		errs = append(errs, FieldError{Field: "schedule.start_at", Message: "tidak boleh di masa lalu"})
	}
	if len(errs) > 0 {
		return Campaign{}, Validation(errs...)
	}
	name, desc, sims, policyID, prio, startAt, ap := cur.Name, cur.Description, cur.SimIDs, cur.PolicyID, cur.Priority, cur.Schedule.StartAt, cur.AutoPause
	if cf.name {
		name = *cf.v.name
	}
	if cf.description {
		desc = cf.v.desc
	}
	if cf.simIDs {
		sims = cf.v.sims
	}
	if cf.policyID {
		policyID = cf.v.policy
	}
	if cf.priority {
		prio = cf.v.priority
	}
	if cf.startAt {
		startAt = cf.v.startAt
	}
	if cf.autoPause {
		ap = cf.v.ap
	}
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE campaigns SET name=$2,description=$3,body=$4,template_id=$5,sim_ids=$6,policy_id=$7,priority=$8,start_at=$9,auto_pause=$10 WHERE id=$1`,
			id, name, desc, body, tpl, nonNilUUIDs(sims), policyID, prio, startAt, ap); err != nil {
			return err
		}
		if cf.policyID { // pesan yang sudah di-materialisasi namun belum di-lease mengikuti policy baru
			if _, err := tx.Exec(ctx, `UPDATE messages SET policy_id=$2 WHERE campaign_id=$1 AND status='queued'`, id, policyID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Campaign{}, err
	}
	s.Audit(ctx, actor, "campaign.update", "campaign", id.String(), map[string]any{"fields": keysOf(f)})
	s.publishCampaign(ctx, id)
	s.Wake()
	return s.GetCampaign(ctx, id)
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (s *Service) DeleteCampaign(ctx context.Context, id uuid.UUID, actor string) error {
	var st string
	if err := s.Pool.QueryRow(ctx, `SELECT status::text FROM campaigns WHERE id=$1`, id).Scan(&st); errors.Is(err, pgx.ErrNoRows) {
		return NotFound("campaign")
	} else if err != nil {
		return err
	}
	if st != "draft" {
		return problem(http.StatusConflict, "campaign.not_editable", "Tidak dapat dihapus", "hanya campaign berstatus draft yang dapat dihapus")
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM campaigns WHERE id=$1 AND status='draft'`, id); err != nil {
		return err
	}
	s.Audit(ctx, actor, "campaign.delete", "campaign", id.String(), nil)
	return nil
}

func (s *Service) publishCampaign(ctx context.Context, id uuid.UUID) {
	var st string
	var reason *string
	if err := s.Pool.QueryRow(ctx, `SELECT status::text, pause_reason FROM campaigns WHERE id=$1`, id).Scan(&st, &reason); err == nil {
		s.Bus.Publish("campaign.updated", map[string]any{"id": id, "status": st, "pause_reason": reason})
	}
}

// ---------- impor penerima ----------

type ImportInvalid struct {
	Row    int    `json:"row"`
	Value  string `json:"value"`
	Reason string `json:"reason"`
}

type ImportResult struct {
	Accepted   int             `json:"accepted"`
	Duplicates int             `json:"duplicates"`
	Suppressed int             `json:"suppressed"`
	Invalid    []ImportInvalid `json:"invalid"`
}

// campaignText: isi pesan efektif campaign (body atau isi template saat ini).
func (s *Service) campaignText(ctx context.Context, c Campaign) (string, error) {
	if c.Body != nil {
		return *c.Body, nil
	}
	if c.TemplateID != nil {
		var b string
		if err := s.Pool.QueryRow(ctx, `SELECT body FROM templates WHERE id=$1`, *c.TemplateID).Scan(&b); err != nil {
			return "", err
		}
		return b, nil
	}
	return "", nil
}

// ImportRecipients: rows[i] = nomor baris untuk laporan (0 -> indeks+1).
func (s *Service) ImportRecipients(ctx context.Context, id uuid.UUID, recs []RecipientInput, rows []int, dedupe bool) (ImportResult, error) {
	res := ImportResult{Invalid: []ImportInvalid{}}
	if !dedupe {
		return res, Validation(FieldError{Field: "dedupe", Message: "hanya dedupe=true yang didukung (satu nomor sekali per campaign)"})
	}
	c, err := scanCampaign(s.Pool.QueryRow(ctx, `SELECT `+campCols+` FROM campaigns WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return res, NotFound("campaign")
	}
	if err != nil {
		return res, err
	}
	if c.Status != "draft" {
		return res, notEditable("penerima hanya dapat diimpor saat campaign berstatus draft")
	}
	var existing int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM campaign_recipients WHERE campaign_id=$1`, id).Scan(&existing); err != nil {
		return res, err
	}
	if len(recs) > maxRecipients || existing+len(recs) > maxRecipients {
		return res, Validation(FieldError{Field: "recipients", Message: fmt.Sprintf("maksimal %d penerima per campaign (sudah ada %d)", maxRecipients, existing)})
	}
	text, err := s.campaignText(ctx, c)
	if err != nil {
		return res, err
	}
	need := sms.Variables(text)

	type item struct {
		id                         uuid.UUID
		row                        int
		raw, e164, vars, clientRef string
	}
	var items []item
	seen := map[string]bool{}
	e164s := []string{}
	for i, r := range recs {
		row := i + 1
		if i < len(rows) && rows[i] > 0 {
			row = rows[i]
		}
		e164, nerr := sms.Normalize(r.To, s.Cfg.DefaultRegion)
		if nerr != nil {
			res.Invalid = append(res.Invalid, ImportInvalid{Row: row, Value: r.To, Reason: "invalid_number"})
			continue
		}
		if !s.DestinationAllowed(e164) {
			res.Invalid = append(res.Invalid, ImportInvalid{Row: row, Value: r.To, Reason: "destination_not_allowed"})
			continue
		}
		missing := ""
		for _, n := range need {
			if v, ok := r.Vars[n]; !ok || strings.TrimSpace(v) == "" {
				missing = n
				break
			}
		}
		if missing != "" {
			res.Invalid = append(res.Invalid, ImportInvalid{Row: row, Value: r.To, Reason: "missing_variable:" + missing})
			continue
		}
		if seen[e164] {
			res.Duplicates++
			continue
		}
		seen[e164] = true
		vj, _ := json.Marshal(r.Vars)
		if r.Vars == nil {
			vj = []byte("{}")
		}
		items = append(items, item{newID(), existing + len(items) + 1, r.To, e164, string(vj), r.ClientRef})
		e164s = append(e164s, e164)
	}
	supp := map[string]bool{}
	if len(e164s) > 0 {
		rs, err := s.Pool.Query(ctx, `SELECT number_e164 FROM suppressions WHERE number_e164 = ANY($1)`, e164s)
		if err != nil {
			return res, err
		}
		for rs.Next() {
			var n string
			_ = rs.Scan(&n)
			supp[n] = true
		}
		rs.Close()
	}
	var ids []uuid.UUID
	var rowNos []int32
	var raws, e164col, vars, refs []string
	for _, it := range items {
		if supp[it.e164] {
			res.Suppressed++
			continue
		}
		ids = append(ids, it.id)
		rowNos = append(rowNos, int32(it.row))
		raws = append(raws, it.raw)
		e164col = append(e164col, it.e164)
		vars = append(vars, it.vars)
		refs = append(refs, it.clientRef)
	}
	if len(ids) > 0 {
		tag, err := s.Pool.Exec(ctx, `INSERT INTO campaign_recipients(id,campaign_id,row_no,to_raw,to_e164,vars,client_ref)
			SELECT t.id, $7, t.row_no, t.raw, t.e164, t.vars::jsonb, NULLIF(t.ref,'')
			FROM unnest($1::uuid[], $2::int[], $3::text[], $4::text[], $5::text[], $6::text[]) AS t(id,row_no,raw,e164,vars,ref)
			ON CONFLICT (campaign_id, to_e164) DO NOTHING`, ids, rowNos, raws, e164col, vars, refs, id)
		if err != nil {
			return res, err
		}
		res.Accepted = int(tag.RowsAffected())
		res.Duplicates += len(ids) - res.Accepted // sudah ada dari impor sebelumnya
	}
	return res, nil
}

func (s *Service) ClearRecipients(ctx context.Context, id uuid.UUID) (int, error) {
	var st string
	if err := s.Pool.QueryRow(ctx, `SELECT status::text FROM campaigns WHERE id=$1`, id).Scan(&st); errors.Is(err, pgx.ErrNoRows) {
		return 0, NotFound("campaign")
	} else if err != nil {
		return 0, err
	}
	if st != "draft" {
		return 0, notEditable("penerima hanya dapat dihapus saat campaign berstatus draft")
	}
	tag, err := s.Pool.Exec(ctx, `DELETE FROM campaign_recipients WHERE campaign_id=$1`, id)
	return int(tag.RowsAffected()), err
}

var varName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// ParseRecipientsCSV: UTF-8 (BOM diterima), pemisah , atau ; terdeteksi, kolom wajib phone. Mengembalikan nomor baris berkas (header = 1).
func ParseRecipientsCSV(r io.Reader) ([]RecipientInput, []int, error) {
	br := bufio.NewReader(r)
	if b, _ := br.Peek(3); bytes.Equal(b, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = br.Discard(3)
	}
	head, _ := br.Peek(8192)
	line := head
	if i := bytes.IndexByte(head, '\n'); i >= 0 {
		line = head[:i]
	}
	comma := ','
	if bytes.Count(line, []byte(";")) > bytes.Count(line, []byte(",")) {
		comma = ';'
	}
	cr := csv.NewReader(br)
	cr.Comma, cr.FieldsPerRecord, cr.LazyQuotes, cr.TrimLeadingSpace = comma, -1, true, true
	header, err := cr.Read()
	if err != nil {
		return nil, nil, Validation(FieldError{Field: "file", Message: "CSV kosong atau tidak terbaca"})
	}
	phone, ref := -1, -1
	for i, h := range header {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "phone", "to", "nomor", "msisdn":
			if phone < 0 {
				phone = i
			}
		case "client_ref":
			ref = i
		}
	}
	if phone < 0 {
		return nil, nil, Validation(FieldError{Field: "file", Message: "kolom wajib 'phone' tidak ditemukan pada header"})
	}
	var recs []RecipientInput
	var rows []int
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, Validation(FieldError{Field: "file", Message: "CSV rusak: " + err.Error()})
		}
		ln, _ := cr.FieldPos(0)
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue // baris kosong
		}
		if len(recs) >= maxRecipients {
			return nil, nil, Validation(FieldError{Field: "file", Message: fmt.Sprintf("maksimal %d baris", maxRecipients)})
		}
		in := RecipientInput{Vars: map[string]string{}}
		if phone < len(rec) {
			in.To = strings.TrimSpace(rec[phone])
		}
		if ref >= 0 && ref < len(rec) {
			in.ClientRef = strings.TrimSpace(rec[ref])
		}
		for i, h := range header {
			name := strings.TrimSpace(h)
			if i == phone || i == ref || i >= len(rec) || !varName.MatchString(name) {
				continue
			}
			if v := strings.TrimSpace(rec[i]); v != "" {
				in.Vars[name] = v
			}
		}
		recs = append(recs, in)
		rows = append(rows, ln)
	}
	return recs, rows, nil
}

// ---------- preview ----------

type PreviewSample struct {
	To       string `json:"to"`
	Body     string `json:"body"`
	Encoding string `json:"encoding"`
	Parts    int    `json:"parts"`
}

type MissingVar struct {
	Name       string `json:"name"`
	Recipients int    `json:"recipients"`
}

type CampaignPreview struct {
	Recipients               int             `json:"recipients"`
	Samples                  []PreviewSample `json:"samples"`
	TotalParts               int             `json:"total_parts"`
	OnlineSims               int             `json:"online_sims"`
	EstimatedDurationSeconds *int            `json:"estimated_duration_seconds"`
	MissingVariables         []MissingVar    `json:"missing_variables"`
}

func (s *Service) missingVariables(ctx context.Context, id uuid.UUID, need []string) ([]MissingVar, error) {
	out := []MissingVar{}
	for _, n := range need {
		var cnt int
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM campaign_recipients WHERE campaign_id=$1 AND NOT (vars ? $2 AND btrim(vars->>$2) <> '')`, id, n).Scan(&cnt); err != nil {
			return nil, err
		}
		if cnt > 0 {
			out = append(out, MissingVar{Name: n, Recipients: cnt})
		}
	}
	return out, nil
}

func (s *Service) PreviewCampaign(ctx context.Context, id uuid.UUID, sample int) (CampaignPreview, error) {
	p := CampaignPreview{Samples: []PreviewSample{}, MissingVariables: []MissingVar{}}
	c, err := s.GetCampaign(ctx, id)
	if err != nil {
		return p, err
	}
	if sample < 1 || sample > 20 {
		sample = 3
	}
	text, err := s.campaignText(ctx, c)
	if err != nil {
		return p, err
	}
	p.Recipients = c.Recipients.Count
	p.MissingVariables, err = s.missingVariables(ctx, id, sms.Variables(text))
	if err != nil {
		return p, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT to_e164, vars FROM campaign_recipients WHERE campaign_id=$1 ORDER BY row_no LIMIT $2`, id, sample)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	sumParts := 0
	for rows.Next() {
		var to string
		var vars map[string]string
		if err := rows.Scan(&to, &vars); err != nil {
			return p, err
		}
		body, _ := sms.Render(text, vars)
		in := sms.Analyze(body)
		p.Samples = append(p.Samples, PreviewSample{To: to, Body: body, Encoding: string(in.Encoding), Parts: in.Parts})
		sumParts += in.Parts
	}
	if len(p.Samples) > 0 {
		p.TotalParts = int(math.Round(float64(sumParts) / float64(len(p.Samples)) * float64(p.Recipients)))
	}
	online, secs := s.estimate(ctx, c.PolicyID, c.SimIDs, p.Recipients)
	p.OnlineSims = online
	if secs > 0 && p.Recipients > 0 {
		d := int(math.Ceil(secs))
		p.EstimatedDurationSeconds = &d
	}
	return p, nil
}
