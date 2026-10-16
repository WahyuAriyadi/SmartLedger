package receipt

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"sort"
	"time"
	"unicode/utf8"

	"smartledger/internal/apperr"
	"smartledger/internal/ledger"
	"smartledger/internal/platform/db"
	"smartledger/internal/secure"
)

const (
	aadMerchant = "receipts.merchant"
	aadRaw      = "receipts.ocr_raw"
	aadItemName = "receipt_items.name"
)

type Item struct {
	Name       string  `json:"name"`
	Qty        float64 `json:"qty"`
	UnitPrice  int64   `json:"unit_price"`
	Subtotal   int64   `json:"subtotal"`
	CategoryID *string `json:"category_id"`
}

type Receipt struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"` // processing | needs_review | confirmed | failed
	Engine      string    `json:"engine"`
	Merchant    string    `json:"merchant"`
	ReceiptDate *string   `json:"receipt_date"`
	Total       int64     `json:"total"`
	Confidence  float64   `json:"confidence"`
	Items       []Item    `json:"items"`
	CreatedAt   time.Time `json:"created_at"`
}

type Service struct {
	db        *db.DB
	cipher    *secure.Cipher
	storage   Storage
	extractor Extractor
	imgSem    chan struct{} // membatasi decode gambar bersamaan (memori)
	loc       *time.Location
	now       func() time.Time
}

func NewService(d *db.DB, c *secure.Cipher, st Storage, ex Extractor) *Service {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		loc = time.FixedZone("WIB", 7*3600)
	}
	return &Service{db: d, cipher: c, storage: st, extractor: ex, imgSem: make(chan struct{}, 2), loc: loc, now: time.Now}
}

