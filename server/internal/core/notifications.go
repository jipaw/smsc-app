package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Notifikasi dalam aplikasi (fase 40). Disimpan per pengguna; klien diberi tahu lewat event SSE `notification.created` TANPA muatan
// (hanya sinyal untuk refetch), sehingga pengguna lain tidak melihat isi/siapa penerimanya.
type Notification struct {
	ID        uuid.UUID      `json:"id"`
	Kind      string         `json:"kind"`
	Data      map[string]any `json:"data"`
	CreatedAt time.Time      `json:"created_at"`
	ReadAt    *time.Time     `json:"read_at"`
}

// notifyAllExcept menulis satu notifikasi untuk setiap pengguna aktif selain `except`. Notifikasi sejenis untuk objek yang sama
// (dedupeKey = data->>'view_id') tidak diulang dalam 1 jam agar bolak-balik berbagi/berhenti berbagi tidak membanjiri.
func (s *Service) notifyAllExcept(ctx context.Context, db dbx, except uuid.UUID, kind string, data map[string]any, dedupeKey string) error {
	raw, _ := json.Marshal(data)
	_, err := db.Exec(ctx, `INSERT INTO notifications(id, user_id, kind, data)
		SELECT gen_random_uuid(), u.id, $2, $3::jsonb FROM users u
		WHERE NOT u.disabled AND u.id <> $1
		  AND NOT EXISTS (SELECT 1 FROM notifications n WHERE n.user_id=u.id AND n.kind=$2 AND n.data->>'view_id'=$4 AND n.created_at > now() - interval '1 hour')`,
		except, kind, raw, dedupeKey)
	return err
}

func (s *Service) notifyUser(ctx context.Context, db dbx, user uuid.UUID, kind string, data map[string]any) error {
	raw, _ := json.Marshal(data)
	_, err := db.Exec(ctx, `INSERT INTO notifications(id, user_id, kind, data) VALUES (gen_random_uuid(), $1, $2, $3::jsonb)`, user, kind, raw)
	return err
}

// signalNotifications memberi tahu klien (setelah commit) bahwa ada notifikasi baru; muatan kosong.
func (s *Service) signalNotifications() { s.Bus.Publish("notification.created", map[string]any{}) }

func (s *Service) ListNotifications(ctx context.Context, user uuid.UUID, unreadOnly bool, limit int) ([]Notification, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var unread int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id=$1 AND read_at IS NULL`, user).Scan(&unread); err != nil {
		return nil, 0, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT id, kind, data, created_at, read_at FROM notifications WHERE user_id=$1 AND (NOT $2 OR read_at IS NULL) ORDER BY created_at DESC, id LIMIT $3`, user, unreadOnly, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		var raw []byte
		if err := rows.Scan(&n.ID, &n.Kind, &raw, &n.CreatedAt, &n.ReadAt); err != nil {
			return nil, 0, err
		}
		_ = json.Unmarshal(raw, &n.Data)
		out = append(out, n)
	}
	return out, unread, rows.Err()
}

// MarkNotificationRead: milik pengguna lain/tidak ada = 404 (tidak membedakan).
func (s *Service) MarkNotificationRead(ctx context.Context, user, id uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE notifications SET read_at = COALESCE(read_at, now()) WHERE id=$1 AND user_id=$2`, id, user)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return NotFound("notifikasi")
	}
	return nil
}

func (s *Service) MarkAllNotificationsRead(ctx context.Context, user uuid.UUID) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE user_id=$1 AND read_at IS NULL`, user)
	return tag.RowsAffected(), err
}

// userLocal: bagian lokal email (label tanpa membuka email penuh).
func (s *Service) userLocal(ctx context.Context, id uuid.UUID) string {
	var l string
	if err := s.Pool.QueryRow(ctx, `SELECT split_part(email, '@', 1) FROM users WHERE id=$1`, id).Scan(&l); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	return l
}

// ---------- preferensi & email (fase 41) ----------

type UserPrefs struct {
	EmailNotifications bool `json:"email_notifications"` // moderasi tampilan berbagi milik sendiri (default aktif)
	EmailSharedViews   bool `json:"email_shared_views"`  // ada tampilan baru dibagikan orang lain (opt-in, default mati)
	EmailAvailable     bool `json:"email_available"`
}

func (s *Service) GetPrefs(ctx context.Context, user uuid.UUID) (UserPrefs, error) {
	p := UserPrefs{EmailNotifications: true, EmailAvailable: s.EmailEnabled()}
	err := s.Pool.QueryRow(ctx, `SELECT email_notifications, email_shared_views FROM user_prefs WHERE user_id=$1`, user).Scan(&p.EmailNotifications, &p.EmailSharedViews)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	return p, err
}

