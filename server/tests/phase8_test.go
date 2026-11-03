package tests

import (
	"context"
	"io"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jipaw/smsc-app/server/internal/config"
	"github.com/jipaw/smsc-app/server/internal/mailer"
	"github.com/jipaw/smsc-app/server/internal/mailer/mailertest"
)

func withMail(s *mailertest.Server, mode string) func(*config.Config) {
	return func(c *config.Config) {
		c.Mail = mailer.Config{Host: s.Host, Port: s.Port, From: "SMSC Gateway <smsc@contoh.id>", Mode: mode, Timeout: 3 * time.Second, RootCAs: s.Pool}
	}
}

func mailBody(t *testing.T, raw string) string {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(quotedprintable.NewReader(m.Body))
	return string(b)
}

func (c *client) inviteMail(email, role string) (int, invResp) {
	var v invResp
	var raw map[string]any
	st := c.do("POST", "/api/v1/invitations", map[string]any{"email": email, "role": role, "send_email": true}, &raw)
	if st == 201 {
		v.ID, _ = raw["id"].(string)
		v.Token, _ = raw["token"].(string)
		v.InviteURL, _ = raw["invite_url"].(string)
		v.Status, _ = raw["status"].(string)
		if es, ok := raw["email_status"].(string); ok {
			v.Role = es // pinjam kolom: status email dari respons pembuatan
		}
	}
	return st, v
}

type invMail struct {
	ID          string `json:"id"`
	EmailStatus string `json:"email_status"`
	EmailError  string `json:"email_error"`
}

func (c *client) emailState(id string) invMail {
	var l struct{ Items []invMail }
	c.do("GET", "/api/v1/invitations", nil, &l)
	for _, it := range l.Items {
		if it.ID == id {
			return it
		}
	}
	return invMail{}
}

func (e *env) outbox(invID string) (status string, attempts int, bodyNull bool, lastErr string) {
	var body []byte
	var le *string
	_ = e.pool.QueryRow(context.Background(), `SELECT status, attempts, body, last_error FROM email_outbox WHERE invitation_id=$1 ORDER BY created_at DESC LIMIT 1`, invID).Scan(&status, &attempts, &body, &le)
	if le != nil {
		lastErr = *le
	}
	return status, attempts, body == nil, lastErr
}

