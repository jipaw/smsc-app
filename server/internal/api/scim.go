package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/jipaw/smsc-app/server/internal/core"
)

// SCIM 2.0 di /scim/v2 (terpisah dari /api/v1; autentikasi token SCIM, bukan sesi/API key/cookie).

const scimCT = "application/scim+json"

type scimCtxKey struct{}

func (a *API) scimWrite(w http.ResponseWriter, status int, v any, loc string) {
	w.Header().Set("Content-Type", scimCT)
	w.Header().Set("Cache-Control", "no-store")
	if loc != "" {
		w.Header().Set("Location", loc)
	}
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func (a *API) scimFail(w http.ResponseWriter, err error) {
	var se *core.SCIMError
	if !errors.As(err, &se) {
		a.Log.Error("scim: galat internal", "err", err)
		se = &core.SCIMError{Status: 500, Detail: "kesalahan internal"}
	}
	body := map[string]any{"schemas": []string{core.SCIMErrorSchema}, "status": strconv.Itoa(se.Status), "detail": se.Detail}
	if se.Type != "" {
		body["scimType"] = se.Type
	}
	a.scimWrite(w, se.Status, body, "")
}

// scimAuth: hanya Authorization: Bearer smsc_scim_… ; SCIM nonaktif → 404 (tidak membocorkan keberadaan endpoint).
func (a *API) scimAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Svc.SCIMEnabled() {
			http.NotFound(w, r)
			return
		}
		tok := bearer(r)
		name, err := a.Svc.AuthSCIM(r.Context(), tok)
		if tok == "" || err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="scim"`)
			a.scimWrite(w, 401, map[string]any{"schemas": []string{core.SCIMErrorSchema}, "status": "401", "detail": "token SCIM tidak valid"}, "")
			return
		}
		if !a.scimLimit.allow(name) {
			w.Header().Set("Retry-After", "1")
			a.scimWrite(w, 429, map[string]any{"schemas": []string{core.SCIMErrorSchema}, "status": "429", "detail": "terlalu banyak permintaan"}, "")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), scimCtxKey{}, name)))
	})
}

func scimActor(r *http.Request) string {
	n, _ := r.Context().Value(scimCtxKey{}).(string)
	return "scim:" + n
}

func (a *API) scimBase() string { return a.Svc.Cfg.PublicBaseURL + "/scim/v2" }

func (a *API) scimUserJSON(u core.SCIMUser, groups []core.GroupRef) map[string]any {
	loc := a.scimBase() + "/Users/" + u.ID.String()
	out := map[string]any{
		"schemas": []string{core.SCIMUserSchema}, "id": u.ID.String(), "userName": u.UserName, "active": u.Active,
		"emails": []map[string]any{{"value": u.UserName, "type": "work", "primary": true}},
		"roles":  []map[string]any{{"value": u.Role, "primary": true}},
		"meta":   map[string]any{"resourceType": "User", "created": u.Created.UTC().Format("2006-01-02T15:04:05Z07:00"), "lastModified": u.Updated.UTC().Format("2006-01-02T15:04:05Z07:00"), "location": loc},
	}
	if u.ExternalID != nil {
		out["externalId"] = *u.ExternalID
	}
	gl := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		gl = append(gl, map[string]any{"value": g.ID.String(), "display": g.Display, "$ref": a.scimBase() + "/Groups/" + g.ID.String(), "type": "direct"})
	}
	out["groups"] = gl // read-only (RFC 7643 §4.1.2)
	if v, ok := u.Attrs["displayName"].(string); ok {
		out["displayName"] = v
	}
	name := map[string]any{}
	for _, k := range []string{"givenName", "familyName", "formatted"} {
		if v, ok := u.Attrs[k].(string); ok {
			name[k] = v
		}
	}
	if len(name) > 0 {
		out["name"] = name
	}
	return out
}

func (a *API) scimDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	b, err := io.ReadAll(r.Body)
	if err != nil {
		a.scimFail(w, &core.SCIMError{Status: 413, Type: "tooMany", Detail: "badan terlalu besar"})
		return false
	}
	if err := json.Unmarshal(b, v); err != nil {
		a.scimFail(w, &core.SCIMError{Status: 400, Type: "invalidSyntax", Detail: "JSON tidak valid"})
		return false
	}
	return true
}

func hasSchema(schemas []string, want string) bool {
	for _, s := range schemas {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

func asBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string: // Azure AD mengirim "True"/"False"
		switch strings.ToLower(x) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

// rolesFrom: roles = [{value:"operator"}, …] → peran. Kosong → Clear. Beberapa nilai → yang tertinggi (admin > operator > viewer).
func rolesFrom(v any) (core.Opt[string], error) {
	list, ok := v.([]any)
	if !ok {
		return core.Opt[string]{}, &core.SCIMError{Status: 400, Type: "invalidValue", Detail: "roles harus berupa array"}
	}
	rank := map[string]int{"viewer": 1, "operator": 2, "admin": 3}
	best := ""
	for _, it := range list {
		var val string
		switch x := it.(type) {
		case map[string]any:
			val, _ = x["value"].(string)
		case string:
			val = x
		}
		val = strings.ToLower(strings.TrimSpace(val))
		if rank[val] == 0 {
			return core.Opt[string]{}, &core.SCIMError{Status: 400, Type: "invalidValue", Detail: "peran harus viewer, operator, atau admin"}
		}
		if rank[val] > rank[best] {
			best = val
		}
	}
	if best == "" {
		return core.Clear[string](), nil
	}
	return core.Some(best), nil
}

// changeFromResource: tubuh User (POST/PUT) → perubahan.
func changeFromResource(m map[string]any) (core.SCIMChange, error) {
	var c core.SCIMChange
	if v, ok := m["userName"].(string); ok {
		c.UserName = core.Some(v)
	}
	if v, ok := m["externalId"].(string); ok {
		c.ExternalID = core.Some(v)
	}
	if v, ok := m["displayName"].(string); ok {
		c.DisplayName = core.Some(v)
	}
	if v, ok := m["active"]; ok {
		b, ok := asBool(v)
		if !ok {
			return c, &core.SCIMError{Status: 400, Type: "invalidValue", Detail: "active harus boolean"}
		}
		c.Active = core.Some(b)
	}
	if n, ok := m["name"].(map[string]any); ok {
		if v, ok := n["givenName"].(string); ok {
			c.GivenName = core.Some(v)
		}
		if v, ok := n["familyName"].(string); ok {
			c.FamilyName = core.Some(v)
		}
		if v, ok := n["formatted"].(string); ok {
			c.Formatted = core.Some(v)
		}
	}
	if v, ok := m["roles"]; ok {
		r, err := rolesFrom(v)
		if err != nil {
			return c, err
		}
		c.Role = r
	}
	return c, nil
}

func (a *API) scimDiscoverySPC(w http.ResponseWriter, r *http.Request) {
	a.scimWrite(w, 200, map[string]any{
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"}, "documentationUri": a.Svc.Cfg.PublicBaseURL + "/",
		"patch": map[string]any{"supported": true}, "bulk": map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter": map[string]any{"supported": true, "maxResults": 100}, "changePassword": map[string]any{"supported": false},
		"sort": map[string]any{"supported": false}, "etag": map[string]any{"supported": false},
		"authenticationSchemes": []map[string]any{{"type": "oauthbearertoken", "name": "OAuth Bearer Token", "description": "Token SCIM (smsc_scim_…) yang dibuat admin di Pengaturan > SCIM", "specUri": "https://www.rfc-editor.org/info/rfc6750", "primary": true}},
		"meta":                  map[string]any{"resourceType": "ServiceProviderConfig", "location": a.scimBase() + "/ServiceProviderConfig"},
	}, "")
}

func (a *API) scimDiscoveryRT(w http.ResponseWriter, r *http.Request) {
	types := map[string]map[string]any{
		"User": {"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, "id": "User", "name": "User", "endpoint": "/Users", "description": "Pengguna dashboard",
			"schema": core.SCIMUserSchema, "meta": map[string]any{"resourceType": "ResourceType", "location": a.scimBase() + "/ResourceTypes/User"}},
		"Group": {"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, "id": "Group", "name": "Group", "endpoint": "/Groups", "description": "Grup; nama grup dipetakan ke peran lewat SCIM_*_GROUPS",
			"schema": core.SCIMGroupSchema, "meta": map[string]any{"resourceType": "ResourceType", "location": a.scimBase() + "/ResourceTypes/Group"}},
	}
	if id := chi.URLParam(r, "id"); id != "" {
		rt, ok := types[id]
		if !ok {
			a.scimFail(w, &core.SCIMError{Status: 404, Detail: "resource type tidak ditemukan"})
			return
		}
		a.scimWrite(w, 200, rt, "")
		return
	}
	a.scimWrite(w, 200, map[string]any{"schemas": []string{core.SCIMListSchema}, "totalResults": 2, "startIndex": 1, "itemsPerPage": 2, "Resources": []any{types["User"], types["Group"]}}, "")
}

func scimUserSchema() map[string]any {
	str := func(name, desc string, req bool, uniq string) map[string]any {
		return map[string]any{"name": name, "type": "string", "multiValued": false, "description": desc, "required": req, "caseExact": false, "mutability": "readWrite", "returned": "default", "uniqueness": uniq}
	}
	return map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Schema"}, "id": core.SCIMUserSchema, "name": "User", "description": "Pengguna dashboard SMSC",
		"attributes": []map[string]any{
			str("userName", "Alamat email; dipakai sebagai login", true, "server"),
			str("externalId", "ID di IdP", false, "server"),
			str("displayName", "Nama tampilan (hanya disimpan)", false, "none"),
			{"name": "name", "type": "complex", "multiValued": false, "required": false, "mutability": "readWrite", "returned": "default", "uniqueness": "none",
				"subAttributes": []map[string]any{str("givenName", "", false, "none"), str("familyName", "", false, "none"), str("formatted", "", false, "none")}},
			{"name": "active", "type": "boolean", "multiValued": false, "required": false, "mutability": "readWrite", "returned": "default", "description": "false = akun dinonaktifkan dan semua sesinya dicabut"},
			{"name": "roles", "type": "complex", "multiValued": true, "required": false, "mutability": "readWrite", "returned": "default", "description": "Peran dashboard: viewer, operator, admin (admin hanya bila SCIM_ALLOW_ADMIN_ROLE)",
				"subAttributes": []map[string]any{{"name": "value", "type": "string", "multiValued": false, "required": true, "canonicalValues": []string{"viewer", "operator", "admin"}, "mutability": "readWrite", "returned": "default", "uniqueness": "none"}, {"name": "primary", "type": "boolean", "multiValued": false, "required": false, "mutability": "readWrite", "returned": "default"}},
				"uniqueness":    "none"},
			{"name": "emails", "type": "complex", "multiValued": true, "required": false, "mutability": "readOnly", "returned": "default", "description": "Dikembalikan dari userName; perubahan diabaikan", "uniqueness": "none"},
		}}
}

func scimGroupSchema() map[string]any {
	return map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Schema"}, "id": core.SCIMGroupSchema, "name": "Group", "description": "Grup SMSC",
		"attributes": []map[string]any{
			{"name": "displayName", "type": "string", "multiValued": false, "required": true, "caseExact": false, "mutability": "readWrite", "returned": "default", "uniqueness": "server"},
			{"name": "externalId", "type": "string", "multiValued": false, "required": false, "mutability": "readWrite", "returned": "default", "uniqueness": "server"},
			{"name": "members", "type": "complex", "multiValued": true, "required": false, "mutability": "readWrite", "returned": "default", "uniqueness": "none", "description": "Hanya anggota bertipe User yang dikelola SCIM",
				"subAttributes": []map[string]any{{"name": "value", "type": "string", "multiValued": false, "required": true, "mutability": "immutable", "returned": "default", "uniqueness": "none"}, {"name": "display", "type": "string", "multiValued": false, "required": false, "mutability": "readOnly", "returned": "default", "uniqueness": "none"}, {"name": "type", "type": "string", "multiValued": false, "required": false, "canonicalValues": []string{"User"}, "mutability": "immutable", "returned": "default", "uniqueness": "none"}}},
		}}
}

func (a *API) scimDiscoverySchemas(w http.ResponseWriter, r *http.Request) {
	us, gs := scimUserSchema(), scimGroupSchema()
	us["meta"] = map[string]any{"resourceType": "Schema", "location": a.scimBase() + "/Schemas/" + core.SCIMUserSchema}
	gs["meta"] = map[string]any{"resourceType": "Schema", "location": a.scimBase() + "/Schemas/" + core.SCIMGroupSchema}
	if id := chi.URLParam(r, "id"); id != "" {
		switch id {
		case core.SCIMUserSchema:
			a.scimWrite(w, 200, us, "")
		case core.SCIMGroupSchema:
			a.scimWrite(w, 200, gs, "")
		default:
			a.scimFail(w, &core.SCIMError{Status: 404, Detail: "schema tidak ditemukan"})
		}
		return
	}
	a.scimWrite(w, 200, map[string]any{"schemas": []string{core.SCIMListSchema}, "totalResults": 2, "startIndex": 1, "itemsPerPage": 2, "Resources": []any{us, gs}}, "")
}

func (a *API) scimListUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, err := core.ParseSCIMFilter(q.Get("filter"))
	if err != nil {
		a.scimFail(w, err)
		return
	}
	if q.Get("sortBy") != "" {
		a.scimFail(w, &core.SCIMError{Status: 400, Type: "invalidValue", Detail: "sortBy tidak didukung"})
		return
	}
	start, count := 1, 50
	if v := q.Get("startIndex"); v != "" {
		start, _ = strconv.Atoi(v)
	}
	if v := q.Get("count"); v != "" {
		count, _ = strconv.Atoi(v)
	}
	list, total, err := a.Svc.SCIMListUsers(r.Context(), f, start, count)
	if err != nil {
		a.scimFail(w, err)
		return
	}
	ids := make([]uuid.UUID, 0, len(list))
	for _, u := range list {
		ids = append(ids, u.ID)
	}
	grp, _ := a.Svc.SCIMUserGroups(r.Context(), ids)
	res := make([]map[string]any, 0, len(list))
	for _, u := range list {
		res = append(res, a.scimUserJSON(u, grp[u.ID]))
	}
	if start < 1 {
		start = 1
	}
	a.scimWrite(w, 200, map[string]any{"schemas": []string{core.SCIMListSchema}, "totalResults": total, "startIndex": start, "itemsPerPage": len(res), "Resources": res}, "")
}

func (a *API) scimUserID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		a.scimFail(w, &core.SCIMError{Status: 404, Detail: "pengguna tidak ditemukan"})
		return uuid.Nil, false
	}
	return id, true
}

func (a *API) scimGetUser(w http.ResponseWriter, r *http.Request) {
	id, ok := a.scimUserID(w, r)
	if !ok {
		return
	}
	u, err := a.Svc.SCIMGetUser(r.Context(), id)
	if err != nil {
		a.scimFail(w, err)
		return
	}
	a.scimWrite(w, 200, a.scimOneUser(r, u), "")
}

func (a *API) scimCreateUser(w http.ResponseWriter, r *http.Request) {
	var m map[string]any
	if !a.scimDecode(w, r, &m) {
		return
	}
	c, err := changeFromResource(m)
	if err == nil {
		var u core.SCIMUser
		if u, err = a.Svc.SCIMCreateUser(r.Context(), scimActor(r), c); err == nil {
			a.scimWrite(w, 201, a.scimOneUser(r, u), a.scimBase()+"/Users/"+u.ID.String())
			return
		}
	}
	a.scimFail(w, err)
}

func (a *API) scimReplaceUser(w http.ResponseWriter, r *http.Request) {
	id, ok := a.scimUserID(w, r)
	if !ok {
		return
	}
	var m map[string]any
	if !a.scimDecode(w, r, &m) {
		return
	}
	c, err := changeFromResource(m)
	if err == nil {
		var u core.SCIMUser
		if u, err = a.Svc.SCIMUpdateUser(r.Context(), scimActor(r), id, c, true); err == nil {
			a.scimWrite(w, 200, a.scimOneUser(r, u), "")
			return
		}
	}
	a.scimFail(w, err)
}

// atribut tanpa padanan di gateway: diterima dan diabaikan agar IdP (Azure AD / Okta) tidak gagal sinkronisasi
var scimIgnorable = []string{"emails", "phonenumbers", "addresses", "title", "preferredlanguage", "locale", "timezone", "nickname", "profileurl", "usertype", "photos", "ims",
	"urn:ietf:params:scim:schemas:extension:enterprise:2.0:user", "name.honorificprefix", "name.honorificsuffix", "name.middlename", "meta", "groups", "entitlements", "x509certificates"}

func ignorable(path string) bool {
	p := strings.ToLower(path)
	for _, ig := range scimIgnorable {
		if p == ig || strings.HasPrefix(p, ig+".") || strings.HasPrefix(p, ig+"[") || strings.HasPrefix(p, ig+":") {
			return true
		}
	}
	return false
}

func (a *API) scimPatchUser(w http.ResponseWriter, r *http.Request) {
	id, ok := a.scimUserID(w, r)
	if !ok {
		return
	}
	var body struct {
		Schemas []string `json:"schemas"`
		Ops     []struct {
			Op    string          `json:"op"`
			Path  string          `json:"path"`
			Value json.RawMessage `json:"value"`
		} `json:"Operations"`
	}
	if !a.scimDecode(w, r, &body) {
		return
	}
	if !hasSchema(body.Schemas, core.SCIMPatchSchema) || len(body.Ops) == 0 {
		a.scimFail(w, &core.SCIMError{Status: 400, Type: "invalidSyntax", Detail: "butuh schemas PatchOp dan minimal satu Operations"})
		return
	}
	var c core.SCIMChange
	apply := func(attr string, val any, remove bool) error {
		switch strings.ToLower(attr) {
		case "username":
			s, _ := val.(string)
			c.UserName = core.Some(s)
		case "externalid":
			if remove {
				c.ExternalID = core.Clear[string]()
			} else {
				s, _ := val.(string)
				c.ExternalID = core.Some(s)
			}
		case "displayname":
			if remove {
				c.DisplayName = core.Clear[string]()
			} else {
				s, _ := val.(string)
				c.DisplayName = core.Some(s)
			}
		case "name.givenname":
			if remove {
				c.GivenName = core.Clear[string]()
			} else {
				s, _ := val.(string)
				c.GivenName = core.Some(s)
			}
		case "name.familyname":
			if remove {
				c.FamilyName = core.Clear[string]()
			} else {
				s, _ := val.(string)
				c.FamilyName = core.Some(s)
			}
		case "name.formatted":
			if remove {
				c.Formatted = core.Clear[string]()
			} else {
				s, _ := val.(string)
				c.Formatted = core.Some(s)
			}
		case "active":
			if remove {
				return &core.SCIMError{Status: 400, Type: "invalidValue", Detail: "active tidak dapat dihapus"}
			}
			b, ok := asBool(val)
			if !ok {
				return &core.SCIMError{Status: 400, Type: "invalidValue", Detail: "active harus boolean"}
			}
			c.Active = core.Some(b)
		case "roles":
			if remove {
				c.Role = core.Clear[string]()
				return nil
			}
			r, err := rolesFrom(val)
			if err != nil {
				return err
			}
			c.Role = r
		default:
			if ignorable(attr) {
				return nil
			}
			return &core.SCIMError{Status: 400, Type: "invalidPath", Detail: "atribut tidak dikenal: " + attr}
		}
		return nil
	}
	for _, op := range body.Ops {
		kind := strings.ToLower(op.Op)
		if kind != "add" && kind != "replace" && kind != "remove" {
			a.scimFail(w, &core.SCIMError{Status: 400, Type: "invalidSyntax", Detail: "op harus add, replace, atau remove"})
			return
		}
		var val any
		if len(op.Value) > 0 {
			_ = json.Unmarshal(op.Value, &val)
		}
		if op.Path == "" { // tanpa path: value = objek berisi atribut (gaya Okta)
			m, ok := val.(map[string]any)
			if !ok || kind == "remove" {
				a.scimFail(w, &core.SCIMError{Status: 400, Type: "noTarget", Detail: "operasi tanpa path harus berisi objek value"})
				return
			}
			for k, v := range m {
				if k == "name" {
					if nm, ok := v.(map[string]any); ok {
						for nk, nv := range nm {
							if err := apply("name."+nk, nv, false); err != nil {
								a.scimFail(w, err)
								return
							}
						}
						continue
					}
				}
				if err := apply(k, v, false); err != nil {
					a.scimFail(w, err)
					return
				}
			}
			continue
		}
		if kind != "remove" && op.Value == nil {
			a.scimFail(w, &core.SCIMError{Status: 400, Type: "invalidValue", Detail: "value wajib untuk add/replace"})
			return
		}
		if err := apply(op.Path, val, kind == "remove"); err != nil {
			a.scimFail(w, err)
			return
		}
	}
	u, err := a.Svc.SCIMUpdateUser(r.Context(), scimActor(r), id, c, false)
	if err != nil {
		a.scimFail(w, err)
		return
	}
	a.scimWrite(w, 200, a.scimOneUser(r, u), "")
}

func (a *API) scimDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := a.scimUserID(w, r)
	if !ok {
		return
	}
	if err := a.Svc.SCIMDeleteUser(r.Context(), scimActor(r), id); err != nil {
		a.scimFail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- admin: token + ringkasan ----------

func (a *API) listSCIMTokens(w http.ResponseWriter, r *http.Request) {
	items, err := a.Svc.ListSCIMTokens(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) createSCIMToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !a.decode(w, r, &in) {
		return
	}
	p := principalOf(r.Context())
	t, err := a.Svc.CreateSCIMToken(r.Context(), p.user.ID, in.Name)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Svc.Audit(r.Context(), actorOf(p), "scim.token_create", "scim_token", t.ID.String(), map[string]any{"name": t.Name})
	a.writeJSON(w, http.StatusCreated, t)
}

func (a *API) deleteSCIMToken(w http.ResponseWriter, r *http.Request) {
	id, perr := pathUUID(r, "tokenId")
	if perr != nil {
		a.writeProblem(w, r, perr)
		return
	}
	name, err := a.Svc.RevokeSCIMToken(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Svc.Audit(r.Context(), actorOf(principalOf(r.Context())), "scim.token_revoke", "scim_token", id.String(), map[string]any{"name": name})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) getSCIMSettings(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, a.Svc.SCIMConfig(r.Context()))
}

func (a *API) scimOneUser(r *http.Request, u core.SCIMUser) map[string]any {
	g, _ := a.Svc.SCIMUserGroups(r.Context(), []uuid.UUID{u.ID})
	return a.scimUserJSON(u, g[u.ID])
}

// ---------- Groups (fase 13) ----------

func (a *API) scimGroupJSON(g core.SCIMGroup, withMembers bool) map[string]any {
	out := map[string]any{
		"schemas": []string{core.SCIMGroupSchema}, "id": g.ID.String(), "displayName": g.DisplayName,
		"meta": map[string]any{"resourceType": "Group", "created": g.Created.UTC().Format("2006-01-02T15:04:05Z07:00"), "lastModified": g.Updated.UTC().Format("2006-01-02T15:04:05Z07:00"), "location": a.scimBase() + "/Groups/" + g.ID.String()},
	}
	if g.ExternalID != nil {
		out["externalId"] = *g.ExternalID
	}
	if withMembers {
		ms := make([]map[string]any, 0, len(g.Members))
		for _, m := range g.Members {
			ms = append(ms, map[string]any{"value": m.ID.String(), "display": m.UserName, "$ref": a.scimBase() + "/Users/" + m.ID.String(), "type": "User"})
		}
		out["members"] = ms
	}
	return out
}

func excludesMembers(r *http.Request) bool {
	for _, v := range strings.Split(r.URL.Query().Get("excludedAttributes"), ",") {
		if strings.EqualFold(strings.TrimSpace(v), "members") {
			return true
		}
	}
	return false
}

func (a *API) scimListGroups(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, err := core.ParseSCIMFilter(q.Get("filter"))
	if err != nil {
		a.scimFail(w, err)
		return
	}
	start, count := 1, 50
	if v := q.Get("startIndex"); v != "" {
		start, _ = strconv.Atoi(v)
	}
	if v := q.Get("count"); v != "" {
		count, _ = strconv.Atoi(v)
	}
	wm := !excludesMembers(r)
	list, total, err := a.Svc.SCIMListGroups(r.Context(), f, start, count, wm)
	if err != nil {
		a.scimFail(w, err)
		return
	}
	res := make([]map[string]any, 0, len(list))
	for _, g := range list {
		res = append(res, a.scimGroupJSON(g, wm))
	}
	if start < 1 {
		start = 1
	}
	a.scimWrite(w, 200, map[string]any{"schemas": []string{core.SCIMListSchema}, "totalResults": total, "startIndex": start, "itemsPerPage": len(res), "Resources": res}, "")
}

func (a *API) scimGetGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := a.scimUserID(w, r)
	if !ok {
		return
	}
	g, err := a.Svc.SCIMGetGroup(r.Context(), id, !excludesMembers(r))
	if err != nil {
		a.scimFail(w, err)
		return
	}
	a.scimWrite(w, 200, a.scimGroupJSON(g, !excludesMembers(r)), "")
}

func memberIDs(v any) ([]uuid.UUID, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, &core.SCIMError{Status: 400, Type: "invalidValue", Detail: "members harus berupa array"}
	}
	var out []uuid.UUID
	for _, it := range list {
		m, ok := it.(map[string]any)
		if !ok {
			return nil, &core.SCIMError{Status: 400, Type: "invalidValue", Detail: "elemen members harus objek dengan value"}
		}
		if t, _ := m["type"].(string); t != "" && !strings.EqualFold(t, "User") {
			return nil, &core.SCIMError{Status: 400, Type: "invalidValue", Detail: "hanya anggota bertipe User yang didukung (grup bersarang tidak)"}
		}
		raw, _ := m["value"].(string)
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, &core.SCIMError{Status: 400, Type: "invalidValue", Detail: "members[].value harus berupa id pengguna"}
		}
		out = append(out, id)
	}
	return out, nil
}

func groupChangeFromResource(m map[string]any) (core.SCIMGroupChange, error) {
	var c core.SCIMGroupChange
	if v, ok := m["displayName"].(string); ok {
		c.DisplayName = core.Some(v)
	}
	if v, ok := m["externalId"].(string); ok {
		c.ExternalID = core.Some(v)
	}
	if v, ok := m["members"]; ok && v != nil {
		ids, err := memberIDs(v)
		if err != nil {
			return c, err
		}
		c.MemberOps = []core.MemberOp{{Kind: "replace", IDs: ids}}
	}
	return c, nil
}

func (a *API) scimCreateGroup(w http.ResponseWriter, r *http.Request) {
	var m map[string]any
	if !a.scimDecode(w, r, &m) {
		return
	}
	c, err := groupChangeFromResource(m)
	if err == nil {
		var g core.SCIMGroup
		if g, err = a.Svc.SCIMCreateGroup(r.Context(), scimActor(r), c); err == nil {
			a.scimWrite(w, 201, a.scimGroupJSON(g, true), a.scimBase()+"/Groups/"+g.ID.String())
			return
		}
	}
	a.scimFail(w, err)
}

func (a *API) scimReplaceGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := a.scimUserID(w, r)
	if !ok {
		return
	}
	var m map[string]any
	if !a.scimDecode(w, r, &m) {
		return
	}
	c, err := groupChangeFromResource(m)
	if err == nil {
		var g core.SCIMGroup
		if g, err = a.Svc.SCIMUpdateGroup(r.Context(), scimActor(r), id, c, true); err == nil {
			a.scimWrite(w, 200, a.scimGroupJSON(g, true), "")
			return
		}
	}
	a.scimFail(w, err)
}

var memberPathRe = regexp.MustCompile(`(?i)^members\[\s*value\s+eq\s+"([^"]+)"\s*\]$`)

