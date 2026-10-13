// Package receipt: unggah nota, ekstraksi OCR, koreksi pengguna, dan konfirmasi
// menjadi transaksi ledger.
package receipt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var ErrNotFound = errors.New("receipt: objek tidak ditemukan")

// Storage menyimpan foto nota. Kunci selalu dibuat server ("<business>/<receipt>.jpg"),
// tidak pernah dari nama file pengguna, sehingga path traversal tidak mungkin.
type Storage interface {
	Put(ctx context.Context, key string, data []byte, contentType string) error
	Get(ctx context.Context, key string) ([]byte, error)
}

var keyRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.jpg$`)

// LocalDisk: penyimpanan di disk lokal untuk pengembangan. Di cloud diganti GCS (storage_gcs.go).
type LocalDisk struct{ root string }

func NewLocalDisk(root string) (*LocalDisk, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("tidak bisa membuat folder penyimpanan %s: %w", abs, err)
	}
	return &LocalDisk{root: abs}, nil
}

func (d *LocalDisk) path(key string) (string, error) {
	if !keyRe.MatchString(key) {
		return "", fmt.Errorf("receipt: kunci penyimpanan tidak valid")
	}
	p := filepath.Join(d.root, filepath.FromSlash(key))
	if !strings.HasPrefix(p, d.root+string(filepath.Separator)) { // lapisan pertahanan kedua
		return "", fmt.Errorf("receipt: kunci keluar dari folder penyimpanan")
	}
	return p, nil
}

// Put menulis atomik (file sementara + rename) agar crash di tengah tidak meninggalkan file separuh.
func (d *LocalDisk) Put(_ context.Context, key string, data []byte, _ string) error {
	p, err := d.path(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // no-op setelah rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func (d *LocalDisk) Get(_ context.Context, key string) ([]byte, error) {
	p, err := d.path(key)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}
