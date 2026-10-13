// Package auth: registrasi, login PIN, dan sesi. Semua akses ke tabel users/sessions
// lewat fungsi SECURITY DEFINER di database (lihat migrations/002_security.sql).
package auth

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"smartledger/internal/apperr"
	"smartledger/internal/platform/db"
	"smartledger/internal/secure"
)

const (
	aadPhone = "users.phone"
	aadName  = "users.name"
)

type Service struct {
	db        *db.DB
	cipher    *secure.Cipher
	hasher    *secure.Hasher
	pepper    []byte
	ttl       time.Duration
	maxFailed int
	lockFor   time.Duration
	now       func() time.Time
}

type Options struct {
	SessionTTL      time.Duration
	MaxFailedLogins int
	LockDuration    time.Duration
}

func NewService(d *db.DB, c *secure.Cipher, h *secure.Hasher, pepper []byte, o Options) *Service {
	return &Service{db: d, cipher: c, hasher: h, pepper: pepper, ttl: o.SessionTTL,
		maxFailed: o.MaxFailedLogins, lockFor: o.LockDuration, now: time.Now}
}

type Session struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Profile struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Phone     string    `json:"phone"`
	CreatedAt time.Time `json:"created_at"`
}

var phoneRe = regexp.MustCompile(`^08[0-9]{8,12}$`)

// NormalizePhone menyeragamkan nomor HP Indonesia ke format 08xxxxxxxxxx.
func NormalizePhone(raw string) (string, error) {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= '0' && r <= '9', r == '+':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')':
		default:
			return "", apperr.Validation("invalid_phone", "nomor HP tidak valid")
		}
	}
	p := b.String()
	switch {
	case strings.HasPrefix(p, "+62"):
		p = "0" + p[3:]
	case strings.HasPrefix(p, "62"):
		p = "0" + p[2:]
	}
	if !phoneRe.MatchString(p) {
		return "", apperr.Validation("invalid_phone", "nomor HP tidak valid (contoh: 08123456789)")
	}
	return p, nil
}

var weakPINs = map[string]bool{"123456": true, "654321": true, "111111": true, "000000": true, "123123": true, "121212": true}

func validatePIN(pin string) error {
	if n := utf8.RuneCountInString(pin); n < 6 || n > 64 {
		return apperr.Validation("invalid_pin", "PIN minimal 6 karakter")
	}
	if weakPINs[pin] || strings.Count(pin, string([]rune(pin)[0])) == utf8.RuneCountInString(pin) {
		return apperr.Validation("weak_pin", "PIN terlalu mudah ditebak")
	}
	return nil
}

func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 2 || n > 80 {
		return "", apperr.Validation("invalid_name", "nama harus 2-80 karakter")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", apperr.Validation("invalid_name", "nama mengandung karakter tidak valid")
		}
	}
	return name, nil
}

func (s *Service) Register(ctx context.Context, name, phone, pin string) (string, error) {
	name, err := validateName(name)
	if err != nil {
		return "", err
	}
	p, err := NormalizePhone(phone)
	if err != nil {
		return "", err
	}
	if err := validatePIN(pin); err != nil {
		return "", err
	}
	hash, err := s.hasher.Hash(ctx, pin)
	if err != nil {
		return "", err
	}
	phoneEnc, err := s.cipher.Encrypt(p, aadPhone)
	if err != nil {
		return "", err
	}
	nameEnc, err := s.cipher.Encrypt(name, aadName)
	if err != nil {
		return "", err
	}
	var id string
	err = s.db.QueryRowContext(ctx, "SELECT auth_register($1, $2, $3, $4)",
		phoneEnc, secure.BlindIndex(s.pepper, p), nameEnc, hash).Scan(&id)
	if db.IsUniqueViolation(err) {
		return "", apperr.Conflict("phone_taken", "nomor HP sudah terdaftar")
	}
	return id, err
}

func (s *Service) Login(ctx context.Context, phone, pin string) (Session, error) {
	bad := apperr.Unauthorized("nomor HP atau PIN salah")

	p, perr := NormalizePhone(phone)
	if perr != nil {
		_, _ = s.hasher.Verify(ctx, pin, s.hasher.Dummy()) // samakan waktu respons
		return Session{}, bad
	}

	var (
		id, hash string
		failed   int
		locked   sql.NullTime
	)
	err := s.db.QueryRowContext(ctx,
		"SELECT id, pin_hash, failed_attempts, locked_until FROM auth_get_credentials($1)",
		secure.BlindIndex(s.pepper, p)).Scan(&id, &hash, &failed, &locked)
	if errors.Is(err, sql.ErrNoRows) {
		_, _ = s.hasher.Verify(ctx, pin, s.hasher.Dummy())
		return Session{}, bad
	}
	if err != nil {
		return Session{}, err
	}
	if locked.Valid && locked.Time.After(s.now()) {
		return Session{}, apperr.TooMany("akun dikunci sementara karena terlalu banyak percobaan, coba lagi nanti")
	}

	ok, err := s.hasher.Verify(ctx, pin, hash)
	if err != nil {
		return Session{}, err
	}
	if !ok {
		var until sql.NullTime
		if err := s.db.QueryRowContext(ctx, "SELECT auth_record_failure($1, $2, $3)",
			id, s.maxFailed, int(s.lockFor.Seconds())).Scan(&until); err != nil {
			return Session{}, err
		}
		return Session{}, bad
	}

	if _, err := s.db.ExecContext(ctx, "SELECT auth_record_success($1)", id); err != nil {
		return Session{}, err
	}
	token, tokenHash := secure.NewToken()
	expires := s.now().Add(s.ttl)
	if _, err := s.db.ExecContext(ctx, "SELECT auth_create_session($1, $2, $3)", id, tokenHash, expires); err != nil {
		return Session{}, err
	}
	return Session{Token: token, ExpiresAt: expires}, nil
}

// Resolve mengubah token sesi menjadi user ID, atau 401.
func (s *Service) Resolve(ctx context.Context, token string) (string, error) {
	if len(token) != secure.TokenLen {
		return "", apperr.Unauthorized("sesi tidak valid")
	}
	var uid sql.NullString
	if err := s.db.QueryRowContext(ctx, "SELECT auth_resolve_session($1)", secure.HashToken(token)).Scan(&uid); err != nil {
		return "", err
	}
	if !uid.Valid {
		return "", apperr.Unauthorized("sesi tidak valid atau sudah berakhir")
	}
	return uid.String, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, "SELECT auth_revoke_session($1)", secure.HashToken(token))
	return err
}

func (s *Service) Me(ctx context.Context, userID string) (Profile, error) {
	var p Profile
	var nameEnc, phoneEnc string
	err := s.db.InTx(ctx, userID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT id, name, phone_enc, created_at FROM users WHERE id = $1", userID).
			Scan(&p.ID, &nameEnc, &phoneEnc, &p.CreatedAt)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return p, apperr.Unauthorized("sesi tidak valid")
	}
	if err != nil {
		return p, err
	}
	if p.Name, err = s.cipher.Decrypt(nameEnc, aadName); err != nil {
		return p, err
	}
	p.Phone, err = s.cipher.Decrypt(phoneEnc, aadPhone)
	return p, err
}
