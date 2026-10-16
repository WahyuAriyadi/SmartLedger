// Package config membaca konfigurasi dari environment dan menolak start bila
// ada yang salah (fail fast), terutama rahasia.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// StorageMode menentukan di mana data pengguna disimpan:
	//   device (default): data keuangan hanya ada di perangkat pengguna (terenkripsi); server tanpa database,
	//                     hanya menyajikan aplikasi dan menjadi gerbang OCR tanpa menyimpan apa pun.
	//   server: mode lama dengan akun dan PostgreSQL.
	StorageMode string

	// Gerbang OCR (mode device).
	OCRAccessCode string // kosong = tidak diwajibkan
	OCRDailyCap   int    // batas total panggilan OCR per hari (pengaman biaya)

	Addr             string
	DatabaseURL      string // role ledger_app (terkena RLS)
	DataKeys         map[string][]byte
	ActiveKeyID      string
	PhonePepper      []byte
	SessionTTL       time.Duration
	MaxFailedLogins  int
	LockDuration     time.Duration
	TrustedProxyHops int // 0 = pakai RemoteAddr; 1 = percaya 1 proxy di depan (Cloud Run/LB)
	RequestTimeout   time.Duration
	DrainDelay       time.Duration
	DBMaxOpen        int
	DBMaxIdle        int
	HashConcurrency  int
	RateLimitRPS     float64
	RateLimitBurst   int

	// Backend yang bisa ditukar tanpa mengubah kode lain (dipilih lewat env).
	StorageBackend  string // local (default) | gcs (butuh baris CLOUD diaktifkan)
	StorageLocalDir string
	OCRBackend      string // mock (default) | vertex (butuh baris CLOUD diaktifkan)

	// CLOUD: GCSBucket      string
	// CLOUD: VertexProject  string
	// CLOUD: VertexLocation string
	// CLOUD: VertexModel    string
}

var keyIDRe = regexp.MustCompile(`^[a-z0-9]{1,8}$`)

// MigrateURL mengembalikan DSN pemilik skema (hanya dipakai perintah `migrate`).
func MigrateURL(getenv func(string) string) (string, error) {
	u := getenv("MIGRATE_DATABASE_URL")
	if u == "" {
		return "", errors.New("MIGRATE_DATABASE_URL wajib diisi")
	}
	return u, nil
}

