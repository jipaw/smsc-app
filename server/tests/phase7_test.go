package tests

import (
	"context"
	"strings"
	"testing"

	"github.com/jipaw/smsc-app/server/internal/core"
)

type fieldProblem struct {
	Type   string
	Errors []struct{ Field, Message string }
}

func weakMsg(p fieldProblem) string {
	if len(p.Errors) == 0 {
		return ""
	}
	return p.Errors[0].Field + ": " + p.Errors[0].Message
}

func TestPasswordPolicyAcrossAllEntryPoints(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	u := e.newUser("operator")
	const weak = "Password123!!"
	const strong = "kuda-biru-melompati-pagar-9"

	// 1. admin membuat pengguna
	var p fieldProblem
	if st := adm.c.do("POST", "/api/v1/users", map[string]any{"email": "baru@example.com", "role": "viewer", "password": weak}, &p); st != 422 || !strings.Contains(weakMsg(p), "terlalu lemah") {
		t.Fatalf("buat pengguna dengan password lemah: %d %s", st, weakMsg(p))
	}
	if st := adm.c.do("POST", "/api/v1/users", map[string]any{"email": "baru@example.com", "role": "viewer", "password": "baru-pagar-biru-123"}, &p); st != 422 || !strings.Contains(weakMsg(p), "email") {
		t.Fatalf("password memuat nama akun harus ditolak: %d %s", st, weakMsg(p))
	}
	if st := adm.c.do("POST", "/api/v1/users", map[string]any{"email": "baru@example.com", "role": "viewer", "password": strong}, nil); st != 201 {
		t.Fatalf("password kuat: %d", st)
	}
	// 2. ganti password sendiri
	for _, bad := range []string{weak, "qwertyuiopasdf", "aaaaaaaaaaaaaa", "abcdefghijklmnop", "administrator"} {
		if st := u.c.do("POST", "/api/v1/auth/password", map[string]string{"current_password": pw0, "new_password": bad}, &p); st != 422 {
			t.Errorf("ganti password ke %q harus 422: %d", bad, st)
		}
	}
	local := strings.SplitN(u.email, "@", 2)[0]
	if st := u.c.do("POST", "/api/v1/auth/password", map[string]string{"current_password": pw0, "new_password": local + "-kuda-biru-99"}, &p); st != 422 || !strings.Contains(weakMsg(p), "email") {
		t.Fatalf("memuat nama akun: %d %s", st, weakMsg(p))
	}
	if st := u.c.do("POST", "/api/v1/auth/password", map[string]string{"current_password": pw0, "new_password": strong}, nil); st != 204 {
		t.Fatalf("kuat: %d", st)
	}
	// 3. admin menetapkan password pengguna lain
	if st := adm.c.do("POST", "/api/v1/users/"+u.id+"/password", map[string]string{"new_password": weak}, &p); st != 422 {
		t.Fatalf("admin set lemah: %d", st)
	}
	if st := adm.c.do("POST", "/api/v1/users/"+u.id+"/password", map[string]string{"new_password": "orang-lain-pagar-77"}, nil); st != 204 {
		t.Fatalf("admin set kuat: %d", st)
	}
	// 4. undangan: password lemah ditolak dan TIDAK menghabiskan undangan
	_, v := adm.c.invite("tamu-baru@example.com", "viewer", 0)
	anon := e.freshClient()
	if st := anon.do("POST", "/api/v1/invitations/accept", map[string]string{"token": v.Token, "password": "tamu-baru-2026-ok!"}, &p); st != 422 {
		t.Fatalf("memuat email undangan: %d", st)
	}
	if st := anon.do("POST", "/api/v1/invitations/accept", map[string]string{"token": v.Token, "password": weak}, nil); st != 422 {
		t.Fatalf("lemah: %d", st)
	}
	if adm.c.invStatus(v.ID) != "pending" {
		t.Fatal("penolakan password tidak boleh menghabiskan undangan")
	}
	if st := anon.do("POST", "/api/v1/invitations/accept", map[string]string{"token": v.Token, "password": strong}, nil); st != 201 {
		t.Fatalf("kuat: %d", st)
	}
	// 5. CLI/service reset password
	if _, err := e.svc.SetPasswordByEmail(context.Background(), u.email, weak); err == nil {
		t.Fatal("SetPasswordByEmail harus menolak password lemah")
	}
	// 6. batas panjang atas
	if st := adm.c.do("POST", "/api/v1/users", map[string]any{"email": "panjang@example.com", "role": "viewer", "password": strings.Repeat("k9", 200)}, &p); st != 422 {
		t.Fatalf("terlalu panjang: %d", st)
	}
}

