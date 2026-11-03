package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SCIM 2.0 (RFC 7643/7644), fase 12: IdP mendorong pengguna ke gateway. Hanya resource User (peran lewat atribut `roles`).
// Pengaman: token khusus SCIM (bukan sesi/API key), SCIM hanya melihat/mengubah pengguna yang dikelolanya (scim_managed)
// kecuali SCIM_ADOPT_EXISTING, SCIM tidak dapat memberi/mengubah admin kecuali SCIM_ALLOW_ADMIN_ROLE, dan tidak pernah boleh
// menyisakan nol admin aktif.

const (
	SCIMUserSchema  = "urn:ietf:params:scim:schemas:core:2.0:User"
	SCIMListSchema  = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	SCIMErrorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"
	SCIMPatchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	scimMaxCount    = 100
)

// SCIMError: galat berformat SCIM (RFC 7644 §3.12).
type SCIMError struct {
	Status int
	Type   string // scimType: invalidFilter, uniqueness, mutability, invalidSyntax, invalidPath, noTarget, invalidValue, ...
	Detail string
}

func (e *SCIMError) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Type, e.Detail) }

func serr(status int, typ, f string, a ...any) *SCIMError {
	return &SCIMError{Status: status, Type: typ, Detail: fmt.Sprintf(f, a...)}
}

// Opt: nilai atribut dalam perubahan SCIM. Set=false → tidak disebut; Null=true → dihapus/dikosongkan.
type Opt[T any] struct {
	Set, Null bool
	V         T
}

func Some[T any](v T) Opt[T] { return Opt[T]{Set: true, V: v} }
func Clear[T any]() Opt[T]   { return Opt[T]{Set: true, Null: true} }

type SCIMChange struct {
	UserName                                      Opt[string]
	ExternalID                                    Opt[string]
	Active                                        Opt[bool]
	Role                                          Opt[string]
	DisplayName, GivenName, FamilyName, Formatted Opt[string]
}

type SCIMUser struct {
	ID         uuid.UUID
	UserName   string
	ExternalID *string
	Active     bool
	Role       string
	Attrs      map[string]any
	Created    time.Time
	Updated    time.Time
}

func (s *Service) SCIMEnabled() bool { return s.Cfg.SCIM.Enabled }

// ---------- token ----------

type SCIMToken struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedBy  *string    `json:"created_by"`
	Token      string     `json:"token,omitempty"` // hanya pada respons pembuatan
}

var errSCIMOff = NewProblem(http.StatusConflict, "scim.not_enabled", "SCIM belum diaktifkan", "atur SCIM_ENABLED=true di server lalu restart")

