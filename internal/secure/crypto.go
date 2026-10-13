// Package secure berisi primitif kriptografi: enkripsi field (AES-256-GCM),
// hash PIN (argon2id), blind index, dan token sesi.
package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidCiphertext = errors.New("secure: ciphertext tidak valid")

// Cipher mengenkripsi field sensitif sebelum masuk database. Format keluaran:
//
//	v1.<key-id>.<base64url(nonce || ciphertext)>
//
// key-id ikut disimpan sehingga kunci bisa dirotasi: data lama tetap bisa
// dibaca selama kunci lamanya masih ada di keyring, data baru memakai kunci aktif.
// AAD mengikat ciphertext ke konteksnya (mis. "users.phone") agar tidak bisa
// dipindah ke kolom lain.
type Cipher struct {
	aeads  map[string]cipher.AEAD
	active string
}

func NewCipher(keys map[string][]byte, active string) (*Cipher, error) {
	c := &Cipher{aeads: make(map[string]cipher.AEAD, len(keys)), active: active}
	for id, k := range keys {
		if len(k) != 32 {
			return nil, fmt.Errorf("kunci %q harus 32 byte (AES-256), dapat %d", id, len(k))
		}
		block, err := aes.NewCipher(k)
		if err != nil {
			return nil, err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		c.aeads[id] = gcm
	}
	if _, ok := c.aeads[active]; !ok {
		return nil, fmt.Errorf("kunci aktif %q tidak ada di keyring", active)
	}
	return c, nil
}

func (c *Cipher) Encrypt(plaintext, aad string) (string, error) {
	aead := c.aeads[c.active]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(plaintext), []byte(aad))
	return "v1." + c.active + "." + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (c *Cipher) Decrypt(token, aad string) (string, error) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 || parts[0] != "v1" {
		return "", ErrInvalidCiphertext
	}
	aead, ok := c.aeads[parts[1]]
	if !ok {
		return "", ErrInvalidCiphertext
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(raw) < aead.NonceSize() {
		return "", ErrInvalidCiphertext
	}
	ns := aead.NonceSize()
	pt, err := aead.Open(nil, raw[:ns], raw[ns:], []byte(aad))
	if err != nil {
		return "", ErrInvalidCiphertext
	}
	return string(pt), nil
}
