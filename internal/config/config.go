// Package config reads configuration from the environment and fails fast on invalid values,
// secrets in particular.
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
	// StorageMode selects where user data lives:
	//   device (default): financial data stays on the user's device (encrypted); the server has no database,
	//                     it only serves the app and acts as an OCR gateway without persisting anything.
	//   server: legacy mode with accounts and PostgreSQL.
	StorageMode string

	// OCR gateway (device mode).
	OCRAccessCode string // empty = not required
	OCRDailyCap   int    // total OCR calls per day (cost guard)

	Addr             string
	DatabaseURL      string // ledger_app role (subject to RLS)
	DataKeys         map[string][]byte
	ActiveKeyID      string
	PhonePepper      []byte
	SessionTTL       time.Duration
	MaxFailedLogins  int
	LockDuration     time.Duration
	TrustedProxyHops int // 0 = use RemoteAddr; 1 = trust one proxy in front (Cloud Run / LB)
	RequestTimeout   time.Duration
	DrainDelay       time.Duration
	DBMaxOpen        int
	DBMaxIdle        int
	HashConcurrency  int
	RateLimitRPS     float64
	RateLimitBurst   int

	// Swappable backends, selected via environment.
	StorageBackend  string // local (only option; server-mode receipt photos are stored on disk)
	StorageLocalDir string
	OCRBackend      string // mock (default, free) | gemini (Gemini API, free tier available) | vertex (Vertex AI, paid)

	// Used only when OCRBackend=gemini. Keys come from Google AI Studio.
	GeminiAPIKey string
	GeminiModel  string

	// Used only when OCRBackend=vertex.
	VertexProject  string
	VertexLocation string // "global" or a region, e.g. asia-southeast1
	VertexModel    string
	// VertexAccessToken is for local runs against the real Vertex AI (`gcloud auth print-access-token`).
	// Leave empty on Cloud Run, where the service account is used automatically.
	VertexAccessToken string
}

// DefaultVertexModel is the Vertex AI model ID. Gemini models are retired periodically;
// gemini-2.5-flash was scheduled to retire on 20 Oct 2026.
const DefaultVertexModel = "gemini-3.5-flash-lite"

// DefaultGeminiModel is the Gemini API model ID (same model as the Vertex default).
const DefaultGeminiModel = "gemini-3.5-flash-lite"

var keyIDRe = regexp.MustCompile(`^[a-z0-9]{1,8}$`)

// MigrateURL returns the schema-owner DSN (used only by the `migrate` command).
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
	// Cloud Run provides the port via PORT; elsewhere it is empty and ADDR applies.
	if p := getenv("PORT"); p != "" {
		c.Addr = ":" + p
	}
	c.GeminiAPIKey = getenv("GEMINI_API_KEY")
	c.GeminiModel = str(getenv, "GEMINI_MODEL", DefaultGeminiModel)
	c.VertexProject = getenv("VERTEX_PROJECT")
	c.VertexLocation = str(getenv, "VERTEX_LOCATION", "global")
	c.VertexModel = str(getenv, "VERTEX_MODEL", DefaultVertexModel)
	c.VertexAccessToken = getenv("VERTEX_ACCESS_TOKEN")
	switch c.OCRBackend {
	case "mock":
	case "gemini":
		if c.GeminiAPIKey == "" {
			fail("GEMINI_API_KEY wajib diisi bila OCR_BACKEND=gemini (buat gratis di aistudio.google.com)")
		}
	case "vertex":
		if c.VertexProject == "" {
			fail("VERTEX_PROJECT wajib diisi bila OCR_BACKEND=vertex")
		}
	default:
		fail("OCR_BACKEND %q tidak dikenal (mock | gemini | vertex)", c.OCRBackend)
	}
	if c.StorageBackend != "local" {
		fail("STORAGE_BACKEND %q tidak dikenal (local)", c.StorageBackend)
	}
	switch c.StorageMode {
	case "device":
		// No database: there is no server-side secret protecting user data because the server holds none.
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

// parseKeys reads "id:base64,id:base64"; each key must be 32 bytes.
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

// FromOS is the default getenv.
var FromOS = os.Getenv
