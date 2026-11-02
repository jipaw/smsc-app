package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL          string
	HTTPAddr             string
	PublicBaseURL        string
	SessionSecret        string
	DefaultRegion        string
	TrustTailscale       bool
	AutoMigrate          bool
	LogLevel             slog.Level
	DispatchTick         time.Duration // tick pengaman dispatcher
	ReaperInterval       time.Duration
	OfflineAfter         time.Duration
	HeartbeatIntervalSec int
	EnrollTTL            time.Duration
	SecureCookies        bool
	RequireVPN           bool          // Profil A: device tanpa VPN aktif ditandai degraded
	CampaignTick         time.Duration // siklus materializer/scheduler/auto-pause
	CampaignQueueCap     int           // maks. pesan campaign berstatus queued sekaligus (backpressure materialisasi)
	NoCapacityAfter      time.Duration // pause otomatis bila tak ada SIM online selama ini
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
		SecureCookies:        env("SECURE_COOKIES", "true") == "true",
		RequireVPN:           env("REQUIRE_VPN", "true") == "true",
		CampaignTick:         envDur("CAMPAIGN_TICK", time.Second),
		NoCapacityAfter:      envDur("NO_CAPACITY_AFTER", 10*time.Minute),
		CampaignQueueCap:     20000,
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
