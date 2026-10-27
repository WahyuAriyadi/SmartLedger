package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"
)

// captureStdout runs f and returns what it wrote to stdout.
func captureStdout(t *testing.T, f func()) []byte {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	f()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.Bytes()
}

func logLine(t *testing.T, env map[string]string) map[string]any {
	t.Helper()
	out := captureStdout(t, func() {
		newLogger(func(k string) string { return env[k] }).Info("halo")
	})
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("log bukan JSON: %v (%s)", err, out)
	}
	return m
}

func TestLoggerLocalKeepsSlogDefaults(t *testing.T) {
	m := logLine(t, map[string]string{})
	if m["level"] != "INFO" || m["msg"] != "halo" {
		t.Fatalf("format lokal berubah: %v", m)
	}
}

func TestLoggerUsesCloudLoggingFieldsOnCloudRun(t *testing.T) {
	for _, env := range []map[string]string{{"K_SERVICE": "smartledger"}, {"LOG_FORMAT": "cloud"}} {
		m := logLine(t, env)
		if m["severity"] != "INFO" || m["message"] != "halo" || m["level"] != nil || m["msg"] != nil {
			t.Fatalf("format Cloud Logging salah untuk %v: %v", env, m)
		}
	}
}
