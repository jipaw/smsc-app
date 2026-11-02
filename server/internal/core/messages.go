package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jipaw/smsc-app/server/internal/proto"
	"github.com/jipaw/smsc-app/server/internal/sms"
)

func newEnvelopeCancel(id uuid.UUID) (proto.Envelope, error) {
	return proto.New(proto.TypeSmsCancel, proto.SmsCancel{MessageID: id.String()}, nil)
}

func ackAccepted(env proto.Envelope) bool {
	var a proto.CommandAck
	if json.Unmarshal(env.Payload, &a) != nil {
		return false
	}
	return a.Accepted
}

type Status string

const (
	StatusQueued    Status = "queued"
	StatusAssigned  Status = "assigned"
	StatusSending   Status = "sending"
	StatusSent      Status = "sent"
	StatusDelivered Status = "delivered"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusExpired   Status = "expired"
	StatusUnknown   Status = "unknown"
)

// Graf transisi valid (docs/03 §3 + V4: sending->delivered, unknown->*).
var allowed = map[Status][]Status{
	StatusQueued:    {StatusAssigned, StatusCancelled, StatusExpired},
	StatusAssigned:  {StatusQueued, StatusSending, StatusCancelled, StatusExpired, StatusFailed},
	StatusSending:   {StatusSent, StatusFailed, StatusQueued, StatusUnknown, StatusDelivered},
	StatusUnknown:   {StatusSent, StatusFailed, StatusQueued, StatusDelivered},
	StatusSent:      {StatusDelivered, StatusFailed},
	StatusFailed:    {StatusQueued},
	StatusExpired:   {StatusQueued},
	StatusDelivered: {},
	StatusCancelled: {},
}

func canTransition(from, to Status) bool {
	for _, t := range allowed[from] {
		if t == to {
			return true
		}
	}
	return false
}

type Message struct {
	ID          uuid.UUID  `json:"id"`
	Source      string     `json:"source"`
	CampaignID  *uuid.UUID `json:"campaign_id"`
	ClientRef   *string    `json:"client_ref"`
	To          string     `json:"to"`
	Body        *string    `json:"body"`
	Encoding    string     `json:"encoding"`
	Parts       int        `json:"parts"`
	Status      Status     `json:"status"`
	Attempt     int        `json:"attempt"`
	MaxAttempts int        `json:"max_attempts"`
	ErrorCode   *string    `json:"error_code"`
	ErrorDetail *string    `json:"error_detail"`
	Priority    int        `json:"priority"`
	DeviceID    *uuid.UUID `json:"device_id"`
	SimID       *uuid.UUID `json:"sim_id"`
	CreatedAt   time.Time  `json:"created_at"`
	NotBefore   time.Time  `json:"not_before"`
	ExpiresAt   time.Time  `json:"expires_at"`
	AssignedAt  *time.Time `json:"assigned_at"`
	SentAt      *time.Time `json:"sent_at"`
	DeliveredAt *time.Time `json:"delivered_at"`
	FinishedAt  *time.Time `json:"finished_at"`

	policyID *uuid.UUID
}

const msgCols = `id,source,campaign_id,client_ref,to_e164,body,encoding,parts,status,attempt,max_attempts,error_code,error_detail,priority,
	device_id,sim_id,created_at,not_before,expires_at,assigned_at,sent_at,delivered_at,finished_at,policy_id`

func scanMsg(r pgx.Row) (Message, error) {
	var m Message
	err := r.Scan(&m.ID, &m.Source, &m.CampaignID, &m.ClientRef, &m.To, &m.Body, &m.Encoding, &m.Parts, &m.Status, &m.Attempt, &m.MaxAttempts,
		&m.ErrorCode, &m.ErrorDetail, &m.Priority, &m.DeviceID, &m.SimID, &m.CreatedAt, &m.NotBefore, &m.ExpiresAt, &m.AssignedAt, &m.SentAt,
		&m.DeliveredAt, &m.FinishedAt, &m.policyID)
	return m, err
}

