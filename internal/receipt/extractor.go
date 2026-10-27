package receipt

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"smartledger/internal/ledger"
)

// Extractor turns a receipt photo into structured data. Local implementation: MockExtractor.
// Cloud implementations: GeminiExtractor (API key) and VertexExtractor (service account).
type Extractor interface {
	Name() string
	Extract(ctx context.Context, img []byte, mime string) (Extraction, error)
}

type Extraction struct {
	Merchant   string
	Date       string // YYYY-MM-DD, may be empty
	Items      []ExtractedItem
	Confidence float64
	Raw        string // raw OCR engine response (stored encrypted for accuracy evaluation)
}

type ExtractedItem struct {
	Name         string
	Qty          float64
	UnitPrice    int64
	CategoryHint string // built-in category name, e.g. "Bahan baku"
}

const (
	maxItems    = 50
	maxNameLen  = 100
	maxMerchant = 80
	maxQty      = 100_000
)

func cleanText(s string, limit int) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if utf8.RuneCountInString(out) > limit {
		out = string([]rune(out)[:limit])
	}
	return out
}

// Normalize treats OCR output as UNTRUSTED INPUT. Text in the photo may carry instructions
// (prompt injection) or garbage numbers; everything is bounded, sanitized, and validated
// before it reaches the database.
func (e *Extraction) Normalize() error {
	if len(e.Items) == 0 {
		return fmt.Errorf("tidak ada item terbaca")
	}
	if len(e.Items) > maxItems {
		e.Items = e.Items[:maxItems]
	}
	e.Merchant = cleanText(e.Merchant, maxMerchant)
	kept := e.Items[:0]
	for _, it := range e.Items {
		it.Name = cleanText(it.Name, maxNameLen)
		it.CategoryHint = cleanText(it.CategoryHint, 40)
		if it.Name == "" || math.IsNaN(it.Qty) || it.Qty <= 0 || it.Qty > maxQty ||
			it.UnitPrice < 0 || it.UnitPrice > ledger.MaxAmount {
			continue // malformed items are dropped, not guessed
		}
		it.Qty = math.Round(it.Qty*1000) / 1000
		kept = append(kept, it)
	}
	e.Items = kept
	if len(e.Items) == 0 {
		return fmt.Errorf("tidak ada item valid")
	}
	if _, err := time.Parse("2006-01-02", e.Date); err != nil {
		e.Date = ""
	}
	if math.IsNaN(e.Confidence) || e.Confidence < 0 {
		e.Confidence = 0
	}
	if e.Confidence > 1 {
		e.Confidence = 1
	}
	return nil
}

func lineTotal(qty float64, unit int64) int64 { return int64(math.Round(qty * float64(unit))) }

func (e *Extraction) Total() int64 {
	var t int64
	for _, it := range e.Items {
		t += lineTotal(it.Qty, it.UnitPrice)
	}
	return t
}

// ---------- mock OCR engine for local development ----------

// MockExtractor does NOT read the image. It deterministically picks one of the sample receipts
// from the image hash, so the whole flow (upload, review, correct, confirm) can be exercised
// without cloud services or cost.
type MockExtractor struct{}

func (MockExtractor) Name() string { return "mock" }

func (MockExtractor) Extract(_ context.Context, img []byte, _ string) (Extraction, error) {
	samples := []Extraction{
		{Merchant: "Toko Sumber Rejeki", Confidence: 0.94, Items: []ExtractedItem{
			{"Beras 25 kg", 1, 310_000, "Bahan baku"},
			{"Minyak goreng 5 L", 1, 95_000, "Bahan baku"},
			{"Plastik kemasan", 1, 22_000, "Kemasan"},
		}},
		{Merchant: "Pasar Gede - Pak Slamet", Confidence: 0.88, Items: []ExtractedItem{
			{"Ayam potong", 6.5, 38_000, "Bahan baku"},
			{"Bawang merah", 2, 32_000, "Bahan baku"},
			{"Cabai rawit", 1.5, 60_000, "Bahan baku"},
		}},
		{Merchant: "Agen Gas Mandiri", Confidence: 0.91, Items: []ExtractedItem{
			{"Gas LPG 3 kg", 3, 21_000, "Listrik, air, dan gas"},
			{"Ongkos antar", 1, 5_000, "Transportasi"},
		}},
	}
	sum := sha256.Sum256(img)
	out := samples[int(sum[0])%len(samples)]
	out.Raw = `{"engine":"mock","note":"data contoh, bukan hasil membaca gambar"}`
	return out, nil
}
