package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jipaw/smsc-app/server/internal/mailer"
)

// Email lewat SMTP (fase 8). Pola outbox seperti webhook: baris ditulis dalam transaksi yang sama dengan peristiwanya,
// pekerja (leader) mengirim dengan retry/backoff. Isi email memuat tautan undangan (rahasia), jadi disimpan TERENKRIPSI
// dan dikosongkan begitu email terkirim, gagal final, atau dibatalkan (undangan dicabut/diterima).

func (s *Service) EmailEnabled() bool { return s.Cfg.Mail.Enabled() }

type EmailConfigView struct {
	Configured  bool   `json:"configured"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	Mode        string `json:"mode,omitempty"`
	From        string `json:"from,omitempty"`
	AuthEnabled bool   `json:"auth_enabled"`
}

// EmailConfig: pengaturan SMTP yang aman ditampilkan (tanpa password).
func (s *Service) EmailConfig() EmailConfigView {
	m := s.Cfg.Mail
	if !m.Enabled() {
		return EmailConfigView{}
	}
	return EmailConfigView{Configured: true, Host: m.Host, Port: m.Port, Mode: m.Mode, From: m.From, AuthEnabled: m.Username != ""}
}

var errEmailNotConfigured = NewProblem(http.StatusConflict, "email.not_configured", "Email belum dikonfigurasi", "atur SMTP_HOST, SMTP_FROM (dan kredensial) di server lalu restart")

// SendTestEmail mengirim langsung (tanpa outbox) agar admin langsung melihat hasil/kesalahan SMTP.
func (s *Service) SendTestEmail(ctx context.Context, to string) error {
	if !s.EmailEnabled() {
		return errEmailNotConfigured
	}
	if err := mailer.Send(ctx, s.Cfg.Mail, to, "Uji email SMSC Gateway", "Email ini dikirim dari halaman Pengaturan > Email SMSC Gateway.\nJika Anda menerimanya, konfigurasi SMTP bekerja.\n"); err != nil {
		var perm mailer.Permanent
		code := http.StatusBadGateway
		if errors.As(err, &perm) && strings.Contains(err.Error(), "alamat tujuan") {
			return Validation(FieldError{"to", err.Error()})
		}
		return NewProblem(code, "email.send_failed", "Pengiriman email gagal", truncate(err.Error(), 300))
	}
	return nil
}

// enqueueEmail menulis ke outbox (di dalam transaksi pemanggil).
func (s *Service) enqueueEmail(ctx context.Context, db dbx, kind, to, subject, body string, invitationID *uuid.UUID, by uuid.UUID) error {
	enc, err := s.seal(purposeEmail, body)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO email_outbox(id,kind,to_email,subject,body,invitation_id,created_by) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		newID(), kind, to, subject, enc, invitationID, by)
	return err
}

func invitationEmail(inviter, role, url string, expires time.Time) (subject, body string) {
	subject = "Undangan akun SMSC Gateway"
	body = fmt.Sprintf(`Halo,

%s mengundang Anda untuk bergabung di SMSC Gateway sebagai %s.

Buka tautan berikut untuk menetapkan password dan membuat akun Anda:

%s

Tautan ini hanya dapat dipakai satu kali dan berlaku sampai %s.
Jangan teruskan tautan ini kepada siapa pun.

Jika Anda tidak merasa diundang, abaikan email ini.
`, inviter, role, url, expires.Local().Format("2 January 2006 15:04 MST"))
	return
}

// RunEmails: pekerja outbox (leader). Mengirim berurutan; volume undangan kecil.
func (s *Service) RunEmails(ctx context.Context) {
	t := time.NewTicker(s.Cfg.EmailTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !s.EmailEnabled() {
			continue
		}
		for _, m := range s.claimEmails(ctx, 20) {
			if ctx.Err() != nil {
				return
			}
			s.sendOutboxEmail(ctx, m)
		}
	}
}

type claimedEmail struct {
	id       uuid.UUID
	to, subj string
	body     []byte
	attempts int
}

func (s *Service) claimEmails(ctx context.Context, n int) []claimedEmail {
	rows, err := s.Pool.Query(ctx, `UPDATE email_outbox e SET next_attempt_at = now() + interval '2 minutes'
		WHERE e.id IN (SELECT id FROM email_outbox WHERE status='pending' AND next_attempt_at <= now() ORDER BY next_attempt_at LIMIT $1 FOR UPDATE SKIP LOCKED)
		RETURNING e.id, e.to_email, e.subject, e.body, e.attempts`, n)
	if err != nil {
		s.Log.Error("email: claim", "err", err)
		return nil
	}
	defer rows.Close()
	var out []claimedEmail
	for rows.Next() {
		var c claimedEmail
		if rows.Scan(&c.id, &c.to, &c.subj, &c.body, &c.attempts) == nil {
			out = append(out, c)
		}
	}
	return out
}

func (s *Service) sendOutboxEmail(ctx context.Context, m claimedEmail) {
	if m.body == nil { // sudah dibatalkan di sela-sela
		_, _ = s.Pool.Exec(ctx, `UPDATE email_outbox SET status='cancelled' WHERE id=$1 AND status='pending'`, m.id)
		return
	}
	plain, _, err := s.open(purposeEmail, m.body)
	if err == nil {
		err = mailer.Send(ctx, s.Cfg.Mail, m.to, m.subj, plain)
	}
	attempts := m.attempts + 1
	var perm mailer.Permanent
	switch {
	case err == nil:
		s.M.EmailResults.WithLabelValues("sent").Inc()
		_, _ = s.Pool.Exec(ctx, `UPDATE email_outbox SET status='sent', body=NULL, attempts=$2, sent_at=now(), last_error=NULL WHERE id=$1 AND status='pending'`, m.id, attempts)
	case errors.As(err, &perm) || attempts > len(s.Cfg.EmailBackoff):
		s.M.EmailResults.WithLabelValues("failed").Inc()
		s.Log.Warn("email gagal final", "id", m.id, "attempts", attempts, "err", err)
		_, _ = s.Pool.Exec(ctx, `UPDATE email_outbox SET status='failed', body=NULL, attempts=$2, last_error=$3 WHERE id=$1 AND status='pending'`, m.id, attempts, truncate(err.Error(), 300))
	default:
		s.M.EmailResults.WithLabelValues("retry").Inc()
		wait := s.Cfg.EmailBackoff[attempts-1]
		_, _ = s.Pool.Exec(ctx, `UPDATE email_outbox SET attempts=$2, last_error=$3, next_attempt_at = now() + make_interval(secs => $4) WHERE id=$1 AND status='pending'`,
			m.id, attempts, truncate(err.Error(), 300), wait.Seconds())
	}
}

// cancelInvitationEmails membatalkan email undangan yang belum terkirim dan membuang isinya (tautan tidak berlaku lagi).
func cancelInvitationEmails(ctx context.Context, db dbx, where string, args ...any) error {
	_, err := db.Exec(ctx, `UPDATE email_outbox SET status='cancelled', body=NULL WHERE status='pending' AND invitation_id IN (SELECT id FROM invitations WHERE `+where+`)`, args...)
	return err
}

var _ = pgx.ErrNoRows
