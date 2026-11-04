package core

import (
	"context"
	"encoding/json"
	"errors"
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
