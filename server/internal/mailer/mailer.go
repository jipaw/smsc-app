// Package mailer mengirim email teks lewat SMTP dengan pengaman: STARTTLS/TLS wajib kecuali mode "none" dipilih
// eksplisit (tanpa downgrade diam-diam), verifikasi sertifikat, batas waktu menyeluruh, dan penolakan injeksi header.
package mailer

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

const (
	ModeSTARTTLS = "starttls" // sambungan biasa lalu STARTTLS (port 587); gagal bila server tidak menawarkannya
	ModeTLS      = "tls"      // TLS implisit (port 465)
	ModeNone     = "none"     // tanpa enkripsi — hanya untuk relay lokal tepercaya; AUTH ditolak kecuali ke loopback
)

type Config struct {
	Host, Username, Password string
	Port                     int
	From                     string // alamat pengirim, mis. "SMSC Gateway <smsc@contoh.id>"
	Mode                     string
	Timeout                  time.Duration
	RootCAs                  *x509.CertPool // nil = pool sistem
}

func (c Config) Enabled() bool { return c.Host != "" }

// Validate memeriksa konfigurasi (dipanggil saat startup agar salah-ketik terlihat segera).
func (c Config) Validate() error {
	if !c.Enabled() {
		return nil
	}
	switch c.Mode {
	case ModeSTARTTLS, ModeTLS, ModeNone:
	default:
		return fmt.Errorf("SMTP_MODE harus starttls, tls, atau none (dapat %q)", c.Mode)
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("SMTP_PORT tidak valid: %d", c.Port)
	}
	if _, err := parseAddr(c.From); err != nil {
		return fmt.Errorf("SMTP_FROM wajib alamat email yang valid: %w", err)
	}
	if (c.Username == "") != (c.Password == "") {
		return errors.New("SMTP_USERNAME dan SMTP_PASSWORD harus diisi bersamaan")
	}
	return nil
}

func parseAddr(s string) (*mail.Address, error) {
	if strings.ContainsAny(s, "\r\n") {
		return nil, errors.New("mengandung baris baru")
	}
	return mail.ParseAddress(s)
}

// Permanent: kegagalan yang tidak layak diulang (5xx dari server, alamat tidak valid).
type Permanent struct{ Err error }

func (p Permanent) Error() string { return p.Err.Error() }
func (p Permanent) Unwrap() error { return p.Err }

// Send mengirim satu email teks UTF-8. Mengembalikan Permanent untuk penolakan 5xx / masukan tidak valid.
func Send(ctx context.Context, c Config, to, subject, body string) error {
	rcpt, err := parseAddr(to)
	if err != nil {
		return Permanent{fmt.Errorf("alamat tujuan tidak valid: %w", err)}
	}
	from, err := parseAddr(c.From)
	if err != nil {
		return Permanent{fmt.Errorf("SMTP_FROM tidak valid: %w", err)}
	}
	if strings.ContainsAny(subject, "\r\n") {
		return Permanent{errors.New("subjek mengandung baris baru")}
	}
	msg, err := build(from, rcpt, subject, body)
	if err != nil {
		return Permanent{err}
	}
	to1 := c.Timeout
	if to1 <= 0 {
		to1 = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, to1)
	defer cancel()

	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("sambung ke %s: %w", addr, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	tlsCfg := &tls.Config{ServerName: c.Host, RootCAs: c.RootCAs, MinVersion: tls.VersionTLS12}
	if c.Mode == ModeTLS {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("TLS: %w", err)
		}
		conn = tc
	}
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		return fmt.Errorf("salam SMTP: %w", wrap(err))
	}
	defer cl.Close()
	if err := cl.Hello("smsc.local"); err != nil {
		return fmt.Errorf("EHLO: %w", wrap(err))
	}
	if c.Mode == ModeSTARTTLS {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return errors.New("server tidak menawarkan STARTTLS (tidak akan turun ke koneksi tanpa enkripsi)")
		}
		if err := cl.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if c.Username != "" {
		if ok, _ := cl.Extension("AUTH"); !ok {
			return errors.New("server tidak mendukung AUTH padahal SMTP_USERNAME diisi")
		}
		if err := cl.Auth(smtp.PlainAuth("", c.Username, c.Password, c.Host)); err != nil {
			return fmt.Errorf("AUTH: %w", wrap(err))
		}
	}
	if err := cl.Mail(from.Address); err != nil {
		return fmt.Errorf("MAIL FROM: %w", wrap(err))
	}
	if err := cl.Rcpt(rcpt.Address); err != nil {
		return fmt.Errorf("RCPT TO: %w", wrap(err))
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", wrap(err))
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("DATA: %w", wrap(err))
	}
	_ = cl.Quit()
	return nil
}

// wrap menandai penolakan permanen (kode 5xx) agar tidak diulang.
func wrap(err error) error {
	var te *textproto.Error
	if errors.As(err, &te) && te.Code >= 500 {
		return Permanent{err}
	}
	return err
}

func build(from, to *mail.Address, subject, body string) ([]byte, error) {
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	domain := "smsc.local"
	if i := strings.LastIndexByte(from.Address, '@'); i >= 0 {
		domain = from.Address[i+1:]
	}
	var b strings.Builder
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from.String())
	h("To", to.String())
	h("Subject", mime.QEncoding.Encode("utf-8", subject))
	h("Date", time.Now().Format(time.RFC1123Z))
	h("Message-ID", "<"+hex.EncodeToString(id[:])+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=UTF-8")
	h("Content-Transfer-Encoding", "quoted-printable")
	h("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")
	qw := quotedprintable.NewWriter(&b)
	if _, err := qw.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))); err != nil {
		return nil, err
	}
	if err := qw.Close(); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}
