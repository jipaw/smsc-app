package core

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 default; dipakai semua aplikasi authenticator
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP RFC 6238: SHA-1, 6 digit, langkah 30 dtk. Jendela toleransi ±1 langkah (selisih jam).
const (
	totpPeriod = 30
	totpDigits = 6
	totpSkew   = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func newTOTPSecret() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return b32.EncodeToString(b)
}

func totpAt(secretB32 string, step int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secretB32, " ", "")))
	if err != nil {
		return "", err
	}
	var ctr [8]byte
	binary.BigEndian.PutUint64(ctr[:], uint64(step))
	m := hmac.New(sha1.New, key)
	m.Write(ctr[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := (uint32(sum[off]&0x7f) << 24) | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	return fmt.Sprintf("%0*d", totpDigits, v%1000000), nil
}

// verifyTOTP mengembalikan langkah yang cocok (0 bila tidak ada). Hanya langkah > lastStep yang diterima (anti-replay).
func verifyTOTP(secretB32, code string, now time.Time, lastStep int64) int64 {
	if len(code) != totpDigits {
		return 0
	}
	cur := now.Unix() / totpPeriod
	var hit int64
	for d := int64(-totpSkew); d <= totpSkew; d++ { // tanpa early-exit: waktu tetap
		step := cur + d
		want, err := totpAt(secretB32, step)
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 && step > lastStep && step > hit {
			hit = step
		}
	}
	return hit
}

func otpauthURI(issuer, account, secretB32 string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{"secret": {secretB32}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

const recoveryAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // tanpa 0/O/1/I

func newRecoveryCode() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	out := make([]byte, 10)
	for i, x := range b {
		out[i] = recoveryAlphabet[int(x)%len(recoveryAlphabet)]
	}
	return string(out[:5]) + "-" + string(out[5:])
}

func normalizeRecovery(c string) string {
	c = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(c)))
	return c
}
