package core

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TailnetIdentity: hasil WhoIs untuk permintaan yang masuk lewat listener tailnet smscd (tsnet). Tidak ada untuk
// permintaan lewat loopback/HTTP_ADDR. NodeID = StableNodeID (tetap walau IP/nama berubah).
type TailnetIdentity struct {
	NodeID    string
	NodeName  string
	Tags      []string
	UserLogin string // kosong untuk node ber-tag
	Addr      string
}

type tailnetKey struct{}

func WithTailnetIdentity(ctx context.Context, id *TailnetIdentity) context.Context {
	return context.WithValue(ctx, tailnetKey{}, id)
}

func TailnetIdentityFrom(ctx context.Context) *TailnetIdentity {
	id, _ := ctx.Value(tailnetKey{}).(*TailnetIdentity)
	return id
}

// DeviceTailnet: identitas node yang terikat ke device (null bila belum pernah tersambung lewat listener tailnet).
type DeviceTailnet struct {
	NodeID     string    `json:"node_id"`
	NodeName   string    `json:"node_name"`
	Tags       []string  `json:"tags"`
	VerifiedAt time.Time `json:"verified_at"`
}

func (s *Service) tailnetTagOK(id *TailnetIdentity) bool {
	for _, t := range id.Tags {
		if slices.Contains(s.Cfg.Tailnet.DeviceTags, t) {
			return true
		}
	}
	return false
}

// checkEnrollTailnet dipanggil sebelum enrollment dikonsumsi. enforce: wajib lewat tailnet dan node ber-tag device.
func (s *Service) checkEnrollTailnet(ctx context.Context) (*TailnetIdentity, error) {
	id := TailnetIdentityFrom(ctx)
	if s.Cfg.Tailnet.DevicePolicy != "enforce" {
		return id, nil
	}
	if id == nil {
		s.M.TailnetChecks.WithLabelValues("rejected_no_tailnet").Inc()
		return nil, NewProblem(http.StatusForbidden, "device.tailnet_required", "Harus lewat tailnet", "enrollment hanya diterima dari node tailnet (TAILNET_DEVICE_POLICY=enforce)")
	}
	if !s.tailnetTagOK(id) {
		s.M.TailnetChecks.WithLabelValues("rejected_tag").Inc()
		return nil, NewProblem(http.StatusForbidden, "device.tailnet_tag", "Node tailnet tidak diizinkan", "node harus ber-tag salah satu dari TAILNET_DEVICE_TAGS")
	}
	return id, nil
}

func bindTailnet(ctx context.Context, tx pgx.Tx, devID uuid.UUID, id *TailnetIdentity) error {
	if id == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE devices SET tailnet_node_id=$2, tailnet_node_name=$3, tailnet_tags=$4, tailnet_verified_at=now() WHERE id=$1`,
		devID, id.NodeID, id.NodeName, id.Tags)
	return err
}

// checkDeviceTailnet dipanggil setiap autentikasi device (WebSocket dan HTTP device). Device terikat ke node tailnet
// pertama yang dipakainya; secret device yang bocor tidak dapat dipakai dari node lain (enforce) atau tercatat (record).
func (s *Service) checkDeviceTailnet(ctx context.Context, devID uuid.UUID) error {
	pol := s.Cfg.Tailnet.DevicePolicy
	if pol == "off" || pol == "" {
		return nil
	}
	id := TailnetIdentityFrom(ctx)
	if id == nil {
		if pol == "enforce" {
			s.M.TailnetChecks.WithLabelValues("rejected_no_tailnet").Inc()
			return NewProblem(http.StatusForbidden, "device.tailnet_required", "Harus lewat tailnet", "device hanya diterima dari node tailnet (TAILNET_DEVICE_POLICY=enforce)")
		}
		return nil
	}
	if pol == "enforce" && !s.tailnetTagOK(id) {
		s.M.TailnetChecks.WithLabelValues("rejected_tag").Inc()
		return NewProblem(http.StatusForbidden, "device.tailnet_tag", "Node tailnet tidak diizinkan", "node harus ber-tag salah satu dari TAILNET_DEVICE_TAGS")
	}
	var bound *string
	if err := s.Pool.QueryRow(ctx, `SELECT tailnet_node_id FROM devices WHERE id=$1`, devID).Scan(&bound); err != nil {
		return err
	}
	switch {
	case bound == nil: // device lama (sebelum kebijakan) atau enroll lewat loopback: ikat ke node ini (trust on first use)
		_, err := s.Pool.Exec(ctx, `UPDATE devices SET tailnet_node_id=$2, tailnet_node_name=$3, tailnet_tags=$4, tailnet_verified_at=now() WHERE id=$1 AND tailnet_node_id IS NULL`,
			devID, id.NodeID, id.NodeName, id.Tags)
		if err == nil {
			s.Audit(ctx, "device:"+devID.String(), "device.tailnet_bound", "device", devID.String(), map[string]any{"node": id.NodeName, "node_id": id.NodeID})
		}
		s.M.TailnetChecks.WithLabelValues("bound").Inc()
		return err
	case *bound != id.NodeID:
		s.Audit(ctx, "device:"+devID.String(), "device.tailnet_mismatch", "device", devID.String(),
			map[string]any{"expected_node_id": *bound, "node_id": id.NodeID, "node": id.NodeName, "policy": pol})
		if pol == "enforce" {
			s.M.TailnetChecks.WithLabelValues("rejected_node").Inc()
			return NewProblem(http.StatusForbidden, "device.tailnet_node_mismatch", "Node tailnet berbeda", "kredensial device ini terikat ke node tailnet lain; daftarkan ulang device bila HP diganti")
		}
		s.M.TailnetChecks.WithLabelValues("mismatch_recorded").Inc()
		return nil
	default:
		_, _ = s.Pool.Exec(ctx, `UPDATE devices SET tailnet_node_name=$2, tailnet_tags=$3, tailnet_verified_at=now() WHERE id=$1`, devID, id.NodeName, id.Tags)
		s.M.TailnetChecks.WithLabelValues("ok").Inc()
		return nil
	}
}
