package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Grup SCIM → peran (fase 13). IdP mendorong grup + anggota; pemetaan nama grup → peran dari environment
// (SCIM_ADMIN_GROUPS / SCIM_OPERATOR_GROUPS / SCIM_VIEWER_GROUPS, tak peka huruf). Peran efektif pengguna terkelola =
// tertinggi(atribut roles langsung, peran dari semua grupnya); tanpa keduanya → SCIM_DEFAULT_ROLE.
// Grup yang tak dipetakan tetap disimpan (informasi) tetapi tidak memengaruhi peran.

const (
	SCIMGroupSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"
	scimMaxMembers  = 10000
)

// multiQuery: pgx.Tx maupun pool.
type multiQuery interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

var roleRank = map[string]int{"": 0, "viewer": 1, "operator": 2, "admin": 3}

// effectiveRole menggabungkan peran langsung (roles) dan peran dari grup; tertinggi menang.
func (s *Service) effectiveRole(direct *string, group string) string {
	best := ""
	if direct != nil {
		best = *direct
	}
	if roleRank[group] > roleRank[best] {
		best = group
	}
	if best == "" {
		return s.Cfg.SCIM.DefaultRole
	}
	return best
}

// mappedRole: peran untuk nama grup ("" = tidak dipetakan). Pemetaan admin diabaikan bila SCIM_ALLOW_ADMIN_ROLE mati.
func (s *Service) mappedRole(name string) string {
	r := s.Cfg.SCIM.GroupRoles[strings.ToLower(strings.TrimSpace(name))]
	if r == "admin" && !s.Cfg.SCIM.AllowAdminRole {
		return ""
	}
	return r
}

// groupRole: peran tertinggi dari grup-grup pengguna ("" = tidak ada grup yang dipetakan).
func (s *Service) groupRole(ctx context.Context, db multiQuery, userID uuid.UUID) (string, error) {
	rows, err := db.Query(ctx, `SELECT g.display_name FROM scim_group_members m JOIN scim_groups g ON g.id=m.group_id WHERE m.user_id=$1`, userID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	best := ""
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return "", err
		}
		if r := s.mappedRole(n); roleRank[r] > roleRank[best] {
			best = r
		}
	}
	return best, rows.Err()
}

type roleChange struct {
	ID       uuid.UUID
	From, To string
}

// recomputeRoles menghitung ulang peran efektif untuk pengguna terkait di dalam transaksi pemanggil. Akun admin tidak disentuh
// bila SCIM_ALLOW_ADMIN_ROLE mati, pengguna yang tidak terkelola dilewati (kecuali mode adopsi), dan admin aktif terakhir tidak
// pernah diturunkan (409 mutability — seluruh transaksi dibatalkan oleh pemanggil).
func (s *Service) recomputeRoles(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) ([]roleChange, error) {
	sorted := append([]uuid.UUID(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].String() < sorted[j].String() }) // urutan tetap → tanpa deadlock
	var out []roleChange
	for _, id := range sorted {
		var role string
		var direct *string
		var disabled, managed bool
		err := tx.QueryRow(ctx, `SELECT role::text, scim_direct_role::text, disabled, scim_managed FROM users WHERE id=$1 FOR UPDATE`, id).Scan(&role, &direct, &disabled, &managed)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if (!managed && !s.Cfg.SCIM.AdoptExisting) || (role == "admin" && !s.Cfg.SCIM.AllowAdminRole) {
			continue
		}
		if !managed && direct == nil { // adopsi lewat grup: peran saat ini menjadi peran langsung
			r := role
			direct = &r
		}
		gr, err := s.groupRole(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		nr := s.effectiveRole(direct, gr)
		if nr == role {
			continue
		}
		if role == "admin" && !disabled {
			var others int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='admin' AND NOT disabled AND id<>$1`, id).Scan(&others); err != nil {
				return nil, err
			}
			if others == 0 {
				return nil, serr(409, "mutability", "perubahan grup akan menurunkan admin aktif terakhir")
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET role=$2::user_role, scim_direct_role=$3::user_role, scim_managed=true, scim_updated_at=now() WHERE id=$1`, id, nr, direct); err != nil {
			return nil, err
		}
		out = append(out, roleChange{id, role, nr})
	}
	return out, nil
}

func (s *Service) auditRoleChanges(ctx context.Context, actor, via string, ch []roleChange) {
	for _, c := range ch {
		s.Audit(ctx, actor, "scim.user_update", "user", c.ID.String(), map[string]any{"role": map[string]string{"from": c.From, "to": c.To}, "via": via})
	}
}

