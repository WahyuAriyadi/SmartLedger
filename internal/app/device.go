package app

import (
	"net/http"

	"smartledger/internal/apperr"
	"smartledger/internal/platform/httpx"
	"smartledger/internal/receipt"
	"smartledger/internal/web"
)

// newDevice merakit server untuk MODE PERANGKAT. Server ini sengaja "bodoh": tidak punya database, akun,
// ataupun penyimpanan. Seluruh data keuangan hidup terenkripsi di perangkat pengguna; server hanya
//  1. menyajikan aplikasi web (statis), dan
//  2. menjadi gerbang OCR tanpa penyimpanan ke Gemini (butuh kredensial cloud yang tidak boleh ada di perangkat).
//
// Dampaknya: pembobolan server tidak membocorkan data keuangan siapa pun karena memang tidak ada di sana.
func newDevice(d Deps) (http.Handler, error) {
	extractor, err := buildExtractor(d.Cfg)
	if err != nil {
		return nil, err
	}
	gw := receipt.NewGateway(extractor, d.Cfg.OCRAccessCode, d.Cfg.OCRDailyCap)

	ip := func(r *http.Request) string { return httpx.ClientIP(r, d.Cfg.TrustedProxyHops) }
	global := httpx.NewLimiter(d.Cfg.RateLimitRPS, d.Cfg.RateLimitBurst)
	// OCR berbiaya per panggilan dan tidak punya akun: 10 foto per menit per IP.
	ocrLimit := httpx.NewLimiter(10.0/60, 10).Middleware(ip)

	mux := http.NewServeMux()
	gw.Routes(mux, ocrLimit)

	ui := web.Handler()
	mux.Handle("GET /{$}", ui)
	mux.Handle("GET /assets/{file}", ui)
	mux.Handle("GET /sw.js", ui)
	mux.Handle("GET /manifest.webmanifest", ui)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !d.Ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /v1/config", func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"mode": "device",
			"ocr":  map[string]any{"engine": gw.Engine(), "access_code_required": gw.AccessRequired()},
		})
	})
	mux.Handle("/", httpx.Handle(func(w http.ResponseWriter, r *http.Request) error {
		return apperr.NotFound("endpoint tidak ditemukan")
	}))

	return middlewareChain(mux, d.Cfg, global.Middleware(ip)), nil
}
