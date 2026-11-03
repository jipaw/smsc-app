package core

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

// Kunci enkripsi data rahasia di database (secret webhook, rahasia TOTP): AES-256-GCM.
// Kunci diturunkan per tujuan dari WEBHOOK_ENC_KEY (atau SESSION_SECRET). Cincin kunci: kunci saat ini dipakai menulis;
// kunci lama (…_PREVIOUS) hanya untuk membaca selama rotasi. `smscd secrets reencrypt` menulis ulang semuanya dengan kunci baru.
const (
	purposeWebhook = "smsc-webhook-secret-v1" // jangan diubah: kompatibel dengan data fase 3
	purposeTOTP    = "smsc-totp-v1"
	purposeEmail   = "smsc-email-v1" // isi email di outbox (memuat tautan undangan rahasia)
)

func deriveKey(base, purpose string) []byte {
	h := sha256.Sum256([]byte(base + "|" + purpose))
	return h[:]
}

func gcmFor(key []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

func (s *Service) seal(purpose, plain string) ([]byte, error) {
	gcm, err := gcmFor(deriveKey(s.Cfg.WebhookKey, purpose))
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plain), nil), nil
}

// open mencoba kunci saat ini lalu kunci lama. current=false berarti data masih terenkripsi dengan kunci lama.
func (s *Service) open(purpose string, blob []byte) (plain string, current bool, err error) {
	keys := append([]string{s.Cfg.WebhookKey}, s.Cfg.WebhookKeyPrev...)
	for i, k := range keys {
		gcm, err := gcmFor(deriveKey(k, purpose))
		if err != nil || len(blob) < gcm.NonceSize() {
			continue
		}
		if out, err := gcm.Open(nil, blob[:gcm.NonceSize()], blob[gcm.NonceSize():], nil); err == nil {
			return string(out), i == 0, nil
		}
	}
	return "", false, errors.New("data terenkripsi tidak dapat dibuka (kunci enkripsi berubah?)")
}

func (s *Service) encryptSecret(plain string) ([]byte, error) { return s.seal(purposeWebhook, plain) }

func (s *Service) decryptSecret(blob []byte) (string, error) {
	p, _, err := s.open(purposeWebhook, blob)
	return p, err
}

// hmacTokens: hash token sesi dengan SESSION_SECRET saat ini lalu yang lama (rotasi tanpa logout massal).
func (s *Service) hmacTokens(t string) [][]byte {
	out := [][]byte{s.hmacToken(t)}
	for _, old := range s.Cfg.SessionSecretPrev {
		m := hmac.New(sha256.New, []byte(old))
		m.Write([]byte(t))
		out = append(out, m.Sum(nil))
	}
	return out
}

// ReencryptSecrets menulis ulang semua data terenkripsi dengan kunci saat ini. Mengembalikan jumlah baris diubah.
func (s *Service) ReencryptSecrets(ctx context.Context) (webhooks, totp int, err error) {
	type row struct {
		id   any
		a, b []byte
	}
	rows, err := s.Pool.Query(ctx, `SELECT id, signing_secret, prev_signing_secret FROM webhooks`)
	if err != nil {
		return 0, 0, err
	}
	var whs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.a, &r.b); err != nil {
			rows.Close()
			return 0, 0, err
		}
		whs = append(whs, r)
	}
	rows.Close()
	redo := func(blob []byte, purpose string) ([]byte, bool, error) {
		if blob == nil {
			return nil, false, nil
		}
		plain, cur, err := s.open(purpose, blob)
		if err != nil {
			return nil, false, err
		}
		if cur {
			return blob, false, nil
		}
		nb, err := s.seal(purpose, plain)
		return nb, true, err
	}
	for _, r := range whs {
		a, ca, err := redo(r.a, purposeWebhook)
		if err != nil {
			return webhooks, totp, err
		}
		b, cb, err := redo(r.b, purposeWebhook)
		if err != nil {
			return webhooks, totp, err
		}
		if ca || cb {
			if _, err := s.Pool.Exec(ctx, `UPDATE webhooks SET signing_secret=$2, prev_signing_secret=$3 WHERE id=$1`, r.id, a, b); err != nil {
				return webhooks, totp, err
			}
			webhooks++
		}
	}
	rows, err = s.Pool.Query(ctx, `SELECT id, totp_secret, totp_pending FROM users WHERE totp_secret IS NOT NULL OR totp_pending IS NOT NULL`)
	if err != nil {
		return webhooks, 0, err
	}
	var us []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.a, &r.b); err != nil {
			rows.Close()
			return webhooks, 0, err
		}
		us = append(us, r)
	}
	rows.Close()
	for _, r := range us {
		a, ca, err := redo(r.a, purposeTOTP)
		if err != nil {
			return webhooks, totp, err
		}
		b, cb, err := redo(r.b, purposeTOTP)
		if err != nil {
			return webhooks, totp, err
		}
		if ca || cb {
			if _, err := s.Pool.Exec(ctx, `UPDATE users SET totp_secret=$2, totp_pending=$3 WHERE id=$1`, r.id, a, b); err != nil {
				return webhooks, totp, err
			}
			totp++
		}
	}
	return webhooks, totp, nil
}