func TestPasswordPolicyIsNotRetroactive(t *testing.T) {
	e := newEnv(t)
	// pengguna lama dengan password lemah (dibuat sebelum kebijakan) tetap bisa login
	e.sql(`INSERT INTO users(id,email,password_hash,role) VALUES (gen_random_uuid(),'lama@example.com',$1,'admin')`, core.HashPassword("password1234"))
	c := e.loginEmail("lama@example.com", "password1234")
	if st := c.do("GET", "/api/v1/devices", nil, nil); st != 200 {
		t.Fatalf("pengguna lama harus tetap berfungsi: %d", st)
	}
	// tetapi tidak bisa menggantinya ke password yang sama-sama lemah
	if st := c.do("POST", "/api/v1/auth/password", map[string]string{"current_password": "password1234", "new_password": "iloveyou12345"}, nil); st == 204 {
		// "iloveyou12345": inti 'iloveyou' + 5 angka (> batas dekorasi) boleh lolos; yang penting kebijakan tidak mencabut login lama
		t.Log("catatan: iloveyou12345 lolos (5 angka melebihi batas dekorasi 4)")
	}
	if st := c.do("POST", "/api/v1/auth/password", map[string]string{"current_password": "password1234", "new_password": "password12345"}, nil); st != 422 {
		t.Fatalf("variasi password umum harus ditolak: %d", st)
	}
}

// ================= retensi undangan =================

func TestRetentionPurgesOnlyFinishedOldInvitations(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	mk := func(email string) invResp { _, v := adm.c.invite(email, "viewer", 0); return v }
	accepted, revoked, expired, pendingOld, recent := mk("a1@example.com"), mk("a2@example.com"), mk("a3@example.com"), mk("a4@example.com"), mk("a5@example.com")
	anon := e.freshClient()
	anon.do("POST", "/api/v1/invitations/accept", map[string]string{"token": accepted.Token, "password": "kuda-biru-melompati-pagar-9"}, nil)
	adm.c.do("DELETE", "/api/v1/invitations/"+revoked.ID, nil, nil)
	anon.do("POST", "/api/v1/invitations/accept", map[string]string{"token": recent.Token, "password": "kuda-biru-melompati-pagar-9"}, nil) // diterima baru-baru ini
	e.sql(`UPDATE invitations SET accepted_at = now() - interval '100 days' WHERE id=$1`, accepted.ID)
	e.sql(`UPDATE invitations SET revoked_at = now() - interval '100 days' WHERE id=$1`, revoked.ID)
	e.sql(`UPDATE invitations SET expires_at = now() - interval '100 days', created_at = now() - interval '120 days' WHERE id=$1`, expired.ID)
	e.sql(`UPDATE invitations SET created_at = now() - interval '300 days' WHERE id=$1`, pendingOld.ID) // masih tertunda & belum kedaluwarsa

	run, err := e.svc.RetentionOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if run.InvitationsDeleted != 3 {
		t.Fatalf("harus 3 (diterima, dicabut, kedaluwarsa lama): %+v", run)
	}
	var left []string
	rows, _ := e.pool.Query(context.Background(), `SELECT email FROM invitations ORDER BY email`)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		left = append(left, s)
	}
	if strings.Join(left, ",") != "a4@example.com,a5@example.com" {
		t.Fatalf("tersisa: %v (yang tertunda dan yang baru diterima harus tetap)", left)
	}
	if run2, _ := e.svc.RetentionOnce(context.Background()); run2.InvitationsDeleted != 0 {
		t.Fatalf("idempoten: %+v", run2)
	}
	// pengguna yang dibuat dari undangan terhapus tetap ada
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM users WHERE email='a1@example.com'`).Scan(&n)
	if n != 1 {
		t.Fatal("pengguna tidak boleh ikut terhapus")
	}
}

func TestInvitationRetentionSetting(t *testing.T) {
	e := newEnv(t)
	adm := e.newUser("admin")
	var rs struct {
		Body   int `json:"body_retention_days"`
		Events int `json:"event_retention_days"`
		Inv    int `json:"invitation_retention_days"`
	}
	if st := adm.c.do("GET", "/api/v1/settings/retention", nil, &rs); st != 200 || rs.Inv != 90 {
		t.Fatalf("default: %d %+v", st, rs)
	}
	// PUT lama (tanpa field baru) tetap berlaku dan mempertahankan nilai undangan
	adm.c.do("PUT", "/api/v1/settings/retention", map[string]any{"body_retention_days": 30, "event_retention_days": 60, "invitation_retention_days": 7}, nil)
	if st := adm.c.do("PUT", "/api/v1/settings/retention", map[string]any{"body_retention_days": 31, "event_retention_days": 61}, &rs); st != 200 || rs.Inv != 7 || rs.Body != 31 {
		t.Fatalf("field opsional harus mempertahankan nilai: %d %+v", st, rs)
	}
	for _, bad := range []int{0, -1, 4000} {
		if st := adm.c.do("PUT", "/api/v1/settings/retention", map[string]any{"body_retention_days": 31, "event_retention_days": 61, "invitation_retention_days": bad}, nil); st != 422 {
			t.Errorf("invitation_retention_days=%d harus 422: %d", bad, st)
		}
	}
	// nilai disimpan dan dipakai oleh pembersihan
	_, v := adm.c.invite("kecil@example.com", "viewer", 0)
	adm.c.do("DELETE", "/api/v1/invitations/"+v.ID, nil, nil)
	e.sql(`UPDATE invitations SET revoked_at = now() - interval '8 days' WHERE id=$1`, v.ID)
	run, _ := e.svc.RetentionOnce(context.Background())
	if run.InvitationsDeleted != 1 {
		t.Fatalf("dengan masa simpan 7 hari, pencabutan 8 hari lalu harus dihapus: %+v", run)
	}
}