func (s *Service) CreateSCIMToken(ctx context.Context, by uuid.UUID, name string) (SCIMToken, error) {
	if !s.SCIMEnabled() {
		return SCIMToken{}, errSCIMOff
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 60 {
		return SCIMToken{}, Validation(FieldError{"name", "wajib diisi, maksimal 60 karakter"})
	}
	t := SCIMToken{ID: newID(), Name: name, CreatedAt: time.Now(), Token: "smsc_scim_" + randToken(32)}
	_, err := s.Pool.Exec(ctx, `INSERT INTO scim_tokens(id,name,token_hash,created_by) VALUES ($1,$2,$3,$4)`, t.ID, name, sha(t.Token), by)
	return t, err
}

func (s *Service) ListSCIMTokens(ctx context.Context) ([]SCIMToken, error) {
	rows, err := s.Pool.Query(ctx, `SELECT t.id,t.name,t.created_at,t.last_used_at,(SELECT email FROM users u WHERE u.id=t.created_by) FROM scim_tokens t WHERE t.revoked_at IS NULL ORDER BY t.created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SCIMToken, error) {
		var t SCIMToken
		return t, r.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.LastUsedAt, &t.CreatedBy)
	})
}

func (s *Service) RevokeSCIMToken(ctx context.Context, id uuid.UUID) (string, error) {
	var name string
	err := s.Pool.QueryRow(ctx, `UPDATE scim_tokens SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL RETURNING name`, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", NotFound("token SCIM")
	}
	return name, err
}

// AuthSCIM memvalidasi bearer token; mengembalikan nama token (untuk aktor audit).
func (s *Service) AuthSCIM(ctx context.Context, token string) (string, error) {
	if !strings.HasPrefix(token, "smsc_scim_") {
		return "", errors.New("token bukan token SCIM")
	}
	var name string
	err := s.Pool.QueryRow(ctx, `UPDATE scim_tokens SET last_used_at = CASE WHEN last_used_at IS NULL OR last_used_at < now() - interval '1 minute' THEN now() ELSE last_used_at END
		WHERE token_hash=$1 AND revoked_at IS NULL RETURNING name`, sha(token)).Scan(&name)
	return name, err
}

type SCIMView struct {
	Enabled        bool   `json:"enabled"`
	BaseURL        string `json:"base_url,omitempty"`
	DefaultRole    string `json:"default_role,omitempty"`
	AllowAdminRole bool   `json:"allow_admin_role"`
	AdoptExisting  bool   `json:"adopt_existing"`
	ManagedUsers   int    `json:"managed_users"`
	// pemetaan grup → peran dari environment (nama grup huruf kecil); admin hanya muncul bila SCIM_ALLOW_ADMIN_ROLE
	GroupRoles map[string]string `json:"group_roles"`
	Groups     int               `json:"groups"`
}

func (s *Service) SCIMConfig(ctx context.Context) SCIMView {
	c := s.Cfg.SCIM
	v := SCIMView{Enabled: c.Enabled}
	if c.Enabled {
		v.BaseURL, v.DefaultRole, v.AllowAdminRole, v.AdoptExisting = s.Cfg.PublicBaseURL+"/scim/v2", c.DefaultRole, c.AllowAdminRole, c.AdoptExisting
		_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE scim_managed`).Scan(&v.ManagedUsers)
		_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM scim_groups`).Scan(&v.Groups)
		v.GroupRoles = map[string]string{}
		for g, r := range c.GroupRoles {
			v.GroupRoles[g] = r
		}
	}
	return v
}

// ---------- filter ----------

type SCIMFilter struct{ Terms []filterTerm }
type filterTerm struct {
	Attr string
	Val  string
	Bool *bool
}

// ParseSCIMFilter mendukung `attr eq "nilai"` (dan `eq true|false`) yang digabung `and`; atribut: userName, externalId, id, active, emails.value.
func ParseSCIMFilter(f string) (SCIMFilter, error) {
	var out SCIMFilter
	f = strings.TrimSpace(f)
	if f == "" {
		return out, nil
	}
	toks, err := lexFilter(f)
	if err != nil {
		return out, err
	}
	for i := 0; i < len(toks); {
		if i+2 >= len(toks) {
			return out, serr(400, "invalidFilter", "filter tidak lengkap")
		}
		attr, op, val := toks[i], strings.ToLower(toks[i+1].text), toks[i+2]
		if attr.quoted || op != "eq" {
			return out, serr(400, "invalidFilter", "hanya operator eq yang didukung")
		}
		var a string
		switch strings.ToLower(attr.text) {
		case "username", "emails.value", "emails[type eq \"work\"].value":
			a = "userName"
		case "externalid":
			a = "externalId"
		case "displayname":
			a = "displayName"
		case "id":
			a = "id"
		case "active":
			a = "active"
		default:
			return out, serr(400, "invalidFilter", "atribut filter tidak didukung: %s", attr.text)
		}
		t := filterTerm{Attr: a, Val: val.text}
		if a == "active" {
			b := strings.EqualFold(val.text, "true")
			if !val.quoted && !b && !strings.EqualFold(val.text, "false") {
				return out, serr(400, "invalidFilter", "nilai active harus true atau false")
			}
			t.Bool = &b
		}
		out.Terms = append(out.Terms, t)
		i += 3
		if i < len(toks) {
			if strings.ToLower(toks[i].text) != "and" || toks[i].quoted {
				return out, serr(400, "invalidFilter", "hanya penghubung and yang didukung")
			}
			i++
			if i >= len(toks) {
				return out, serr(400, "invalidFilter", "filter berakhir dengan and")
			}
		}
	}
	return out, nil
}

type ftok struct {
	text   string
	quoted bool
}

func lexFilter(f string) ([]ftok, error) {
	var out []ftok
	for i := 0; i < len(f); {
		switch c := f[i]; {
		case c == ' ' || c == '\t':
			i++
		case c == '"':
			j, sb := i+1, strings.Builder{}
			for ; j < len(f) && f[j] != '"'; j++ {
				if f[j] == '\\' && j+1 < len(f) {
					j++
				}
				sb.WriteByte(f[j])
			}
			if j >= len(f) {
				return nil, serr(400, "invalidFilter", "tanda kutip tidak ditutup")
			}
			out = append(out, ftok{sb.String(), true})
			i = j + 1
		case c == '(' || c == ')':
			return nil, serr(400, "invalidFilter", "tanda kurung tidak didukung")
		default:
			j := i
			for j < len(f) && f[j] != ' ' && f[j] != '\t' && f[j] != '"' {
				j++
			}
			out = append(out, ftok{f[i:j], false})
			i = j
		}
	}
	return out, nil
}

// ---------- baca ----------

const scimCols = `u.id, u.email, u.scim_external_id, NOT u.disabled, u.role::text, u.scim_attrs, u.created_at, COALESCE(u.scim_updated_at, u.created_at)`

func scanSCIM(r pgx.Row) (SCIMUser, error) {
	var u SCIMUser
	var raw []byte
	err := r.Scan(&u.ID, &u.UserName, &u.ExternalID, &u.Active, &u.Role, &raw, &u.Created, &u.Updated)
	u.Attrs = map[string]any{}
	_ = json.Unmarshal(raw, &u.Attrs)
	return u, err
}

func (s *Service) scimVisible() string {
	if s.Cfg.SCIM.AdoptExisting {
		return "TRUE"
	}
	return "u.scim_managed"
}

func (s *Service) SCIMGetUser(ctx context.Context, id uuid.UUID) (SCIMUser, error) {
	u, err := scanSCIM(s.Pool.QueryRow(ctx, `SELECT `+scimCols+` FROM users u WHERE u.id=$1 AND `+s.scimVisible(), id))
	if errors.Is(err, pgx.ErrNoRows) {
		return u, serr(404, "", "pengguna %s tidak ditemukan", id)
	}
	return u, err
}

func (s *Service) SCIMListUsers(ctx context.Context, f SCIMFilter, start, count int) ([]SCIMUser, int, error) {
	if start < 1 {
		start = 1
	}
	if count < 0 {
		count = 0
	}
	if count > scimMaxCount {
		count = scimMaxCount
	}
	where, args := []string{s.scimVisible()}, []any{}
	for _, t := range f.Terms {
		switch t.Attr {
		case "userName":
			args = append(args, strings.ToLower(t.Val))
			where = append(where, fmt.Sprintf("lower(u.email)=$%d", len(args)))
		case "externalId":
			args = append(args, t.Val)
			where = append(where, fmt.Sprintf("u.scim_external_id=$%d", len(args)))
		case "id":
			id, err := uuid.Parse(t.Val)
			if err != nil {
				return nil, 0, nil // id yang bukan UUID tidak mungkin cocok
			}
			args = append(args, id)
			where = append(where, fmt.Sprintf("u.id=$%d", len(args)))
		case "displayName":
			return nil, 0, serr(400, "invalidFilter", "displayName tidak didukung sebagai filter Users")
		case "active":
			args = append(args, *t.Bool)
			where = append(where, fmt.Sprintf("(NOT u.disabled)=$%d", len(args)))
		}
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM users u WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if count == 0 {
		return []SCIMUser{}, total, nil
	}
	args = append(args, count, start-1)
	rows, err := s.Pool.Query(ctx, fmt.Sprintf(`SELECT `+scimCols+` FROM users u WHERE %s ORDER BY u.created_at, u.id LIMIT $%d OFFSET $%d`, w, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (SCIMUser, error) { return scanSCIM(r) })
	return list, total, err
}

// ---------- tulis ----------

func (s *Service) scimRoleOK(role string) error {
	switch role {
	case "viewer", "operator":
		return nil
	case "admin":
		if s.Cfg.SCIM.AllowAdminRole {
			return nil
		}
		return serr(400, "invalidValue", "peran admin tidak diizinkan lewat SCIM (SCIM_ALLOW_ADMIN_ROLE=false)")
	}
	return serr(400, "invalidValue", "peran harus viewer, operator, atau admin (dapat %q)", role)
}

func normUserName(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	a, err := mail.ParseAddress(v)
	if err != nil || a.Address != v || !strings.Contains(v, "@") || strings.ContainsAny(v, " \r\n") {
		return "", serr(400, "invalidValue", "userName harus berupa alamat email (dipakai sebagai login): %q", v)
	}
	return v, nil
}

func mergeAttrs(cur map[string]any, c SCIMChange, replace bool) map[string]any {
	out := map[string]any{}
	if !replace {
		for k, v := range cur {
			out[k] = v
		}
	}
	setS := func(k string, o Opt[string]) {
		switch {
		case o.Set && !o.Null && o.V != "":
			out[k] = o.V
		case o.Set:
			delete(out, k)
		}
	}
	setS("displayName", c.DisplayName)
	setS("givenName", c.GivenName)
	setS("familyName", c.FamilyName)
	setS("formatted", c.Formatted)
	return out
}

// SCIMCreateUser membuat pengguna SSO-saja (tanpa password) yang dikelola SCIM.
func (s *Service) SCIMCreateUser(ctx context.Context, actor string, c SCIMChange) (SCIMUser, error) {
	if !c.UserName.Set || c.UserName.Null {
		return SCIMUser{}, serr(400, "invalidValue", "userName wajib")
	}
	email, err := normUserName(c.UserName.V)
	if err != nil {
		return SCIMUser{}, err
	}
	var direct *string
	if c.Role.Set && !c.Role.Null {
		v := c.Role.V
		direct = &v
		if err := s.scimRoleOK(v); err != nil {
			return SCIMUser{}, err
		}
	}
	role := s.effectiveRole(direct, "") // belum ada grup saat pembuatan; keanggotaan datang kemudian lewat /Groups
	active := !(c.Active.Set && !c.Active.Null && !c.Active.V)
	var ext *string
	if c.ExternalID.Set && !c.ExternalID.Null && c.ExternalID.V != "" {
		ext = &c.ExternalID.V
	}
	attrs, _ := json.Marshal(mergeAttrs(nil, c, true))
	id := newID()
	_, err = s.Pool.Exec(ctx, `INSERT INTO users(id,email,password_hash,role,disabled,scim_managed,scim_external_id,scim_attrs,scim_updated_at,scim_direct_role) VALUES ($1,$2,NULL,$3,$4,true,$5,$6,now(),$7::user_role)`,
		id, email, role, !active, ext, attrs, direct)
	if err != nil {
		if isUnique(err, "") {
			return SCIMUser{}, serr(409, "uniqueness", "userName atau externalId sudah dipakai")
		}
		return SCIMUser{}, err
	}
	s.Audit(ctx, actor, "scim.user_create", "user", id.String(), map[string]any{"role": role, "active": active})
	return s.SCIMGetUser(ctx, id)
}

// SCIMUpdateUser menerapkan perubahan. replace=true (PUT): atribut yang tidak disebut dikosongkan, KECUALI active (tetap, agar PUT
// parsial tidak mengaktifkan ulang akun yang dinonaktifkan). replace=false (PATCH): hanya yang disebut.
func (s *Service) SCIMUpdateUser(ctx context.Context, actor string, id uuid.UUID, c SCIMChange, replace bool) (SCIMUser, error) {
	if replace && (!c.UserName.Set || c.UserName.Null) {
		return SCIMUser{}, serr(400, "invalidValue", "userName wajib pada PUT")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return SCIMUser{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('smsc-scim-admin-guard'))`); err != nil {
		return SCIMUser{}, err
	}
	var email, role string
	var disabled, managed bool
	var ext, direct *string
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT email, role::text, disabled, scim_managed, scim_external_id, scim_attrs, scim_direct_role::text FROM users u WHERE u.id=$1 AND `+s.scimVisible()+` FOR UPDATE`, id).
		Scan(&email, &role, &disabled, &managed, &ext, &raw, &direct)
	if errors.Is(err, pgx.ErrNoRows) {
		return SCIMUser{}, serr(404, "", "pengguna %s tidak ditemukan", id)
	}
	if err != nil {
		return SCIMUser{}, err
	}
	if role == "admin" && !s.Cfg.SCIM.AllowAdminRole {
		return SCIMUser{}, serr(403, "mutability", "akun admin tidak dapat diubah lewat SCIM (SCIM_ALLOW_ADMIN_ROLE=false)")
	}
	cur := map[string]any{}
	_ = json.Unmarshal(raw, &cur)

	newEmail, newRole, newDisabled, newExt := email, role, disabled, ext
	if c.UserName.Set {
		if c.UserName.Null {
			return SCIMUser{}, serr(400, "invalidValue", "userName tidak boleh dikosongkan")
		}
		if newEmail, err = normUserName(c.UserName.V); err != nil {
			return SCIMUser{}, err
		}
	}
	// peran efektif = tertinggi(roles langsung, peran dari grup); tanpa keduanya → peran bawaan
	if !managed && direct == nil { // adopsi: peran saat ini menjadi peran langsung
		r := role
		direct = &r
	}
	switch {
	case c.Role.Set && !c.Role.Null:
		v := c.Role.V
		direct = &v
	case c.Role.Set || (replace && !c.Role.Set): // roles dihapus / PUT tanpa roles
		direct = nil
	}
	if direct != nil {
		if err := s.scimRoleOK(*direct); err != nil {
			return SCIMUser{}, err
		}
	}
	gr, err := s.groupRole(ctx, tx, id)
	if err != nil {
		return SCIMUser{}, err
	}
	newRole = s.effectiveRole(direct, gr)
	if newRole != role {
		if err := s.scimRoleOK(newRole); err != nil {
			return SCIMUser{}, err
		}
	}
	if c.Active.Set {
		if c.Active.Null {
			return SCIMUser{}, serr(400, "invalidValue", "active tidak boleh dikosongkan")
		}
		newDisabled = !c.Active.V
	}
	switch {
	case c.ExternalID.Set && !c.ExternalID.Null && c.ExternalID.V != "":
		newExt = &c.ExternalID.V
	case c.ExternalID.Set || replace:
		newExt = nil
	}
	// jangan pernah menyisakan nol admin aktif
	if role == "admin" && !disabled && (newDisabled || newRole != "admin") {
		var others int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='admin' AND NOT disabled AND id<>$1`, id).Scan(&others); err != nil {
			return SCIMUser{}, err
		}
		if others == 0 {
			return SCIMUser{}, serr(409, "mutability", "tidak dapat menonaktifkan/menurunkan admin aktif terakhir")
		}
	}
	attrs, _ := json.Marshal(mergeAttrs(cur, c, replace))
	_, err = tx.Exec(ctx, `UPDATE users SET email=$2, role=$3::user_role, disabled=$4, scim_external_id=$5, scim_attrs=$6, scim_managed=true, scim_updated_at=now(), scim_direct_role=$7::user_role WHERE id=$1`,
		id, newEmail, newRole, newDisabled, newExt, attrs, direct)
	if err != nil {
		if isUnique(err, "") {
			return SCIMUser{}, serr(409, "uniqueness", "userName atau externalId sudah dipakai")
		}
		return SCIMUser{}, err
	}
	if newDisabled && !disabled {
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id); err != nil {
			return SCIMUser{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return SCIMUser{}, err
	}
	data := map[string]any{}
	if newDisabled != disabled {
		data["active"] = !newDisabled
	}
	if newRole != role {
		data["role"] = map[string]string{"from": role, "to": newRole}
	}
	if newEmail != email {
		data["user_name_changed"] = true
	}
	if !managed {
		data["adopted"] = true
	}
	action := "scim.user_update"
	if newDisabled && !disabled {
		action = "scim.user_deactivate"
	}
	s.Audit(ctx, actor, action, "user", id.String(), data)
	return s.SCIMGetUser(ctx, id)
}

func (s *Service) SCIMDeleteUser(ctx context.Context, actor string, id uuid.UUID) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('smsc-scim-admin-guard'))`); err != nil {
		return err
	}
	var role string
	var disabled bool
	err = tx.QueryRow(ctx, `SELECT role::text, disabled FROM users u WHERE u.id=$1 AND `+s.scimVisible()+` FOR UPDATE`, id).Scan(&role, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return serr(404, "", "pengguna %s tidak ditemukan", id)
	}
	if err != nil {
		return err
	}
	if role == "admin" {
		if !s.Cfg.SCIM.AllowAdminRole {
			return serr(403, "mutability", "akun admin tidak dapat dihapus lewat SCIM (SCIM_ALLOW_ADMIN_ROLE=false)")
		}
		if !disabled {
			var others int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='admin' AND NOT disabled AND id<>$1`, id).Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				return serr(409, "mutability", "tidak dapat menghapus admin aktif terakhir")
			}
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id=$1`, id); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.Audit(ctx, actor, "scim.user_delete", "user", id.String(), nil)
	return nil
}
