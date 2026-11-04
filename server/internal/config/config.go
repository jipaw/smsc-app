package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/jipaw/smsc-app/server/internal/mailer"
	"time"
)

// OIDCConfig: SSO dengan OpenID Connect (authorization code + PKCE). Kosong OIDC_ISSUER = SSO nonaktif.
type OIDCConfig struct {
	Issuer         string
	ClientID       string
	ClientSecret   string // boleh kosong (klien publik, PKCE saja)
	RedirectURL    string
	Scopes         []string
	Name           string   // label tombol, mis. "Google Workspace"
	AllowedDomains []string // batasi domain email untuk penyediaan/penautan otomatis (kosong = semua)
	AutoProvision  bool     // buat pengguna baru saat login SSO pertama
	DefaultRole    string   // viewer|operator untuk pengguna baru
	GroupsClaim    string
	AdminGroups    []string
	OperatorGroups []string
	SyncRoles      bool         // sinkronkan peran dari grup IdP di setiap login
	LinkByEmail    bool         // tautkan otomatis ke pengguna lokal dengan email yang sama (email_verified wajib)
	TrustIdPMFA    bool         // sesi SSO dianggap memenuhi kewajiban 2FA (IdP yang menegakkan MFA)
	HTTPClient     *http.Client // klien untuk IdP (CA privat / pengujian); nil = bawaan
}

func (o OIDCConfig) Enabled() bool { return o.Issuer != "" }

// SCIMConfig: provisioning SCIM 2.0 dari IdP. Nonaktif secara default.
type SCIMConfig struct {
	Enabled        bool
	DefaultRole    string // peran pengguna baru tanpa atribut roles: viewer|operator
	AllowAdminRole bool   // izinkan SCIM memberi/mengubah peran admin (default false: kompromi IdP tidak boleh mencetak admin)
	AdoptExisting  bool   // pengguna lokal yang sudah ada terlihat/dapat dikelola SCIM (default false)
	// pemetaan grup SCIM (displayName, tak peka huruf) → peran; peran efektif pengguna = tertinggi(roles, grup)
	GroupRoles map[string]string
}

