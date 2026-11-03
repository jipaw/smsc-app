package core

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Undangan pengguna: admin membuat tautan sekali pakai (tidak ada pengiriman email; admin membagikan tautannya).
// Token 256-bit, hanya hash yang disimpan, tampil sekali. Penerima menetapkan password sendiri di halaman undangan,
// sehingga admin tidak pernah mengetahui password pengguna.
const (
	inviteDefaultTTLHours = 168 // 7 hari
	inviteMaxTTLHours     = 720 // 30 hari
)

var errInviteInvalid = NewProblem(http.StatusNotFound, "invitation.invalid", "Undangan tidak valid", "undangan tidak ditemukan, sudah dipakai, dicabut, atau kedaluwarsa")

type Invitation struct {
	ID         uuid.UUID  `json:"id"`
	Email      string     `json:"email"`
	Role       string     `json:"role"`
	Status     string     `json:"status"` // pending|accepted|expired|revoked
	CreatedBy  *string    `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AcceptedAt *time.Time `json:"accepted_at"`
	Token      string     `json:"token,omitempty"`      // hanya pada respons pembuatan
	InviteURL  string     `json:"invite_url,omitempty"` // idem
}

const invCols = `i.id, i.email, i.role::text, CASE WHEN i.accepted_at IS NOT NULL THEN 'accepted' WHEN i.revoked_at IS NOT NULL THEN 'revoked'
	WHEN i.expires_at <= now() THEN 'expired' ELSE 'pending' END, (SELECT email FROM users u WHERE u.id=i.created_by), i.created_at, i.expires_at, i.accepted_at`

func scanInvitation(r pgx.Row) (Invitation, error) {
	var v Invitation
	err := r.Scan(&v.ID, &v.Email, &v.Role, &v.Status, &v.CreatedBy, &v.CreatedAt, &v.ExpiresAt, &v.AcceptedAt)
	return v, err
}

func (s *Service) CreateInvitation(ctx context.Context, by uuid.UUID, email, role string, ttlHours int) (Invitation, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var errs []FieldError
	if !strings.Contains(email, "@") || len(email) < 3 {
		errs = append(errs, FieldError{"email", "format email tidak valid"})
	}
	if role != "admin" && role != "operator" && role != "viewer" {
		errs = append(errs, FieldError{"role", "harus admin, operator, atau viewer"})
	}
	if ttlHours == 0 {
		ttlHours = inviteDefaultTTLHours
	}
	if ttlHours < 1 || ttlHours > inviteMaxTTLHours {
		errs = append(errs, FieldError{"ttl_hours", "1 sampai 720"})
	}
	if len(errs) > 0 {
		return Invitation{}, Validation(errs...)
	}
	var exists bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE lower(email)=$1)`, email).Scan(&exists); err != nil {
		return Invitation{}, err
	}
	if exists {
		return Invitation{}, NewProblem(http.StatusConflict, "resource.conflict", "Konflik", "email sudah terdaftar sebagai pengguna")
	}
	token := "inv_" + randToken(32)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Invitation{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// undangan baru menggantikan undangan tertunda untuk email yang sama (tautan lama tidak berlaku lagi)
	if _, err := tx.Exec(ctx, `UPDATE invitations SET revoked_at=now() WHERE lower(email)=$1 AND accepted_at IS NULL AND revoked_at IS NULL`, email); err != nil {
		return Invitation{}, err
	}
	id := newID()
	if _, err := tx.Exec(ctx, `INSERT INTO invitations(id,email,role,token_hash,created_by,expires_at) VALUES ($1,$2,$3,$4,$5, now() + make_interval(hours => $6))`,
		id, email, role, sha(token), by, ttlHours); err != nil {
		return Invitation{}, err
	}
	v, err := scanInvitation(tx.QueryRow(ctx, `SELECT `+invCols+` FROM invitations i WHERE i.id=$1`, id))
	if err != nil {
		return Invitation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Invitation{}, err
	}
	v.Token = token
	v.InviteURL = s.Cfg.PublicBaseURL + "/invite/" + token
	return v, nil
}