func (a *API) scimPatchGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := a.scimUserID(w, r)
	if !ok {
		return
	}
	var body struct {
		Schemas []string `json:"schemas"`
		Ops     []struct {
			Op    string          `json:"op"`
			Path  string          `json:"path"`
			Value json.RawMessage `json:"value"`
		} `json:"Operations"`
	}
	if !a.scimDecode(w, r, &body) {
		return
	}
	if !hasSchema(body.Schemas, core.SCIMPatchSchema) || len(body.Ops) == 0 {
		a.scimFail(w, &core.SCIMError{Status: 400, Type: "invalidSyntax", Detail: "butuh schemas PatchOp dan minimal satu Operations"})
		return
	}
	var c core.SCIMGroupChange
	bad := func(t, d string) { a.scimFail(w, &core.SCIMError{Status: 400, Type: t, Detail: d}) }
	for _, op := range body.Ops {
		kind := strings.ToLower(op.Op)
		if kind != "add" && kind != "replace" && kind != "remove" {
			bad("invalidSyntax", "op harus add, replace, atau remove")
			return
		}
		var val any
		if len(op.Value) > 0 {
			_ = json.Unmarshal(op.Value, &val)
		}
		path := strings.TrimSpace(op.Path)
		if mm := memberPathRe.FindStringSubmatch(path); mm != nil {
			uid, err := uuid.Parse(mm[1])
			if err != nil || kind != "remove" {
				bad("invalidPath", "members[value eq \"id\"] hanya untuk remove dengan id pengguna")
				return
			}
			c.MemberOps = append(c.MemberOps, core.MemberOp{Kind: "remove", IDs: []uuid.UUID{uid}})
			continue
		}
		setField := func(attr string, v any, remove bool) error {
			switch strings.ToLower(attr) {
			case "displayname":
				if remove {
					return &core.SCIMError{Status: 400, Type: "invalidValue", Detail: "displayName tidak boleh dihapus"}
				}
				s, _ := v.(string)
				c.DisplayName = core.Some(s)
			case "externalid":
				if remove {
					c.ExternalID = core.Clear[string]()
				} else {
					s, _ := v.(string)
					c.ExternalID = core.Some(s)
				}
			case "members":
				switch {
				case remove && v == nil:
					c.MemberOps = append(c.MemberOps, core.MemberOp{Kind: "clear"})
				default:
					ids, err := memberIDs(v)
					if err != nil {
						return err
					}
					k := kind
					c.MemberOps = append(c.MemberOps, core.MemberOp{Kind: k, IDs: ids})
				}
			case "meta", "id", "schemas":
			default:
				return &core.SCIMError{Status: 400, Type: "invalidPath", Detail: "atribut grup tidak dikenal: " + attr}
			}
			return nil
		}
		if path == "" {
			m, ok := val.(map[string]any)
			if !ok || kind == "remove" {
				bad("noTarget", "operasi tanpa path harus berisi objek value")
				return
			}
			for k, v := range m {
				if err := setField(k, v, false); err != nil {
					a.scimFail(w, err)
					return
				}
			}
			continue
		}
		if kind != "remove" && op.Value == nil {
			bad("invalidValue", "value wajib untuk add/replace")
			return
		}
		if err := setField(path, val, kind == "remove"); err != nil {
			a.scimFail(w, err)
			return
		}
	}
	g, err := a.Svc.SCIMUpdateGroup(r.Context(), scimActor(r), id, c, false)
	if err != nil {
		a.scimFail(w, err)
		return
	}
	a.scimWrite(w, 200, a.scimGroupJSON(g, true), "")
}

func (a *API) scimDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := a.scimUserID(w, r)
	if !ok {
		return
	}
	if err := a.Svc.SCIMDeleteGroup(r.Context(), scimActor(r), id); err != nil {
		a.scimFail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listSCIMGroups(w http.ResponseWriter, r *http.Request) {
	items, err := a.Svc.SCIMGroupSummaries(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