func (s *Service) sanitize(ctx context.Context, raw []byte) ([]byte, error) {
	select {
	case s.imgSem <- struct{}{}:
		defer func() { <-s.imgSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return SanitizeImage(raw)
}

func (s *Service) encOrNil(plain, aad string) (any, error) {
	if plain == "" {
		return nil, nil
	}
	return s.cipher.Encrypt(plain, aad)
}

// Upload: validasi gambar -> catat (processing) -> simpan foto -> OCR -> simpan hasil (needs_review).
// Panggilan OCR TIDAK berada di dalam transaksi database agar koneksi tidak tertahan.
// Kegagalan OCR bukan error bagi pengguna: nota ditandai 'failed' dan UI menawarkan input manual.
func (s *Service) Upload(ctx context.Context, uid, bid string, raw []byte) (Receipt, error) {
	jpg, err := s.sanitize(ctx, raw)
	if err != nil {
		return Receipt{}, err
	}
	rid := secure.NewUUID()
	err = s.db.InTx(ctx, uid, func(tx *sql.Tx) error {
		if err := ledger.RequireBusiness(ctx, tx, bid); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO receipts (id, business_id, uploaded_by, image_path, status, engine)
			VALUES ($1, $2, $3, $4, 'processing', $5)`, rid, bid, uid, bid+"/"+rid+".jpg", s.extractor.Name()); err != nil {
			return err
		}
		return ledger.Audit(ctx, tx, uid, bid, "receipt_uploaded", "receipt", rid)
	})
	if err != nil {
		return Receipt{}, err
	}

	if err := s.storage.Put(ctx, bid+"/"+rid+".jpg", jpg, "image/jpeg"); err != nil {
		s.markFailed(ctx, uid, rid)
		return Receipt{}, err
	}

	ex, err := s.extractor.Extract(ctx, jpg, "image/jpeg")
	if err == nil {
		err = ex.Normalize()
	}
	if err != nil {
		slog.WarnContext(ctx, "ekstraksi nota gagal", "receipt_id", rid, "err", err)
		s.markFailed(ctx, uid, rid)
		return s.Get(ctx, uid, bid, rid)
	}
	if err := s.saveExtraction(ctx, uid, bid, rid, ex); err != nil {
		s.markFailed(ctx, uid, rid)
		return Receipt{}, err
	}
	return s.Get(ctx, uid, bid, rid)
}

func (s *Service) markFailed(ctx context.Context, uid, rid string) {
	// Konteks baru: request asal mungkin sudah dibatalkan/timeout.
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = s.db.InTx(c, uid, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(c, "UPDATE receipts SET status = 'failed', error = 'ekstraksi gagal' WHERE id = $1 AND status = 'processing'", rid)
		return err
	})
}

func (s *Service) saveExtraction(ctx context.Context, uid, bid, rid string, ex Extraction) error {
	merchantEnc, err := s.encOrNil(ex.Merchant, aadMerchant)
	if err != nil {
		return err
	}
	rawEnc, err := s.cipher.Encrypt(ex.Raw, aadRaw)
	if err != nil {
		return err
	}
	rawJSON, _ := json.Marshal(rawEnc) // kolom JSONB: simpan ciphertext sebagai string JSON

	return s.db.InTx(ctx, uid, func(tx *sql.Tx) error {
		hints := map[string]string{}
		rows, err := tx.QueryContext(ctx, `SELECT name, id FROM categories
			WHERE business_id IS NULL AND kind = 'expense' AND scope IN ('business', 'any')`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name, id string
			if err := rows.Scan(&name, &id); err != nil {
				rows.Close()
				return err
			}
			hints[name] = id
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, "DELETE FROM receipt_items WHERE receipt_id = $1", rid); err != nil {
			return err
		}
		for i, it := range ex.Items {
			nameEnc, err := s.cipher.Encrypt(it.Name, aadItemName)
			if err != nil {
				return err
			}
			var cat any
			if id, ok := hints[it.CategoryHint]; ok {
				cat = id
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO receipt_items (receipt_id, position, name, qty, unit_price, subtotal, category_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				rid, i, nameEnc, it.Qty, it.UnitPrice, lineTotal(it.Qty, it.UnitPrice), cat); err != nil {
				return err
			}
		}
		var date any
		if ex.Date != "" {
			date = ex.Date
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE receipts SET status = 'needs_review', merchant = $2, receipt_date = $3, total = $4,
			                    confidence = $5, ocr_raw = $6
			WHERE id = $1 AND business_id = $7`,
			rid, merchantEnc, date, ex.Total(), ex.Confidence, string(rawJSON), bid)
		return err
	})
}

func (s *Service) Get(ctx context.Context, uid, bid, rid string) (Receipt, error) {
	var r Receipt
	if !secure.IsUUID(rid) {
		return r, apperr.NotFound("nota tidak ditemukan")
	}
	err := s.db.InTx(ctx, uid, func(tx *sql.Tx) error {
		r = Receipt{Items: []Item{}}
		if err := ledger.RequireBusiness(ctx, tx, bid); err != nil {
			return err
		}
		var merchant sql.NullString
		var date sql.NullTime
		var total sql.NullInt64
		var conf sql.NullFloat64
		err := tx.QueryRowContext(ctx, `
			SELECT id, status, engine, merchant, receipt_date, total, confidence, created_at
			FROM receipts WHERE id = $1 AND business_id = $2`, rid, bid).
			Scan(&r.ID, &r.Status, &r.Engine, &merchant, &date, &total, &conf, &r.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFound("nota tidak ditemukan")
		}
		if err != nil {
			return err
		}
		if merchant.Valid {
			if r.Merchant, err = s.cipher.Decrypt(merchant.String, aadMerchant); err != nil {
				return err
			}
		}
		if date.Valid {
			d := date.Time.Format("2006-01-02")
			r.ReceiptDate = &d
		}
		r.Total, r.Confidence = total.Int64, conf.Float64

		rows, err := tx.QueryContext(ctx, `
			SELECT name, qty, unit_price, subtotal, category_id FROM receipt_items
			WHERE receipt_id = $1 ORDER BY position`, rid)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it Item
			var nameEnc string
			var cat sql.NullString
			if err := rows.Scan(&nameEnc, &it.Qty, &it.UnitPrice, &it.Subtotal, &cat); err != nil {
				return err
			}
			if it.Name, err = s.cipher.Decrypt(nameEnc, aadItemName); err != nil {
				return err
			}
			if cat.Valid {
				it.CategoryID = &cat.String
			}
			r.Items = append(r.Items, it)
		}
		return rows.Err()
	})
	return r, err
}

