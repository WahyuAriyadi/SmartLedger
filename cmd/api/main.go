// Perintah:
//
//	ledger serve    jalankan API (default)
//	ledger migrate  terapkan migrasi database (pakai MIGRATE_DATABASE_URL, pemilik skema)
//	ledger genkey   cetak 32 byte acak base64 untuk DATA_KEYS / PHONE_PEPPER
//	ledger healthcheck  keluar 0 bila /readyz sehat (untuk Docker; image distroless tanpa curl)
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
	_ "time/tzdata" // zona waktu tertanam: image distroless tidak punya /usr/share/zoneinfo

	"smartledger/internal/app"
	"smartledger/internal/config"
	"smartledger/internal/platform/db"
	"smartledger/migrations"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	// CLOUD: // Cloud Logging membaca field "severity", bukan "level" bawaan slog.
	// CLOUD: log = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
	// CLOUD: 	ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
	// CLOUD: 		switch a.Key {
	// CLOUD: 		case slog.LevelKey:
	// CLOUD: 			a.Key = "severity"
	// CLOUD: 		case slog.MessageKey:
	// CLOUD: 			a.Key = "message"
	// CLOUD: 		}
	// CLOUD: 		return a
	// CLOUD: 	},
	// CLOUD: }))
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

func serve(log *slog.Logger) error {
	cfg, err := config.Load(config.FromOS)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.DatabaseURL, db.Options{MaxOpen: cfg.DBMaxOpen, MaxIdle: cfg.DBMaxIdle})
	if err != nil {
		return err
	}
	defer database.Close()

	var ready atomic.Bool
	ready.Store(true)
	handler, err := app.New(app.Deps{Cfg: cfg, DB: database, Ready: &ready})
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second, // anti Slowloris
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("server berjalan", "addr", cfg.Addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	// Shutdown mulus: tandai tidak siap -> beri waktu load balancer menarik instance ->
	// selesaikan request yang sedang berjalan -> tutup.
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

// healthcheck dipakai HEALTHCHECK Docker. Image distroless tidak punya curl/wget.
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