type Config struct {
	OIDC OIDCConfig
	SCIM SCIMConfig

	DatabaseURL          string
	HTTPAddr             string
	PublicBaseURL        string
	SessionSecret        string
	SessionSecretPrev    []string // SESSION_SECRET_PREVIOUS (koma): sesi lama tetap sah selama rotasi
	DefaultRegion        string
	TrustTailscale       bool
	AllowedDestPrefixes  []string // ALLOWED_DESTINATION_PREFIXES: awalan E.164 tujuan yang diizinkan (kosong = semua)
	AuditRetentionDays   int      // AUDIT_RETENTION_DAYS: hapus audit_logs lebih tua dari N hari (0 = simpan selamanya)
	TrustedProxies       []*net.IPNet
	SecretFromFile       map[string]bool // rahasia yang dibaca dari <NAMA>_FILE (untuk smscd doctor)
	WebhookKeyDerived    bool            // WEBHOOK_ENC_KEY kosong → kunci enkripsi diturunkan dari SESSION_SECRET // TRUSTED_PROXIES: hanya dari alamat ini X-Forwarded-For/Tailscale-User-Login dipercaya
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
	RetentionInterval     time.Duration
	PartialRefreshAfter   time.Duration // jeda sebelum hitungan bagian pesan gagal-parsial disegarkan lewat sms.status
	StatusQueryWait       time.Duration // tunggu jawaban sms.status sebelum menilai ulang pesan sending
	HubTick               time.Duration // poll cadangan/antar-instance WS Hub
	LeaderTick            time.Duration // interval cek/ambil-alih lock leader
	SCIMReconcileInterval time.Duration // 0 = hanya saat start
	MetricsAddr           string        // listener terpisah; kosong = nonaktif
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

// SecretEnvs: variabel rahasia yang juga dapat dibaca dari berkas lewat <NAMA>_FILE (Docker/Kubernetes secrets, systemd credentials).
var SecretEnvs = []string{"DATABASE_URL", "SESSION_SECRET", "SESSION_SECRET_PREVIOUS", "WEBHOOK_ENC_KEY", "WEBHOOK_ENC_KEY_PREVIOUS", "SMTP_PASSWORD", "OIDC_CLIENT_SECRET"}

// loadSecretFiles: untuk setiap NAMA dengan NAMA_FILE terisi, isi berkas (tanpa spasi/baris baru di ujung) menjadi nilai NAMA.
// Mengisi keduanya sekaligus ditolak agar sumber nilai tidak ambigu.
func loadSecretFiles() (map[string]bool, error) {
	fromFile := map[string]bool{}
	for _, k := range SecretEnvs {
		f := os.Getenv(k + "_FILE")
		if f == "" {
			continue
		}
		if os.Getenv(k) != "" {
			return nil, fmt.Errorf("%s dan %s_FILE tidak boleh diisi bersamaan", k, k)
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("%s_FILE: %w", k, err)
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return nil, fmt.Errorf("%s_FILE: berkas %s kosong", k, f)
		}
		_ = os.Setenv(k, v)
		fromFile[k] = true
	}
	return fromFile, nil
}

func Load() (Config, error) {
	fromFile, ferr := loadSecretFiles()
	if ferr != nil {
		return Config{}, ferr
	}
	c := Config{
		SecretFromFile:        fromFile,
		WebhookKeyDerived:     os.Getenv("WEBHOOK_ENC_KEY") == "",
		DatabaseURL:           os.Getenv("DATABASE_URL"),
		HTTPAddr:              env("HTTP_ADDR", "127.0.0.1:8080"),
		PublicBaseURL:         strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"),
		SessionSecret:         os.Getenv("SESSION_SECRET"),
		DefaultRegion:         env("DEFAULT_REGION", "ID"),
		TrustTailscale:        env("TRUST_TAILSCALE_HEADERS", "false") == "true",
		AutoMigrate:           env("AUTO_MIGRATE", "false") == "true",
		DispatchTick:          envDur("DISPATCH_TICK", 5*time.Second),
		ReaperInterval:        envDur("REAPER_INTERVAL", 2*time.Second),
		OfflineAfter:          envDur("OFFLINE_AFTER", 90*time.Second),
		HeartbeatIntervalSec:  30,
		EnrollTTL:             envDur("ENROLL_TTL", 15*time.Minute),
		DeviceRotateGrace:     envDur("DEVICE_ROTATE_GRACE", 60*time.Second),
		LoginRatePerMin:       envFloat("LOGIN_RATE_PER_MIN", 5),
		LoginBurst:            envFloat("LOGIN_BURST", 5),
		SecureCookies:         env("SECURE_COOKIES", "true") == "true",
		RequireVPN:            env("REQUIRE_VPN", "true") == "true",
		CampaignTick:          envDur("CAMPAIGN_TICK", time.Second),
		NoCapacityAfter:       envDur("NO_CAPACITY_AFTER", 10*time.Minute),
		CampaignQueueCap:      20000,
		WebhookTimeout:        envDur("WEBHOOK_TIMEOUT", 10*time.Second),
		WebhookBackoff:        []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour},
		WebhookAllowLoopback:  env("WEBHOOK_ALLOW_LOOPBACK", "false") == "true",
		WebhookKey:            os.Getenv("WEBHOOK_ENC_KEY"),
		WebhookWorkers:        8,
		WebhookTick:           envDur("WEBHOOK_TICK", time.Second),
		EmailBackoff:          []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour},
		EmailTick:             envDur("EMAIL_TICK", 2*time.Second),
		RetentionInterval:     envDur("RETENTION_INTERVAL", time.Hour),
		PartialRefreshAfter:   envDur("PARTIAL_REFRESH_AFTER", 30*time.Second),
		StatusQueryWait:       envDur("STATUS_QUERY_WAIT", 15*time.Second),
		HubTick:               envDur("HUB_TICK", time.Second),
		LeaderTick:            envDur("LEADER_TICK", 5*time.Second),
		SCIMReconcileInterval: envDur("SCIM_RECONCILE_INTERVAL", time.Hour),
		MetricsAddr:           env("METRICS_ADDR", "127.0.0.1:9090"),
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
	c.SCIM = SCIMConfig{Enabled: env("SCIM_ENABLED", "false") == "true", DefaultRole: strings.ToLower(env("SCIM_DEFAULT_ROLE", "viewer")),
		AllowAdminRole: env("SCIM_ALLOW_ADMIN_ROLE", "false") == "true", AdoptExisting: env("SCIM_ADOPT_EXISTING", "false") == "true"}
	c.SCIM.GroupRoles = map[string]string{}
	for role, key := range map[string]string{"admin": "SCIM_ADMIN_GROUPS", "operator": "SCIM_OPERATOR_GROUPS", "viewer": "SCIM_VIEWER_GROUPS"} {
		for _, g := range splitList(os.Getenv(key)) {
			n := strings.ToLower(strings.TrimSpace(g))
			if prev, dup := c.SCIM.GroupRoles[n]; dup && prev != role {
				return c, fmt.Errorf("grup SCIM %q dipetakan ke dua peran (%s dan %s)", g, prev, role)
			}
			c.SCIM.GroupRoles[n] = role
		}
	}
	if c.SCIM.Enabled && c.SCIM.DefaultRole != "viewer" && c.SCIM.DefaultRole != "operator" {
		return c, fmt.Errorf("SCIM_DEFAULT_ROLE harus viewer atau operator")
	}
	for g, r := range c.SCIM.GroupRoles {
		if r == "admin" && !c.SCIM.AllowAdminRole {
			return c, fmt.Errorf("SCIM_ADMIN_GROUPS (%s) butuh SCIM_ALLOW_ADMIN_ROLE=true", g)
		}
	}
	if err := c.loadOIDC(); err != nil {
		return c, err
	}
	c.SessionSecretPrev = splitList(os.Getenv("SESSION_SECRET_PREVIOUS"))
	c.WebhookKeyPrev = splitList(os.Getenv("WEBHOOK_ENC_KEY_PREVIOUS"))
	for _, p := range splitList(os.Getenv("ALLOWED_DESTINATION_PREFIXES")) {
		if !strings.HasPrefix(p, "+") {
			p = "+" + p
		}
		if len(p) < 2 || strings.Trim(p[1:], "0123456789") != "" {
			return c, fmt.Errorf("ALLOWED_DESTINATION_PREFIXES tidak valid: %q (contoh: +62,+65)", p)
		}
		c.AllowedDestPrefixes = append(c.AllowedDestPrefixes, p)
	}
	if v := os.Getenv("AUDIT_RETENTION_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return c, fmt.Errorf("AUDIT_RETENTION_DAYS tidak valid: %q", v)
		}
		c.AuditRetentionDays = n
	}
	tp := env("TRUSTED_PROXIES", "127.0.0.1/32,::1/128")
	for _, p := range splitList(tp) {
		if !strings.Contains(p, "/") {
			if strings.Contains(p, ":") {
				p += "/128"
			} else {
				p += "/32"
			}
		}
		_, n, err := net.ParseCIDR(p)
		if err != nil {
			return c, fmt.Errorf("TRUSTED_PROXIES tidak valid: %q", p)
		}
		c.TrustedProxies = append(c.TrustedProxies, n)
	}
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

