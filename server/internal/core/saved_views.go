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
	Shared    bool       `json:"shared"` // dibagikan ke seluruh tim (hanya-baca bagi pengguna lain)
	Mine      bool       `json:"mine"`   // milik pengguna yang membaca
	Owner     string     `json:"owner"`  // bagian lokal email pemilik (sebelum @), untuk label tampilan orang lain
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
	if err := r.Scan(&v.ID, &v.Name, &raw, &v.CreatedAt, &v.UpdatedAt, &v.Shared, &v.Mine, &v.Owner); err != nil {
		return v, err
	}
	_ = json.Unmarshal(raw, &v.Filter)
	if v.Filter.Status == nil {
		v.Filter.Status = []string{}
	}
	return v, nil
}

// viewSel: $1 = pengguna yang membaca (menentukan mine); pemilik diberi label bagian lokal email saja agar email penuh tidak bocor ke viewer.
const viewSel = `v.id, v.name, v.filter, v.created_at, v.updated_at, v.shared, v.user_id = $1, split_part(u.email, '@', 1)`

var errShareNumber = Validation(FieldError{"shared", "tampilan dengan filter nomor tidak boleh dibagikan (awalan nomor akan terlihat oleh viewer yang nomornya di-masking); kosongkan nomor atau jangan bagikan"})

func (s *Service) ListSavedViews(ctx context.Context, user uuid.UUID) ([]SavedView, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+viewSel+` FROM saved_views v JOIN users u ON u.id=v.user_id WHERE v.user_id=$1 OR v.shared ORDER BY (v.user_id <> $1), lower(v.name)`, user)
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

func (s *Service) CreateSavedView(ctx context.Context, user uuid.UUID, name string, f ViewFilter, shared bool) (SavedView, error) {
	name, err := cleanViewName(name)
	if err != nil {
		return SavedView{}, err
	}
	nf, err := f.normalize()
	if err != nil {
		return SavedView{}, err
	}
	if shared && nf.To != "" {
		return SavedView{}, errShareNumber
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
		id := newID()
		if _, e = tx.Exec(ctx, `INSERT INTO saved_views(id, user_id, name, filter, shared) VALUES ($1,$2,$3,$4,$5)`, id, user, name, raw, shared); e != nil {
			return e
		}
		v, e = scanView(tx.QueryRow(ctx, `SELECT `+viewSel+` FROM saved_views v JOIN users u ON u.id=v.user_id WHERE v.id=$2`, user, id))
		if e == nil && shared {
			e = s.notifyAllExcept(ctx, tx, user, "saved_view.shared", map[string]any{"view_id": id.String(), "name": name, "owner": v.Owner}, id.String())
			if e == nil {
				e = s.emailShared(ctx, tx, user, v.Owner, name)
			}
		}
		return e
	})
	if isUnique(err, "") {
		return SavedView{}, dupView()
	}
	if err == nil && v.Shared {
		s.signalNotifications()
	}
	return v, err
}

// UpdateSavedView mengganti nama/filter (dan status berbagi bila shared != nil); tampilan milik pengguna lain dilaporkan 404.
// Tampilan yang dibagikan tidak boleh memuat filter nomor.
func (s *Service) UpdateSavedView(ctx context.Context, user, id uuid.UUID, name string, f ViewFilter, shared *bool) (SavedView, error) {
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
	newlyShared := false
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		var cur bool
		if e := tx.QueryRow(ctx, `SELECT shared FROM saved_views WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, user).Scan(&cur); e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return NotFound("tampilan")
			}
			return e
		}
		final := cur
		if shared != nil {
			final = *shared
		}
		if final && nf.To != "" {
			return errShareNumber
		}
		if _, e := tx.Exec(ctx, `UPDATE saved_views SET name=$3, filter=$4, shared=$5, updated_at=now() WHERE id=$1 AND user_id=$2`, id, user, name, raw, final); e != nil {
			return e
		}
		var e error
		v, e = scanView(tx.QueryRow(ctx, `SELECT `+viewSel+` FROM saved_views v JOIN users u ON u.id=v.user_id WHERE v.id=$2`, user, id))
		if e == nil && final && !cur { // baru dibagikan (bukan sekadar disunting)
			newlyShared = true
			e = s.notifyAllExcept(ctx, tx, user, "saved_view.shared", map[string]any{"view_id": id.String(), "name": name, "owner": v.Owner}, id.String())
			if e == nil {
				e = s.emailShared(ctx, tx, user, v.Owner, name)
			}
		}
		return e
	})
	if isUnique(err, "") {
		return SavedView{}, dupView()
	}
	if err == nil && newlyShared {
		s.signalNotifications()
	}
	return v, err
}

// DeleteSavedView menghapus tampilan milik sendiri. Admin juga boleh menghapus tampilan BERBAGI milik orang lain (moderasi, diaudit);
// tampilan pribadi orang lain tetap tak terlihat (404) bagi siapa pun.
func (s *Service) DeleteSavedView(ctx context.Context, user uuid.UUID, role string, id uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM saved_views WHERE id=$1 AND user_id=$2`, id, user)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	if role == "admin" {
		var owner uuid.UUID
		var name string
		err := s.Pool.QueryRow(ctx, `DELETE FROM saved_views WHERE id=$1 AND shared RETURNING user_id, name`, id).Scan(&owner, &name)
		if err == nil {
			s.Audit(ctx, "user:"+user.String(), "saved_view.moderate_delete", "saved_view", id.String(), map[string]any{"owner": owner, "name": name})
			// pemilik diberi tahu (bukan diam-diam): siapa (bagian lokal email admin) dan nama tampilan
			by := s.userLocal(ctx, user)
			if e := s.notifyUser(ctx, s.Pool, owner, "saved_view.moderated", map[string]any{"view_id": id.String(), "name": name, "by": by}); e == nil {
				s.signalNotifications()
			}
			s.emailModeration(ctx, owner, user, name, by)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	return NotFound("tampilan")
}
