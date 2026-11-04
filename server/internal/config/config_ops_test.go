package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretFromFile(t *testing.T) {
	base(t)
	dir := t.TempDir()
	f := filepath.Join(dir, "sess")
	if err := os.WriteFile(f, []byte(strings.Repeat("f", 40)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSION_SECRET", "")
	t.Setenv("SESSION_SECRET_FILE", f)
	c, err := Load()
	if err != nil || c.SessionSecret != strings.Repeat("f", 40) || !c.SecretFromFile["SESSION_SECRET"] || !c.WebhookKeyDerived {
		t.Fatalf("SESSION_SECRET_FILE: %v %q %v", err, c.SessionSecret, c.SecretFromFile)
	}
	// keduanya diisi → ditolak; berkas hilang → ditolak
	t.Setenv("SESSION_SECRET", strings.Repeat("s", 40))
	if _, err := Load(); err == nil {
		t.Fatal("env + _FILE bersamaan harus ditolak")
	}
	t.Setenv("SESSION_SECRET", "")
	t.Setenv("SESSION_SECRET_FILE", filepath.Join(dir, "tidak-ada"))
	if _, err := Load(); err == nil {
		t.Fatal("berkas hilang harus ditolak")
	}
}

func TestTrustedProxiesAndPrefixes(t *testing.T) {
	base(t)
	c, err := Load()
	if err != nil || len(c.TrustedProxies) != 2 || len(c.AllowedDestPrefixes) != 0 || c.AuditRetentionDays != 0 || c.QuotaLocation.String() != "Asia/Jakarta" {
		t.Fatalf("default: %v %v %v", err, c.TrustedProxies, c.AllowedDestPrefixes)
	}
	t.Setenv("TRUSTED_PROXIES", "10.0.0.5, 172.16.0.0/12, fd7a::1")
	t.Setenv("ALLOWED_DESTINATION_PREFIXES", "+62, 65")
	t.Setenv("AUDIT_RETENTION_DAYS", "365")
	t.Setenv("DAILY_QUOTA_PER_KEY", "1000")
	t.Setenv("QUOTA_TIMEZONE", "Asia/Makassar")
	c, err = Load()
	if err == nil && (c.DailyQuotaKey != 1000 || c.DailyQuotaUser != 0 || c.QuotaLocation.String() != "Asia/Makassar") {
		t.Fatalf("kuota: %d %d %v", c.DailyQuotaKey, c.DailyQuotaUser, c.QuotaLocation)
	}
	if err != nil || len(c.TrustedProxies) != 3 || c.TrustedProxies[0].String() != "10.0.0.5/32" || c.TrustedProxies[2].String() != "fd7a::1/128" ||
		strings.Join(c.AllowedDestPrefixes, ",") != "+62,+65" || c.AuditRetentionDays != 365 {
		t.Fatalf("parse: %v %v %v %d", err, c.TrustedProxies, c.AllowedDestPrefixes, c.AuditRetentionDays)
	}
	for k, v := range map[string]string{"TRUSTED_PROXIES": "bukan-ip", "ALLOWED_DESTINATION_PREFIXES": "+62a", "AUDIT_RETENTION_DAYS": "-1",
		"DAILY_QUOTA_PER_USER": "-5", "QUOTA_TIMEZONE": "Mars/Olympus"} {
		t.Run(k, func(t *testing.T) {
			t.Setenv(k, v)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%s harus ditolak", k, v)
			}
		})
	}
}
