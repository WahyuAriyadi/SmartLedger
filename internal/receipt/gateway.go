package receipt

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"smartledger/internal/apperr"
	"smartledger/internal/platform/httpx"
)

// Gateway adalah gerbang OCR TANPA PENYIMPANAN untuk mode perangkat: menerima foto, memanggil mesin OCR,
// mengembalikan hasil, lalu melupakan semuanya. Tidak ada akun, database, atau berkas yang ditulis, dan log
// tidak pernah memuat isi foto maupun hasil bacaan. Karena tidak ada akun, penyalahgunaan dibatasi dengan:
// kode akses opsional, rate limit per IP (dipasang pemanggil), dan batas harian global (pengaman biaya).
type Gateway struct {
	ex         Extractor
	accessHash [32]byte
	needCode   bool
	dailyCap   int

	mu   sync.Mutex
	day  string
	used int
	loc  *time.Location
	now  func() time.Time
	sem  chan struct{} // membatasi decode gambar bersamaan (memori)
}

func NewGateway(ex Extractor, accessCode string, dailyCap int) *Gateway {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		loc = time.FixedZone("WIB", 7*3600)
	}
	g := &Gateway{ex: ex, dailyCap: dailyCap, loc: loc, now: time.Now, sem: make(chan struct{}, 2)}
	if accessCode != "" {
		g.needCode = true
		g.accessHash = sha256.Sum256([]byte(accessCode))
	}
	return g
}

func (g *Gateway) Engine() string       { return g.ex.Name() }
func (g *Gateway) AccessRequired() bool { return g.needCode }

func (g *Gateway) Routes(mux *http.ServeMux, limit httpx.Middleware) {
	mux.Handle("POST /v1/ocr/extract", limit(httpx.Handle(g.extract)))
}

// reserve mengambil satu jatah harian (zona WIB). Jatah tidak dikembalikan bila OCR gagal:
// biaya sudah tentu keluar pada panggilan cloud.
func (g *Gateway) reserve() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	today := g.now().In(g.loc).Format("2006-01-02")
	if g.day != today {
		g.day, g.used = today, 0
	}
	if g.dailyCap > 0 && g.used >= g.dailyCap {
		return false
	}
	g.used++
	return true
}

type gatewayItem struct {
	Name         string  `json:"name"`
	Qty          float64 `json:"qty"`
	UnitPrice    int64   `json:"unit_price"`
	CategoryHint string  `json:"category_hint"`
}

type gatewayResponse struct {
	Engine     string        `json:"engine"`
	Failed     bool          `json:"failed"`
	Merchant   string        `json:"merchant"`
	Date       string        `json:"date"`
	Confidence float64       `json:"confidence"`
	Items      []gatewayItem `json:"items"`
}

func (g *Gateway) extract(w http.ResponseWriter, r *http.Request) error {
	if g.needCode {
		got := sha256.Sum256([]byte(r.Header.Get("X-Access-Code")))
		if subtle.ConstantTimeCompare(got[:], g.accessHash[:]) != 1 {
			return apperr.Unauthorized("kode akses OCR salah atau belum diisi")
		}
	}
	raw, err := readImageUpload(w, r)
	if err != nil {
		return err
	}

	select {
	case g.sem <- struct{}{}:
		defer func() { <-g.sem }()
	case <-r.Context().Done():
		return r.Context().Err()
	}
	jpg, err := SanitizeImage(raw)
	if err != nil {
		return err
	}
	if !g.reserve() {
		return apperr.TooMany("kuota OCR harian sudah habis, silakan isi manual atau coba lagi besok")
	}

	// Lepas dari batas 10 detik milik request, tapi tetap dibatasi OCR timeout sendiri.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), ocrTimeout)
	defer cancel()
	resp := gatewayResponse{Engine: g.ex.Name(), Items: []gatewayItem{}}
	ex, err := g.ex.Extract(ctx, jpg, "image/jpeg")
	if err == nil {
		err = ex.Normalize()
	}
	if err != nil {
		slog.WarnContext(r.Context(), "ekstraksi OCR gagal", "request_id", httpx.RequestIDFrom(r.Context()), "err", err)
		resp.Failed = true // klien menampilkan form manual; bukan error bagi pengguna
		return httpx.WriteJSON(w, http.StatusOK, resp)
	}
	resp.Merchant, resp.Date, resp.Confidence = ex.Merchant, ex.Date, ex.Confidence
	for _, it := range ex.Items {
		resp.Items = append(resp.Items, gatewayItem{Name: it.Name, Qty: it.Qty, UnitPrice: it.UnitPrice, CategoryHint: it.CategoryHint})
	}
	return httpx.WriteJSON(w, http.StatusOK, resp)
}
