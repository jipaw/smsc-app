package config

import (
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/jipaw/smsc-app/server/internal/mailer"
	"time"
)

type Config struct {
	DatabaseURL          string
	HTTPAddr             string
	PublicBaseURL        string
	SessionSecret        string
	SessionSecretPrev    []string // SESSION_SECRET_PREVIOUS (koma): sesi lama tetap sah selama rotasi
	DefaultRegion        string
	TrustTailscale       bool
	AutoMigrate          bool
	LogLevel             slog.Level
	DispatchTick         time.Duration // tick pengaman dispatcher
	ReaperInterval       time.Duration
	OfflineAfter         time.Duration
	HeartbeatIntervalSec int
	EnrollTTL            time.Duration
	LoginRatePerMin      float64       // LOGIN_RATE_PER_MIN (default 5/mnt/IP) — juga untuk /auth/login/verify; endpoint akun memakai 2x per pengguna
	LoginBurst           float64       // LOGIN_BURST (default 5)
	DeviceRotateGrace    time.Duration // masa secret device lama masih sah setelah rotasi (spec 06: 60 dtk)
	SecureCookies        bool
	RequireVPN           bool          // Profil A: device tanpa VPN aktif ditandai degraded
	CampaignTick         time.Duration // siklus materializer/scheduler/auto-pause
	CampaignQueueCap     int           // maks. pesan campaign berstatus queued sekaligus (backpressure materialisasi)
	NoCapacityAfter      time.Duration // pause otomatis bila tak ada SIM online selama ini

	// webhook (fase 3)
	WebhookTimeout       time.Duration
	WebhookBackoff       []time.Duration // jeda antar percobaan ulang; len = jumlah retry (total percobaan = len+1)
	WebhookAllowLoopback bool            // izinkan target 127.0.0.0/8 (dev/tes); metadata cloud 169.254/16 selalu diblokir
	WebhookKey           string          // kunci enkripsi secret webhook/TOTP (default: SESSION_SECRET)
	WebhookKeyPrev       []string        // kunci lama (WEBHOOK_ENC_KEY_PREVIOUS) untuk mendekripsi saat rotasi
	WebhookWorkers       int
	WebhookTick          time.Duration

	// email (fase 8): kosong SMTP_HOST = pengiriman email nonaktif
	Mail         mailer.Config
	EmailBackoff []time.Duration // jeda sebelum percobaan ulang; len = jumlah retry
	EmailTick    time.Duration

	// WebAuthn (fase 9). RP ID default = host dari PUBLIC_BASE_URL (tanpa port); alamat IP tidak valid sebagai RP ID
	WebAuthnRPID    string
	WebAuthnRPName  string
	WebAuthnOrigins []string

	// retensi & metrik (fase 3)
	RetentionInterval time.Duration
	MetricsAddr       string // listener terpisah; kosong = nonaktif
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envDur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func Load() (Config, error) {
	c := Config{
		DatabaseURL:          os.Getenv("DATABASE_URL"),
		HTTPAddr:             env("HTTP_ADDR", "127.0.0.1:8080"),
		PublicBaseURL:        strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"),
		SessionSecret:        os.Getenv("SESSION_SECRET"),
		DefaultRegion:        env("DEFAULT_REGION", "ID"),
		TrustTailscale:       env("TRUST_TAILSCALE_HEADERS", "false") == "true",
		AutoMigrate:          env("AUTO_MIGRATE", "false") == "true",
		DispatchTick:         envDur("DISPATCH_TICK", 5*time.Second),
		ReaperInterval:       envDur("REAPER_INTERVAL", 2*time.Second),
		OfflineAfter:         envDur("OFFLINE_AFTER", 90*time.Second),
		HeartbeatIntervalSec: 30,
		EnrollTTL:            envDur("ENROLL_TTL", 15*time.Minute),
		DeviceRotateGrace:    envDur("DEVICE_ROTATE_GRACE", 60*time.Second),
		LoginRatePerMin:      envFloat("LOGIN_RATE_PER_MIN", 5),
		LoginBurst:           envFloat("LOGIN_BURST", 5),
		SecureCookies:        env("SECURE_COOKIES", "true") == "true",
		RequireVPN:           env("REQUIRE_VPN", "true") == "true",
		CampaignTick:         envDur("CAMPAIGN_TICK", time.Second),
		NoCapacityAfter:      envDur("NO_CAPACITY_AFTER", 10*time.Minute),
		CampaignQueueCap:     20000,
		WebhookTimeout:       envDur("WEBHOOK_TIMEOUT", 10*time.Second),
		WebhookBackoff:       []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour},
		WebhookAllowLoopback: env("WEBHOOK_ALLOW_LOOPBACK", "false") == "true",
		WebhookKey:           os.Getenv("WEBHOOK_ENC_KEY"),
		WebhookWorkers:       8,
		WebhookTick:          envDur("WEBHOOK_TICK", time.Second),
		EmailBackoff:         []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour},
		EmailTick:            envDur("EMAIL_TICK", 2*time.Second),
		RetentionInterval:    envDur("RETENTION_INTERVAL", time.Hour),
		MetricsAddr:          env("METRICS_ADDR", "127.0.0.1:9090"),
	}
	if v, err := strconv.Atoi(env("HEARTBEAT_INTERVAL_SEC", "30")); err == nil {
		c.HeartbeatIntervalSec = v
	}
	if v, err := strconv.Atoi(env("CAMPAIGN_QUEUE_CAP", "20000")); err == nil && v > 0 {
		c.CampaignQueueCap = v
	}
	switch strings.ToLower(env("LOG_LEVEL", "info")) {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "warn":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		c.LogLevel = slog.LevelInfo
	}
	if v := os.Getenv("WEBHOOK_BACKOFF"); v != "" { // mis. "30s,2m,10m"
		var bo []time.Duration
		for _, part := range strings.Split(v, ",") {
			if d, err := time.ParseDuration(strings.TrimSpace(part)); err == nil && d > 0 {
				bo = append(bo, d)
			}
		}
		if len(bo) > 0 {
			c.WebhookBackoff = bo
		}
	}
	c.Mail = mailer.Config{
		Host: os.Getenv("SMTP_HOST"), Username: os.Getenv("SMTP_USERNAME"), Password: os.Getenv("SMTP_PASSWORD"),
		From: os.Getenv("SMTP_FROM"), Mode: strings.ToLower(env("SMTP_MODE", "starttls")), Timeout: envDur("SMTP_TIMEOUT", 15*time.Second),
	}
	defPort := map[string]int{"starttls": 587, "tls": 465, "none": 25}[c.Mail.Mode]
	if v, err := strconv.Atoi(os.Getenv("SMTP_PORT")); err == nil {
		c.Mail.Port = v
	} else {
		c.Mail.Port = defPort
	}
	if f := os.Getenv("SMTP_CA_FILE"); f != "" { // CA privat untuk relay internal
		pem, err := os.ReadFile(f)
		if err != nil {
			return c, fmt.Errorf("SMTP_CA_FILE: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return c, fmt.Errorf("SMTP_CA_FILE: tidak ada sertifikat PEM yang valid")
		}
		c.Mail.RootCAs = pool
	}
	if v := os.Getenv("EMAIL_BACKOFF"); v != "" {
		var bo []time.Duration
		for _, part := range strings.Split(v, ",") {
			if d, err := time.ParseDuration(strings.TrimSpace(part)); err == nil && d > 0 {
				bo = append(bo, d)
			}
		}
		if len(bo) > 0 {
			c.EmailBackoff = bo
		}
	}
	if err := c.Mail.Validate(); err != nil {
		return c, err
	}
	c.WebAuthnRPName = env("WEBAUTHN_RP_NAME", "SMSC Gateway")
	c.WebAuthnRPID = os.Getenv("WEBAUTHN_RP_ID")
	if c.WebAuthnRPID == "" {
		if u, err := url.Parse(c.PublicBaseURL); err == nil {
			c.WebAuthnRPID = u.Hostname()
		}
	}
	c.WebAuthnOrigins = splitList(os.Getenv("WEBAUTHN_ORIGINS"))
	if len(c.WebAuthnOrigins) == 0 && c.PublicBaseURL != "" {
		c.WebAuthnOrigins = []string{c.PublicBaseURL}
	}
	c.SessionSecretPrev = splitList(os.Getenv("SESSION_SECRET_PREVIOUS"))
	c.WebhookKeyPrev = splitList(os.Getenv("WEBHOOK_ENC_KEY_PREVIOUS"))
	if c.WebhookKey == "" {
		// kunci enkripsi mengikuti SESSION_SECRET; rahasia sesi lama otomatis menjadi kunci dekripsi lama
		c.WebhookKey = c.SessionSecret
		c.WebhookKeyPrev = append(c.WebhookKeyPrev, c.SessionSecretPrev...)
	}
	var missing []string
	if c.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if c.PublicBaseURL == "" {
		missing = append(missing, "PUBLIC_BASE_URL")
	}
	if len(c.SessionSecret) < 32 {
		missing = append(missing, "SESSION_SECRET (>= 32 karakter)")
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("konfigurasi wajib belum diisi: %s", strings.Join(missing, ", "))
	}
	return c, nil
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envFloat(k string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil && v > 0 {
		return v
	}
	return def
}
