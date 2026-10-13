package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func b64(n int) string { return base64.StdEncoding.EncodeToString(make([]byte, n)) }

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func valid() map[string]string {
	return map[string]string{
		"DATABASE_URL": "postgres://x", "DATA_KEYS": "1:" + b64(32), "DATA_KEY_ACTIVE": "1", "PHONE_PEPPER": b64(32),
	}
}

func TestLoadValidAndDefaults(t *testing.T) {
	c, err := Load(env(valid()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8080" || c.StorageBackend != "local" || c.OCRBackend != "mock" || c.MaxFailedLogins != 5 {
		t.Fatalf("default tidak sesuai: %+v", c)
	}
}

func TestLoadFailsFast(t *testing.T) {
	cases := map[string]func(m map[string]string){
		"tanpa DATABASE_URL":        func(m map[string]string) { delete(m, "DATABASE_URL") },
		"kunci bukan 32 byte":       func(m map[string]string) { m["DATA_KEYS"] = "1:" + b64(16) },
		"kunci bukan base64":        func(m map[string]string) { m["DATA_KEYS"] = "1:%%%" },
		"id kunci tidak valid":      func(m map[string]string) { m["DATA_KEYS"] = "KUNCI-1:" + b64(32) },
		"kunci aktif tidak ada":     func(m map[string]string) { m["DATA_KEY_ACTIVE"] = "9" },
		"pepper terlalu pendek":     func(m map[string]string) { m["PHONE_PEPPER"] = b64(8) },
		"tanpa pepper":              func(m map[string]string) { delete(m, "PHONE_PEPPER") },
		"batas login terlalu kecil": func(m map[string]string) { m["MAX_FAILED_LOGINS"] = "1" },
		"durasi tidak valid":        func(m map[string]string) { m["SESSION_TTL"] = "sebulan" },
		"angka negatif":             func(m map[string]string) { m["DB_MAX_OPEN"] = "-3" },
	}
	for name, mutate := range cases {
		m := valid()
		mutate(m)
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%s: seharusnya ditolak saat start", name)
		}
	}
}

func TestLoadReportsAllProblemsAtOnce(t *testing.T) {
	_, err := Load(env(map[string]string{}))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "PHONE_PEPPER") {
		t.Fatalf("semua masalah konfigurasi harus dilaporkan sekaligus: %v", err)
	}
}
