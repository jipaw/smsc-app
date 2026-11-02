package sms

import (
	"strings"
	"testing"
)

func TestSegmenGSM7(t *testing.T) { // T-SEG-01
	for n, want := range map[int]int{1: 1, 160: 1, 161: 2, 306: 2, 307: 3} {
		in := Analyze(strings.Repeat("a", n))
		if in.Encoding != GSM7 || in.Parts != want {
			t.Errorf("gsm7 n=%d: %+v mau parts=%d", n, in, want)
		}
	}
}

func TestSegmenUCS2(t *testing.T) { // T-SEG-02
	for n, want := range map[int]int{1: 1, 70: 1, 71: 2, 134: 2, 135: 3} {
		in := Analyze(strings.Repeat("ä", n-1) + "ж") // 'ж' memaksa UCS-2
		if in.Encoding != UCS2 || in.Parts != want {
			t.Errorf("ucs2 n=%d: %+v mau parts=%d", n, in, want)
		}
	}
}

func TestExtensiDanEmoji(t *testing.T) { // T-SEG-03
	if in := Analyze("{}"); in.Encoding != GSM7 || in.Units != 4 {
		t.Errorf("ext GSM harus 2 septet/karakter: %+v", in)
	}
	if in := Analyze(strings.Repeat("€", 81)); in.Parts != 2 { // 162 septet
		t.Errorf("81 euro = 162 septet = 2 segmen: %+v", in)
	}
	if in := Analyze("hi 😀"); in.Encoding != UCS2 || in.Units != 5 { // 'h','i',' ' + surrogate pair(2)
		t.Errorf("emoji = 2 unit UTF-16: %+v", in)
	}
}

func TestNormalize(t *testing.T) { // T-PH-01
	for _, in := range []string{"08123456789", "+62 812-3456-789", "628123456789"} {
		got, err := Normalize(in, "ID")
		if err != nil || got != "+628123456789" {
			t.Errorf("%q -> %q err=%v", in, got, err)
		}
	}
	for _, bad := range []string{"0812", "", "abc", "12345"} {
		if _, err := Normalize(bad, "ID"); err == nil {
			t.Errorf("%q harus invalid", bad)
		}
	}
}

func TestRender(t *testing.T) { // T-TPL-01
	out, miss := Render("Halo {{name}}, {{ amount }} / {{due}}", map[string]string{"name": " Budi ", "amount": "150000", "extra": "x"})
	if out != "Halo Budi, 150000 / {{due}}" || len(miss) != 1 || miss[0] != "due" {
		t.Fatalf("out=%q miss=%v", out, miss)
	}
	if v := Variables("{{a}} {{b}} {{a}}"); len(v) != 2 || v[0] != "a" {
		t.Fatalf("variables=%v", v)
	}
}
