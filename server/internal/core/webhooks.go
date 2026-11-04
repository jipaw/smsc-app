package core

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Event yang dapat dilanggan. "webhook.test" hanya dikirim lewat endpoint uji.
var WebhookEvents = []string{"message.sent", "message.delivered", "message.failed", "campaign.completed", "campaign.paused", "device.offline"}

const webhookTestEvent = "webhook.test"

type Webhook struct {
	ID            uuid.UUID `json:"id"`
	URL           string    `json:"url"`
	Events        []string  `json:"events"`
	Enabled       bool      `json:"enabled"`
	Description   *string   `json:"description"`
	CreatedAt     time.Time `json:"created_at"`
	SigningSecret string    `json:"signing_secret,omitempty"` // hanya pada respons pembuatan/rotasi
	// rotasi secret: waktu terakhir, dan sampai kapan secret lama masih ikut menandatangani (null = tidak ada)
	SecretRotatedAt          *time.Time `json:"secret_rotated_at"`
	PreviousSecretValidUntil *time.Time `json:"previous_secret_valid_until"`
}

type WebhookDelivery struct {
	ID            uuid.UUID  `json:"id"`
	WebhookID     uuid.UUID  `json:"webhook_id"`
	Event         string     `json:"event"`
	Status        string     `json:"status"` // pending | delivered | failed
	Attempts      int        `json:"attempts"`
	LastStatus    *int       `json:"last_status"`
	LastError     *string    `json:"last_error"`
	NextAttemptAt *time.Time `json:"next_attempt_at"`
	DeliveredAt   *time.Time `json:"delivered_at"`
	FailedAt      *time.Time `json:"failed_at"`
	CreatedAt     time.Time  `json:"created_at"`
	// rotasi secret: waktu terakhir, dan sampai kapan secret lama masih ikut menandatangani (null = tidak ada)
	SecretRotatedAt          *time.Time `json:"secret_rotated_at"`
	PreviousSecretValidUntil *time.Time `json:"previous_secret_valid_until"`
}

// ---------- validasi URL + perlindungan SSRF ----------

// blockedIP: loopback (kecuali diizinkan), link-local (termasuk metadata cloud 169.254.169.254), unspecified, multicast.
// Jaringan privat (RFC 1918, 100.64/10 Tailscale) DIIZINKAN: itu target utama (sistem internal di tailnet).
func (s *Service) blockedIP(ip net.IP) bool {
	switch {
	case ip.IsLoopback():
		return !s.Cfg.WebhookAllowLoopback
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip.IsUnspecified(), ip.IsMulticast():
		return true
	}
	return false
}

func (s *Service) validateWebhookURL(raw string) *FieldError {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return &FieldError{Field: "url", Message: "harus URL http(s) yang valid"}
	}
	if u.User != nil {
		return &FieldError{Field: "url", Message: "tidak boleh memuat kredensial (user:pass@)"}
	}
	if len(raw) > 2000 {
		return &FieldError{Field: "url", Message: "terlalu panjang"}
	}
	host := u.Hostname()
	ips := []net.IP{}
	if ip := net.ParseIP(host); ip != nil {
		ips = append(ips, ip)
	} else {
		addrs, err := net.LookupIP(host)
		if err != nil || len(addrs) == 0 {
			return &FieldError{Field: "url", Message: "host tidak dapat di-resolve"}
		}
		ips = addrs
	}
	for _, ip := range ips {
		if s.blockedIP(ip) {
			return &FieldError{Field: "url", Message: "alamat tujuan tidak diizinkan (loopback/link-local/metadata)"}
		}
	}
	return nil
}

