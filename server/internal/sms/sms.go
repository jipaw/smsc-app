// Package sms: normalisasi nomor, encoding/segmen GSM-7/UCS-2, dan render template (spec 04 §9).
package sms

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/nyaruka/phonenumbers"
)

// Normalize mengubah input (mis. 0812…, +62 812-…) menjadi E.164. region contoh "ID".
func Normalize(raw, region string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("nomor kosong")
	}
	num, err := phonenumbers.Parse(raw, region)
	if err != nil {
		return "", fmt.Errorf("bukan nomor telepon: %w", err)
	}
	if !phonenumbers.IsValidNumber(num) {
		return "", fmt.Errorf("nomor tidak valid untuk wilayahnya")
	}
	return phonenumbers.Format(num, phonenumbers.E164), nil
}

const gsmBasic = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞ\x1bÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"
const gsmExt = "^{}\\[~]|€\f"

var (
	basicSet = func() map[rune]bool {
		m := map[rune]bool{}
		for _, r := range gsmBasic {
			if r != 0x1b {
				m[r] = true
			}
		}
		return m
	}()
	extSet = func() map[rune]bool {
		m := map[rune]bool{}
		for _, r := range gsmExt {
			m[r] = true
		}
		return m
	}()
)

type Encoding string

const (
	GSM7 Encoding = "gsm7"
	UCS2 Encoding = "ucs2"
)

// Info: encoding, jumlah unit (septet GSM-7 atau unit UTF-16), dan jumlah segmen.
type Info struct {
	Encoding Encoding
	Units    int
	Parts    int
}

// Analyze: GSM-7 160 / 153 per bagian; UCS-2 70 / 67. Karakter extension GSM = 2 septet; emoji (di luar BMP) = 2 unit UTF-16.
func Analyze(body string) Info {
	septets, gsm := 0, true
	for _, r := range body {
		switch {
		case basicSet[r]:
			septets++
		case extSet[r]:
			septets += 2
		default:
			gsm = false
		}
		if !gsm {
			break
		}
	}
	if gsm {
		return Info{GSM7, septets, parts(septets, 160, 153)}
	}
	units := len(utf16.Encode([]rune(body)))
	return Info{UCS2, units, parts(units, 70, 67)}
}

func parts(n, single, multi int) int {
	if n <= single {
		return 1
	}
	return (n + multi - 1) / multi
}

var tplVar = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)

// Variables mengembalikan nama variabel unik sesuai urutan kemunculan.
func Variables(tpl string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range tplVar.FindAllStringSubmatch(tpl, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// Render mengganti {{var}}; mengembalikan daftar variabel yang tidak tersedia (tidak ada ekspresi/eksekusi).
func Render(tpl string, vars map[string]string) (string, []string) {
	var missing []string
	out := tplVar.ReplaceAllStringFunc(tpl, func(s string) string {
		name := tplVar.FindStringSubmatch(s)[1]
		v, ok := vars[name]
		if !ok {
			missing = append(missing, name)
			return s
		}
		v = strings.TrimSpace(v)
		if r := []rune(v); len(r) > 200 {
			v = string(r[:200])
		}
		return v
	})
	return out, missing
}