type MessageEvent struct {
	ID        uuid.UUID      `json:"id"`
	Attempt   int            `json:"attempt"`
	Type      string         `json:"type"`
	At        time.Time      `json:"at"`
	Actor     string         `json:"actor"`
	SimID     *uuid.UUID     `json:"sim_id"`
	ErrorCode *string        `json:"error_code"`
	Data      map[string]any `json:"data"`
}

// ---------- pembuatan ----------

type RecipientInput struct {
	To        string            `json:"to"`
	Vars      map[string]string `json:"vars"`
	ClientRef string            `json:"client_ref"`
}

type MessageCreate struct {
	Recipients []RecipientInput `json:"recipients"`
	Body       string           `json:"body"`
	TemplateID *uuid.UUID       `json:"template_id"`
	SimIDs     []uuid.UUID      `json:"sim_ids"`
	DeviceIDs  []uuid.UUID      `json:"device_ids"`
	PolicyID   *uuid.UUID       `json:"policy_id"`
	Priority   *int             `json:"priority"`
	NotBefore  *time.Time       `json:"not_before"`
	TTLSeconds *int             `json:"ttl_seconds"`
}

type Rejected struct {
	Index  int    `json:"index"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

type CreateResult struct {
	Messages []Message  `json:"messages"`
	Rejected []Rejected `json:"rejected"`
}

// CreateMessages: FR-06/FR-07. owner = user atau API key; source = dashboard|api.
func (s *Service) CreateMessages(ctx context.Context, owner uuid.UUID, source, idemKey string, req MessageCreate) (CreateResult, error) {
	res := CreateResult{Messages: []Message{}, Rejected: []Rejected{}}
	var errs []FieldError
	if len(req.Recipients) < 1 || len(req.Recipients) > 1000 {
		errs = append(errs, FieldError{"recipients", "harus 1..1000"})
	}
	body := req.Body
	if (body == "") == (req.TemplateID == nil) {
		errs = append(errs, FieldError{"body", "isi salah satu: body atau template_id"})
	}
	if req.TemplateID != nil && body == "" {
		if err := s.Pool.QueryRow(ctx, `SELECT body FROM templates WHERE id=$1`, *req.TemplateID).Scan(&body); err != nil {
			errs = append(errs, FieldError{"template_id", "template tidak ditemukan"})
		}
	}
	if len([]rune(body)) > 1600 {
		errs = append(errs, FieldError{"body", "maksimal 1600 karakter"})
	}
	prio := 5
	if req.Priority != nil {
		if *req.Priority < 0 || *req.Priority > 9 {
			errs = append(errs, FieldError{"priority", "0..9"})
		}
		prio = *req.Priority
	}
	if len(errs) > 0 {
		return res, Validation(errs...)
	}
	eff, err := s.Effective(ctx, s.Pool, nil, nil, req.PolicyID)
	if err != nil {
		return res, err
	}
	ttl := eff.MessageTTLSeconds
	if req.TTLSeconds != nil {
		ttl = *req.TTLSeconds
	}
	notBefore := time.Now()
	if req.NotBefore != nil {
		notBefore = *req.NotBefore
	}
	expires := notBefore.Add(time.Duration(ttl) * time.Second)

	// normalisasi + suppression
	type prepared struct {
		idx  int
		e164 string
		body string
		info sms.Info
		ref  string
	}
	var prep []prepared
	e164s := []string{}
	for i, r := range req.Recipients {
		n, err := sms.Normalize(r.To, s.Cfg.DefaultRegion)
		if err != nil {
			res.Rejected = append(res.Rejected, Rejected{i, r.To, "invalid_number"})
			continue
		}
		text, missing := sms.Render(body, r.Vars)
		if len(missing) > 0 {
			res.Rejected = append(res.Rejected, Rejected{i, r.To, "missing_variable"})
			continue
		}
		prep = append(prep, prepared{i, n, text, sms.Analyze(text), r.ClientRef})
		e164s = append(e164s, n)
	}
	supp := map[string]bool{}
	if len(e164s) > 0 {
		rows, err := s.Pool.Query(ctx, `SELECT number_e164 FROM suppressions WHERE number_e164 = ANY($1)`, e164s)
		if err != nil {
			return res, err
		}
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			supp[n] = true
		}
		rows.Close()
	}
	idem := nullStr(idemKey)
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		for _, p := range prep {
			if supp[p.e164] {
				res.Rejected = append(res.Rejected, Rejected{p.idx, p.e164, "suppressed"})
				continue
			}
			id := newID()
			var ref *string
			if source == "api" && p.ref != "" {
				ref = &p.ref
			}
			sub, err := tx.Begin(ctx) // savepoint: pelanggaran unik tidak boleh membatalkan seluruh batch
			if err != nil {
				return err
			}
			_, err = sub.Exec(ctx, `INSERT INTO messages(id,source,client_ref,idempotency_key,owner_id,to_e164,body,encoding,parts,max_attempts,priority,
				route_sim_ids,route_device_ids,policy_id,not_before,expires_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
				id, source, ref, idem, owner, p.e164, p.body, p.info.Encoding, p.info.Parts, eff.MaxAttempts, prio,
				nonNilUUIDs(req.SimIDs), nonNilUUIDs(req.DeviceIDs), req.PolicyID, notBefore, expires)
			if err != nil {
				_ = sub.Rollback(ctx)
				switch {
				case isUnique(err, "messages_idem_uq"):
					// idempoten: kembalikan pesan yang sudah ada; isi berbeda = konflik
					m, e := scanMsg(tx.QueryRow(ctx, `SELECT `+msgCols+` FROM messages WHERE owner_id=$1 AND idempotency_key=$2 AND to_e164=$3`, owner, idemKey, p.e164))
					if e != nil {
						return e
					}
					if m.Body != nil && *m.Body != p.body {
						return NewProblem(http.StatusConflict, "idempotency.conflict", "Konflik idempotensi", "Idempotency-Key sama dengan isi berbeda")
					}
					res.Messages = append(res.Messages, m)
					continue
				case isUnique(err, "messages_api_ref_uq"):
					res.Rejected = append(res.Rejected, Rejected{p.idx, p.e164, "duplicate_client_ref"})
					continue
				}
				return err
			}
			if err := sub.Commit(ctx); err != nil {
				return err
			}
			if err := insertEvent(ctx, tx, id, 0, "created", source, nil, nil, nil, nil); err != nil {
				return err
			}
			if err := insertEvent(ctx, tx, id, 0, "queued", "system", nil, nil, nil, nil); err != nil {
				return err
			}
			m, err := scanMsg(tx.QueryRow(ctx, `SELECT `+msgCols+` FROM messages WHERE id=$1`, id))
			if err != nil {
				return err
			}
			res.Messages = append(res.Messages, m)
		}
		return nil
	})
	if err != nil {
		return CreateResult{Messages: []Message{}, Rejected: res.Rejected}, err
	}
	if len(res.Messages) == 0 && len(res.Rejected) > 0 && len(req.Recipients) == len(res.Rejected) {
		return res, Validation(FieldError{"recipients", "semua penerima ditolak"})
	}
	for _, m := range res.Messages {
		s.publishMessage(m)
	}
	s.Wake()
	return res, nil
}