// webhookClient: tanpa proxy lingkungan, tanpa mengikuti redirect, dan memeriksa IP SAAT KONEKSI (menahan DNS rebinding).
func (s *Service) webhookClient() *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if ip := net.ParseIP(host); ip != nil && s.blockedIP(ip) {
			return fmt.Errorf("alamat %s diblokir (SSRF)", host)
		}
		return nil
	}}
	tr := &http.Transport{Proxy: nil, DialContext: d.DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: s.Cfg.WebhookTimeout, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second}
	return &http.Client{Transport: tr, Timeout: s.Cfg.WebhookTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (s *Service) validateWebhookEvents(ev []string) *FieldError {
	if len(ev) == 0 {
		return &FieldError{Field: "events", Message: "pilih minimal satu event"}
	}
	ok := map[string]bool{}
	for _, e := range WebhookEvents {
		ok[e] = true
	}
	for _, e := range ev {
		if !ok[e] {
			return &FieldError{Field: "events", Message: fmt.Sprintf("event %q tidak dikenal (tersedia: %s)", e, strings.Join(WebhookEvents, ", "))}
		}
	}
	return nil
}

// ---------- CRUD ----------

const whCols = `id,url,events,enabled,description,created_at,secret_rotated_at,CASE WHEN prev_valid_until > now() THEN prev_valid_until END`

func scanWebhook(r pgx.Row) (Webhook, error) {
	var w Webhook
	err := r.Scan(&w.ID, &w.URL, &w.Events, &w.Enabled, &w.Description, &w.CreatedAt, &w.SecretRotatedAt, &w.PreviousSecretValidUntil)
	if w.Events == nil {
		w.Events = []string{}
	}
	return w, err
}

func (s *Service) CreateWebhook(ctx context.Context, by uuid.UUID, rawURL string, events []string, desc string) (Webhook, error) {
	var errs []FieldError
	if fe := s.validateWebhookURL(rawURL); fe != nil {
		errs = append(errs, *fe)
	}
	if fe := s.validateWebhookEvents(events); fe != nil {
		errs = append(errs, *fe)
	}
	if len(errs) > 0 {
		return Webhook{}, Validation(errs...)
	}
	secret := "whsec_" + randToken(32)
	enc, err := s.encryptSecret(secret)
	if err != nil {
		return Webhook{}, err
	}
	w, err := scanWebhook(s.Pool.QueryRow(ctx, `INSERT INTO webhooks(id,url,events,signing_secret,description,created_by) VALUES ($1,$2,$3,$4,$5,$6) RETURNING `+whCols,
		newID(), rawURL, uniqueStrings(events), enc, nullStr(desc), by))
	if err != nil {
		return Webhook{}, err
	}
	w.SigningSecret = secret
	return w, nil
}

// RotateWebhookSecret menerbitkan secret baru (tampil sekali). Selama graceSeconds, setiap pengiriman ditandatangani
// dengan KEDUA secret (header berisi dua nilai) agar penerima bisa berganti tanpa kehilangan event.
func (s *Service) RotateWebhookSecret(ctx context.Context, id uuid.UUID, graceSeconds int) (Webhook, error) {
	if fe := graceOK(graceSeconds); fe != nil {
		return Webhook{}, Validation(*fe)
	}
	secret := "whsec_" + randToken(32)
	enc, err := s.encryptSecret(secret)
	if err != nil {
		return Webhook{}, err
	}
	w, err := scanWebhook(s.Pool.QueryRow(ctx, `UPDATE webhooks SET
		  prev_signing_secret = CASE WHEN $3 > 0 THEN signing_secret END,
		  prev_valid_until    = CASE WHEN $3 > 0 THEN now() + make_interval(secs => $3) END,
		  signing_secret=$2, secret_rotated_at=now(), updated_at=now()
		WHERE id=$1 RETURNING `+whCols, id, enc, graceSeconds))
	if errors.Is(err, pgx.ErrNoRows) {
		return Webhook{}, NotFound("webhook")
	}
	if err != nil {
		return Webhook{}, err
	}
	w.SigningSecret = secret
	return w, nil
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range in {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func (s *Service) ListWebhooks(ctx context.Context) ([]Webhook, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+whCols+` FROM webhooks ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Webhook, error) { return scanWebhook(r) })
	if out == nil {
		out = []Webhook{}
	}
	return out, err
}

func (s *Service) GetWebhook(ctx context.Context, id uuid.UUID) (Webhook, error) {
	w, err := scanWebhook(s.Pool.QueryRow(ctx, `SELECT `+whCols+` FROM webhooks WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return w, NotFound("webhook")
	}
	return w, err
}

func (s *Service) PatchWebhook(ctx context.Context, id uuid.UUID, f map[string]json.RawMessage) (Webhook, error) {
	cur, err := s.GetWebhook(ctx, id)
	if err != nil {
		return cur, err
	}
	var errs []FieldError
	if v, ok := f["url"]; ok {
		var u string
		if json.Unmarshal(v, &u) != nil {
			errs = append(errs, FieldError{Field: "url", Message: "harus string"})
		} else if fe := s.validateWebhookURL(u); fe != nil {
			errs = append(errs, *fe)
		} else {
			cur.URL = u
		}
	}
	if v, ok := f["events"]; ok {
		var ev []string
		if json.Unmarshal(v, &ev) != nil {
			errs = append(errs, FieldError{Field: "events", Message: "harus array string"})
		} else if fe := s.validateWebhookEvents(ev); fe != nil {
			errs = append(errs, *fe)
		} else {
			cur.Events = uniqueStrings(ev)
		}
	}
	if v, ok := f["enabled"]; ok {
		if json.Unmarshal(v, &cur.Enabled) != nil {
			errs = append(errs, FieldError{Field: "enabled", Message: "harus boolean"})
		}
	}
	if v, ok := f["description"]; ok {
		_ = json.Unmarshal(v, &cur.Description)
	}
	if len(errs) > 0 {
		return cur, Validation(errs...)
	}
	_, err = s.Pool.Exec(ctx, `UPDATE webhooks SET url=$2,events=$3,enabled=$4,description=$5,updated_at=now() WHERE id=$1`, id, cur.URL, cur.Events, cur.Enabled, cur.Description)
	return cur, err
}

func (s *Service) DeleteWebhook(ctx context.Context, id uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM webhooks WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return NotFound("webhook")
	}
	return err
}

func (s *Service) ListDeliveries(ctx context.Context, webhookID uuid.UUID, limit int, cursor string) ([]WebhookDelivery, string, error) {
	if _, err := s.GetWebhook(ctx, webhookID); err != nil {
		return nil, "", err
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	args := []any{webhookID, limit + 1}
	q := `SELECT id,webhook_id,event_type,attempts,last_status,last_error,next_attempt_at,delivered_at,failed_at,created_at FROM webhook_deliveries WHERE webhook_id=$1`
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
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (WebhookDelivery, error) {
		var d WebhookDelivery
		err := r.Scan(&d.ID, &d.WebhookID, &d.Event, &d.Attempts, &d.LastStatus, &d.LastError, &d.NextAttemptAt, &d.DeliveredAt, &d.FailedAt, &d.CreatedAt)
		switch {
		case d.DeliveredAt != nil:
			d.Status = "delivered"
		case d.FailedAt != nil:
			d.Status = "failed"
		default:
			d.Status = "pending"
		}
		return d, err
	})
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = encodeCursor(out[len(out)-1].CreatedAt, out[len(out)-1].ID)
	}
	if out == nil {
		out = []WebhookDelivery{}
	}
	return out, next, err
}