func (c *Config) loadOIDC() error {
	o := OIDCConfig{
		Issuer: strings.TrimRight(os.Getenv("OIDC_ISSUER"), "/"), ClientID: os.Getenv("OIDC_CLIENT_ID"), ClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
		RedirectURL: os.Getenv("OIDC_REDIRECT_URL"), Scopes: splitSpaceList(env("OIDC_SCOPES", "openid email profile")), Name: env("OIDC_NAME", "SSO"),
		AllowedDomains: lowerAll(splitList(os.Getenv("OIDC_ALLOWED_DOMAINS"))), AutoProvision: env("OIDC_AUTO_PROVISION", "false") == "true",
		DefaultRole: strings.ToLower(env("OIDC_DEFAULT_ROLE", "viewer")), GroupsClaim: env("OIDC_GROUPS_CLAIM", "groups"),
		AdminGroups: splitList(os.Getenv("OIDC_ADMIN_GROUPS")), OperatorGroups: splitList(os.Getenv("OIDC_OPERATOR_GROUPS")),
		SyncRoles: env("OIDC_SYNC_ROLES", "false") == "true", LinkByEmail: env("OIDC_LINK_BY_EMAIL", "false") == "true",
		TrustIdPMFA: env("OIDC_TRUST_IDP_MFA", "true") == "true",
	}
	c.OIDC = o
	if !o.Enabled() {
		return nil
	}
	u, err := url.Parse(o.Issuer)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("OIDC_ISSUER harus URL http(s) yang valid")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) && env("OIDC_ALLOW_INSECURE", "false") != "true" {
		return fmt.Errorf("OIDC_ISSUER harus https (http hanya untuk localhost atau OIDC_ALLOW_INSECURE=true)")
	}
	if o.ClientID == "" {
		return fmt.Errorf("OIDC_CLIENT_ID wajib bila OIDC_ISSUER diisi")
	}
	if o.RedirectURL == "" {
		c.OIDC.RedirectURL = c.PublicBaseURL + "/api/v1/auth/oidc/callback"
	}
	if ru, err := url.Parse(c.OIDC.RedirectURL); err != nil || ru.Scheme == "" || ru.Host == "" {
		return fmt.Errorf("OIDC_REDIRECT_URL harus URL absolut (default: PUBLIC_BASE_URL + /api/v1/auth/oidc/callback)")
	}
	if o.DefaultRole != "viewer" && o.DefaultRole != "operator" {
		return fmt.Errorf("OIDC_DEFAULT_ROLE harus viewer atau operator (admin hanya lewat OIDC_ADMIN_GROUPS atau diangkat manual)")
	}
	hasOpenID := false
	for _, sc := range o.Scopes {
		hasOpenID = hasOpenID || sc == "openid"
	}
	if !hasOpenID {
		return fmt.Errorf("OIDC_SCOPES harus memuat openid")
	}
	if f := os.Getenv("OIDC_CA_FILE"); f != "" {
		pem, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("OIDC_CA_FILE: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return fmt.Errorf("OIDC_CA_FILE: tidak ada sertifikat PEM yang valid")
		}
		c.OIDC.HTTPClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	}
	return nil
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func splitSpaceList(v string) []string { return strings.Fields(v) }

func lowerAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, x := range in {
		out = append(out, strings.ToLower(x))
	}
	return out
}