func nonNilUUIDs(v []uuid.UUID) []uuid.UUID {
	if v == nil {
		return []uuid.UUID{}
	}
	return v
}

func (s *Service) publishMessage(m Message) {
	s.Bus.Publish("message.updated", map[string]any{"id": m.ID, "status": m.Status, "error_code": m.ErrorCode, "campaign_id": m.CampaignID,
		"sim_id": m.SimID, "device_id": m.DeviceID, "attempt": m.Attempt, "at": time.Now().UTC()})
}

func insertEvent(ctx context.Context, tx pgx.Tx, msgID uuid.UUID, attempt int, typ, actor string, simID *uuid.UUID, errCode *string, data map[string]any, devEventID *uuid.UUID) error {
	if data == nil {
		data = map[string]any{}
	}
	kind := actorKind(actor)
	if actor != kind { // simpan identitas lengkap (mis. user:email) tanpa melanggar CHECK kolom actor
		data["by"] = actor
	}
	actor = kind
	_, err := tx.Exec(ctx, `INSERT INTO message_events(id,message_id,attempt,type,actor,sim_id,error_code,data,device_event_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		newID(), msgID, attempt, typ, actor, simID, errCode, data, devEventID)
	return err
}

// ---------- transisi (satu pintu) ----------

type TransitionOpts struct {
	To            Status
	EventType     string
	Actor         string
	ErrorCode     *string
	ErrorDetail   *string
	Data          map[string]any
	DeviceEventID *uuid.UUID
	NotBefore     *time.Time // untuk requeue
	NewAttempt    *int       // untuk requeue setelah gagal
	SimID         *uuid.UUID
	DeviceID      *uuid.UUID
	LeaseUntil    *time.Time
	SkipLane      bool // jangan terapkan jeda lane (mis. ack_timeout, revoke)
}

// transition memvalidasi graf, memperbarui kolom, dan menulis message_events dalam transaksi pemanggil.
// Pemanggil mem-publish setelah commit (publishMessage).
func (s *Service) transition(ctx context.Context, tx pgx.Tx, id uuid.UUID, o TransitionOpts) (Message, error) {
	cur, err := scanMsg(tx.QueryRow(ctx, `SELECT `+msgCols+` FROM messages WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return cur, NotFound("pesan")
	}
	if err != nil {
		return cur, err
	}
	if !canTransition(cur.Status, o.To) {
		return cur, NewProblem(http.StatusConflict, "message.invalid_transition", "Transisi tidak valid",
			fmt.Sprintf("pesan berstatus %q tidak dapat menjadi %q", cur.Status, o.To))
	}
	attempt := cur.Attempt
	if o.NewAttempt != nil {
		attempt = *o.NewAttempt
	}
	var q string
	switch o.To {
	case StatusQueued:
		q = `UPDATE messages SET status='queued', attempt=$2, not_before=COALESCE($3,not_before), lease_expires_at=NULL, error_code=$4, error_detail=$5,
			finished_at=NULL WHERE id=$1`
		_, err = tx.Exec(ctx, q, id, attempt, o.NotBefore, o.ErrorCode, o.ErrorDetail)
	case StatusSending:
		_, err = tx.Exec(ctx, `UPDATE messages SET status='sending', lease_expires_at=NULL WHERE id=$1`, id)
	case StatusSent:
		_, err = tx.Exec(ctx, `UPDATE messages SET status='sent', sent_at=COALESCE(sent_at,now()), lease_expires_at=NULL, error_code=NULL, error_detail=NULL WHERE id=$1`, id)
	case StatusDelivered:
		_, err = tx.Exec(ctx, `UPDATE messages SET status='delivered', sent_at=COALESCE(sent_at,now()), delivered_at=now(), finished_at=now(), lease_expires_at=NULL, error_code=NULL WHERE id=$1`, id)
	case StatusUnknown:
		_, err = tx.Exec(ctx, `UPDATE messages SET status='unknown', error_code=$2, lease_expires_at=NULL WHERE id=$1`, id, o.ErrorCode)
	default: // failed, cancelled, expired
		_, err = tx.Exec(ctx, `UPDATE messages SET status=$2, error_code=$3, error_detail=$4, finished_at=now(), lease_expires_at=NULL WHERE id=$1`, id, string(o.To), o.ErrorCode, o.ErrorDetail)
	}
	if err != nil {
		return cur, err
	}
	simID := cur.SimID
	if o.SimID != nil {
		simID = o.SimID
	}
	if err := insertEvent(ctx, tx, id, attempt, o.EventType, o.Actor, simID, o.ErrorCode, o.Data, o.DeviceEventID); err != nil {
		return cur, err
	}
	// jeda lane: slot SIM dilepas saat pesan keluar dari assigned/sending (ADR-04)
	if !o.SkipLane && (cur.Status == StatusAssigned || cur.Status == StatusSending) && cur.SimID != nil &&
		(o.To == StatusSent || o.To == StatusDelivered || o.To == StatusFailed || o.To == StatusUnknown || o.To == StatusQueued) {
		if err := s.closeLane(ctx, tx, *cur.SimID, cur.policyID); err != nil {
			return cur, err
		}
	}
	return scanMsg(tx.QueryRow(ctx, `SELECT `+msgCols+` FROM messages WHERE id=$1`, id))
}