// TestWebhook membuat pengiriman uji (tanpa memedulikan langganan event).
func (s *Service) TestWebhook(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	if _, err := s.GetWebhook(ctx, id); err != nil {
		return uuid.Nil, err
	}
	did := newID()
	_, err := s.Pool.Exec(ctx, `INSERT INTO webhook_deliveries(id,webhook_id,event_type,payload,next_attempt_at) VALUES ($1,$2,$3,$4,now())`,
		did, id, webhookTestEvent, []byte(`{"message":"uji webhook SMSC"}`))
	return did, err
}

func (s *Service) RetryDelivery(ctx context.Context, webhookID, deliveryID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE webhook_deliveries SET failed_at=NULL, attempts=0, last_error=NULL, next_attempt_at=now() WHERE id=$1 AND webhook_id=$2 AND failed_at IS NOT NULL`, deliveryID, webhookID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var one int
		if s.Pool.QueryRow(ctx, `SELECT 1 FROM webhook_deliveries WHERE id=$1 AND webhook_id=$2`, deliveryID, webhookID).Scan(&one) != nil {
			return problem(http.StatusNotFound, "resource.not_found", "Tidak ditemukan", "pengiriman webhook tidak ditemukan")
		}
		return problem(http.StatusConflict, "webhook.not_retryable", "Tidak dapat diulang", "hanya pengiriman berstatus failed yang dapat dikirim ulang")
	}
	return nil
}

// ---------- outbox ----------

// emitWebhook menulis pengiriman untuk semua webhook aktif yang berlangganan. Dipanggil DI DALAM transaksi perubahan
// state (outbox): event tidak pernah hilang bila proses mati setelah commit, dan tidak pernah terkirim bila transaksi batal.
func (s *Service) emitWebhook(ctx context.Context, db dbx, event string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO webhook_deliveries(id, webhook_id, event_type, payload, next_attempt_at)
		SELECT gen_random_uuid(), w.id, $1, $2::jsonb, now() FROM webhooks w WHERE w.enabled AND $1 = ANY(w.events)`, event, raw)
	return err
}

