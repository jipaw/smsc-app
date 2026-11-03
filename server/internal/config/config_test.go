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

func oidcBase(t *testing.T) {
	base(t)
	for _, k := range []string{"OIDC_ISSUER", "OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET", "OIDC_REDIRECT_URL", "OIDC_SCOPES", "OIDC_NAME", "OIDC_ALLOWED_DOMAINS",
		"OIDC_AUTO_PROVISION", "OIDC_DEFAULT_ROLE", "OIDC_GROUPS_CLAIM", "OIDC_ADMIN_GROUPS", "OIDC_OPERATOR_GROUPS", "OIDC_SYNC_ROLES", "OIDC_LINK_BY_EMAIL",
		"OIDC_TRUST_IDP_MFA", "OIDC_CA_FILE", "OIDC_ALLOW_INSECURE"} {
		t.Setenv(k, "")
	}
}

func TestOIDCDisabledByDefaultAndDefaults(t *testing.T) {
	oidcBase(t)
	c, err := Load()
	if err != nil || c.OIDC.Enabled() {
		t.Fatalf("default harus nonaktif: %v", err)
	}
	t.Setenv("OIDC_ISSUER", "https://idp.contoh.id/")
	t.Setenv("OIDC_CLIENT_ID", "smsc")
	c, err = Load()
	o := c.OIDC
	if err != nil || !o.Enabled() || o.Issuer != "https://idp.contoh.id" || o.RedirectURL != "https://smsc.contoh.id/api/v1/auth/oidc/callback" ||
		o.DefaultRole != "viewer" || o.AutoProvision || o.LinkByEmail || o.SyncRoles || !o.TrustIdPMFA || o.GroupsClaim != "groups" || o.Name != "SSO" || len(o.Scopes) != 3 {
		t.Fatalf("default: %v %+v", err, o)
	}
	t.Setenv("OIDC_ALLOWED_DOMAINS", "Contoh.ID, kantor.id")
	t.Setenv("OIDC_ADMIN_GROUPS", "a, b")
	c, _ = Load()
	if len(c.OIDC.AllowedDomains) != 2 || c.OIDC.AllowedDomains[0] != "contoh.id" || len(c.OIDC.AdminGroups) != 2 {
		t.Fatalf("daftar: %+v", c.OIDC)
	}
}

func TestOIDCMisconfigurationFailsFast(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"tanpa client id":        {"OIDC_ISSUER": "https://idp.contoh.id"},
		"issuer bukan url":       {"OIDC_ISSUER": "idp.contoh.id", "OIDC_CLIENT_ID": "x"},
		"http non-loopback":      {"OIDC_ISSUER": "http://idp.contoh.id", "OIDC_CLIENT_ID": "x"},
		"role default admin":     {"OIDC_ISSUER": "https://idp.contoh.id", "OIDC_CLIENT_ID": "x", "OIDC_DEFAULT_ROLE": "admin"},
		"scope tanpa openid":     {"OIDC_ISSUER": "https://idp.contoh.id", "OIDC_CLIENT_ID": "x", "OIDC_SCOPES": "email profile"},
		"redirect bukan absolut": {"OIDC_ISSUER": "https://idp.contoh.id", "OIDC_CLIENT_ID": "x", "OIDC_REDIRECT_URL": "/callback"},
		"CA tidak ada":           {"OIDC_ISSUER": "https://idp.contoh.id", "OIDC_CLIENT_ID": "x", "OIDC_CA_FILE": "/tidak/ada.pem"},
	} {
		t.Run(name, func(t *testing.T) {
			oidcBase(t)
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Fatal("harus gagal saat startup")
			}
		})
	}
	// http diizinkan untuk loopback dan dengan OIDC_ALLOW_INSECURE
	oidcBase(t)
	t.Setenv("OIDC_ISSUER", "http://localhost:9000")
	t.Setenv("OIDC_CLIENT_ID", "x")
	if _, err := Load(); err != nil {
		t.Fatalf("loopback http: %v", err)
	}
	t.Setenv("OIDC_ISSUER", "http://idp.internal")
	t.Setenv("OIDC_ALLOW_INSECURE", "true")
	if _, err := Load(); err != nil {
		t.Fatalf("OIDC_ALLOW_INSECURE: %v", err)
	}
}

func TestSCIMConfig(t *testing.T) {
	base(t)
	for _, k := range []string{"SCIM_ENABLED", "SCIM_DEFAULT_ROLE", "SCIM_ALLOW_ADMIN_ROLE", "SCIM_ADOPT_EXISTING"} {
		t.Setenv(k, "")
	}
	c, err := Load()
	if err != nil || c.SCIM.Enabled || c.SCIM.AllowAdminRole || c.SCIM.AdoptExisting || c.SCIM.DefaultRole != "viewer" {
		t.Fatalf("default: %v %+v", err, c.SCIM)
	}
	t.Setenv("SCIM_ENABLED", "true")
	t.Setenv("SCIM_DEFAULT_ROLE", "Operator")
	c, err = Load()
	if err != nil || !c.SCIM.Enabled || c.SCIM.DefaultRole != "operator" {
		t.Fatalf("aktif: %v %+v", err, c.SCIM)
	}
	t.Setenv("SCIM_DEFAULT_ROLE", "admin")
	if _, err := Load(); err == nil {
		t.Fatal("peran bawaan admin harus ditolak")
	}
}

func TestSCIMGroupMappingConfig(t *testing.T) {
	base(t)
	for _, k := range []string{"SCIM_ENABLED", "SCIM_DEFAULT_ROLE", "SCIM_ALLOW_ADMIN_ROLE", "SCIM_ADOPT_EXISTING", "SCIM_ADMIN_GROUPS", "SCIM_OPERATOR_GROUPS", "SCIM_VIEWER_GROUPS"} {
		t.Setenv(k, "")
	}
	t.Setenv("SCIM_ENABLED", "true")
	t.Setenv("SCIM_OPERATOR_GROUPS", "Gateway Ops, Tim Ops")
	t.Setenv("SCIM_VIEWER_GROUPS", "Gateway Viewers")
	c, err := Load()
	if err != nil || c.SCIM.GroupRoles["gateway ops"] != "operator" || c.SCIM.GroupRoles["tim ops"] != "operator" || c.SCIM.GroupRoles["gateway viewers"] != "viewer" || len(c.SCIM.GroupRoles) != 3 {
		t.Fatalf("pemetaan: %v %v", err, c.SCIM.GroupRoles)
	}
	// grup yang sama di dua peran = ambigu → gagal start
	t.Setenv("SCIM_VIEWER_GROUPS", "gateway OPS")
	if _, err := Load(); err == nil {
		t.Fatal("grup ganda peran harus ditolak")
	}
	// grup admin butuh SCIM_ALLOW_ADMIN_ROLE
	t.Setenv("SCIM_VIEWER_GROUPS", "")
	t.Setenv("SCIM_ADMIN_GROUPS", "Gateway Admins")
	if _, err := Load(); err == nil {
		t.Fatal("SCIM_ADMIN_GROUPS tanpa SCIM_ALLOW_ADMIN_ROLE harus ditolak")
	}
	t.Setenv("SCIM_ALLOW_ADMIN_ROLE", "true")
	if c, err = Load(); err != nil || c.SCIM.GroupRoles["gateway admins"] != "admin" {
		t.Fatalf("admin diizinkan: %v", err)
	}
}
