package app

import (
	"bytes"
	"image"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"smartledger/internal/config"
)

func deviceServer(t *testing.T, ready bool, code string) *httptest.Server {
	t.Helper()
	var r atomic.Bool
	r.Store(ready)
	h, err := New(Deps{Ready: &r, Cfg: config.Config{
		StorageMode: "device", OCRBackend: "mock", OCRAccessCode: code, OCRDailyCap: 100,
		RequestTimeout: 5 * time.Second, RateLimitRPS: 1000, RateLimitBurst: 1000,
	}}) // DB sengaja nil: mode device tidak punya database
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestDeviceModeHasNoServerSideDataEndpoints(t *testing.T) {
	srv := deviceServer(t, true, "")
	for _, p := range []string{"/v1/businesses", "/v1/me", "/v1/businesses/x/transactions"} {
		if resp, _ := get(t, srv.URL+p); resp.StatusCode != 404 {
			t.Errorf("GET %s: mode device tidak boleh punya endpoint data, status %d", p, resp.StatusCode)
		}
	}
	for _, p := range []string{"/v1/auth/register", "/v1/auth/login", "/v1/businesses"} {
		resp, err := http.Post(srv.URL+p, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("POST %s: tidak boleh ada akun/data di server, status %d", p, resp.StatusCode)
		}
	}
}

func TestDeviceModeServesAppAndPWA(t *testing.T) {
	srv := deviceServer(t, true, "")
	resp, body := get(t, srv.URL+"/")
	csp := resp.Header.Get("Content-Security-Policy")
	if resp.StatusCode != 200 || !strings.Contains(body, "Buku Kas Pintar") ||
		!strings.Contains(csp, "manifest-src 'self'") || !strings.Contains(csp, "worker-src 'self'") || strings.Contains(csp, "'unsafe-inline'") {
		t.Fatalf("UI/CSP salah: %d %s", resp.StatusCode, csp)
	}
	if resp, _ := get(t, srv.URL+"/sw.js"); resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "javascript") {
		t.Fatalf("service worker harus dilayani dari root: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, _ := get(t, srv.URL+"/manifest.webmanifest"); resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "manifest") {
		t.Fatalf("manifest salah: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}

func TestDeviceModeHealthAndConfig(t *testing.T) {
	srv := deviceServer(t, true, "kode")
	if resp, _ := get(t, srv.URL+"/readyz"); resp.StatusCode != 200 {
		t.Fatalf("readyz tanpa DB harus 200, dapat %d", resp.StatusCode)
	}
	_, body := get(t, srv.URL+"/v1/config")
	if !strings.Contains(body, `"mode":"device"`) || !strings.Contains(body, `"access_code_required":true`) || strings.Contains(body, "kode\"") {
		t.Fatalf("config salah / membocorkan kode akses: %s", body)
	}
	notReady := deviceServer(t, false, "")
	if resp, _ := get(t, notReady.URL+"/readyz"); resp.StatusCode != 503 {
		t.Fatalf("saat shutdown readyz harus 503, dapat %d", resp.StatusCode)
	}
}

func TestDeviceModeOCRGatewayEndToEnd(t *testing.T) {
	srv := deviceServer(t, true, "")
	img := image.NewRGBA(image.Rect(0, 0, 240, 180))
	var jb bytes.Buffer
	_ = jpeg.Encode(&jb, img, nil)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("image", "n.jpg")
	_, _ = fw.Write(jb.Bytes())
	_ = mw.Close()
	resp, err := http.Post(srv.URL+"/v1/ocr/extract", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(out), `"items"`) {
		t.Fatalf("gerbang OCR harus bekerja: %d %s", resp.StatusCode, out)
	}
	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Request-Id", "Cache-Control"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("header %s hilang", h)
		}
	}
	if !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		t.Fatal("hasil OCR tidak boleh di-cache")
	}
}
