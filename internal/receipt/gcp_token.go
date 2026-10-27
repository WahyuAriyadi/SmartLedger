package receipt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// TokenSource supplies OAuth access tokens for Google API calls.
// On Cloud Run it is backed by metadataToken (the default service account, no API key or key file).
// For local runs against the real Vertex AI it is backed by staticToken (see NewVertexExtractorWithToken).
type TokenSource interface {
	Get(ctx context.Context) (string, error)
}

// metadataToken fetches the service account access token from the GCP metadata server
// (Cloud Run / GCE). No API key or key file is stored.
type metadataToken struct {
	hc    *http.Client
	mu    sync.Mutex
	token string
	exp   time.Time
}

func newMetadataToken() *metadataToken {
	return &metadataToken{hc: &http.Client{Timeout: 5 * time.Second}}
}

func (m *metadataToken) Get(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token != "" && time.Now().Before(m.exp.Add(-time.Minute)) {
		return m.token, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := m.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("metadata server tidak terjangkau (apakah ini berjalan di Cloud Run? untuk uji lokal isi VERTEX_ACCESS_TOKEN): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata server: status %d", resp.StatusCode)
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", err
	}
	m.token, m.exp = t.AccessToken, time.Now().Add(time.Duration(t.ExpiresIn)*time.Second)
	return m.token, nil
}

// staticToken is a fixed token for local testing only; `gcloud auth print-access-token` is valid for about 1 hour.
// Not used in production: on Cloud Run, VERTEX_ACCESS_TOKEN stays empty.
type staticToken string

func (s staticToken) Get(context.Context) (string, error) { return string(s), nil }
