package mailer_test

import (
	"context"
	"errors"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/jipaw/smsc-app/server/internal/mailer"
	"github.com/jipaw/smsc-app/server/internal/mailer/mailertest"
)

func cfg(s *mailertest.Server, mode string) mailer.Config {
	return mailer.Config{Host: s.Host, Port: s.Port, From: "SMSC Gateway <smsc@contoh.id>", Mode: mode, Timeout: 5 * time.Second, RootCAs: s.Pool}
}

func decode(t *testing.T, raw string) (hdr mail.Header, body string) {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("pesan tidak valid: %v\n%s", err, raw)
	}
	b := new(strings.Builder)
	r := quotedprintable.NewReader(m.Body)
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return m.Header, b.String()
}

func TestSendModesAndContent(t *testing.T) {
	for _, mode := range []string{"none", "starttls", "tls"} {
		t.Run(mode, func(t *testing.T) {
			s := mailertest.Start(mailertest.Options{Implicit: mode == "tls", StartTLS: mode == "starttls"})
			defer s.Close()
			subj := "Undangan akun — Ünïcode ✓"
			body := "Halo,\nBuka https://smsc.contoh.id/invite/inv_abc?x=1&y=2 \nBaris dengan karakter non-ASCII: é ñ 日本\n.titik di awal baris\n"
			if err := mailer.Send(context.Background(), cfg(s, mode), "Budi <budi@contoh.id>", subj, body); err != nil {
				t.Fatal(err)
			}
			ms := s.Messages()
			if len(ms) != 1 || ms[0].To != "budi@contoh.id" || ms[0].From != "smsc@contoh.id" {
				t.Fatalf("amplop: %+v", ms)
			}
			h, got := decode(t, ms[0].Data)
			dec := new(mime.WordDecoder)
			gs, _ := dec.DecodeHeader(h.Get("Subject"))
			if gs != subj {
				t.Errorf("subjek: %q", gs)
			}
			if !strings.Contains(h.Get("From"), "smsc@contoh.id") || h.Get("Message-Id") == "" || h.Get("Date") == "" || h.Get("Content-Type") != "text/plain; charset=UTF-8" {
				t.Errorf("header: %v", h)
			}
			if strings.ReplaceAll(got, "\r\n", "\n") != body {
				t.Errorf("isi berubah:\n%q\n%q", got, body)
			}
		})
	}
}

func TestAuthPlain(t *testing.T) {
	s := mailertest.Start(mailertest.Options{StartTLS: true, User: "kirim", Pass: "rahasia-smtp"})
	defer s.Close()
	c := cfg(s, "starttls")
	c.Username, c.Password = "kirim", "rahasia-smtp"
	if err := mailer.Send(context.Background(), c, "a@contoh.id", "uji", "isi"); err != nil {
		t.Fatalf("auth benar: %v", err)
	}
	c.Password = "salah"
	err := mailer.Send(context.Background(), c, "a@contoh.id", "uji", "isi")
	var perm mailer.Permanent
	if err == nil || !errors.As(err, &perm) {
		t.Fatalf("AUTH salah (535) harus permanen: %v", err)
	}
	// tanpa kredensial ke server yang mewajibkan auth -> 530 permanen
	c.Username, c.Password = "", ""
	if err := mailer.Send(context.Background(), c, "a@contoh.id", "uji", "isi"); !errors.As(err, &perm) {
		t.Fatalf("tanpa auth: %v", err)
	}
	if len(s.Messages()) != 1 {
		t.Fatalf("hanya satu pesan yang boleh sampai: %d", len(s.Messages()))
	}
}

func TestPlainAuthRefusedWithoutTLS(t *testing.T) {
	// host selain localhost/127.0.0.1/::1 tanpa TLS: kredensial tidak boleh dikirim polos (aturan net/smtp)
	s := mailertest.Start(mailertest.Options{User: "kirim", Pass: "x", Bind: "127.0.0.2"})
	defer s.Close()
	c := cfg(s, "none")
	c.Username, c.Password = "kirim", "x"
	err := mailer.Send(context.Background(), c, "a@contoh.id", "uji", "isi")
	if err == nil || !strings.Contains(err.Error(), "unencrypted") {
		t.Fatalf("harus menolak AUTH tanpa enkripsi: %v", err)
	}
	if len(s.Messages()) != 0 {
		t.Fatal("tidak boleh ada pesan")
	}
	// ke 127.0.0.1 (loopback) diizinkan
	l := mailertest.Start(mailertest.Options{User: "kirim", Pass: "x"})
	defer l.Close()
	c = cfg(l, "none")
	c.Username, c.Password = "kirim", "x"
	if err := mailer.Send(context.Background(), c, "a@contoh.id", "uji", "isi"); err != nil {
		t.Fatalf("loopback: %v", err)
	}
}

