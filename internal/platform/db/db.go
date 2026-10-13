// Package db membungkus database/sql: koneksi yang tahan start lambat,
// transaksi dengan konteks pengguna (untuk RLS), dan retry otomatis.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/lib/pq"
)

type DB struct{ *sql.DB }

type Options struct {
	MaxOpen, MaxIdle int
	MaxLifetime      time.Duration
}

// Open membuka pool dan menunggu database siap (backoff), karena saat cold start
// atau deploy bersamaan database bisa belum menerima koneksi.
func Open(ctx context.Context, url string, o Options) (*DB, error) {
	sdb, err := sql.Open("postgres", url)
	if err != nil {
		return nil, err
	}
	sdb.SetMaxOpenConns(o.MaxOpen)
	sdb.SetMaxIdleConns(o.MaxIdle)
	if o.MaxLifetime == 0 {
		o.MaxLifetime = 30 * time.Minute
	}
	sdb.SetConnMaxLifetime(o.MaxLifetime)
	sdb.SetConnMaxIdleTime(5 * time.Minute)

	wait := 500 * time.Millisecond
	var last error
	for i := 0; i < 8; i++ {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		last = sdb.PingContext(pctx)
		cancel()
		if last == nil {
			return &DB{sdb}, nil
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			sdb.Close()
			return nil, ctx.Err()
		}
		if wait < 5*time.Second {
			wait *= 2
		}
	}
	sdb.Close()
	return nil, fmt.Errorf("database tidak bisa dihubungi: %w", last)
}

// InTx menjalankan fn dalam satu transaksi. Bila userID tidak kosong, ia dipasang
// sebagai app.user_id (hanya berlaku dalam transaksi ini) sehingga policy RLS aktif.
//
// fn bisa dijalankan ulang (deadlock / serialization failure), jadi fn harus
// mereset state lokalnya di awal dan tidak boleh punya efek samping di luar tx.
func (d *DB) InTx(ctx context.Context, userID string, fn func(tx *sql.Tx) error) error {
	const maxAttempts = 3
	for attempt := 1; ; attempt++ {
		err := d.once(ctx, userID, fn)
		if err == nil || attempt >= maxAttempts || !retryable(err) {
			return err
		}
		backoff := time.Duration(attempt*20+rand.Intn(30)) * time.Millisecond
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (d *DB) once(ctx context.Context, userID string, fn func(tx *sql.Tx) error) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op setelah Commit
	if userID != "" {
		if _, err := tx.ExecContext(ctx, "SELECT set_config('app.user_id', $1, true)", userID); err != nil {
			return err
		}
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func retryable(err error) bool {
	var pe *pq.Error
	if errors.As(err, &pe) {
		return pe.Code == "40001" || pe.Code == "40P01"
	}
	return false
}

// IsUniqueViolation true bila err adalah pelanggaran UNIQUE (SQLSTATE 23505).
func IsUniqueViolation(err error) bool {
	var pe *pq.Error
	return errors.As(err, &pe) && pe.Code == "23505"
}
