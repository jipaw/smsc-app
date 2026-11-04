package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Tampilan tersimpan (fase 38): filter daftar transaksi yang diberi nama, milik satu pengguna (sesi dashboard; API key tidak punya).
const maxSavedViews = 20

var (
	viewStatuses = map[string]bool{"queued": true, "assigned": true, "sending": true, "sent": true, "delivered": true, "failed": true, "cancelled": true, "expired": true, "unknown": true}
	viewSources  = map[string]bool{"dashboard": true, "api": true, "campaign": true}
	viewToRe     = regexp.MustCompile(`^[+0-9]{0,20}$`)
)

// ViewFilter mencerminkan filter dashboard (kunci sama dengan parameter URL fase 37).
type ViewFilter struct {
	Status   []string `json:"status"`
	Source   string   `json:"source"`
	To       string   `json:"to"`
	Since    string   `json:"since"`   // format datetime-local "2006-01-02T15:04" (waktu lokal browser)
	Partial  string   `json:"partial"` // "" | partial | allsent
	Campaign string   `json:"campaign"`
}

type SavedView struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	Filter    ViewFilter `json:"filter"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// normalize memvalidasi KETAT (beda dengan URL yang membuang nilai buruk): nilai tak dikenal = 422.
func (f ViewFilter) normalize() (ViewFilter, error) {
	var errs []FieldError
	out := ViewFilter{Status: []string{}}
	seen := map[string]bool{}
	for _, s := range f.Status {
		if !viewStatuses[s] {
			errs = append(errs, FieldError{"filter.status", "status tidak dikenal: " + s})
			continue
		}
		if !seen[s] {
			seen[s] = true
			out.Status = append(out.Status, s)
		}
	}
	if f.Source != "" && !viewSources[f.Source] {
		errs = append(errs, FieldError{"filter.source", "harus dashboard, api, atau campaign"})
	}
	out.Source = f.Source
	if !viewToRe.MatchString(f.To) {
		errs = append(errs, FieldError{"filter.to", "hanya + dan angka, maksimal 20 karakter"})
	}
	out.To = f.To
	if f.Since != "" {
		if _, err := time.Parse("2006-01-02T15:04", f.Since); err != nil {
			errs = append(errs, FieldError{"filter.since", "format YYYY-MM-DDTHH:MM"})
		}
	}
	out.Since = f.Since
	if f.Partial != "" && f.Partial != "partial" && f.Partial != "allsent" {
		errs = append(errs, FieldError{"filter.partial", "harus kosong, partial, atau allsent"})
	}
	out.Partial = f.Partial
	if f.Campaign != "" {
		id, err := uuid.Parse(f.Campaign)
		if err != nil {
			errs = append(errs, FieldError{"filter.campaign", "harus UUID"})
		} else {
			out.Campaign = id.String()
		}
	}
	if len(errs) > 0 {
		return out, Validation(errs...)
	}
	return out, nil
}

func cleanViewName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if l := len([]rune(n)); l < 1 || l > 60 {
		return "", Validation(FieldError{"name", "1–60 karakter"})
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return "", Validation(FieldError{"name", "tidak boleh memuat karakter kontrol"})
		}
	}
	return n, nil
}

func scanView(r pgx.Row) (SavedView, error) {
	var v SavedView
	var raw []byte
	if err := r.Scan(&v.ID, &v.Name, &raw, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return v, err
	}
	_ = json.Unmarshal(raw, &v.Filter)
	if v.Filter.Status == nil {
		v.Filter.Status = []string{}
	}
	return v, nil
}

const viewCols = `id, name, filter, created_at, updated_at`

func (s *Service) ListSavedViews(ctx context.Context, user uuid.UUID) ([]SavedView, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+viewCols+` FROM saved_views WHERE user_id=$1 ORDER BY lower(name)`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SavedView{}
	for rows.Next() {
		v, err := scanView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func dupView() error {
	return NewProblem(http.StatusConflict, "saved_view.duplicate", "Nama sudah dipakai", "sudah ada tampilan dengan nama itu (huruf besar/kecil dianggap sama)")
}

func (s *Service) CreateSavedView(ctx context.Context, user uuid.UUID, name string, f ViewFilter) (SavedView, error) {
	name, err := cleanViewName(name)
	if err != nil {
		return SavedView{}, err
	}
	nf, err := f.normalize()
	if err != nil {
		return SavedView{}, err
	}
	raw, _ := json.Marshal(nf)
	var v SavedView
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		// kunci per pengguna agar batas jumlah tidak terlampaui oleh permintaan paralel
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('saved-views:'||$1::text))`, user); e != nil {
			return e
		}
		var n int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM saved_views WHERE user_id=$1`, user).Scan(&n); e != nil {
			return e
		}
		if n >= maxSavedViews {
			return NewProblem(http.StatusConflict, "saved_view.limit", "Terlalu banyak tampilan", "maksimal 20 tampilan tersimpan per pengguna; hapus yang tidak dipakai")
		}
		var e error
		v, e = scanView(tx.QueryRow(ctx, `INSERT INTO saved_views(id, user_id, name, filter) VALUES ($1,$2,$3,$4) RETURNING `+viewCols, newID(), user, name, raw))
		return e
	})
	if isUnique(err, "") {
		return SavedView{}, dupView()
	}
	return v, err
}

// UpdateSavedView mengganti nama dan/atau filter; tampilan milik pengguna lain dilaporkan 404 (bukan 403).
func (s *Service) UpdateSavedView(ctx context.Context, user, id uuid.UUID, name string, f ViewFilter) (SavedView, error) {
	name, err := cleanViewName(name)
	if err != nil {
		return SavedView{}, err
	}
	nf, err := f.normalize()
	if err != nil {
		return SavedView{}, err
	}
	raw, _ := json.Marshal(nf)
	v, err := scanView(s.Pool.QueryRow(ctx, `UPDATE saved_views SET name=$3, filter=$4, updated_at=now() WHERE id=$1 AND user_id=$2 RETURNING `+viewCols, id, user, name, raw))
	if errors.Is(err, pgx.ErrNoRows) {
		return SavedView{}, NotFound("tampilan")
	}
	if isUnique(err, "") {
		return SavedView{}, dupView()
	}
	return v, err
}

func (s *Service) DeleteSavedView(ctx context.Context, user, id uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM saved_views WHERE id=$1 AND user_id=$2`, id, user)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return NotFound("tampilan")
	}
	return nil
}
