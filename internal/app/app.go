// Package app merakit seluruh dependensi menjadi satu http.Handler.
// Dipisah dari main agar bisa diuji end-to-end dengan httptest.
package app

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"smartledger/internal/apperr"
	"smartledger/internal/auth"
	"smartledger/internal/config"
	"smartledger/internal/identity"
	"smartledger/internal/ledger"
	"smartledger/internal/platform/db"
	"smartledger/internal/platform/httpx"
	"smartledger/internal/receipt"
	"smartledger/internal/secure"
	"smartledger/internal/web"
)

type Deps struct {
	Cfg   config.Config
	DB    *db.DB
	Ready *atomic.Bool // false saat shutdown agar load balancer berhenti mengirim trafik

	// AuthLimiter opsional; nil = bawaan (10 percobaan lalu 1 per 6 detik per IP).
	// Dipakai tes agar tidak terkena batas saat memanggil login berulang.
	AuthLimiter *httpx.Limiter
}

func New(d Deps) (http.Handler, error) {
	cipher, err := secure.NewCipher(d.Cfg.DataKeys, d.Cfg.ActiveKeyID)
	if err != nil {
		return nil, err
	}
	hasher, err := secure.NewHasher(d.Cfg.HashConcurrency)
	if err != nil {
		return nil, err
	}

	authSvc := auth.NewService(d.DB, cipher, hasher, d.Cfg.PhonePepper, auth.Options{
		SessionTTL: d.Cfg.SessionTTL, MaxFailedLogins: d.Cfg.MaxFailedLogins, LockDuration: d.Cfg.LockDuration,
	})
	authH := auth.NewHandler(authSvc)
	ledgerH := ledger.NewHandler(ledger.NewService(d.DB, cipher))

	ip := func(r *http.Request) string { return httpx.ClientIP(r, d.Cfg.TrustedProxyHops) }
	global := httpx.NewLimiter(d.Cfg.RateLimitRPS, d.Cfg.RateLimitBurst)
	// Endpoint login/daftar jauh lebih ketat: 10 percobaan lalu 1 per 6 detik per IP.
	authLimiter := d.AuthLimiter
	if authLimiter == nil {
		authLimiter = httpx.NewLimiter(1.0/6, 10)
	}
	authLimit := authLimiter.Middleware(ip)

	storage, err := buildStorage(d.Cfg)
	if err != nil {
		return nil, err
	}
	extractor, err := buildExtractor(d.Cfg)
	if err != nil {
		return nil, err
	}
	receiptH := receipt.NewHandler(receipt.NewService(d.DB, cipher, storage, extractor))
	// Unggah nota: maksimal 10 per menit per PENGGUNA (OCR cloud berbiaya per panggilan).
	uploadLimit := httpx.NewLimiter(10.0/60, 10).Middleware(func(r *http.Request) string {
		uid, _ := identity.Require(r.Context())
		return uid
	})

	mux := http.NewServeMux()
	authH.Routes(mux, authLimit, authH.Require)
	ledgerH.Routes(mux, authH.Require)
	receiptH.Routes(mux, authH.Require, uploadLimit)

	// Antarmuka web (statis, di-embed). Pola "/" untuk API tetap menangani 404 JSON.
	ui := web.Handler()
	mux.Handle("GET /{$}", ui)
	mux.Handle("GET /assets/{file}", ui)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { // liveness: proses hidup
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) { // readiness: siap menerima trafik
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if !d.Ready.Load() || d.DB.PingContext(ctx) != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/", httpx.Handle(func(w http.ResponseWriter, r *http.Request) error {
		return apperr.NotFound("endpoint tidak ditemukan")
	}))

	return httpx.Chain(mux,
		httpx.RequestID,
		httpx.Recover,
		httpx.AccessLog(d.Cfg.TrustedProxyHops),
		httpx.SecurityHeaders,
		httpx.Timeout(d.Cfg.RequestTimeout),
		global.Middleware(ip),
	), nil
}