// SCIMReconcileState: hasil rekonsiliasi terakhir (disimpan di settings.scim_reconcile_last).
type SCIMReconcileState struct {
	At      time.Time `json:"at"`
	Changed int       `json:"changed"`
	Error   string    `json:"error,omitempty"`
	Runs    int       `json:"runs"`
}

// LastSCIMReconcile membaca hasil run terakhir dari tabel settings (dibagi semua instance, bertahan melewati restart).
func (s *Service) LastSCIMReconcile(ctx context.Context) SCIMReconcileState {
	var st SCIMReconcileState
	var raw []byte
	if err := s.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE key='scim_reconcile_last'`).Scan(&raw); err == nil {
		_ = json.Unmarshal(raw, &st)
	}
	return st
}

func (s *Service) reconcileOnce(ctx context.Context) {
	if !s.SCIMEnabled() || len(s.Cfg.SCIM.GroupRoles) == 0 {
		return // tidak ada yang direkonsiliasi: jangan mencatat run (metrik/alert tidak muncul untuk deployment tanpa SCIM grup)
	}
	n, err := s.ReconcileSCIMRoles(ctx)
	if ctx.Err() != nil {
		return
	}
	st := SCIMReconcileState{At: time.Now().UTC(), Changed: n}
	if err != nil {
		st.Error = err.Error()
		s.M.SCIMReconcileRuns.WithLabelValues("error").Inc()
		s.Log.Error("rekonsiliasi peran SCIM gagal", "err", err)
	} else {
		s.M.SCIMReconcileRuns.WithLabelValues("ok").Inc()
		s.M.SCIMReconcileChanged.Add(float64(n))
	}
	if err == nil && n > 0 {
		s.Log.Info("rekonsiliasi peran SCIM", "diubah", n)
	}
	// "runs" dinaikkan atomik di SQL; kegagalan menyimpan hanya dicatat (tidak boleh menghentikan worker)
	raw, _ := json.Marshal(st)
	if _, err := s.Pool.Exec(ctx, `INSERT INTO settings(key,value) VALUES ('scim_reconcile_last', $1::jsonb || '{"runs":1}'::jsonb)
		ON CONFLICT (key) DO UPDATE SET value = $1::jsonb || jsonb_build_object('runs', COALESCE((settings.value->>'runs')::int,0)+1), updated_at=now()`, raw); err != nil && ctx.Err() == nil {
		s.Log.Error("menyimpan status rekonsiliasi SCIM gagal", "err", err)
	}
}

// RunSCIMReconcile menjalankan rekonsiliasi saat start lalu berkala (SCIM_RECONCILE_INTERVAL; <=0 = hanya sekali).
// Kegagalan (mis. guard admin terakhir, DB sesaat mati) hanya dicatat dan dicoba lagi pada tick berikutnya.
func (s *Service) RunSCIMReconcile(ctx context.Context) {
	s.reconcileOnce(ctx)
	if s.Cfg.SCIMReconcileInterval <= 0 {
		return
	}
	t := time.NewTicker(s.Cfg.SCIMReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.reconcileOnce(ctx)
		}
	}
}

// ReconcileSCIMRoles menerapkan ulang pemetaan ke semua pengguna terkelola (mis. setelah SCIM_*_GROUPS diubah dan server restart).
func (s *Service) ReconcileSCIMRoles(ctx context.Context) (int, error) {
	if !s.SCIMEnabled() || len(s.Cfg.SCIM.GroupRoles) == 0 {
		return 0, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('smsc-scim-admin-guard'))`); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT user_id FROM scim_group_members`)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	ch, err := s.recomputeRoles(ctx, tx, ids)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	s.auditRoleChanges(ctx, "system", "reconcile", ch)
	return len(ch), nil
}

// ---------- model ----------

type SCIMMember struct {
	ID       uuid.UUID
	UserName string
}

type SCIMGroup struct {
	ID          uuid.UUID
	DisplayName string
	ExternalID  *string
	Members     []SCIMMember
	Role        string // peran hasil pemetaan ("" = tidak dipetakan)
	Created     time.Time
	Updated     time.Time
}

type MemberOp struct {
	Kind string // add | remove | replace | clear
	IDs  []uuid.UUID
}

type SCIMGroupChange struct {
	DisplayName Opt[string]
	ExternalID  Opt[string]
	MemberOps   []MemberOp
}

type GroupRef struct {
	ID      uuid.UUID
	Display string
}

// SCIMUserGroups: grup-grup beberapa pengguna sekaligus (atribut `groups` read-only pada User).
func (s *Service) SCIMUserGroups(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]GroupRef, error) {
	out := map[uuid.UUID][]GroupRef{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT m.user_id, g.id, g.display_name FROM scim_group_members m JOIN scim_groups g ON g.id=m.group_id WHERE m.user_id = ANY($1) ORDER BY g.display_name`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u uuid.UUID
		var g GroupRef
		if err := rows.Scan(&u, &g.ID, &g.Display); err != nil {
			return nil, err
		}
		out[u] = append(out[u], g)
	}
	return out, rows.Err()
}

