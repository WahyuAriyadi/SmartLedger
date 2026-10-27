package receipt

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestGeminiExtractorHappyPath(t *testing.T) {
	var req http.Request
	var body []byte
	reply := candidate(`{"merchant":"Toko Maju","confidence":0.9,"items":[` +
		`{"name":"Gula","qty":2,"unit_price":16000,"category":"Bahan baku"}]}`)
	srv := fakeVertex(t, 200, reply, &req, &body)
	defer srv.Close()

	g := NewGeminiExtractor("kunci-rahasia", "gemini-uji")
	g.endpoint = srv.URL
	ex, err := g.Extract(context.Background(), []byte("jpegbytes"), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Path != "/v1beta/models/gemini-uji:generateContent" {
		t.Fatalf("path salah: %s", req.URL.Path)
	}
	if req.Header.Get("x-goog-api-key") != "kunci-rahasia" {
		t.Fatal("kunci harus lewat header x-goog-api-key")
	}
	if req.URL.RawQuery != "" || strings.Contains(req.URL.String(), "kunci-rahasia") {
		t.Fatalf("kunci tidak boleh ada di URL: %s", req.URL.String())
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("Gemini API tidak memakai Authorization Bearer")
	}
	if !strings.Contains(string(body), `"inlineData"`) || !strings.Contains(string(body), `"responseSchema"`) {
		t.Fatalf("badan permintaan tidak memuat gambar/skema: %s", body)
	}
	if len(ex.Items) != 1 || ex.Items[0].UnitPrice != 16000 || g.Name() != "gemini:gemini-uji" {
		t.Fatalf("hasil salah: %+v %s", ex, g.Name())
	}
	if err := ex.Normalize(); err != nil {
		t.Fatal(err)
	}
}

func TestGeminiErrorsNeverLeakTheKey(t *testing.T) {
	srv := fakeVertex(t, 429, `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota)."}}`, nil, nil)
	defer srv.Close()
	g := NewGeminiExtractor("kunci-rahasia", "m")
	g.endpoint = srv.URL
	_, err := g.Extract(context.Background(), []byte("x"), "image/jpeg")
	if err == nil || !strings.Contains(err.Error(), "status 429") || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("galat kuota harus jelas: %v", err)
	}
	if strings.Contains(err.Error(), "kunci-rahasia") {
		t.Fatal("galat tidak boleh memuat kunci API")
	}
	// network errors (server down) must not contain the key either
	srv.Close()
	_, err = g.Extract(context.Background(), []byte("x"), "image/jpeg")
	if err == nil || strings.Contains(err.Error(), "kunci-rahasia") {
		t.Fatalf("galat jaringan salah: %v", err)
	}
}

func TestGatewayWithGeminiEndToEndAndQuotaFallback(t *testing.T) {
	ok := fakeVertex(t, 200, candidate(`{"merchant":"Warung","confidence":0.8,"items":[`+
		`{"name":"Telur","qty":1,"unit_price":28000,"category":"Bahan baku"}]}`), nil, nil)
	defer ok.Close()
	g := NewGeminiExtractor("k", "m")
	g.endpoint = ok.URL
	srv := gatewayServer(t, NewGateway(g, "", 0))
	defer srv.Close()
	code, out := postImage(t, srv.URL, jpegBytes(t, 300, 200), "")
	if code != 200 || out["failed"] != false || out["engine"] != "gemini:m" {
		t.Fatalf("alur gemini harus berhasil: %d %v", code, out)
	}

	// free-tier quota exhausted (429 from Google) -> the user falls back to manual entry, not an error
	limited := fakeVertex(t, 429, `{"error":{"message":"quota"}}`, nil, nil)
	defer limited.Close()
	g2 := NewGeminiExtractor("k", "m")
	g2.endpoint = limited.URL
	srv2 := gatewayServer(t, NewGateway(g2, "", 0))
	defer srv2.Close()
	code, out = postImage(t, srv2.URL, jpegBytes(t, 300, 200), "")
	if code != 200 || out["failed"] != true {
		t.Fatalf("429 dari Google harus berujung failed=true: %d %v", code, out)
	}
}

func TestGeminiIsAnExtractor(t *testing.T) { var _ Extractor = (*GeminiExtractor)(nil) }
