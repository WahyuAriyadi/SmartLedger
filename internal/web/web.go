// Package web menyajikan antarmuka (Vue 3, tanpa build tools) dari dalam binary.
// Satu origin dengan API: tidak perlu CORS, dan CSP bisa dikunci ke 'self'.
package web

import (
	"embed"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed static
var files embed.FS

// Content-Security-Policy untuk UI.
// 'unsafe-eval' dibutuhkan build penuh Vue (kompiler template di browser). Tanpa 'unsafe-inline':
// script/style sisipan tetap diblokir. Untuk CSP tanpa eval, precompile template (butuh build step).
const uiCSP = "default-src 'none'; script-src 'self' 'unsafe-eval'; style-src 'self'; img-src 'self' blob: data:; " +
	"connect-src 'self'; manifest-src 'self'; worker-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

var contentTypes = map[string]string{
	".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".svg": "image/svg+xml", ".png": "image/png",
}

// Handler menyajikan index.html di "/" dan berkas di bawah /assets/ (allowlist ekstensi,
// tanpa listing direktori). Selain itu 404.
func Handler() http.Handler {
	start := time.Now()
	serve := func(w http.ResponseWriter, r *http.Request, name, ctype string) {
		b, err := files.ReadFile("static/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", ctype)
		h.Set("Content-Security-Policy", uiCSP)
		h.Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, name, start, strings.NewReader(string(b)))
	}
	mux := http.NewServeMux()
	// Service worker harus dilayani dari root agar cakupannya seluruh aplikasi; manifest untuk "Pasang ke layar utama".
	mux.HandleFunc("GET /sw.js", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, "sw.js", "text/javascript; charset=utf-8")
	})
	mux.HandleFunc("GET /manifest.webmanifest", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, "manifest.webmanifest", "application/manifest+json")
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, "index.html", "text/html; charset=utf-8")
	})
	mux.HandleFunc("GET /assets/{file}", func(w http.ResponseWriter, r *http.Request) {
		name := path.Base(r.PathValue("file"))
		ctype, ok := contentTypes[path.Ext(name)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		serve(w, r, "assets/"+name, ctype)
	})
	return mux
}
