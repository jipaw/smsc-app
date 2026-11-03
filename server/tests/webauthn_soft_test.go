package tests

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"
)

// softAuth: authenticator perangkat lunak minimal (ES256, attestation "none") untuk menguji upacara WebAuthn dari
// sisi klien secara independen dari pustaka server.

var b64 = base64.RawURLEncoding

type softAuth struct {
	t         *testing.T
	priv      *ecdsa.PrivateKey
	CredID    []byte
	SignCount uint32
	RPID      string
	Origin    string
	Flags     byte // bit tambahan (mis. 0x08 BE)
	// untuk passkey
	UserHandle []byte // dikembalikan pada assertion (dipelajari dari opsi pendaftaran)
	NoUV       bool   // authenticator tidak melakukan verifikasi pengguna
	RKProp     *bool  // nilai credProps.rk yang dilaporkan klien (nil = tidak dilaporkan)
}

func newSoft(t *testing.T) *softAuth {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	return &softAuth{t: t, priv: k, CredID: id, RPID: "example.test", Origin: "http://example.test"}
}

// ---- CBOR minimal (hanya tipe yang dipakai WebAuthn) ----

func cborHead(major byte, n uint64) []byte {
	switch {
	case n < 24:
		return []byte{major<<5 | byte(n)}
	case n < 256:
		return []byte{major<<5 | 24, byte(n)}
	default:
		b := make([]byte, 3)
		b[0] = major<<5 | 25
		binary.BigEndian.PutUint16(b[1:], uint16(n))
		return b
	}
}
func cborInt(n int64) []byte {
	if n >= 0 {
		return cborHead(0, uint64(n))
	}
	return cborHead(1, uint64(-1-n))
}
func cborBytes(b []byte) []byte { return append(cborHead(2, uint64(len(b))), b...) }
func cborText(s string) []byte  { return append(cborHead(3, uint64(len(s))), s...) }
func cborMap(kv ...[]byte) []byte {
	out := cborHead(5, uint64(len(kv)/2))
	for _, x := range kv {
		out = append(out, x...)
	}
	return out
}

func (a *softAuth) coseKey() []byte {
	x, y := a.priv.PublicKey.X.FillBytes(make([]byte, 32)), a.priv.PublicKey.Y.FillBytes(make([]byte, 32))
	return cborMap(cborInt(1), cborInt(2), cborInt(3), cborInt(-7), cborInt(-1), cborInt(1), cborInt(-2), cborBytes(x), cborInt(-3), cborBytes(y))
}

func (a *softAuth) authData(flags byte, withCred bool) []byte {
	h := sha256.Sum256([]byte(a.RPID))
	out := append([]byte{}, h[:]...)
	out = append(out, flags|a.Flags)
	cnt := make([]byte, 4)
	binary.BigEndian.PutUint32(cnt, a.SignCount)
	out = append(out, cnt...)
	if withCred {
		out = append(out, make([]byte, 16)...) // AAGUID nol
		l := make([]byte, 2)
		binary.BigEndian.PutUint16(l, uint16(len(a.CredID)))
		out = append(out, l...)
		out = append(out, a.CredID...)
		out = append(out, a.coseKey()...)
	}
	return out
}

func clientData(typ, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return b
}

// challengeOf mengambil challenge dari opsi (creation atau request).
func challengeOf(opts map[string]any) string {
	pk := opts["publicKey"].(map[string]any)
	return pk["challenge"].(string)
}

// Create menghasilkan respons navigator.credentials.create() untuk opsi dari server.
func (a *softAuth) Create(opts map[string]any) json.RawMessage {
	if u, ok := opts["publicKey"].(map[string]any)["user"].(map[string]any); ok {
		a.UserHandle, _ = b64.DecodeString(u["id"].(string))
	}
	return a.CreateWith(challengeOf(opts), a.Origin)
}

func (a *softAuth) CreateWith(challenge, origin string) json.RawMessage {
	a.SignCount = 0
	flags := byte(0x01 | 0x04 | 0x40)
	if a.NoUV {
		flags = 0x01 | 0x40
	}
	att := cborMap(cborText("fmt"), cborText("none"), cborText("attStmt"), cborMap(), cborText("authData"), cborBytes(a.authData(flags, true)))
	ext := map[string]any{}
	if a.RKProp != nil {
		ext["credProps"] = map[string]any{"rk": *a.RKProp}
	}
	return mustJSON(map[string]any{
		"id": b64.EncodeToString(a.CredID), "rawId": b64.EncodeToString(a.CredID), "type": "public-key",
		"response":               map[string]any{"clientDataJSON": b64.EncodeToString(clientData("webauthn.create", challenge, origin)), "attestationObject": b64.EncodeToString(att), "transports": []string{"internal"}},
		"clientExtensionResults": ext,
	})
}

// Assert menghasilkan respons navigator.credentials.get(); menaikkan sign count satu langkah.
func (a *softAuth) Assert(opts map[string]any) json.RawMessage {
	a.SignCount++
	return a.AssertWith(challengeOf(opts), a.Origin, false)
}

// AssertWith: kontrol penuh (challenge/origin) untuk skenario serangan. badSig = tanda tangan dirusak.
func (a *softAuth) AssertWith(challenge, origin string, badSig bool) json.RawMessage {
	cd := clientData("webauthn.get", challenge, origin)
	af := byte(0x01 | 0x04)
	if a.NoUV {
		af = 0x01
	}
	ad := a.authData(af, false)
	h := sha256.Sum256(cd)
	sig, err := ecdsa.SignASN1(rand.Reader, a.priv, sha256Sum(append(append([]byte{}, ad...), h[:]...)))
	if err != nil {
		a.t.Fatal(err)
	}
	if badSig {
		sig[len(sig)-1] ^= 0xff
	}
	resp := map[string]any{"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(ad), "signature": b64.EncodeToString(sig)}
	if len(a.UserHandle) > 0 {
		resp["userHandle"] = b64.EncodeToString(a.UserHandle)
	}
	return mustJSON(map[string]any{
		"id": b64.EncodeToString(a.CredID), "rawId": b64.EncodeToString(a.CredID), "type": "public-key",
		"response":               resp,
		"clientExtensionResults": map[string]any{},
	})
}

func sha256Sum(b []byte) []byte { h := sha256.Sum256(b); return h[:] }

func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