func messageWebhookData(m Message) map[string]any {
	return map[string]any{
		"message_id": m.ID, "client_ref": m.ClientRef, "to": m.To, "status": m.Status, "campaign_id": m.CampaignID,
		"sim_id": m.SimID, "device_id": m.DeviceID, "attempt": m.Attempt, "error_code": m.ErrorCode,
		"created_at": m.CreatedAt, "sent_at": m.SentAt, "delivered_at": m.DeliveredAt,
		"partial": m.Partial, "parts_sent": m.PartsSent, "parts_total": m.Parts,
	}
}

// ---------- worker ----------

func (s *Service) RunWebhooks(ctx context.Context) {
	client := s.webhookClient()
	t := time.NewTicker(s.Cfg.WebhookTick)
	defer t.Stop()
	sem := make(chan struct{}, s.Cfg.WebhookWorkers)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, d := range s.claimDeliveries(ctx, 50) {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			wg.Add(1)
			go func(d claimedDelivery) {
				defer wg.Done()
				defer func() { <-sem }()
				s.deliver(ctx, client, d)
			}(d)
		}
	}
}

type claimedDelivery struct {
	id, webhookID uuid.UUID
	event         string
	payload       json.RawMessage
	attempts      int
	createdAt     time.Time
}

// claimDeliveries memberi lease 2 menit pada pengiriman yang jatuh tempo (aman untuk beberapa pekerja; proses mati = diulang).
func (s *Service) claimDeliveries(ctx context.Context, n int) []claimedDelivery {
	rows, err := s.Pool.Query(ctx, `UPDATE webhook_deliveries d SET next_attempt_at = now() + interval '2 minutes'
		WHERE d.id IN (SELECT d2.id FROM webhook_deliveries d2 JOIN webhooks w ON w.id=d2.webhook_id
			WHERE d2.delivered_at IS NULL AND d2.failed_at IS NULL AND d2.next_attempt_at <= now() AND w.enabled
			ORDER BY d2.next_attempt_at LIMIT $1 FOR UPDATE OF d2 SKIP LOCKED)
		RETURNING d.id, d.webhook_id, d.event_type, d.payload, d.attempts, d.created_at`, n)
	if err != nil {
		s.Log.Error("webhook: claim", "err", err)
		return nil
	}
	defer rows.Close()
	var out []claimedDelivery
	for rows.Next() {
		var c claimedDelivery
		if rows.Scan(&c.id, &c.webhookID, &c.event, &c.payload, &c.attempts, &c.createdAt) == nil {
			out = append(out, c)
		}
	}
	return out
}