// SetPrefs: hanya bidang yang dikirim (non-nil) yang diubah; baris dibuat dengan default untuk bidang lain.
func (s *Service) SetPrefs(ctx context.Context, user uuid.UUID, emailNotifications, emailSharedViews *bool) (UserPrefs, error) {
	if emailNotifications == nil && emailSharedViews == nil {
		return UserPrefs{}, Validation(FieldError{"body", "isi minimal salah satu: email_notifications, email_shared_views"})
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO user_prefs(user_id, email_notifications, email_shared_views) VALUES ($1, COALESCE($2, true), COALESCE($3, false))
		ON CONFLICT (user_id) DO UPDATE SET email_notifications = COALESCE($2, user_prefs.email_notifications),
			email_shared_views = COALESCE($3, user_prefs.email_shared_views), updated_at=now()`, user, emailNotifications, emailSharedViews); err != nil {
		return UserPrefs{}, err
	}
	return s.GetPrefs(ctx, user)
}

func moderationEmail(name, by string) (subject, body string) {
	subject = "Tampilan transaksi Anda dihapus oleh admin"
	body = fmt.Sprintf(`Halo,

Tampilan transaksi yang Anda bagikan ke tim, “%s”, dihapus oleh admin %s.

Anda dapat membuatnya lagi di halaman Transaksi. Bila tampilan itu dianggap terlalu luas atau tidak sesuai,
hubungi admin untuk penjelasan.

Anda menerima email ini karena notifikasi email aktif di akun Anda. Untuk mematikannya, buka Akun > Preferensi di dashboard.
`, name, by)
	return
}

// emailModeration mengantre email ke pemilik tampilan yang dimoderasi. Tidak dikirim bila SMTP belum diatur, pengguna dinonaktifkan atau
// mematikan notifikasi email, atau sudah ada email sejenis ke alamat itu dalam 10 menit (menahan banjir bila admin menghapus banyak sekaligus;
// notifikasi dalam aplikasi tetap dibuat untuk setiap penghapusan). Kegagalan hanya dicatat: email adalah pelengkap, bukan syarat moderasi.
func (s *Service) emailModeration(ctx context.Context, owner, admin uuid.UUID, name, by string) {
	if !s.EmailEnabled() {
		return
	}
	subject, body := moderationEmail(name, by)
	enc, err := s.seal(purposeEmail, body)
	if err != nil {
		s.Log.Warn("email moderasi: segel gagal", "err", err)
		return
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO email_outbox(id, kind, to_email, subject, body, created_by)
		SELECT $2, 'saved_view_moderated', u.email, $3, $4, $5 FROM users u
		WHERE u.id=$1 AND NOT u.disabled
		  AND COALESCE((SELECT p.email_notifications FROM user_prefs p WHERE p.user_id=u.id), true)
		  AND NOT EXISTS (SELECT 1 FROM email_outbox o WHERE o.kind='saved_view_moderated' AND lower(o.to_email)=lower(u.email) AND o.created_at > now() - interval '10 minutes')`,
		owner, newID(), subject, enc, admin); err != nil {
		s.Log.Warn("email moderasi: antre gagal", "err", err)
	}
}

func sharedEmail(owner, name, link string) (subject, body string) {
	subject = "Tampilan transaksi baru dibagikan di SMSC Gateway"
	body = fmt.Sprintf(`Halo,

%s membagikan tampilan transaksi “%s” ke tim.

Buka halaman Transaksi lalu pilih tampilan itu di bagian "Dibagikan tim":
%s

Anda menerima email ini karena memilih notifikasi email untuk tampilan yang dibagikan. Untuk mematikannya, buka Akun > Preferensi notifikasi di dashboard.
`, owner, name, link)
	return
}

// emailShared mengantre email ke pengguna aktif yang OPT-IN (selain pemilik) saat tampilan baru dibagikan; dipanggil dalam transaksi yang sama
// dengan notifikasi aplikasi. Paling banyak satu email per alamat per 10 menit (tampilan lain yang dibagikan dalam jendela itu hanya muncul
// di lonceng) dan paling banyak 200 penerima.
func (s *Service) emailShared(ctx context.Context, db dbx, owner uuid.UUID, ownerLabel, name string) error {
	if !s.EmailEnabled() {
		return nil
	}
	subject, body := sharedEmail(ownerLabel, name, strings.TrimRight(s.Cfg.PublicBaseURL, "/")+"/transactions")
	enc, err := s.seal(purposeEmail, body)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO email_outbox(id, kind, to_email, subject, body, created_by)
		SELECT gen_random_uuid(), 'saved_view_shared', u.email, $2, $3, $1 FROM users u JOIN user_prefs p ON p.user_id=u.id
		WHERE NOT u.disabled AND u.id <> $1 AND p.email_shared_views
		  AND NOT EXISTS (SELECT 1 FROM email_outbox o WHERE o.kind='saved_view_shared' AND lower(o.to_email)=lower(u.email) AND o.created_at > now() - interval '10 minutes')
		LIMIT 200`, owner, subject, enc)
	return err
}
