// Package pwpolicy menilai password baru: panjang, daftar password umum (termasuk variasi sepele), pola monoton/berurutan/
// jalur keyboard, dan kemiripan dengan email pemilik. Hanya untuk password BARU (tidak mencabut password lama).
package pwpolicy

import (
	_ "embed"
	"strings"
	"sync"
	"unicode"
)

//go:embed common.txt
var commonRaw string

const (
	MinLen = 12
	MaxLen = 256 // batas atas agar hashing argon2id tidak menjadi vektor DoS
)

var (
	once   sync.Once
	common map[string]struct{}
)

func load() {
	once.Do(func() {
		lines := strings.Split(commonRaw, "\n")
		common = make(map[string]struct{}, len(lines))
		for _, l := range lines {
			if l = strings.TrimSpace(l); l != "" {
				common[l] = struct{}{}
			}
		}
	})
}

// Check mengembalikan alasan penolakan (kosong = diterima). hints: email pemilik (bagian sebelum @ dipakai).
func Check(password string, hints ...string) []string {
	load()
	var why []string
	n := len([]rune(password))
	if n < MinLen {
		return []string{"minimal 12 karakter"}
	}
	if n > MaxLen {
		return []string{"maksimal 256 karakter"}
	}
	low := strings.ToLower(password)

	if _, ok := common[low]; ok {
		why = append(why, "termasuk password yang sangat umum")
	} else if baseIsCommon(low) {
		why = append(why, "variasi sepele dari password umum (angka/simbol di ujung atau huruf diganti angka tidak membantu)")
	}
	if distinct(low) < 5 {
		why = append(why, "terlalu monoton (kurang dari 5 karakter berbeda)")
	} else if sequential(low) {
		why = append(why, "pola berurutan atau berulang")
	} else if keyboardWalk(low) {
		why = append(why, "pola tombol keyboard")
	} else if repeatedBlock(low) {
		why = append(why, "blok pendek yang diulang-ulang")
	}
	for _, h := range hints {
		local := strings.ToLower(h)
		if i := strings.IndexByte(local, '@'); i >= 0 {
			local = local[:i]
		}
		if len(local) >= 4 && strings.Contains(low, local) {
			why = append(why, "mengandung nama akun/email Anda")
			break
		}
	}
	return why
}

func distinct(s string) int {
	m := map[rune]struct{}{}
	for _, r := range s {
		m[r] = struct{}{}
	}
	return len(m)
}

// baseIsCommon: buang angka/simbol di awal dan akhir (maks. 4 karakter total), kembalikan huruf yang diganti angka/simbol
// ("p@ssw0rd" → "password"), lalu cek terhadap daftar. Inti harus persis sama dengan satu entri (bukan sekadar mengandung).
func baseIsCommon(low string) bool {
	rs := []rune(low)
	isDeco := func(r rune) bool {
		return unicode.IsDigit(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsSpace(r)
	}
	start, end := 0, len(rs)
	stripped := 0
	for start < end && isDeco(rs[start]) && stripped < 4 {
		start++
		stripped++
	}
	for end > start && isDeco(rs[end-1]) && stripped < 4 {
		end--
		stripped++
	}
	core := string(rs[start:end])
	if len([]rune(core)) < 8 {
		return false
	}
	if _, ok := common[core]; ok {
		return true
	}
	for _, one := range []string{"i", "l"} { // "1" ambigu
		if d := deleet(core, one); d != core {
			if _, ok := common[d]; ok {
				return true
			}
		}
	}
	return false
}

func deleet(s, one string) string {
	return strings.NewReplacer("@", "a", "4", "a", "3", "e", "0", "o", "$", "s", "5", "s", "7", "t", "1", one, "!", one).Replace(s)
}

// sequential: sebagian besar pasangan huruf bersebelahan hanya selisih 0 atau ±1 (abcdefghijkl, 123456789012, aaaaaaaaaaaa).
func sequential(s string) bool {
	rs := []rune(s)
	if len(rs) < 2 {
		return false
	}
	close := 0
	for i := 1; i < len(rs); i++ {
		d := int(rs[i]) - int(rs[i-1])
		if d >= -1 && d <= 1 {
			close++
		}
	}
	return float64(close)/float64(len(rs)-1) >= 0.8
}

var kbdRows = []string{"qwertyuiop", "asdfghjkl", "zxcvbnm", "1234567890", "qwertyuiopasdfghjklzxcvbnm", "1qaz2wsx3edc4rfv5tgb", "qazwsxedcrfvtgbyhnujmikolp"}

func reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

// keyboardWalk: seluruh password (tanpa angka/simbol di ujung) adalah potongan baris keyboard, atau gabungannya diulang.
func keyboardWalk(low string) bool {
	core := strings.TrimFunc(low, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, row := range kbdRows {
		for _, r := range []string{row, reverse(row)} {
			if strings.Contains(r, core) || strings.Contains(r+r, core) {
				return len(core) >= 8
			}
		}
	}
	return false
}

// repeatedBlock: password = blok (1–6 karakter) yang diulang, mis. "abcabcabcabc".
func repeatedBlock(s string) bool {
	rs := []rune(s)
	for b := 1; b <= 6 && b <= len(rs)/2; b++ {
		ok := true
		for i := b; i < len(rs); i++ {
			if rs[i] != rs[i-b] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
