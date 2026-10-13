// CLOUD-MIGRATE: penyimpanan foto nota di Cloud Storage. Hapus awalan `// CLOUD: ` untuk mengaktifkan,
// lalu pilih STORAGE_BACKEND=gcs (lihat internal/app/factory.go). Butuh gcp_token.go aktif.
package receipt

// CLOUD: import (
// CLOUD: 	"bytes"
// CLOUD: 	"context"
// CLOUD: 	"fmt"
// CLOUD: 	"io"
// CLOUD: 	"net/http"
// CLOUD: 	"net/url"
// CLOUD: 	"time"
// CLOUD: )

// CLOUD: // GCSStorage menyimpan foto nota di Cloud Storage lewat JSON API (tanpa SDK).
// CLOUD: // Bucket harus: uniform bucket-level access, public access prevention ENFORCED, dan
// CLOUD: // enkripsi CMEK bila diperlukan. Service account hanya perlu roles/storage.objectUser pada bucket ini.
// CLOUD: type GCSStorage struct {
// CLOUD: 	bucket string
// CLOUD: 	tok    *metadataToken
// CLOUD: 	hc     *http.Client
// CLOUD: }

// CLOUD: func NewGCSStorage(bucket string) *GCSStorage {
// CLOUD: 	return &GCSStorage{bucket: bucket, tok: newMetadataToken(), hc: &http.Client{Timeout: 20 * time.Second}}
// CLOUD: }

// CLOUD: func (g *GCSStorage) Put(ctx context.Context, key string, data []byte, contentType string) error {
// CLOUD: 	if !keyRe.MatchString(key) {
// CLOUD: 		return fmt.Errorf("receipt: kunci penyimpanan tidak valid")
// CLOUD: 	}
// CLOUD: 	token, err := g.tok.Get(ctx)
// CLOUD: 	if err != nil {
// CLOUD: 		return err
// CLOUD: 	}
// CLOUD: 	u := fmt.Sprintf("https://storage.googleapis.com/upload/storage/v1/b/%s/o?uploadType=media&name=%s",
// CLOUD: 		url.PathEscape(g.bucket), url.QueryEscape(key))
// CLOUD: 	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(data))
// CLOUD: 	if err != nil {
// CLOUD: 		return err
// CLOUD: 	}
// CLOUD: 	req.Header.Set("Authorization", "Bearer "+token)
// CLOUD: 	req.Header.Set("Content-Type", contentType)
// CLOUD: 	resp, err := g.hc.Do(req)
// CLOUD: 	if err != nil {
// CLOUD: 		return err
// CLOUD: 	}
// CLOUD: 	defer resp.Body.Close()
// CLOUD: 	if resp.StatusCode != http.StatusOK {
// CLOUD: 		return fmt.Errorf("gcs put: status %d", resp.StatusCode)
// CLOUD: 	}
// CLOUD: 	return nil
// CLOUD: }

// CLOUD: func (g *GCSStorage) Get(ctx context.Context, key string) ([]byte, error) {
// CLOUD: 	if !keyRe.MatchString(key) {
// CLOUD: 		return nil, fmt.Errorf("receipt: kunci penyimpanan tidak valid")
// CLOUD: 	}
// CLOUD: 	token, err := g.tok.Get(ctx)
// CLOUD: 	if err != nil {
// CLOUD: 		return nil, err
// CLOUD: 	}
// CLOUD: 	u := fmt.Sprintf("https://storage.googleapis.com/storage/v1/b/%s/o/%s?alt=media",
// CLOUD: 		url.PathEscape(g.bucket), url.PathEscape(key))
// CLOUD: 	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
// CLOUD: 	if err != nil {
// CLOUD: 		return nil, err
// CLOUD: 	}
// CLOUD: 	req.Header.Set("Authorization", "Bearer "+token)
// CLOUD: 	resp, err := g.hc.Do(req)
// CLOUD: 	if err != nil {
// CLOUD: 		return nil, err
// CLOUD: 	}
// CLOUD: 	defer resp.Body.Close()
// CLOUD: 	if resp.StatusCode == http.StatusNotFound {
// CLOUD: 		return nil, ErrNotFound
// CLOUD: 	}
// CLOUD: 	if resp.StatusCode != http.StatusOK {
// CLOUD: 		return nil, fmt.Errorf("gcs get: status %d", resp.StatusCode)
// CLOUD: 	}
// CLOUD: 	return io.ReadAll(io.LimitReader(resp.Body, MaxUploadBytes*2))
// CLOUD: }
