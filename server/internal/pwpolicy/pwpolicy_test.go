package pwpolicy

import (
	"strings"
	"testing"
)

func TestRejectsWeak(t *testing.T) {
	for _, c := range []struct{ pw, why string }{
		{"short", "minimal 12"},
		{"password1234", "umum"},     // persis ada di daftar atau variasi sepele
		{"Password123!!", "variasi"}, // dekorasi di ujung
		{"P@ssw0rd2024!", "variasi"}, // leet + dekorasi
		{"administrator", "umum"},    // panjang ≥ 12 dan ada di daftar
		{"aaaaaaaaaaaaaa", "monoton"},
		{"abababababab", "monoton"},
		{"abcdefghijklmnop", "berurutan"},
		{"123456789012", "berurutan"},
		{"qwertyuiopasdf", "keyboard"},
		{"Zq9!xKZq9!xKZq9!xK", "diulang"},
		{strings.Repeat("x", 300), "maksimal"},
	} {
		got := strings.Join(Check(c.pw), "; ")
		if !strings.Contains(got, c.why) {
			t.Errorf("%q: harus ditolak dengan %q, dapat %q", c.pw, c.why, got)
		}
	}
}

func TestAcceptsStrong(t *testing.T) {
	for _, pw := range []string{
		"kuda-biru-melompati-pagar-9", "password-panjang-123", "Tr0ub4dor&3-horse-staple",
		"e2e-admin-password-1", "operator-pass-1234", "gaji-bulan-ini-aman!", "xK9#mQ2$vL7@nP4!",
	} {
		if why := Check(pw, "orang@example.com"); len(why) != 0 {
			t.Errorf("%q harus diterima, ditolak: %v", pw, why)
		}
	}
}

func TestEmailHint(t *testing.T) {
	if why := Check("budisantoso-2026-ok!", "budisantoso@perusahaan.id"); len(why) == 0 {
		t.Fatal("password yang memuat nama akun harus ditolak")
	}
	if why := Check("kuda-biru-melompati-pagar-9", "kuda@x.id"); len(why) == 0 {
		t.Fatal("hint ≥ 4 huruf yang ada di password harus ditolak")
	}
	if why := Check("kuda-biru-melompati-pagar-9", "bud@x.id"); len(why) != 0 {
		t.Fatalf("hint pendek (<4) diabaikan: %v", why)
	}
}

func TestListLoadedAndKnownWords(t *testing.T) {
	load()
	if len(common) < 40000 {
		t.Fatalf("daftar terlalu kecil: %d", len(common))
	}
	for _, w := range []string{"password", "qwertyuiop", "iloveyou", "administrator", "football"} {
		if _, ok := common[w]; !ok {
			t.Errorf("kata kunci %q harus ada di daftar", w)
		}
	}
}