// Sign: HMAC-SHA256(secret, timestamp + "." + body) heksadesimal, dengan prefiks "sha256=".
func Sign(secret, timestamp string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(timestamp + "." + string(body)))
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func (s *Service) deliver(ctx context.Context, client *http.Client, d claimedDelivery) {
	var target string
	var enc, encPrev []byte
	if err := s.Pool.QueryRow(ctx, `SELECT url, signing_secret, CASE WHEN prev_valid_until > now() THEN prev_signing_secret END FROM webhooks WHERE id=$1`, d.webhookID).Scan(&target, &enc, &encPrev); err != nil {
		return
	}
	secret, err := s.decryptSecret(enc)
	if err != nil {
		s.finishDelivery(ctx, d, 0, "secret tidak dapat didekripsi (WEBHOOK_ENC_KEY berubah?)", 0)
		return
	}
	body, _ := json.Marshal(map[string]any{"event": d.event, "delivery_id": d.id, "at": d.createdAt.UTC().Format(time.RFC3339Nano), "data": json.RawMessage(d.payload)})
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		s.finishDelivery(ctx, d, 0, err.Error(), 0)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SMSC-Webhook/1")
	req.Header.Set("X-SMSC-Event", d.event)
	req.Header.Set("X-SMSC-Delivery", d.id.String())
	req.Header.Set("X-SMSC-Timestamp", ts)
	sig := Sign(secret, ts, body)
	if encPrev != nil { // masa rotasi: tanda tangan ganda "sha256=<baru>,sha256=<lama>"; penerima menerima bila salah satu cocok
		if old, err := s.decryptSecret(encPrev); err == nil {
			sig += "," + Sign(old, ts, body)
		}
	}
	req.Header.Set("X-SMSC-Signature", sig)
	start := time.Now()
	resp, err := client.Do(req)
	dur := time.Since(start)
	if err != nil {
		s.finishDelivery(ctx, d, 0, truncate(err.Error(), 300), dur)
		return
	}
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)
	resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		s.finishDelivery(ctx, d, resp.StatusCode, "", dur)
		return
	}
	s.finishDelivery(ctx, d, resp.StatusCode, fmt.Sprintf("HTTP %d", resp.StatusCode), dur)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// finishDelivery mencatat hasil satu percobaan: 2xx = berhasil; selain itu jadwalkan ulang menurut Cfg.WebhookBackoff
// atau tandai gagal final setelah retry habis.
func (s *Service) finishDelivery(ctx context.Context, d claimedDelivery, status int, errText string, dur time.Duration) {
	attempts := d.attempts + 1
	s.M.WebhookDuration.Observe(dur.Seconds())
	var st *int
	if status > 0 {
		st = &status
	}
	switch {
	case errText == "" && status >= 200 && status < 300:
		s.M.WebhookDeliveries.WithLabelValues("delivered").Inc()
		_, _ = s.Pool.Exec(ctx, `UPDATE webhook_deliveries SET delivered_at=now(), attempts=$2, last_status=$3, last_error=NULL, last_attempt_at=now(), next_attempt_at=NULL WHERE id=$1`, d.id, attempts, st)
	case attempts > len(s.Cfg.WebhookBackoff):
		s.M.WebhookDeliveries.WithLabelValues("failed").Inc()
		s.Log.Warn("webhook gagal final", "delivery", d.id, "webhook", d.webhookID, "attempts", attempts, "err", errText)
		_, _ = s.Pool.Exec(ctx, `UPDATE webhook_deliveries SET failed_at=now(), attempts=$2, last_status=$3, last_error=$4, last_attempt_at=now(), next_attempt_at=NULL WHERE id=$1`, d.id, attempts, st, errText)
	default:
		s.M.WebhookDeliveries.WithLabelValues("retry").Inc()
		wait := s.Cfg.WebhookBackoff[attempts-1]
		_, _ = s.Pool.Exec(ctx, `UPDATE webhook_deliveries SET attempts=$2, last_status=$3, last_error=$4, last_attempt_at=now(), next_attempt_at = now() + ($5::bigint * interval '1 millisecond') WHERE id=$1`,
			d.id, attempts, st, errText, wait.Milliseconds())
	}
}
