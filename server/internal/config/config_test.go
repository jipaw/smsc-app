package config

import (
	"strings"
	"testing"
)

func base(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("PUBLIC_BASE_URL", "https://smsc.contoh.id")
	t.Setenv("SESSION_SECRET", strings.Repeat("s", 40))
	for _, k := range []string{"SMTP_HOST", "SMTP_PORT", "SMTP_USERNAME", "SMTP_PASSWORD", "SMTP_FROM", "SMTP_MODE", "SMTP_CA_FILE", "EMAIL_BACKOFF"} {
		t.Setenv(k, "")
	}
}

func TestSMTPDisabledByDefault(t *testing.T) {
	base(t)
	c, err := Load()
	if err != nil || c.Mail.Enabled() {
		t.Fatalf("tanpa SMTP_HOST email harus nonaktif: %v %+v", err, c.Mail)
	}
}

func TestSMTPDefaultsPerMode(t *testing.T) {
	base(t)
	t.Setenv("SMTP_HOST", "smtp.contoh.id")
	t.Setenv("SMTP_FROM", "SMSC <smsc@contoh.id>")
	for mode, port := range map[string]int{"": 587, "starttls": 587, "tls": 465, "none": 25} {
		t.Setenv("SMTP_MODE", mode)
		c, err := Load()
		if err != nil || c.Mail.Port != port || !c.Mail.Enabled() {
			t.Errorf("mode %q: port=%d err=%v", mode, c.Mail.Port, err)
		}
	}
	t.Setenv("SMTP_MODE", "tls")
	t.Setenv("SMTP_PORT", "2465")
	if c, _ := Load(); c.Mail.Port != 2465 {
		t.Fatalf("SMTP_PORT eksplisit: %d", c.Mail.Port)
	}
}

func TestSMTPMisconfigurationFailsFast(t *testing.T) {
	base(t)
	t.Setenv("SMTP_HOST", "smtp.contoh.id")
	for name, env := range map[string]map[string]string{
		"tanpa from":      {},
		"from buruk":      {"SMTP_FROM": "bukan-email"},
		"mode buruk":      {"SMTP_FROM": "a@contoh.id", "SMTP_MODE": "ssl"},
		"user tanpa pass": {"SMTP_FROM": "a@contoh.id", "SMTP_USERNAME": "u"},
		"CA tidak ada":    {"SMTP_FROM": "a@contoh.id", "SMTP_CA_FILE": "/tidak/ada.pem"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SMTP_FROM", "")
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Fatal("harus gagal saat startup")
			}
		})
	}
}
