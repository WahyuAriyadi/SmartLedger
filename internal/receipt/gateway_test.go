package receipt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"smartledger/internal/platform/httpx"
)

type failingExtractor struct{}

func (failingExtractor) Name() string { return "fail" }
func (failingExtractor) Extract(context.Context, []byte, string) (Extraction, error) {
	return Extraction{}, errors.New("mesin OCR mati")
}

func gatewayServer(t *testing.T, g *Gateway) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	g.Routes(mux, func(h http.Handler) http.Handler { return h })
	return httptest.NewServer(httpx.Chain(mux, httpx.RequestID, httpx.Recover))
}

func postImage(t *testing.T, url string, content []byte, code string) (int, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("image", "nota.jpg")
	_, _ = fw.Write(content)
	_ = mw.Close()
	req, _ := http.NewRequest("POST", url+"/v1/ocr/extract", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if code != "" {
		req.Header.Set("X-Access-Code", code)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestGatewayExtractsStatelessly(t *testing.T) {
	srv := gatewayServer(t, NewGateway(MockExtractor{}, "", 0))
	defer srv.Close()
	code, out := postImage(t, srv.URL, jpegBytes(t, 300, 200), "")
	if code != 200 || out["failed"] != false || out["engine"] != "mock" || len(out["items"].([]any)) == 0 {
		t.Fatalf("ekstraksi harus berhasil: %d %v", code, out)
	}
	it := out["items"].([]any)[0].(map[string]any)
	for _, k := range []string{"name", "qty", "unit_price", "category_hint"} {
		if _, ok := it[k]; !ok {
			t.Fatalf("item harus memuat %q: %v", k, it)
		}
	}
	if _, leaked := out["raw"]; leaked {
		t.Fatal("respons mentah mesin OCR tidak boleh ikut dikirim ke klien")
	}
}

func TestGatewayAccessCode(t *testing.T) {
	srv := gatewayServer(t, NewGateway(MockExtractor{}, "rahasia-123", 0))
	defer srv.Close()
	img := jpegBytes(t, 300, 200)
	if code, _ := postImage(t, srv.URL, img, ""); code != 401 {
		t.Fatalf("tanpa kode akses harus 401, dapat %d", code)
	}
	if code, _ := postImage(t, srv.URL, img, "salah"); code != 401 {
		t.Fatalf("kode akses salah harus 401, dapat %d", code)
	}
	if code, _ := postImage(t, srv.URL, img, "rahasia-123"); code != 200 {
		t.Fatalf("kode akses benar harus 200, dapat %d", code)
	}
}

func TestGatewayRejectsBadUploads(t *testing.T) {
	srv := gatewayServer(t, NewGateway(MockExtractor{}, "", 0))
	defer srv.Close()
	if code, _ := postImage(t, srv.URL, []byte("<html><script>alert(1)</script></html>"+strings.Repeat(" ", 600)), ""); code != 400 {
		t.Fatalf("HTML menyamar harus 400, dapat %d", code)
	}
	if code, _ := postImage(t, srv.URL, bytes.Repeat([]byte{0}, 6<<20), ""); code != 413 {
		t.Fatalf("file > 5 MB harus 413, dapat %d", code)
	}
}

func TestGatewayDailyCap(t *testing.T) {
	g := NewGateway(MockExtractor{}, "", 2)
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	srv := gatewayServer(t, g)
	defer srv.Close()
	img := jpegBytes(t, 300, 200)

	// unggahan yang ditolak (bukan gambar) tidak boleh menghabiskan kuota
	postImage(t, srv.URL, []byte("bukan gambar"+strings.Repeat("x", 600)), "")
	for i := 0; i < 2; i++ {
		if code, _ := postImage(t, srv.URL, img, ""); code != 200 {
			t.Fatalf("panggilan %d harus lolos, dapat %d", i+1, code)
		}
	}
	code, out := postImage(t, srv.URL, img, "")
	if code != 429 {
		t.Fatalf("melewati kuota harian harus 429, dapat %d %v", code, out)
	}
	now = now.Add(24 * time.Hour) // hari berikutnya (WIB)
	if code, _ := postImage(t, srv.URL, img, ""); code != 200 {
		t.Fatalf("kuota harus reset di hari berikutnya, dapat %d", code)
	}
}

func TestGatewayExtractorFailureIsNotAnError(t *testing.T) {
	srv := gatewayServer(t, NewGateway(failingExtractor{}, "", 0))
	defer srv.Close()
	code, out := postImage(t, srv.URL, jpegBytes(t, 300, 200), "")
	if code != 200 || out["failed"] != true || len(out["items"].([]any)) != 0 {
		t.Fatalf("OCR gagal harus 200 dengan failed=true agar klien menampilkan form manual: %d %v", code, out)
	}
}