// ---------- baca / daftar ----------

type MessageFilter struct {
	Statuses    []string
	Source      string
	CampaignID  *uuid.UUID
	DeviceID    *uuid.UUID
	SimID       *uuid.UUID
	To          string
	ClientRef   string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
}

func encodeCursor(t time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(c string) (time.Time, uuid.UUID, error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, errors.New("cursor tidak valid")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return t, uuid.Nil, err
	}
	id, err := uuid.Parse(parts[1])
	return t, id, err
}

func (f MessageFilter) where(start int) (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.Replace(cond, "?", fmt.Sprintf("$%d", start+len(args)-1), 1))
	}
	if len(f.Statuses) > 0 {
		add("status::text = ANY(?)", f.Statuses)
	}
	if f.Source != "" {
		add("source::text = ?", f.Source)
	}
	if f.CampaignID != nil {
		add("campaign_id = ?", *f.CampaignID)
	}
	if f.DeviceID != nil {
		add("device_id = ?", *f.DeviceID)
	}
	if f.SimID != nil {
		add("sim_id = ?", *f.SimID)
	}
	if f.To != "" {
		add("to_e164 LIKE ?", strings.ReplaceAll(strings.ReplaceAll(f.To, "%", ""), "_", "")+"%")
	}
	if f.ClientRef != "" {
		add("client_ref = ?", f.ClientRef)
	}
	if f.CreatedFrom != nil {
		add("created_at >= ?", *f.CreatedFrom)
	}
	if f.CreatedTo != nil {
		add("created_at < ?", *f.CreatedTo)
	}
	if len(conds) == 0 {
		return "TRUE", nil
	}
	return strings.Join(conds, " AND "), args
}

