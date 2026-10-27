// Command ledger is the API server. Usage:
//
//	ledger serve        run the API (default)
//	ledger migrate      apply database migrations (uses MIGRATE_DATABASE_URL, the schema owner)
//	ledger genkey       print 32 random bytes, base64-encoded, for DATA_KEYS / PHONE_PEPPER
//	ledger healthcheck  exit 0 when /readyz is healthy (for Docker; the distroless image has no curl)
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata" // embedded tz database: the distroless image has no /usr/share/zoneinfo

	"smartledger/internal/app"
	"smartledger/internal/config"
	"smartledger/internal/platform/db"
	"smartledger/migrations"
)

func main() {
	log := newLogger(os.Getenv)
	slog.SetDefault(log)

	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(log)
	case "migrate":
		err = migrate(log)
	case "genkey":
		err = genkey()
	case "healthcheck":
		err = healthcheck()
	default:
		err = fmt.Errorf("perintah tidak dikenal %q (serve | migrate | genkey)", cmd)
	}
	if err != nil {
		log.Error("berhenti karena error", "err", err)
		os.Exit(1)
	}
}

// newLogger picks the log format. On Cloud Run (K_SERVICE is set) or with LOG_FORMAT=cloud,
// "level" becomes "severity" and "msg" becomes "message", the fields Cloud Logging reads.
// Elsewhere (laptop, Docker Compose) slog's default JSON format is used.
func newLogger(getenv func(string) string) *slog.Logger {
	if getenv("LOG_FORMAT") != "cloud" && getenv("K_SERVICE") == "" {
		return slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.LevelKey:
				a.Key = "severity"
			case slog.MessageKey:
				a.Key = "message"
			}
			return a
		},
	}))
}

func serve(log *slog.Logger) error {
	cfg, err := config.Load(config.FromOS)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Device mode: no database (data lives on the user's device). Server mode: PostgreSQL.
	var database *db.DB
	if cfg.StorageMode == "server" {
		database, err = db.Open(ctx, cfg.DatabaseURL, db.Options{MaxOpen: cfg.DBMaxOpen, MaxIdle: cfg.DBMaxIdle})
		if err != nil {
			return err
		}
		defer database.Close()
	}

	var ready atomic.Bool
	ready.Store(true)
	handler, err := app.New(app.Deps{Cfg: cfg, DB: database, Ready: &ready})
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second, // Slowloris protection
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      35 * time.Second, // must exceed the OCR timeout (25 s): real model calls can be slower than the mock
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("server berjalan", "addr", cfg.Addr, "mode", cfg.StorageMode)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	// Graceful shutdown: mark not ready, give the load balancer time to drain the instance,
	// let in-flight requests finish, then close.
	log.Info("shutdown dimulai")
	ready.Store(false)
	time.Sleep(cfg.DrainDelay)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("shutdown selesai")
	return nil
}

func migrate(log *slog.Logger) error {
	url, err := config.MigrateURL(config.FromOS)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	database, err := db.Open(ctx, url, db.Options{MaxOpen: 2, MaxIdle: 1})
	if err != nil {
		return err
	}
	defer database.Close()
	return db.Migrate(ctx, database.DB, migrations.FS, log)
}

func genkey() error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	fmt.Println(base64.StdEncoding.EncodeToString(b))
	return nil
}

// healthcheck backs the Docker HEALTHCHECK; the distroless image has no curl/wget.
func healthcheck() error {
	addr := os.Getenv("ADDR")
	if addr == "" || addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	if addr == "127.0.0.1" {
		addr = "127.0.0.1:8080"
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/readyz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("readyz: status %d", resp.StatusCode)
	}
	return nil
}
