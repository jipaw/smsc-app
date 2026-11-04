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
	// rahasia wajib dari berkas kosong ditolak; rahasia opsional kosong = tidak dipakai
	empty := filepath.Join(dir, "kosong")
	os.WriteFile(empty, []byte("\n"), 0o600)
	t.Setenv("SESSION_SECRET", "")
	t.Setenv("SESSION_SECRET_FILE", empty)
	if _, err := Load(); err == nil {
		t.Fatal("SESSION_SECRET_FILE kosong harus ditolak")
	}
	t.Setenv("SESSION_SECRET_FILE", f)
	t.Setenv("TAILNET_AUTH_KEY_FILE", empty)
	if c, err := Load(); err != nil || c.Tailnet.AuthKey != "" {
		t.Fatalf("TAILNET_AUTH_KEY_FILE kosong: %v", err)
	}
	t.Setenv("SESSION_SECRET_FILE", filepath.Join(dir, "tidak-ada"))
	if _, err := Load(); err == nil {
		t.Fatal("berkas hilang harus ditolak")
	}
}

func TestTailnetConfig(t *testing.T) {
	base(t)
	c, err := Load()
	if err != nil || c.Tailnet.Enabled || c.Tailnet.DevicePolicy != "off" || c.Tailnet.KeyMinting() {
		t.Fatalf("default: %v %+v", err, c.Tailnet)
	}
	t.Setenv("TAILNET_ENABLED", "true")
	t.Setenv("TAILNET_STATE_DIR", t.TempDir())
	t.Setenv("TAILSCALE_OAUTH_CLIENT_ID", "cid")
	t.Setenv("TAILSCALE_OAUTH_CLIENT_SECRET", "cs")
	c, err = Load()
	if err != nil || c.Tailnet.DevicePolicy != "record" || c.Tailnet.Listen != "tls" || c.Tailnet.Hostname != "smsc" || !c.Tailnet.KeyMinting() ||
		strings.Join(c.Tailnet.DeviceTags, ",") != "tag:smsc-device" || c.Tailnet.APIURL != "https://api.tailscale.com" || c.Tailnet.Tailnet != "-" {
		t.Fatalf("aktif: %v %+v", err, c.Tailnet)
	}
	for k, v := range map[string]string{"TAILNET_DEVICE_POLICY": "ketat", "TAILNET_LISTEN": "udp", "TAILNET_DEVICE_TAGS": "smsc-device", "TAILNET_STATE_DIR": ""} {
		t.Run(k, func(t *testing.T) {
			t.Setenv(k, v)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%q harus ditolak", k, v)
			}
		})
	}
	t.Run("enforce tanpa tailnet", func(t *testing.T) {
		t.Setenv("TAILNET_ENABLED", "false")
		t.Setenv("TAILNET_DEVICE_POLICY", "enforce")
		if _, err := Load(); err == nil {
			t.Fatal("enforce tanpa listener tailnet harus ditolak")
		}
	})
	t.Run("secret OAuth wajib", func(t *testing.T) {
		t.Setenv("TAILSCALE_OAUTH_CLIENT_SECRET", "")
		if _, err := Load(); err == nil {
			t.Fatal("client id tanpa secret harus ditolak")
		}
	})
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