func (s *Service) ListMessages(ctx context.Context, f MessageFilter, limit int, cursor string) ([]Message, string, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	w, args := f.where(1)
	q := `SELECT ` + msgCols + ` FROM messages WHERE ` + w
	if cursor != "" {
		t, id, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", Validation(FieldError{"cursor", "tidak valid"})
		}
		args = append(args, t, id)
		q += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", len(args)-1, len(args))
	}
	args = append(args, limit+1)
	q += fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d", len(args))
	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m, err := scanMsg(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	return out, next, nil
}

func (s *Service) GetMessage(ctx context.Context, id uuid.UUID) (Message, []MessageEvent, error) {
	m, err := scanMsg(s.Pool.QueryRow(ctx, `SELECT `+msgCols+` FROM messages WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return m, nil, NotFound("pesan")
	}
	if err != nil {
		return m, nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,attempt,type,at,actor,sim_id,error_code,data FROM message_events WHERE message_id=$1 ORDER BY at, id`, id)
	if err != nil {
		return m, nil, err
	}
	defer rows.Close()
	evs := []MessageEvent{}
	for rows.Next() {
		var e MessageEvent
		var raw []byte
		if err := rows.Scan(&e.ID, &e.Attempt, &e.Type, &e.At, &e.Actor, &e.SimID, &e.ErrorCode, &raw); err != nil {
			return m, nil, err
		}
		_ = json.Unmarshal(raw, &e.Data)
		evs = append(evs, e)
	}
	return m, evs, rows.Err()
}

// ---------- cancel / retry ----------

func (s *Service) CancelMessage(ctx context.Context, id uuid.UUID, actor string) (Message, error) {
	var m Message
	var dev *uuid.UUID
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := scanMsg(tx.QueryRow(ctx, `SELECT `+msgCols+` FROM messages WHERE id=$1 FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return NotFound("pesan")
		}
		if err != nil {
			return err
		}
		if cur.Status != StatusQueued && cur.Status != StatusAssigned {
			return NewProblem(http.StatusConflict, "message.invalid_transition", "Tidak dapat dibatalkan", fmt.Sprintf("pesan berstatus %q", cur.Status))
		}
		dev = cur.DeviceID
		if cur.Status == StatusAssigned && dev != nil && s.link != nil && s.link.Connected(*dev) {
			// minta device menghentikan; jika sudah sending, tolak (409)
			env, _ := newEnvelopeCancel(id)
			rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			resp, rerr := s.link.Request(rctx, *dev, env, 3*time.Second)
			if rerr == nil && !ackAccepted(resp) {
				return NewProblem(http.StatusConflict, "message.invalid_transition", "Tidak dapat dibatalkan", "device sudah mulai mengirim")
			}
		}
		m, err = s.transition(ctx, tx, id, TransitionOpts{To: StatusCancelled, EventType: "cancelled", Actor: actor})
		return err
	})
	if err == nil {
		s.publishMessage(m)
	}
	return m, err
}

type RetryRequest struct {
	ConfirmDuplicateRisk bool        `json:"confirm_duplicate_risk"`
	SimIDs               []uuid.UUID `json:"sim_ids"`
}

func (s *Service) RetryMessage(ctx context.Context, id uuid.UUID, actor string, r RetryRequest) (Message, error) {
	var m Message
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := scanMsg(tx.QueryRow(ctx, `SELECT `+msgCols+` FROM messages WHERE id=$1 FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return NotFound("pesan")
		}
		if err != nil {
			return err
		}
		if cur.Status != StatusFailed && cur.Status != StatusExpired && cur.Status != StatusUnknown {
			return NewProblem(http.StatusConflict, "message.invalid_transition", "Tidak dapat di-retry", fmt.Sprintf("pesan berstatus %q", cur.Status))
		}
		if cur.Status == StatusUnknown && !r.ConfirmDuplicateRisk {
			return Validation(FieldError{"confirm_duplicate_risk", "wajib true untuk pesan berstatus unknown (risiko SMS ganda)"})
		}
		next := cur.Attempt + 1
		nb := time.Now()
		if _, err = tx.Exec(ctx, `UPDATE messages SET expires_at = GREATEST(expires_at, now() + interval '1 hour'), max_attempts = GREATEST(max_attempts, $2),
			route_sim_ids = CASE WHEN cardinality($3::uuid[])>0 THEN $3 ELSE route_sim_ids END WHERE id=$1`, id, next, nonNilUUIDs(r.SimIDs)); err != nil {
			return err
		}
		m, err = s.transition(ctx, tx, id, TransitionOpts{To: StatusQueued, EventType: "manual_retry", Actor: actor, NewAttempt: &next, NotBefore: &nb})
		return err
	})
	if err == nil {
		s.publishMessage(m)
		s.Wake()
	}
	return m, err
}

// actorKind memetakan pelaku ke salah satu nilai CHECK message_events.actor.
func actorKind(a string) string {
	switch {
	case a == "dashboard" || strings.HasPrefix(a, "user"):
		return "user"
	case a == "api" || strings.HasPrefix(a, "apikey"):
		return "api"
	case a == "device":
		return "device"
	default:
		return "system"
	}
}