func Load(getenv func(string) string) (Config, error) {
	var errs []error
	fail := func(f string, a ...any) { errs = append(errs, fmt.Errorf(f, a...)) }

	c := Config{
		StorageMode:      str(getenv, "STORAGE_MODE", "device"),
		OCRAccessCode:    getenv("OCR_ACCESS_CODE"),
		OCRDailyCap:      num(getenv, "OCR_DAILY_CAP", 300, fail),
		Addr:             str(getenv, "ADDR", ":8080"),
		DatabaseURL:      getenv("DATABASE_URL"),
		ActiveKeyID:      getenv("DATA_KEY_ACTIVE"),
		SessionTTL:       dur(getenv, "SESSION_TTL", 30*24*time.Hour, fail),
		MaxFailedLogins:  num(getenv, "MAX_FAILED_LOGINS", 5, fail),
		LockDuration:     dur(getenv, "LOCK_DURATION", 15*time.Minute, fail),
		TrustedProxyHops: num(getenv, "TRUSTED_PROXY_HOPS", 0, fail),
		RequestTimeout:   dur(getenv, "REQUEST_TIMEOUT", 10*time.Second, fail),
		DrainDelay:       dur(getenv, "DRAIN_DELAY", 3*time.Second, fail),
		DBMaxOpen:        num(getenv, "DB_MAX_OPEN", 10, fail),
		DBMaxIdle:        num(getenv, "DB_MAX_IDLE", 5, fail),
		HashConcurrency:  num(getenv, "HASH_CONCURRENCY", 4, fail),
		RateLimitRPS:     float64(num(getenv, "RATE_LIMIT_RPS", 20, fail)),
		RateLimitBurst:   num(getenv, "RATE_LIMIT_BURST", 40, fail),
		StorageBackend:   str(getenv, "STORAGE_BACKEND", "local"),
		StorageLocalDir:  str(getenv, "STORAGE_LOCAL_DIR", "./data/receipts"),
		OCRBackend:       str(getenv, "OCR_BACKEND", "mock"),
	}
	// CLOUD: // Cloud Run memberi tahu port lewat variabel PORT.
	// CLOUD: if p := getenv("PORT"); p != "" {
	// CLOUD: 	c.Addr = ":" + p
	// CLOUD: }
	// CLOUD: c.GCSBucket = getenv("GCS_BUCKET")
	// CLOUD: c.VertexProject = getenv("VERTEX_PROJECT")
	// CLOUD: c.VertexLocation = str(getenv, "VERTEX_LOCATION", "asia-southeast2")
	// CLOUD: c.VertexModel = str(getenv, "VERTEX_MODEL", "gemini-2.5-flash") // cek ID model terbaru sebelum deploy
	// CLOUD: if c.StorageBackend == "gcs" && c.GCSBucket == "" {
	// CLOUD: 	fail("GCS_BUCKET wajib diisi bila STORAGE_BACKEND=gcs")
	// CLOUD: }
	// CLOUD: if c.OCRBackend == "vertex" && c.VertexProject == "" {
	// CLOUD: 	fail("VERTEX_PROJECT wajib diisi bila OCR_BACKEND=vertex")
	// CLOUD: }
	switch c.StorageMode {
	case "device":
		// Tanpa database: tidak ada rahasia server yang melindungi data pengguna, karena memang tidak ada datanya.
	case "server":
		if c.DatabaseURL == "" {
			fail("DATABASE_URL wajib diisi pada STORAGE_MODE=server")
		}
		keys, err := parseKeys(getenv("DATA_KEYS"))
		if err != nil {
			fail("DATA_KEYS: %v", err)
		}
		c.DataKeys = keys
		if _, ok := keys[c.ActiveKeyID]; !ok {
			fail("DATA_KEY_ACTIVE %q tidak ada di DATA_KEYS", c.ActiveKeyID)
		}
		pepper, err := base64.StdEncoding.DecodeString(getenv("PHONE_PEPPER"))
		if err != nil || len(pepper) < 32 {
			fail("PHONE_PEPPER wajib base64 dengan panjang minimal 32 byte (buat dengan: ledger genkey)")
		}
		c.PhonePepper = pepper
		if c.MaxFailedLogins < 3 {
			fail("MAX_FAILED_LOGINS minimal 3")
		}
	default:
		fail("STORAGE_MODE %q tidak dikenal (device | server)", c.StorageMode)
	}
	return c, errors.Join(errs...)
}

// parseKeys membaca "id:base64,id:base64"; tiap kunci wajib 32 byte.
func parseKeys(raw string) (map[string][]byte, error) {
	keys := map[string][]byte{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		id, b64, ok := strings.Cut(item, ":")
		if !ok || !keyIDRe.MatchString(id) {
			return nil, fmt.Errorf("format harus id:base64 dengan id [a-z0-9]{1,8}")
		}
		k, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(k) != 32 {
			return nil, fmt.Errorf("kunci %q harus base64 dari tepat 32 byte", id)
		}
		keys[id] = k
	}
	if len(keys) == 0 {
		return nil, errors.New("minimal satu kunci diperlukan")
	}
	return keys, nil
}

func str(g func(string) string, k, def string) string {
	if v := g(k); v != "" {
		return v
	}
	return def
}

func num(g func(string) string, k string, def int, fail func(string, ...any)) int {
	v := g(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		fail("%s harus bilangan bulat >= 0", k)
		return def
	}
	return n
}

func dur(g func(string) string, k string, def time.Duration, fail func(string, ...any)) time.Duration {
	v := g(k)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		fail("%s harus durasi valid, mis. 15m", k)
		return def
	}
	return d
}

// FromOS adalah getenv bawaan.
var FromOS = os.Getenv
