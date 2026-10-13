// CLOUD-MIGRATE: seluruh isi file ini ter-comment (baris diawali `// CLOUD: `). Untuk migrasi ke GCP,
// hapus awalan `// CLOUD: ` pada tiap baris. Dipakai oleh GCSStorage dan VertexExtractor.
package receipt

// CLOUD: import (
// CLOUD: 	"context"
// CLOUD: 	"encoding/json"
// CLOUD: 	"fmt"
// CLOUD: 	"net/http"
// CLOUD: 	"sync"
// CLOUD: 	"time"
// CLOUD: )

// CLOUD: // metadataToken mengambil access token service account dari metadata server GCP
// CLOUD: // (Cloud Run / GCE). Tidak ada API key atau file kunci yang perlu disimpan.
// CLOUD: type metadataToken struct {
// CLOUD: 	hc    *http.Client
// CLOUD: 	mu    sync.Mutex
// CLOUD: 	token string
// CLOUD: 	exp   time.Time
// CLOUD: }

// CLOUD: func newMetadataToken() *metadataToken {
// CLOUD: 	return &metadataToken{hc: &http.Client{Timeout: 5 * time.Second}}
// CLOUD: }

// CLOUD: func (m *metadataToken) Get(ctx context.Context) (string, error) {
// CLOUD: 	m.mu.Lock()
// CLOUD: 	defer m.mu.Unlock()
// CLOUD: 	if m.token != "" && time.Now().Before(m.exp.Add(-time.Minute)) {
// CLOUD: 		return m.token, nil
// CLOUD: 	}
// CLOUD: 	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
// CLOUD: 		"http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token", nil)
// CLOUD: 	if err != nil {
// CLOUD: 		return "", err
// CLOUD: 	}
// CLOUD: 	req.Header.Set("Metadata-Flavor", "Google")
// CLOUD: 	resp, err := m.hc.Do(req)
// CLOUD: 	if err != nil {
// CLOUD: 		return "", err
// CLOUD: 	}
// CLOUD: 	defer resp.Body.Close()
// CLOUD: 	if resp.StatusCode != http.StatusOK {
// CLOUD: 		return "", fmt.Errorf("metadata server: status %d", resp.StatusCode)
// CLOUD: 	}
// CLOUD: 	var t struct {
// CLOUD: 		AccessToken string `json:"access_token"`
// CLOUD: 		ExpiresIn   int    `json:"expires_in"`
// CLOUD: 	}
// CLOUD: 	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
// CLOUD: 		return "", err
// CLOUD: 	}
// CLOUD: 	m.token, m.exp = t.AccessToken, time.Now().Add(time.Duration(t.ExpiresIn)*time.Second)
// CLOUD: 	return m.token, nil
// CLOUD: }
