package receipt

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A fake server mimics the Vertex AI generateContent response shape, so VertexExtractor is fully tested
// (URL, headers, request body, parsing, error handling) without a GCP account or cost.
func fakeVertex(t *testing.T, status int, reply string, got *http.Request, gotBody *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			*got = *r.Clone(context.Background())
		}
		if gotBody != nil {
			*gotBody, _ = io.ReadAll(r.Body)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
}

func candidate(text string) string {
	b, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{
		"content": map[string]any{"parts": []any{map[string]any{"text": text}}}}}})
	return string(b)
}

func TestVertexExtractorHappyPath(t *testing.T) {
	var req http.Request
	var body []byte
	reply := candidate(`{"merchant":"Toko Maju","date":"2026-10-01","confidence":0.9,"items":[` +
		`{"name":"Beras","qty":2.5,"unit_price":13000,"category":"Bahan baku"}]}`)
	srv := fakeVertex(t, 200, reply, &req, &body)
	defer srv.Close()

	v := NewVertexExtractorWithToken("proj-x", "global", "gemini-test", "tok-123")
	v.endpoint = srv.URL
	ex, err := v.Extract(context.Background(), []byte("jpegbytes"), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Path != "/v1/projects/proj-x/locations/global/publishers/google/models/gemini-test:generateContent" {
		t.Fatalf("path salah: %s", req.URL.Path)
	}
	if req.Header.Get("Authorization") != "Bearer tok-123" {
		t.Fatalf("header Authorization salah: %q", req.Header.Get("Authorization"))
	}
	if !strings.Contains(string(body), `"inlineData"`) || !strings.Contains(string(body), `"responseSchema"`) {
		t.Fatalf("badan permintaan tidak memuat gambar/skema: %s", body)
	}
	if ex.Merchant != "Toko Maju" || len(ex.Items) != 1 || ex.Items[0].UnitPrice != 13000 || ex.Items[0].CategoryHint != "Bahan baku" {
		t.Fatalf("hasil parsing salah: %+v", ex)
	}
	if err := ex.Normalize(); err != nil {
		t.Fatalf("hasil harus lolos Normalize: %v", err)
	}
	if v.Name() != "vertex:gemini-test" {
		t.Fatalf("nama engine: %s", v.Name())
	}
}

func TestVertexExtractorErrorsAreDiagnosable(t *testing.T) {
	srv := fakeVertex(t, 404, `{"error":{"message":"Publisher Model gemini-lama was not found"}}`, nil, nil)
	defer srv.Close()
	v := NewVertexExtractorWithToken("p", "global", "gemini-lama", "t")
	v.endpoint = srv.URL
	_, err := v.Extract(context.Background(), []byte("x"), "image/jpeg")
	if err == nil || !strings.Contains(err.Error(), "status 404") || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("galat harus memuat status dan pesan Google: %v", err)
	}

	for name, reply := range map[string]string{
		"respons kosong": `{"candidates":[]}`,
		"json rusak":     candidate(`bukan json`),
	} {
		s := fakeVertex(t, 200, reply, nil, nil)
		v := NewVertexExtractorWithToken("p", "global", "m", "t")
		v.endpoint = s.URL
		if _, err := v.Extract(context.Background(), []byte("x"), "image/jpeg"); err == nil {
			t.Errorf("%s: seharusnya galat", name)
		}
		s.Close()
	}
}

func TestVertexBaseURLByLocation(t *testing.T) {
	if got := NewVertexExtractor("p", "global", "m").baseURL(); got != "https://aiplatform.googleapis.com" {
		t.Fatalf("global: %s", got)
	}
	if got := NewVertexExtractor("p", "asia-southeast1", "m").baseURL(); got != "https://asia-southeast1-aiplatform.googleapis.com" {
		t.Fatalf("regional: %s", got)
	}
}

func TestErrSnippetIsShortAndSingleLine(t *testing.T) {
	long := strings.Repeat("a\n", 500)
	s := errSnippet([]byte(long))
	if strings.Contains(s, "\n") || len(s) > 310 {
		t.Fatalf("snippet tidak dipotong: %d", len(s))
	}
}

func TestVertexIsAnExtractor(t *testing.T) { var _ Extractor = (*VertexExtractor)(nil) }

// The whole cloud path without the cloud: photo -> gateway (validation, daily cap) -> Vertex (fake) -> clean result to the client.
func TestGatewayWithVertexEndToEnd(t *testing.T) {
	vertex := fakeVertex(t, 200, candidate(`{"merchant":"Warung Bu Tini","confidence":0.8,"items":[`+
		`{"name":"Gas LPG 3 kg","qty":2,"unit_price":21000,"category":"Listrik, air, dan gas"},`+
		`{"name":"","qty":1,"unit_price":5,"category":"Bahan baku"}]}`), nil, nil)
	defer vertex.Close()
	v := NewVertexExtractorWithToken("p", "global", "m", "t")
	v.endpoint = vertex.URL

	srv := gatewayServer(t, NewGateway(v, "", 0))
	defer srv.Close()
	code, out := postImage(t, srv.URL, jpegBytes(t, 300, 200), "")
	if code != 200 || out["failed"] != false || out["engine"] != "vertex:m" {
		t.Fatalf("alur vertex harus berhasil: %d %v", code, out)
	}
	items := out["items"].([]any)
	if len(items) != 1 { // items with an empty name are dropped by Normalize, not guessed
		t.Fatalf("item cacat harus dibuang: %v", items)
	}
	if _, leaked := out["raw"]; leaked {
		t.Fatal("respons mentah mesin OCR tidak boleh ikut ke klien")
	}
}

// A cloud misconfiguration must not surface as an error to users: the client falls back to manual entry.
func TestGatewayWithVertexFailureFallsBackToManual(t *testing.T) {
	vertex := fakeVertex(t, 403, `{"error":{"message":"Permission denied"}}`, nil, nil)
	defer vertex.Close()
	v := NewVertexExtractorWithToken("p", "global", "m", "t")
	v.endpoint = vertex.URL
	srv := gatewayServer(t, NewGateway(v, "", 0))
	defer srv.Close()
	code, out := postImage(t, srv.URL, jpegBytes(t, 300, 200), "")
	if code != 200 || out["failed"] != true {
		t.Fatalf("kegagalan Vertex harus berujung failed=true (isi manual): %d %v", code, out)
	}
}