func (s *Service) ListInvitations(ctx context.Context) ([]Invitation, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+invCols+` FROM invitations i ORDER BY i.created_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Invitation, error) { return scanInvitation(r) })
}

// RevokeInvitation: hanya undangan yang belum dipakai dan belum dicabut (yang kedaluwarsa boleh).
func (s *Service) RevokeInvitation(ctx context.Context, id uuid.UUID) (string, error) {
	var email string
	err := s.Pool.QueryRow(ctx, `UPDATE invitations SET revoked_at=now() WHERE id=$1 AND accepted_at IS NULL AND revoked_at IS NULL RETURNING email`, id).Scan(&email)
	if err == nil {
		return email, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	var one int
	if s.Pool.QueryRow(ctx, `SELECT 1 FROM invitations WHERE id=$1`, id).Scan(&one) != nil {
		return "", NotFound("undangan")
	}
	return "", NewProblem(http.StatusConflict, "invitation.not_pending", "Undangan tidak lagi tertunda", "undangan sudah dipakai atau dicabut")
}

type InvitationInfo struct {
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	ExpiresAt time.Time `json:"expires_at"`
}

// LookupInvitation: untuk halaman undangan (publik). Semua kegagalan memakai satu jawaban seragam.
func (s *Service) LookupInvitation(ctx context.Context, token string) (InvitationInfo, error) {
	var v InvitationInfo
	err := s.Pool.QueryRow(ctx, `SELECT email, role::text, expires_at FROM invitations
		WHERE token_hash=$1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()`, sha(token)).Scan(&v.Email, &v.Role, &v.ExpiresAt)
	if err != nil {
		return InvitationInfo{}, errInviteInvalid
	}
	return v, nil
}

// AcceptInvitation membuat pengguna dan menandai undangan terpakai dalam satu transaksi. Kegagalan validasi (mis. password
// pendek) atau konflik email tidak menghabiskan undangan.
func (s *Service) AcceptInvitation(ctx context.Context, token, password string) (User, error) {
	if len(password) < 12 {
		return User{}, Validation(FieldError{"password", "minimal 12 karakter"})
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var invID uuid.UUID
	var email, role string
	err = tx.QueryRow(ctx, `UPDATE invitations SET accepted_at=now()
		WHERE token_hash=$1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now() RETURNING id, email, role::text`, sha(token)).Scan(&invID, &email, &role)
	if err != nil {
		return User{}, errInviteInvalid
	}
	u := User{ID: newID(), Email: email, Role: role}
	if _, err := tx.Exec(ctx, `INSERT INTO users(id,email,password_hash,role) VALUES ($1,$2,$3,$4)`, u.ID, email, HashPassword(password), role); err != nil {
		if isUnique(err, "") {
			return User{}, NewProblem(http.StatusConflict, "resource.conflict", "Konflik", "email sudah terdaftar")
		}
		return User{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE invitations SET accepted_user_id=$2 WHERE id=$1`, invID, u.ID); err != nil {
		return User{}, err
	}
	return u, tx.Commit(ctx)
}

// DeleteUser menghapus pengguna yang SUDAH dinonaktifkan (dua langkah agar tidak tak sengaja). Sesi, kode pemulihan
// ikut terhapus; kolom created_by pada API key/webhook/campaign menjadi kosong (objeknya tetap ada); audit log tetap
// menyimpan email pelaku. Mengembalikan email untuk audit.
func (s *Service) DeleteUser(ctx context.Context, actor, id uuid.UUID) (string, error) {
	if id == actor {
		return "", NewProblem(http.StatusConflict, "user.self_change", "Tidak dapat menghapus diri sendiri", "minta admin lain")
	}
	var email string
	var disabled bool
	if err := s.Pool.QueryRow(ctx, `SELECT email, disabled FROM users WHERE id=$1`, id).Scan(&email, &disabled); err != nil {
		return "", NotFound("pengguna")
	}
	if !disabled {
		return "", NewProblem(http.StatusConflict, "user.must_disable_first", "Nonaktifkan dahulu", "hanya pengguna yang sudah dinonaktifkan yang dapat dihapus")
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM users WHERE id=$1 AND disabled`, id)
	return email, err
}
