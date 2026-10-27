// ocrcheck tests the OCR path before deploying: key/permissions, model ID, quota, and output shape.
// It uses the same extractor code as the server, so a pass here means Cloud Run works too.
//
// Gemini API (free tier, default). Create a free key at https://aistudio.google.com, then in PowerShell:
//
//	$env:GEMINI_API_KEY = "your-key"
//	go run ./scripts/ocrcheck                          # blank generated image: enough to check key + model + quota
//	go run ./scripts/ocrcheck -image C:\photos\nota.jpg  # real receipt photo: check reading accuracy
//
// Vertex AI (paid):
//
//	$env:VERTEX_PROJECT = "your-gcp-project-id"
//	$env:VERTEX_ACCESS_TOKEN = (gcloud auth print-access-token)
//	go run ./scripts/ocrcheck -backend vertex
//
// Optional: GEMINI_MODEL, VERTEX_LOCATION (default global), VERTEX_MODEL.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"time"

	"smartledger/internal/config"
	"smartledger/internal/receipt"
)

func main() {
	backend := flag.String("backend", "gemini", "gemini | vertex")
	imgPath := flag.String("image", "", "foto nota (jpg/png) untuk dibaca; kosong = foto kosong buatan")
	flag.Parse()

	var ex receipt.Extractor
	switch *backend {
	case "gemini":
		key := os.Getenv("GEMINI_API_KEY")
		if key == "" {
			fail("isi GEMINI_API_KEY dulu (kunci gratis dari https://aistudio.google.com; lihat komentar di atas file ini)")
		}
		ex = receipt.NewGeminiExtractor(key, env("GEMINI_MODEL", config.DefaultGeminiModel))
	case "vertex":
		project, token := os.Getenv("VERTEX_PROJECT"), os.Getenv("VERTEX_ACCESS_TOKEN")
		if project == "" || token == "" {
			fail("isi VERTEX_PROJECT dan VERTEX_ACCESS_TOKEN dulu (lihat komentar di atas file ini)")
		}
		ex = receipt.NewVertexExtractorWithToken(project, env("VERTEX_LOCATION", "global"), env("VERTEX_MODEL", config.DefaultVertexModel), token)
	default:
		fail("-backend harus gemini atau vertex")
	}

	var raw []byte
	if *imgPath != "" {
		b, err := os.ReadFile(*imgPath)
		if err != nil {
			fail("tidak bisa membaca foto: %v", err)
		}
		raw = b
	} else {
		raw = blankJPEG()
	}
	// Same path as the server: validate, downscale, and re-encode before sending to OCR.
	jpg, err := receipt.SanitizeImage(raw)
	if err != nil {
		fail("foto ditolak oleh sanitizer: %v", err)
	}

	fmt.Printf("engine=%s foto=%d byte\n", ex.Name(), len(jpg))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := ex.Extract(ctx, jpg, "image/jpeg")
	if err != nil {
		fail("GAGAL: %v\n\nPetunjuk: 404 = ID model salah atau sudah pensiun (ganti lewat GEMINI_MODEL/VERTEX_MODEL);\n"+
			"400/403 = kunci/izin salah atau API belum aktif; 429 = kuota free tier habis (tunggu atau pakai tier berbayar);\n"+
			"401 = token Vertex kedaluwarsa (buat ulang).", err)
	}
	fmt.Printf("OK: model menjawab. toko=%q tanggal=%q keyakinan=%.2f item=%d\n", out.Merchant, out.Date, out.Confidence, len(out.Items))
	for _, it := range out.Items {
		fmt.Printf("  - %-28s %6.2f x Rp%-9d [%s]\n", it.Name, it.Qty, it.UnitPrice, it.CategoryHint)
	}
	if *imgPath == "" {
		fmt.Println("(foto kosong: 0 item itu wajar. Coba -image dengan foto nota asli untuk melihat akurasinya.)")
	}
}

func blankJPEG() []byte {
	im := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			im.Set(x, y, color.White)
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, im, nil)
	return buf.Bytes()
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(1)
}
