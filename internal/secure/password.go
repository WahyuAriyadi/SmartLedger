package secure

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Parameter minimum OWASP untuk argon2id: m=19 MiB, t=2, p=1.
const (
	argonMemKiB  = 19 * 1024
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

// Hasher membungkus argon2id. Semaphore membatasi jumlah hash yang berjalan
// bersamaan agar lonjakan login tidak menghabiskan memori instance (19 MiB per hash).
type Hasher struct {
	sem   chan struct{}
	dummy string
}

func NewHasher(maxConcurrent int) (*Hasher, error) {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	h := &Hasher{sem: make(chan struct{}, maxConcurrent)}
	// Hash palsu untuk menyamakan waktu respons saat nomor HP tidak terdaftar.
	junk := make([]byte, 16)
	if _, err := rand.Read(junk); err != nil {
		return nil, err
	}
	d, err := h.Hash(context.Background(), base64.RawStdEncoding.EncodeToString(junk))
	if err != nil {
		return nil, err
	}
	h.dummy = d
	return h, nil
}

// Dummy mengembalikan hash palsu untuk dipakai pada Verify bila pengguna tidak ada.
func (h *Hasher) Dummy() string { return h.dummy }

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (h *Hasher) release() { <-h.sem }

func (h *Hasher) Hash(ctx context.Context, secret string) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(secret), salt, argonTime, argonMemKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func (h *Hasher) Verify(ctx context.Context, secret, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, fmt.Errorf("secure: format hash tidak dikenal")
	}
	var version int
	var mem, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, fmt.Errorf("secure: versi argon2 tidak cocok")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil {
		return false, fmt.Errorf("secure: parameter hash rusak")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	got := argon2.IDKey([]byte(secret), salt, t, mem, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