func TestInvitationEmailNotConfigured(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	if st := adm.c.do("POST", "/api/v1/invitations", map[string]any{"email": "a@example.com", "role": "viewer", "send_email": true}, nil); st != 409 {
		t.Fatalf("send_email tanpa SMTP harus 409: %d", st)
	}
	if st, ty := problemType(adm.c, "POST", "/api/v1/invitations", map[string]any{"email": "a@example.com", "role": "viewer", "send_email": true}); st != 409 || ty != "email.not_configured" {
		t.Fatalf("tipe: %d %s", st, ty)
	}
	if st, _ := adm.c.invite("a@example.com", "viewer", 0); st != 201 { // tanpa email tetap bekerja, dan penolakan tadi tidak membuat apa pun
		t.Fatalf("undangan tanpa email: %d", st)
	}
	var cfg struct {
		Configured bool `json:"configured"`
	}
	if st := adm.c.do("GET", "/api/v1/settings/email", nil, &cfg); st != 200 || cfg.Configured {
		t.Fatalf("config: %d %+v", st, cfg)
	}
	if st, ty := problemType(adm.c, "POST", "/api/v1/settings/email/test", map[string]string{"to": "x@example.com"}); st != 409 || ty != "email.not_configured" {
		t.Fatalf("test tanpa SMTP: %d %s", st, ty)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM email_outbox`).Scan(&n)
	if n != 0 {
		t.Fatalf("outbox harus kosong: %d", n)
	}
}

func TestInvitationEmailDeliveredOverEachMode(t *testing.T) {
	for _, mode := range []string{"none", "starttls", "tls"} {
		t.Run(mode, func(t *testing.T) {
			srv := mailertest.Start(mailertest.Options{Implicit: mode == "tls", StartTLS: mode == "starttls"})
			defer srv.Close()
			e := newEnvOpts(t, withMail(srv, mode))
			adm := e.newUser("admin")
			st, v := adm.c.inviteMail("Penerima@Contoh.Id", "operator")
			if st != 201 || v.Role != "queued" || !strings.HasPrefix(v.InviteURL, "http://example.test/invite/inv_") {
				t.Fatalf("buat: %d %+v", st, v)
			}
			waitFor(t, 8*time.Second, "email terkirim", func() bool { return len(srv.Messages()) == 1 })
			m := srv.Messages()[0]
			body := mailBody(t, m.Data)
			if m.To != "penerima@contoh.id" || !strings.Contains(body, v.InviteURL) || !strings.Contains(body, adm.email) || !strings.Contains(body, "operator") {
				t.Fatalf("isi email: to=%s\n%s", m.To, body)
			}
			if strings.Contains(strings.ToLower(body), "password:") {
				t.Fatal("email tidak boleh memuat password")
			}
			waitFor(t, 5*time.Second, "status sent", func() bool { return adm.c.emailState(v.ID).EmailStatus == "sent" })
			if status, attempts, bodyNull, _ := e.outbox(v.ID); status != "sent" || attempts != 1 || !bodyNull {
				t.Fatalf("setelah terkirim isi harus dikosongkan: %s %d null=%v", status, attempts, bodyNull)
			}
			// tautan di email benar-benar bisa dipakai
			var out struct{ Email string }
			if st := e.freshClient().do("POST", "/api/v1/invitations/accept", map[string]string{"token": v.Token, "password": "kuda-biru-melompati-pagar-9"}, &out); st != 201 {
				t.Fatalf("accept: %d", st)
			}
		})
	}
}

func TestInvitationEmailRetryPermanentAndExhaustion(t *testing.T) {
	srv := mailertest.Start(mailertest.Options{})
	defer srv.Close()
	e := newEnvOpts(t, withMail(srv, "none")) // backoff uji: 200ms, 300ms → 3 percobaan
	adm := e.newUser("admin")

	// sementara: gagal sekali (451) lalu terkirim; isi terenkripsi selama menunggu
	srv.FailRcpt(451, 1)
	_, v := adm.c.inviteMail("retry@contoh.id", "viewer")
	waitFor(t, 8*time.Second, "terkirim setelah retry", func() bool { return len(srv.Messages()) == 1 })
	waitFor(t, 5*time.Second, "sent", func() bool { return adm.c.emailState(v.ID).EmailStatus == "sent" })
	if _, attempts, _, _ := e.outbox(v.ID); attempts != 2 {
		t.Fatalf("harus 2 percobaan: %d", attempts)
	}
	fams, _ := scrape(t, e)
	if r, _ := metricValue(fams, "smsc_email_total", map[string]string{"result": "retry"}); r != 1 {
		t.Fatalf("metrik retry: %v", r)
	}
	if r, _ := metricValue(fams, "smsc_email_total", map[string]string{"result": "sent"}); r != 1 {
		t.Fatalf("metrik sent: %v", r)
	}

	// permanen (550): langsung gagal, tanpa retry
	srv.FailRcpt(550, 1)
	_, p := adm.c.inviteMail("tolak@contoh.id", "viewer")
	waitFor(t, 8*time.Second, "gagal permanen", func() bool { return adm.c.emailState(p.ID).EmailStatus == "failed" })
	st := adm.c.emailState(p.ID)
	if _, attempts, bodyNull, _ := e.outbox(p.ID); attempts != 1 || !bodyNull || !strings.Contains(st.EmailError, "550") {
		t.Fatalf("permanen: attempts=%d null=%v err=%q", attempts, bodyNull, st.EmailError)
	}

	// sementara terus-menerus: habis setelah 3 percobaan
	srv.FailRcptAlways(451)
	_, x := adm.c.inviteMail("habis@contoh.id", "viewer")
	waitFor(t, 10*time.Second, "gagal setelah retry habis", func() bool { return adm.c.emailState(x.ID).EmailStatus == "failed" })
	if _, attempts, bodyNull, _ := e.outbox(x.ID); attempts != 3 || !bodyNull {
		t.Fatalf("habis: attempts=%d null=%v", attempts, bodyNull)
	}
	// undangan tetap berlaku walau email gagal (admin bisa membagikan tautan manual)
	if st := e.freshClient().do("POST", "/api/v1/invitations/lookup", map[string]string{"token": x.Token}, nil); st != 200 {
		t.Fatalf("undangan harus tetap valid: %d", st)
	}
	fams, _ = scrape(t, e)
	if r, _ := metricValue(fams, "smsc_email_total", map[string]string{"result": "failed"}); r != 2 {
		t.Fatalf("metrik failed: %v", r)
	}
}

func TestInvitationEmailBodyEncryptedWhilePendingAndCancelled(t *testing.T) {
	srv := mailertest.Start(mailertest.Options{})
	defer srv.Close()
	e := newEnvOpts(t, withMail(srv, "none"), func(c *config.Config) {
		c.EmailBackoff = []time.Duration{time.Hour} // setelah gagal pertama, tunggu lama: baris tetap pending
	})
	srv.FailRcptAlways(451)
	adm := e.newUser("admin")
	_, v := adm.c.inviteMail("tunda@contoh.id", "viewer")
	waitFor(t, 8*time.Second, "percobaan pertama gagal", func() bool { _, a, _, _ := e.outbox(v.ID); return a == 1 })
	var raw []byte
	_ = e.pool.QueryRow(context.Background(), `SELECT body FROM email_outbox WHERE invitation_id=$1`, v.ID).Scan(&raw)
	if raw == nil || strings.Contains(string(raw), v.Token) || strings.Contains(string(raw), "invite/") {
		t.Fatal("isi yang menunggu harus terenkripsi (tidak memuat token/tautan polos)")
	}
	if adm.c.emailState(v.ID).EmailStatus != "queued" {
		t.Fatal("status harus queued")
	}
	// dicabut → email dibatalkan, isi dibuang, tidak pernah terkirim
	if st := adm.c.do("DELETE", "/api/v1/invitations/"+v.ID, nil, nil); st != 204 {
		t.Fatal(st)
	}
	if status, _, bodyNull, _ := e.outbox(v.ID); status != "cancelled" || !bodyNull {
		t.Fatalf("pencabutan: %s null=%v", status, bodyNull)
	}
	// undangan baru untuk email yang sama membatalkan email lama yang menunggu
	_, a := adm.c.inviteMail("ganti@contoh.id", "viewer")
	waitFor(t, 8*time.Second, "a gagal", func() bool { _, n, _, _ := e.outbox(a.ID); return n == 1 })
	_, b := adm.c.inviteMail("ganti@contoh.id", "viewer")
	if status, _, bodyNull, _ := e.outbox(a.ID); status != "cancelled" || !bodyNull {
		t.Fatalf("digantikan: %s null=%v", status, bodyNull)
	}
	// diterima sebelum email terkirim → email dibatalkan
	if st := e.freshClient().do("POST", "/api/v1/invitations/accept", map[string]string{"token": b.Token, "password": "kuda-biru-melompati-pagar-9"}, nil); st != 201 {
		t.Fatalf("accept: %d", st)
	}
	if status, _, bodyNull, _ := e.outbox(b.ID); status != "cancelled" || !bodyNull {
		t.Fatalf("diterima: %s null=%v", status, bodyNull)
	}
	if len(srv.Messages()) != 0 {
		t.Fatalf("tidak ada email yang boleh sampai: %d", len(srv.Messages()))
	}
}

func TestEmailSettingsAndTestEndpoint(t *testing.T) {
	srv := mailertest.Start(mailertest.Options{StartTLS: true, User: "kirim", Pass: "rahasia-smtp-xyz"})
	defer srv.Close()
	e := newEnvOpts(t, withMail(srv, "starttls"), func(c *config.Config) { c.Mail.Username, c.Mail.Password = "kirim", "rahasia-smtp-xyz" })
	adm := e.newUser("admin")
	op := e.newUser("operator")
	var raw map[string]any
	if st := adm.c.do("GET", "/api/v1/settings/email", nil, &raw); st != 200 || raw["configured"] != true || raw["host"] != "127.0.0.1" || raw["mode"] != "starttls" || raw["auth_enabled"] != true {
		t.Fatalf("config: %d %v", st, raw)
	}
	if strings.Contains(toJSON(raw), "rahasia-smtp-xyz") || strings.Contains(toJSON(raw), "kirim\"") {
		t.Fatal("kredensial SMTP tidak boleh bocor")
	}
	if st := op.c.do("GET", "/api/v1/settings/email", nil, nil); st != 403 {
		t.Fatalf("operator: %d", st)
	}
	if st := op.c.do("POST", "/api/v1/settings/email/test", map[string]string{"to": "x@contoh.id"}, nil); st != 403 {
		t.Fatalf("operator test: %d", st)
	}
	if st := adm.c.do("POST", "/api/v1/settings/email/test", map[string]string{"to": "bukan-email"}, nil); st != 422 {
		t.Fatalf("alamat buruk: %d", st)
	}
	if st := adm.c.do("POST", "/api/v1/settings/email/test", map[string]string{"to": "uji@contoh.id"}, nil); st != 200 {
		t.Fatalf("test: %d", st)
	}
	if len(srv.Messages()) != 1 || srv.Messages()[0].To != "uji@contoh.id" {
		t.Fatalf("pesan uji: %+v", srv.Messages())
	}
	// kegagalan SMTP dilaporkan jelas sebagai 502
	srv.FailRcptAlways(550)
	if st, ty := problemType(adm.c, "POST", "/api/v1/settings/email/test", map[string]string{"to": "uji@contoh.id"}); st != 502 || ty != "email.send_failed" {
		t.Fatalf("kegagalan: %d %s", st, ty)
	}
	var n int
	var d string
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*), coalesce(string_agg(data::text,''),'') FROM audit_logs WHERE action='email.test'`).Scan(&n, &d)
	if n != 2 && n != 1 { // sukses dicatat; kegagalan tidak harus dicatat
		t.Fatalf("audit email.test: %d", n)
	}
	if strings.Contains(d, "uji@contoh.id") {
		t.Fatal("alamat tujuan tidak boleh masuk audit")
	}
	_ = uuid.Nil
}