func TestNoSilentDowngradeAndCertVerification(t *testing.T) {
	// server tanpa STARTTLS tetapi mode starttls diminta -> gagal, jangan kirim polos
	plain := mailertest.Start(mailertest.Options{})
	defer plain.Close()
	c := cfg(plain, "starttls")
	err := mailer.Send(context.Background(), c, "a@contoh.id", "uji", "isi")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("harus menolak turun ke koneksi tanpa enkripsi: %v", err)
	}
	if len(plain.Messages()) != 0 {
		t.Fatal("tidak boleh ada pesan terkirim tanpa enkripsi")
	}
	// sertifikat tidak dipercaya
	tlsSrv := mailertest.Start(mailertest.Options{StartTLS: true})
	defer tlsSrv.Close()
	c = cfg(tlsSrv, "starttls")
	c.RootCAs = nil // pool sistem: sertifikat swa-tanda-tangan harus ditolak
	if err := mailer.Send(context.Background(), c, "a@contoh.id", "uji", "isi"); err == nil {
		t.Fatal("sertifikat tak tepercaya harus ditolak")
	}
	// host tidak cocok dengan sertifikat
	c = cfg(tlsSrv, "starttls")
	c.Host = "localhost"
	_ = c // localhost ada di SAN → sukses; yang dites: nama lain gagal
	c.Host = "127.0.0.2"
	if err := mailer.Send(context.Background(), c, "a@contoh.id", "uji", "isi"); err == nil {
		t.Fatal("nama host yang tidak ada di sertifikat harus ditolak")
	}
}

func TestPermanentVsTemporaryAndInjection(t *testing.T) {
	s := mailertest.Start(mailertest.Options{})
	defer s.Close()
	c := cfg(s, "none")
	var perm mailer.Permanent
	s.FailRcpt(451, 1)
	if err := mailer.Send(context.Background(), c, "a@contoh.id", "uji", "isi"); err == nil || errors.As(err, &perm) {
		t.Fatalf("451 sementara tidak boleh permanen: %v", err)
	}
	s.FailRcpt(550, 1)
	if err := mailer.Send(context.Background(), c, "a@contoh.id", "uji", "isi"); !errors.As(err, &perm) {
		t.Fatalf("550 harus permanen: %v", err)
	}
	for name, args := range map[string][3]string{
		"to CRLF":        {"a@contoh.id\r\nBcc: x@evil.id", "s", "b"},
		"to bukan email": {"bukan-email", "s", "b"},
		"subjek CRLF":    {"a@contoh.id", "s\r\nBcc: x@evil.id", "b"},
	} {
		if err := mailer.Send(context.Background(), c, args[0], args[1], args[2]); !errors.As(err, &perm) {
			t.Errorf("%s: harus ditolak permanen: %v", name, err)
		}
	}
	for _, m := range s.Messages() {
		if strings.Contains(m.Data, "evil.id") {
			t.Fatal("injeksi header lolos")
		}
	}
}

func TestTimeoutAndUnreachable(t *testing.T) {
	c := mailer.Config{Host: "127.0.0.1", Port: 1, From: "a@contoh.id", Mode: "none", Timeout: time.Second}
	if err := mailer.Send(context.Background(), c, "a@contoh.id", "s", "b"); err == nil {
		t.Fatal("port tertutup harus gagal")
	}
}

func TestValidate(t *testing.T) {
	ok := mailer.Config{Host: "smtp.contoh.id", Port: 587, From: "a@contoh.id", Mode: "starttls"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if (mailer.Config{}).Validate() != nil || (mailer.Config{}).Enabled() {
		t.Fatal("kosong = nonaktif & valid")
	}
	for name, mut := range map[string]func(*mailer.Config){
		"mode":      func(c *mailer.Config) { c.Mode = "ssl" },
		"port":      func(c *mailer.Config) { c.Port = 0 },
		"from":      func(c *mailer.Config) { c.From = "bukan-email" },
		"from crlf": func(c *mailer.Config) { c.From = "a@contoh.id\r\nBcc: x@y.id" },
		"user saja": func(c *mailer.Config) { c.Username = "u" },
	} {
		c := ok
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s: harus tidak valid", name)
		}
	}
}
