package receipt

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GeminiExtractor calls the Gemini Developer API with an API key from Google AI Studio.
// Selected with OCR_BACKEND=gemini. The key is sent in a header (not the query string), so it never appears in URLs.
//
// Privacy: on the free tier (a key without billing) Google may use request content to improve its products;
// on the paid tier it does not. Receipt photos contain financial data, so the free tier suits demos and
// trials rather than real user data.
type GeminiExtractor struct {
	apiKey, model string
	endpoint      string // base URL override; empty = official host. Set by tests only.
	hc            *http.Client
}

// NewGeminiExtractor returns an extractor that authenticates with the given API key.
func NewGeminiExtractor(apiKey, model string) *GeminiExtractor {
	return &GeminiExtractor{apiKey: apiKey, model: model, hc: &http.Client{Timeout: 20 * time.Second}}
}

// Name implements Extractor; it reports the engine and model, e.g. "gemini:gemini-3.5-flash-lite".
func (g *GeminiExtractor) Name() string { return "gemini:" + g.model }

// Extract implements Extractor.
func (g *GeminiExtractor) Extract(ctx context.Context, img []byte, mime string) (Extraction, error) {
	base := "https://generativelanguage.googleapis.com"
	if g.endpoint != "" {
		base = strings.TrimRight(g.endpoint, "/")
	}
	u := fmt.Sprintf("%s/v1beta/models/%s:generateContent", base, url.PathEscape(g.model))
	return geminiCall(ctx, g.hc, "gemini", u,
		func(r *http.Request) { r.Header.Set("x-goog-api-key", g.apiKey) }, img, mime)
}
