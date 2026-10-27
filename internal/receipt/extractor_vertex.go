package receipt

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// VertexExtractor calls Gemini through Vertex AI. On Cloud Run it uses the default service account
// (no API key). Selected with OCR_BACKEND=vertex. Vertex AI data is not used by Google to train models.
type VertexExtractor struct {
	project, location, model string
	endpoint                 string // base URL override; empty = official Vertex AI host. Set by tests only.
	tok                      TokenSource
	hc                       *http.Client
}

// NewVertexExtractor authenticates through the metadata server (Cloud Run).
func NewVertexExtractor(project, location, model string) *VertexExtractor {
	return newVertexExtractor(project, location, model, newMetadataToken())
}

// NewVertexExtractorWithToken authenticates with a fixed access token, for trying the real Vertex AI from a laptop.
func NewVertexExtractorWithToken(project, location, model, token string) *VertexExtractor {
	return newVertexExtractor(project, location, model, staticToken(token))
}

func newVertexExtractor(project, location, model string, tok TokenSource) *VertexExtractor {
	return &VertexExtractor{project: project, location: location, model: model,
		tok: tok, hc: &http.Client{Timeout: 20 * time.Second}}
}

// Name implements Extractor; it reports the engine and model, e.g. "vertex:gemini-3.5-flash-lite".
func (v *VertexExtractor) Name() string { return "vertex:" + v.model }

// baseURL returns the API host; location "global" uses the host without a region prefix.
func (v *VertexExtractor) baseURL() string {
	if v.endpoint != "" {
		return strings.TrimRight(v.endpoint, "/")
	}
	if v.location == "global" {
		return "https://aiplatform.googleapis.com"
	}
	return "https://" + v.location + "-aiplatform.googleapis.com"
}

// Extract implements Extractor.
func (v *VertexExtractor) Extract(ctx context.Context, img []byte, mime string) (Extraction, error) {
	token, err := v.tok.Get(ctx)
	if err != nil {
		return Extraction{}, err
	}
	url := fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent",
		v.baseURL(), v.project, v.location, v.model)
	return geminiCall(ctx, v.hc, "vertex", url,
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }, img, mime)
}
