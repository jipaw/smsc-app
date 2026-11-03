package core

import (
	"strings"
	"testing"
	"time"
)

// Vektor RFC 6238 (lampiran B, SHA-1, rahasia ASCII "12345678901234567890"), 6 digit terakhir dari nilai 8 digit.
func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	for _, c := range []struct {
		unix int64
		want string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"}} {
		got, err := totpAt(secret, c.unix/30)
		if err != nil || got != c.want {
			t.Errorf("T=%d: dapat %s (%v), mau %s", c.unix, got, err, c.want)
		}
	}
}

func TestVerifyTOTPWindowAndReplay(t *testing.T) {
	secret := newTOTPSecret()
	now := time.Unix(1_800_000_000, 0)
	cur := now.Unix() / 30
	code := func(step int64) string { c, _ := totpAt(secret, step); return c }

	if got := verifyTOTP(secret, code(cur), now, 0); got != cur {
		t.Fatalf("kode saat ini harus diterima: %d", got)
	}
	if verifyTOTP(secret, code(cur-1), now, 0) != cur-1 || verifyTOTP(secret, code(cur+1), now, 0) != cur+1 {
		t.Fatal("toleransi ±1 langkah harus diterima")
	}
	if verifyTOTP(secret, code(cur-2), now, 0) != 0 || verifyTOTP(secret, code(cur+2), now, 0) != 0 {
		t.Fatal("di luar jendela harus ditolak")
	}
	if verifyTOTP(secret, code(cur), now, cur) != 0 {
		t.Fatal("langkah yang sudah dipakai harus ditolak (replay)")
	}
	if verifyTOTP(secret, code(cur-1), now, cur) != 0 {
		t.Fatal("langkah lebih lama dari yang terakhir dipakai harus ditolak")
	}
	if verifyTOTP(secret, code(cur+1), now, cur) != cur+1 {
		t.Fatal("langkah lebih baru boleh")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "12 456"} {
		if verifyTOTP(secret, bad, now, 0) != 0 {
			t.Errorf("format buruk %q harus ditolak", bad)
		}
	}
	if verifyTOTP("bukan-base32!", "123456", now, 0) != 0 {
		t.Fatal("rahasia rusak harus ditolak tanpa panik")
	}
}

func TestOtpauthURIAndRecoveryCodes(t *testing.T) {
	u := otpauthURI("SMSC Gateway", "a@b.test", "JBSWY3DPEHPK3PXP")
	for _, w := range []string{"otpauth://totp/SMSC%20Gateway:a@b.test?", "secret=JBSWY3DPEHPK3PXP", "issuer=SMSC+Gateway", "digits=6", "period=30"} {
		if !strings.Contains(u, w) {
			t.Errorf("URI tanpa %q: %s", w, u)
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		c := newRecoveryCode()
		if len(c) != 11 || c[5] != '-' || strings.ContainsAny(c, "01OI") {
			t.Fatalf("format kode pemulihan: %q", c)
		}
		if seen[c] {
			t.Fatal("kode pemulihan bertabrakan")
		}
		seen[c] = true
		if normalizeRecovery(strings.ToLower(c)) != normalizeRecovery(c) || len(normalizeRecovery(" "+c+" ")) != 10 {
			t.Fatal("normalisasi harus tidak peka huruf besar/kecil dan abaikan tanda hubung")
		}
	}
}