const groupCols = `g.id, g.display_name, g.external_id, g.created_at, g.updated_at`

func (s *Service) loadMembers(ctx context.Context, db multiQuery, gid uuid.UUID) ([]SCIMMember, error) {
	rows, err := db.Query(ctx, `SELECT u.id, u.email FROM scim_group_members m JOIN users u ON u.id=m.user_id WHERE m.group_id=$1 ORDER BY u.email`, gid)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SCIMMember, error) {
		var m SCIMMember
		return m, r.Scan(&m.ID, &m.UserName)
	})
}

func (s *Service) SCIMGetGroup(ctx context.Context, id uuid.UUID, withMembers bool) (SCIMGroup, error) {
	var g SCIMGroup
	err := s.Pool.QueryRow(ctx, `SELECT `+groupCols+` FROM scim_groups g WHERE g.id=$1`, id).Scan(&g.ID, &g.DisplayName, &g.ExternalID, &g.Created, &g.Updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, serr(404, "", "grup %s tidak ditemukan", id)
	}
	if err != nil {
		return g, err
	}
	g.Role = s.mappedRole(g.DisplayName)
	if withMembers {
		g.Members, err = s.loadMembers(ctx, s.Pool, id)
	}
	return g, err
}

func (s *Service) SCIMListGroups(ctx context.Context, f SCIMFilter, start, count int, withMembers bool) ([]SCIMGroup, int, error) {
	if start < 1 {
		start = 1
	}
	if count < 0 {
		count = 0
	}
	if count > scimMaxCount {
		count = scimMaxCount
	}
	where, args := []string{"TRUE"}, []any{}
	for _, t := range f.Terms {
		switch t.Attr {
		case "displayName":
			args = append(args, strings.ToLower(t.Val))
			where = append(where, fmt.Sprintf("lower(g.display_name)=$%d", len(args)))
		case "externalId":
			args = append(args, t.Val)
			where = append(where, fmt.Sprintf("g.external_id=$%d", len(args)))
		case "id":
			id, err := uuid.Parse(t.Val)
			if err != nil {
				return nil, 0, nil
			}
			args = append(args, id)
			where = append(where, fmt.Sprintf("g.id=$%d", len(args)))
		default:
			return nil, 0, serr(400, "invalidFilter", "atribut filter tidak didukung untuk Groups: %s", t.Attr)
		}
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM scim_groups g WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if count == 0 {
		return []SCIMGroup{}, total, nil
	}
	args = append(args, count, start-1)
	rows, err := s.Pool.Query(ctx, fmt.Sprintf(`SELECT `+groupCols+` FROM scim_groups g WHERE %s ORDER BY g.created_at, g.id LIMIT $%d OFFSET $%d`, w, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (SCIMGroup, error) {
		var g SCIMGroup
		return g, r.Scan(&g.ID, &g.DisplayName, &g.ExternalID, &g.Created, &g.Updated)
	})
	if err != nil {
		return nil, 0, err
	}
	for i := range list {
		list[i].Role = s.mappedRole(list[i].DisplayName)
		if withMembers {
			if list[i].Members, err = s.loadMembers(ctx, s.Pool, list[i].ID); err != nil {
				return nil, 0, err
			}
		}
	}
	return list, total, nil
}

func (s *Service) lockAdminGuard(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('smsc-scim-admin-guard'))`)
	return err
}

// memberSet membaca anggota saat ini; checkVisible memastikan id anggota baru adalah pengguna yang boleh dikelola SCIM.
func (s *Service) currentMembers(ctx context.Context, tx pgx.Tx, gid uuid.UUID) (map[uuid.UUID]bool, error) {
	rows, err := tx.Query(ctx, `SELECT user_id FROM scim_group_members WHERE group_id=$1`, gid)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	m := map[uuid.UUID]bool{}
	for _, id := range ids {
		m[id] = true
	}
	return m, nil
}

func (s *Service) checkMembersVisible(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users u WHERE u.id = ANY($1) AND `+s.scimVisible(), ids).Scan(&n); err != nil {
		return err
	}
	if n != len(uniqueIDs(ids)) {
		return serr(400, "invalidValue", "anggota tidak ditemukan atau bukan pengguna yang dikelola SCIM")
	}
	return nil
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// applyMemberOps menerapkan operasi berurutan pada himpunan anggota; mengembalikan himpunan baru.
func applyMemberOps(cur map[uuid.UUID]bool, ops []MemberOp) (map[uuid.UUID]bool, []uuid.UUID, error) {
	next := map[uuid.UUID]bool{}
	for k := range cur {
		next[k] = true
	}
	var added []uuid.UUID
	for _, op := range ops {
		switch op.Kind {
		case "add":
			for _, id := range op.IDs {
				if !next[id] {
					next[id] = true
					added = append(added, id)
				}
			}
		case "remove":
			for _, id := range op.IDs {
				delete(next, id)
			}
		case "replace":
			next = map[uuid.UUID]bool{}
			for _, id := range op.IDs {
				next[id] = true
				added = append(added, id)
			}
		case "clear":
			next = map[uuid.UUID]bool{}
		}
	}
	if len(next) > scimMaxMembers {
		return nil, nil, serr(413, "tooMany", "grup melebihi %d anggota", scimMaxMembers)
	}
	return next, added, nil
}

func normGroupName(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || len([]rune(v)) > 200 || strings.ContainsAny(v, "\r\n") {
		return "", serr(400, "invalidValue", "displayName wajib (maks. 200 karakter, satu baris)")
	}
	return v, nil
}

func (s *Service) SCIMCreateGroup(ctx context.Context, actor string, c SCIMGroupChange) (SCIMGroup, error) {
	if !c.DisplayName.Set || c.DisplayName.Null {
		return SCIMGroup{}, serr(400, "invalidValue", "displayName wajib")
	}
	name, err := normGroupName(c.DisplayName.V)
	if err != nil {
		return SCIMGroup{}, err
	}
	var ext *string
	if c.ExternalID.Set && !c.ExternalID.Null && c.ExternalID.V != "" {
		ext = &c.ExternalID.V
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return SCIMGroup{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := s.lockAdminGuard(ctx, tx); err != nil {
		return SCIMGroup{}, err
	}
	members, _, err := applyMemberOps(map[uuid.UUID]bool{}, c.MemberOps)
	if err != nil {
		return SCIMGroup{}, err
	}
	ids := make([]uuid.UUID, 0, len(members))
	for id := range members {
		ids = append(ids, id)
	}
	if err := s.checkMembersVisible(ctx, tx, ids); err != nil {
		return SCIMGroup{}, err
	}
	gid := newID()
	if _, err := tx.Exec(ctx, `INSERT INTO scim_groups(id,display_name,external_id) VALUES ($1,$2,$3)`, gid, name, ext); err != nil {
		if isUnique(err, "") {
			return SCIMGroup{}, serr(409, "uniqueness", "displayName atau externalId grup sudah dipakai")
		}
		return SCIMGroup{}, err
	}
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `INSERT INTO scim_group_members(group_id,user_id) VALUES ($1,$2)`, gid, id); err != nil {
			return SCIMGroup{}, err
		}
	}
	ch, err := s.recomputeRoles(ctx, tx, ids)
	if err != nil {
		return SCIMGroup{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SCIMGroup{}, err
	}
	s.Audit(ctx, actor, "scim.group_create", "scim_group", gid.String(), map[string]any{"name": name, "members": len(ids), "role": s.mappedRole(name)})
	s.auditRoleChanges(ctx, actor, "group", ch)
	return s.SCIMGetGroup(ctx, gid, true)
}

// SCIMUpdateGroup: replace=true (PUT) mengganti displayName/externalId/anggota; false (PATCH) menerapkan operasi yang diberikan.
func (s *Service) SCIMUpdateGroup(ctx context.Context, actor string, id uuid.UUID, c SCIMGroupChange, replace bool) (SCIMGroup, error) {
	if replace && (!c.DisplayName.Set || c.DisplayName.Null) {
		return SCIMGroup{}, serr(400, "invalidValue", "displayName wajib pada PUT")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return SCIMGroup{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := s.lockAdminGuard(ctx, tx); err != nil {
		return SCIMGroup{}, err
	}
	var name string
	var ext *string
	err = tx.QueryRow(ctx, `SELECT display_name, external_id FROM scim_groups WHERE id=$1 FOR UPDATE`, id).Scan(&name, &ext)
	if errors.Is(err, pgx.ErrNoRows) {
		return SCIMGroup{}, serr(404, "", "grup %s tidak ditemukan", id)
	}
	if err != nil {
		return SCIMGroup{}, err
	}
	if c.DisplayName.Set {
		if c.DisplayName.Null {
			return SCIMGroup{}, serr(400, "invalidValue", "displayName tidak boleh dikosongkan")
		}
		if name, err = normGroupName(c.DisplayName.V); err != nil {
			return SCIMGroup{}, err
		}
	}
	switch {
	case c.ExternalID.Set && !c.ExternalID.Null && c.ExternalID.V != "":
		ext = &c.ExternalID.V
	case c.ExternalID.Set || replace:
		ext = nil
	}
	cur, err := s.currentMembers(ctx, tx, id)
	if err != nil {
		return SCIMGroup{}, err
	}
	ops := c.MemberOps
	if replace { // PUT: anggota yang tidak disebut dihapus
		ops = append([]MemberOp{{Kind: "clear"}}, ops...)
	}
	next, added, err := applyMemberOps(cur, ops)
	if err != nil {
		return SCIMGroup{}, err
	}
	if err := s.checkMembersVisible(ctx, tx, added); err != nil {
		return SCIMGroup{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE scim_groups SET display_name=$2, external_id=$3, updated_at=now() WHERE id=$1`, id, name, ext); err != nil {
		if isUnique(err, "") {
			return SCIMGroup{}, serr(409, "uniqueness", "displayName atau externalId grup sudah dipakai")
		}
		return SCIMGroup{}, err
	}
	var affected []uuid.UUID
	for uid := range cur {
		if !next[uid] {
			if _, err := tx.Exec(ctx, `DELETE FROM scim_group_members WHERE group_id=$1 AND user_id=$2`, id, uid); err != nil {
				return SCIMGroup{}, err
			}
			affected = append(affected, uid)
		}
	}
	for uid := range next {
		if !cur[uid] {
			if _, err := tx.Exec(ctx, `INSERT INTO scim_group_members(group_id,user_id) VALUES ($1,$2)`, id, uid); err != nil {
				return SCIMGroup{}, err
			}
			affected = append(affected, uid)
		}
	}
	// nama grup berubah → pemetaan peran bisa berubah untuk SEMUA anggota
	for uid := range next {
		if cur[uid] {
			affected = append(affected, uid)
		}
	}
	ch, err := s.recomputeRoles(ctx, tx, uniqueIDs(affected))
	if err != nil {
		return SCIMGroup{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SCIMGroup{}, err
	}
	s.Audit(ctx, actor, "scim.group_update", "scim_group", id.String(), map[string]any{"name": name, "members": len(next), "role": s.mappedRole(name)})
	s.auditRoleChanges(ctx, actor, "group", ch)
	return s.SCIMGetGroup(ctx, id, true)
}

func (s *Service) SCIMDeleteGroup(ctx context.Context, actor string, id uuid.UUID) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := s.lockAdminGuard(ctx, tx); err != nil {
		return err
	}
	var name string
	if err := tx.QueryRow(ctx, `SELECT display_name FROM scim_groups WHERE id=$1 FOR UPDATE`, id).Scan(&name); err != nil {
		return serr(404, "", "grup %s tidak ditemukan", id)
	}
	cur, err := s.currentMembers(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM scim_groups WHERE id=$1`, id); err != nil {
		return err
	}
	ids := make([]uuid.UUID, 0, len(cur))
	for uid := range cur {
		ids = append(ids, uid)
	}
	ch, err := s.recomputeRoles(ctx, tx, ids)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.Audit(ctx, actor, "scim.group_delete", "scim_group", id.String(), map[string]any{"name": name, "members": len(ids)})
	s.auditRoleChanges(ctx, actor, "group", ch)
	return nil
}

// GroupSummary untuk dashboard (admin): daftar grup, jumlah anggota, peran hasil pemetaan.
type GroupSummary struct {
	ID      uuid.UUID `json:"id"`
	Name    string    `json:"display_name"`
	Members int       `json:"members"`
	Role    *string   `json:"mapped_role"`
}

func (s *Service) SCIMGroupSummaries(ctx context.Context) ([]GroupSummary, error) {
	rows, err := s.Pool.Query(ctx, `SELECT g.id, g.display_name, (SELECT count(*) FROM scim_group_members m WHERE m.group_id=g.id) FROM scim_groups g ORDER BY g.display_name LIMIT 500`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (GroupSummary, error) {
		var g GroupSummary
		if err := r.Scan(&g.ID, &g.Name, &g.Members); err != nil {
			return g, err
		}
		if m := s.mappedRole(g.Name); m != "" {
			g.Role = &m
		}
		return g, nil
	})
}