// Image mengambil foto nota. Kepemilikan diperiksa lewat database (RLS) SEBELUM menyentuh storage.
func (s *Service) Image(ctx context.Context, uid, bid, rid string) ([]byte, error) {
	if !secure.IsUUID(rid) {
		return nil, apperr.NotFound("nota tidak ditemukan")
	}
	var key string
	err := s.db.InTx(ctx, uid, func(tx *sql.Tx) error {
		if err := ledger.RequireBusiness(ctx, tx, bid); err != nil {
			return err
		}
		err := tx.QueryRowContext(ctx, "SELECT image_path FROM receipts WHERE id = $1 AND business_id = $2", rid, bid).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFound("nota tidak ditemukan")
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	b, err := s.storage.Get(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return nil, apperr.NotFound("foto nota tidak ditemukan")
	}
	return b, err
}

// ---------- konfirmasi ----------

type ConfirmItem struct {
	Name       string  `json:"name"`
	Qty        float64 `json:"qty"`
	UnitPrice  int64   `json:"unit_price"`
	CategoryID *string `json:"category_id"`
}

type ConfirmInput struct {
	Wallet   string        `json:"wallet"` // default: business
	Merchant string        `json:"merchant"`
	Date     string        `json:"date"` // YYYY-MM-DD, opsional
	Items    []ConfirmItem `json:"items"`
}

type CreatedTx struct {
	ID         string `json:"id"`
	CategoryID string `json:"category_id"`
	Amount     int64  `json:"amount"`
}

type ConfirmResult struct {
	ReceiptID    string      `json:"receipt_id"`
	Total        int64       `json:"total"`
	Transactions []CreatedTx `json:"transactions"`
}

func (in *ConfirmInput) validate(now time.Time) (time.Time, error) {
	if in.Wallet == "" {
		in.Wallet = "business"
	}
	if in.Wallet != "business" && in.Wallet != "personal" {
		return time.Time{}, apperr.Validation("invalid_wallet", "dompet harus 'business' atau 'personal'")
	}
	if n := len(in.Items); n < 1 || n > maxItems {
		return time.Time{}, apperr.Validation("invalid_items", "jumlah item harus 1-50")
	}
	if utf8.RuneCountInString(in.Merchant) > maxMerchant {
		return time.Time{}, apperr.Validation("invalid_merchant", "nama toko terlalu panjang")
	}
	var total int64
	for i := range in.Items {
		it := &in.Items[i]
		it.Name = cleanText(it.Name, maxNameLen)
		if it.Name == "" {
			return time.Time{}, apperr.Validation("invalid_item", "nama item wajib diisi")
		}
		if math.IsNaN(it.Qty) || it.Qty <= 0 || it.Qty > maxQty || math.Abs(it.Qty*1000-math.Round(it.Qty*1000)) > 1e-9 {
			return time.Time{}, apperr.Validation("invalid_item", "jumlah item tidak valid (maksimal 3 desimal)")
		}
		if it.UnitPrice < 0 || it.UnitPrice > ledger.MaxAmount {
			return time.Time{}, apperr.Validation("invalid_item", "harga item tidak valid")
		}
		if it.CategoryID != nil && !secure.IsUUID(*it.CategoryID) {
			return time.Time{}, apperr.Validation("invalid_category", "category_id tidak valid")
		}
		total += lineTotal(it.Qty, it.UnitPrice)
		if total > ledger.MaxAmount {
			return time.Time{}, apperr.Validation("invalid_amount", "total nota melebihi batas")
		}
	}
	if total < 1 {
		return time.Time{}, apperr.Validation("invalid_amount", "total nota harus lebih dari nol")
	}
	occurred := now
	if in.Date != "" {
		d, err := time.Parse("2006-01-02", in.Date)
		if err != nil {
			return time.Time{}, apperr.Validation("invalid_date", "format tanggal harus YYYY-MM-DD")
		}
		occurred = time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, now.Location())
		if occurred.After(now.Add(24*time.Hour)) || occurred.Before(now.AddDate(-5, 0, 0)) {
			return time.Time{}, apperr.Validation("invalid_date", "tanggal nota di luar rentang yang diizinkan")
		}
	}
	return occurred, nil
}

// Confirm menyimpan koreksi pengguna dan membuat transaksi pengeluaran, satu per kategori,
// semuanya dalam SATU transaksi database (atomik). Baris receipt dikunci (FOR UPDATE) sehingga
// konfirmasi ganda/serentak tidak bisa membuat transaksi dobel.
func (s *Service) Confirm(ctx context.Context, uid, bid, rid string, in ConfirmInput) (ConfirmResult, error) {
	if !secure.IsUUID(rid) {
		return ConfirmResult{}, apperr.NotFound("nota tidak ditemukan")
	}
	occurred, err := in.validate(s.now().In(s.loc))
	if err != nil {
		return ConfirmResult{}, err
	}
	merchantEnc, err := s.encOrNil(in.Merchant, aadMerchant)
	if err != nil {
		return ConfirmResult{}, err
	}
	noteText := in.Merchant
	if noteText == "" {
		noteText = "Nota belanja"
	}
	noteEnc, err := s.cipher.Encrypt(noteText, ledger.AADNote)
	if err != nil {
		return ConfirmResult{}, err
	}

	var res ConfirmResult
	err = s.db.InTx(ctx, uid, func(tx *sql.Tx) error {
		res = ConfirmResult{ReceiptID: rid}
		if err := ledger.RequireBusiness(ctx, tx, bid); err != nil {
			return err
		}
		var status string
		err := tx.QueryRowContext(ctx, "SELECT status FROM receipts WHERE id = $1 AND business_id = $2 FOR UPDATE", rid, bid).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFound("nota tidak ditemukan")
		}
		if err != nil {
			return err
		}
		// 'failed' (OCR gagal) tetap boleh dikonfirmasi: pengguna mengisi itemnya manual di layar tinjau.
		if status != "needs_review" && status != "failed" {
			return apperr.Conflict("receipt_not_reviewable", "nota ini sudah dikonfirmasi atau belum siap ditinjau")
		}
		wid, err := ledger.WalletID(ctx, tx, bid, in.Wallet)
		if err != nil {
			return err
		}

		// Kategori yang valid: pengeluaran yang cocok dengan dompet tujuan.
		valid := map[string]bool{}
		var fallback string
		fallbackName := "Lain-lain (usaha)"
		if in.Wallet == "personal" {
			fallbackName = "Lain-lain (pribadi)"
		}
		rows, err := tx.QueryContext(ctx, `SELECT id, name, scope FROM categories WHERE kind = 'expense'
			AND (business_id IS NULL OR business_id = $1)`, bid)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, name, scope string
			if err := rows.Scan(&id, &name, &scope); err != nil {
				rows.Close()
				return err
			}
			if scope == "any" || scope == in.Wallet {
				valid[id] = true
				if name == fallbackName {
					fallback = id
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, "DELETE FROM receipt_items WHERE receipt_id = $1", rid); err != nil {
			return err
		}
		perCat := map[string]int64{}
		for i, it := range in.Items {
			cat := fallback
			if it.CategoryID != nil {
				if !valid[*it.CategoryID] {
					return apperr.Validation("invalid_category", "kategori tidak cocok dengan dompet tujuan")
				}
				cat = *it.CategoryID
			}
			sub := lineTotal(it.Qty, it.UnitPrice)
			nameEnc, err := s.cipher.Encrypt(it.Name, aadItemName)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO receipt_items (receipt_id, position, name, qty, unit_price, subtotal, category_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, rid, i, nameEnc, it.Qty, it.UnitPrice, sub, cat); err != nil {
				return err
			}
			perCat[cat] += sub
			res.Total += sub
		}

		cats := make([]string, 0, len(perCat))
		for c := range perCat {
			cats = append(cats, c)
		}
		sort.Strings(cats) // urutan deterministik
		for _, c := range cats {
			if perCat[c] < 1 {
				continue // kategori yang semua itemnya gratis tidak menghasilkan transaksi
			}
			id, err := ledger.InsertReceiptExpense(ctx, tx, ledger.ReceiptExpense{
				BusinessID: bid, WalletID: wid, UserID: uid, ReceiptID: rid, CategoryID: c,
				Amount: perCat[c], OccurredAt: occurred, NoteEnc: noteEnc,
			})
			if err != nil {
				return err
			}
			res.Transactions = append(res.Transactions, CreatedTx{ID: id, CategoryID: c, Amount: perCat[c]})
		}

		var date any
		if in.Date != "" {
			date = in.Date
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE receipts SET status = 'confirmed', merchant = $2, receipt_date = $3, total = $4, confirmed_at = now()
			WHERE id = $1`, rid, merchantEnc, date, res.Total); err != nil {
			return err
		}
		return ledger.Audit(ctx, tx, uid, bid, "receipt_confirmed", "receipt", rid)
	})
	return res, err
}
