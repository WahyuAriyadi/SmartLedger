package secure

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
)

func mustRand(b []byte) {
	if _, err := rand.Read(b); err != nil {
		panic("secure: sumber acak sistem gagal: " + err.Error())
	}
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// IsUUID memvalidasi UUID huruf kecil; dipakai sebelum nilai dari klien menyentuh SQL.
func IsUUID(s string) bool { return uuidRe.MatchString(s) }

// NewUUID membuat UUID v4.
func NewUUID() string {
	var b [16]byte
	mustRand(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// BlindIndex = HMAC-SHA256(pepper, nilai). Dipakai untuk mencari baris berdasarkan
// data terenkripsi (nomor HP) tanpa menyimpannya dalam bentuk terbaca.
func BlindIndex(pepper []byte, value string) string {
	m := hmac.New(sha256.New, pepper)
	m.Write([]byte(value))
	return hex.EncodeToString(m.Sum(nil))
}

// TokenLen adalah panjang token sesi terenkripsi base64url dari 32 byte acak.
const TokenLen = 43

// NewToken membuat token sesi 256-bit. Hanya hash-nya yang disimpan di database.
func NewToken() (token string, hash []byte) {
	var b [32]byte
	mustRand(b[:])
	token = base64.RawURLEncoding.EncodeToString(b[:])
	return token, HashToken(token)
}

func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
