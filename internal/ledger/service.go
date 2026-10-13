package ledger

import (
	"context"
	"database/sql"
	"errors"
	"time"
	"unicode/utf8"

	"smartledger/internal/apperr"
	"smartledger/internal/platform/db"
	"smartledger/internal/secure"
)

// Service berisi aturan bisnis. Setiap method menjalankan semua SQL-nya dalam satu
// transaksi dengan app.user_id terpasang, jadi RLS di database ikut menjaga isolasi
// data walau ada query yang lupa memfilter pemilik.
type Service struct {
	db     *db.DB
	cipher *secure.Cipher
	loc    *time.Location
	now    func() time.Time
}

func NewService(d *db.DB, c *secure.Cipher) *Service {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		loc = time.FixedZone("WIB", 7*3600)
	}
	return &Service{db: d, cipher: c, loc: loc, now: time.Now}
}

var errNoBusiness = apperr.NotFound("usaha tidak ditemukan")

// requireBusiness memastikan usaha ada DAN milik pengguna (RLS menyaring sisanya).
// Memakai 404, bukan 403, agar ID usaha orang lain tidak bisa dienumerasi.
func requireBusiness(ctx context.Context, tx *sql.Tx, bid string) error {
	if !secure.IsUUID(bid) {
		return errNoBusiness
	}
	var one int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM businesses WHERE id = $1", bid).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return errNoBusiness
	}
	return err
}

func walletID(ctx context.Context, tx *sql.Tx, bid, kind string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM wallets WHERE business_id = $1 AND kind = $2", bid, kind).Scan(&id)
	return id, err
}

func audit(ctx context.Context, tx *sql.Tx, uid, bid, action, entity, entityID string) error {
	var eid any
	if entityID != "" {
		eid = entityID
	}
	_, err := tx.ExecContext(ctx,
		"INSERT INTO audit_log (user_id, business_id, action, entity, entity_id) VALUES ($1, $2, $3, $4, $5)",
		uid, bid, action, entity, eid)
	return err
}

// ---------- usaha ----------

func (s *Service) CreateBusiness(ctx context.Context, uid, name, kind string) (Business, error) {
	if n := utf8.RuneCountInString(name); n < 2 || n > 80 {
		return Business{}, apperr.Validation("invalid_name", "nama usaha harus 2-80 karakter")
	}
	if kind == "" {
		kind = "lainnya"
	}
	if !businessKinds[kind] {
		return Business{}, apperr.Validation("invalid_kind", "jenis usaha tidak dikenal")
	}
	var b Business
	err := s.db.InTx(ctx, uid, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM businesses WHERE owner_id = $1", uid).Scan(&count); err != nil {
			return err
		}
		if count >= 5 {
			return apperr.Conflict("business_limit", "maksimal 5 usaha per akun")
		}
		// Trigger database otomatis membuat Dompet Usaha, Dompet Pribadi, dan aturan alert.
		if err := tx.QueryRowContext(ctx,
			"INSERT INTO businesses (owner_id, name, kind) VALUES ($1, $2, $3) RETURNING id, name, kind, created_at",
			uid, name, kind).Scan(&b.ID, &b.Name, &b.Kind, &b.CreatedAt); err != nil {
			return err
		}
		return audit(ctx, tx, uid, b.ID, "business_created", "business", b.ID)
	})
	return b, err
}

func (s *Service) ListBusinesses(ctx context.Context, uid string) ([]Business, error) {
	out := []Business{}
	err := s.db.InTx(ctx, uid, func(tx *sql.Tx) error {
		out = out[:0]
		rows, err := tx.QueryContext(ctx,
			"SELECT id, name, kind, created_at FROM businesses WHERE owner_id = $1 ORDER BY created_at", uid)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b Business
			if err := rows.Scan(&b.ID, &b.Name, &b.Kind, &b.CreatedAt); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) Wallets(ctx context.Context, uid, bid string) ([]Wallet, error) {
	out := []Wallet{}
	err := s.db.InTx(ctx, uid, func(tx *sql.Tx) error {
		out = out[:0]
		if err := requireBusiness(ctx, tx, bid); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT w.id, w.kind, w.name, b.balance
			FROM wallets w JOIN wallet_balances b ON b.wallet_id = w.id
			WHERE w.business_id = $1 ORDER BY w.kind`, bid)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w Wallet
			if err := rows.Scan(&w.ID, &w.Kind, &w.Name, &w.Balance); err != nil {
				return err
			}
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) Categories(ctx context.Context, uid, bid string) ([]Category, error) {
	out := []Category{}
	err := s.db.InTx(ctx, uid, func(tx *sql.Tx) error {
		out = out[:0]
		if err := requireBusiness(ctx, tx, bid); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT id, kind, scope, name FROM categories
			WHERE business_id IS NULL OR business_id = $1
			ORDER BY kind, sort_order, name`, bid)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Category
			if err := rows.Scan(&c.ID, &c.Kind, &c.Scope, &c.Name); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// ---------- transaksi ----------

const txSelect = `
	SELECT t.id, w.kind, t.kind, t.direction, t.amount, t.category_id, c.name, t.note,
	       t.occurred_at, t.source, t.transfer_group_id, t.created_at
	FROM transactions t
	JOIN wallets w ON w.id = t.wallet_id
	LEFT JOIN categories c ON c.id = t.category_id`

type scanner interface{ Scan(dest ...any) error }

func (s *Service) scanTx(row scanner) (Transaction, error) {
	var t Transaction
	var cat, catName, note, group sql.NullString
	if err := row.Scan(&t.ID, &t.Wallet, &t.Kind, &t.Direction, &t.Amount, &cat, &catName, &note,
		&t.OccurredAt, &t.Source, &group, &t.CreatedAt); err != nil {
		return t, err
	}
	if cat.Valid {
		t.CategoryID = &cat.String
	}
	if catName.Valid {
		t.CategoryName = &catName.String
	}
	if group.Valid {
		t.TransferGroupID = &group.String
	}
	if note.Valid {
		plain, err := s.cipher.Decrypt(note.String, AADNote)
		if err != nil {
			return t, err
		}
		t.Note = plain
	}
	return t, nil
}

func (s *Service) encryptNote(note string) (any, error) {
	if note == "" {
		return nil, nil
	}
	return s.cipher.Encrypt(note, AADNote)
}

// AddTransaction mencatat pemasukan/pengeluaran. Mengembalikan created=false bila
// client_id yang sama sudah pernah dikirim (replay), dengan data transaksi aslinya.
func (s *Service) AddTransaction(ctx context.Context, uid, bid string, in NewTransaction) (Transaction, bool, error) {
	if err := in.Validate(s.now()); err != nil {
		return Transaction{}, false, err
	}
	noteEnc, err := s.encryptNote(in.Note)
	if err != nil {
		return Transaction{}, false, err
	}
	direction := "in"
	if in.Kind == "expense" {
		direction = "out"
	}
	occurred := s.now()
	if in.OccurredAt != nil {
		occurred = *in.OccurredAt
	}

	var out Transaction
	created := true
	err = s.db.InTx(ctx, uid, func(tx *sql.Tx) error {
		created = true
		if err := requireBusiness(ctx, tx, bid); err != nil {
			return err
		}
		wid, err := walletID(ctx, tx, bid, in.Wallet)
		if err != nil {
			return err
		}
		if in.CategoryID != nil {
			var ckind, scope string
			err := tx.QueryRowContext(ctx, "SELECT kind, scope FROM categories WHERE id = $1", *in.CategoryID).Scan(&ckind, &scope)
			if errors.Is(err, sql.ErrNoRows) {
				return apperr.Validation("invalid_category", "kategori tidak ditemukan")
			}
			if err != nil {
				return err
			}
			if ckind != in.Kind || (scope != "any" && scope != in.Wallet) {
				return apperr.Validation("invalid_category", "kategori tidak cocok dengan jenis atau dompet")
			}
		}
		var id string
		err = tx.QueryRowContext(ctx, `
			INSERT INTO transactions (business_id, wallet_id, category_id, kind, direction, amount,
			                          occurred_at, note, source, client_id, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'manual', $9, $10)
			ON CONFLICT (business_id, client_id) DO NOTHING
			RETURNING id`,
			bid, wid, in.CategoryID, in.Kind, direction, in.Amount, occurred, noteEnc, in.ClientID, uid).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows): // client_id sudah ada: kembalikan yang lama
			created = false
			err = tx.QueryRowContext(ctx,
				"SELECT id FROM transactions WHERE business_id = $1 AND client_id = $2 AND deleted_at IS NULL",
				bid, in.ClientID).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				return apperr.Conflict("already_deleted", "transaksi dengan client_id ini sudah dihapus")
			}
			if err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if err := audit(ctx, tx, uid, bid, "transaction_created", "transaction", id); err != nil {
				return err
			}
		}
		out, err = s.scanTx(tx.QueryRowContext(ctx, txSelect+" WHERE t.business_id = $1 AND t.id = $2", bid, id))
		return err
	})
	return out, created, err
}

// Transfer memindahkan uang antar dompet: to_personal = prive, to_business = setor modal.
// Dua baris ledger dengan transfer_group_id yang sama, ditulis atomik dalam satu transaksi.
func (s *Service) Transfer(ctx context.Context, uid, bid string, in NewTransfer) ([]Transaction, bool, error) {
	if err := in.Validate(); err != nil {
		return nil, false, err
	}
	noteEnc, err := s.encryptNote(in.Note)
	if err != nil {
		return nil, false, err
	}
	from, to, catName := "business", "personal", "Ambil pribadi (prive)"
	if in.Direction == "to_business" {
		from, to, catName = "personal", "business", "Setor modal"
	}

	var out []Transaction
	created := true
	err = s.db.InTx(ctx, uid, func(tx *sql.Tx) error {
		created, out = true, nil
		if err := requireBusiness(ctx, tx, bid); err != nil {
			return err
		}
		var catID string
		if err := tx.QueryRowContext(ctx,
			"SELECT id FROM categories WHERE business_id IS NULL AND kind = 'transfer' AND name = $1", catName).Scan(&catID); err != nil {
			return err
		}
		fromW, err := walletID(ctx, tx, bid, from)
		if err != nil {
			return err
		}
		toW, err := walletID(ctx, tx, bid, to)
		if err != nil {
			return err
		}
		gid := secure.NewUUID()
		const insert = `
			INSERT INTO transactions (business_id, wallet_id, category_id, kind, direction, amount,
			                          note, source, transfer_group_id, client_id, created_by)
			VALUES ($1, $2, $3, 'transfer', $4, $5, $6, 'manual', $7, $8, $9)`
		// Baris keluar membawa client_id; bila bentrok berarti ini pengiriman ulang.
		res, err := tx.ExecContext(ctx, insert+" ON CONFLICT (business_id, client_id) DO NOTHING",
			bid, fromW, catID, "out", in.Amount, noteEnc, gid, in.ClientID, uid)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			created = false
			err := tx.QueryRowContext(ctx,
				"SELECT transfer_group_id FROM transactions WHERE business_id = $1 AND client_id = $2 AND deleted_at IS NULL",
				bid, in.ClientID).Scan(&gid)
			if errors.Is(err, sql.ErrNoRows) {
				return apperr.Conflict("already_deleted", "transfer dengan client_id ini sudah dihapus")
			}
			if err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, insert, bid, toW, catID, "in", in.Amount, noteEnc, gid, nil, uid); err != nil {
				return err
			}
			if err := audit(ctx, tx, uid, bid, "transfer_created", "transfer", gid); err != nil {
				return err
			}
		}
		rows, err := tx.QueryContext(ctx,
			txSelect+" WHERE t.business_id = $1 AND t.transfer_group_id = $2 AND t.deleted_at IS NULL ORDER BY t.direction DESC", bid, gid)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := s.scanTx(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, created, err
}

type ListParams struct {
	Wallet string // "", "business", atau "personal"
	Limit  int
	Cursor string
}

// List memakai keyset pagination: stabil walau ada data baru dan tidak melambat di halaman dalam.
func (s *Service) List(ctx context.Context, uid, bid string, p ListParams) ([]Transaction, string, error) {
	if p.Wallet != "" && p.Wallet != "business" && p.Wallet != "personal" {
		return nil, "", apperr.Validation("invalid_wallet", "dompet harus 'business' atau 'personal'")
	}
	if p.Limit <= 0 {
		p.Limit = 30
	}
	if p.Limit > 100 {
		p.Limit = 100
	}
	var curTime sql.NullTime
	var curID sql.NullString
	if p.Cursor != "" {
		t, id, err := decodeCursor(p.Cursor)
		if err != nil {
			return nil, "", err
		}
		curTime, curID = sql.NullTime{Time: t, Valid: true}, sql.NullString{String: id, Valid: true}
	}

	out := []Transaction{}
	err := s.db.InTx(ctx, uid, func(tx *sql.Tx) error {
		out = out[:0]
		if err := requireBusiness(ctx, tx, bid); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, txSelect+`
			WHERE t.business_id = $1 AND t.deleted_at IS NULL
			  AND ($2::text = '' OR w.kind = $2)
			  AND ($3::timestamptz IS NULL OR (t.occurred_at, t.id) < ($3::timestamptz, $4::uuid))
			ORDER BY t.occurred_at DESC, t.id DESC
			LIMIT $5`, bid, p.Wallet, curTime, curID, p.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := s.scanTx(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > p.Limit {
		out = out[:p.Limit]
		last := out[len(out)-1]
		next = encodeCursor(last.OccurredAt, last.ID)
	}
	return out, next, nil
}

// Delete menghapus lunak satu transaksi (atau kedua sisi bila itu transfer).
func (s *Service) Delete(ctx context.Context, uid, bid, txID string) error {
	if !secure.IsUUID(txID) {
		return apperr.NotFound("transaksi tidak ditemukan")
	}
	return s.db.InTx(ctx, uid, func(tx *sql.Tx) error {
		if err := requireBusiness(ctx, tx, bid); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE transactions SET deleted_at = now()
			WHERE business_id = $1 AND deleted_at IS NULL
			  AND (id = $2 OR transfer_group_id = (SELECT transfer_group_id FROM transactions WHERE id = $2 AND business_id = $1))`,
			bid, txID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return apperr.NotFound("transaksi tidak ditemukan")
		}
		return audit(ctx, tx, uid, bid, "transaction_deleted", "transaction", txID)
	})
}

// ---------- laporan ----------

// Monthly mengembalikan ringkasan bulan "YYYY-MM" (zona WIB; default bulan ini).
// LeakDetected menyala bila uang yang diambil pribadi melebihi untung: inti deteksi kebocoran kas.
func (s *Service) Monthly(ctx context.Context, uid, bid, month string) (MonthlySummary, error) {
	if month == "" {
		month = s.now().In(s.loc).Format("2006-01")
	}
	first, err := time.Parse("2006-01", month)
	if err != nil {
		return MonthlySummary{}, apperr.Validation("invalid_month", "format bulan harus YYYY-MM")
	}
	m := MonthlySummary{Month: month}
	err = s.db.InTx(ctx, uid, func(tx *sql.Tx) error {
		m = MonthlySummary{Month: month}
		if err := requireBusiness(ctx, tx, bid); err != nil {
			return err
		}
		err := tx.QueryRowContext(ctx, `
			SELECT revenue, expenses, profit, prive, capital_in, net_prive
			FROM monthly_summary WHERE business_id = $1 AND month = $2::date`,
			bid, first.Format("2006-01-02")).
			Scan(&m.Revenue, &m.Expenses, &m.Profit, &m.Prive, &m.CapitalIn, &m.NetPrive)
		if errors.Is(err, sql.ErrNoRows) {
			return nil // bulan tanpa transaksi: semua nol
		}
		return err
	})
	if m.NetPrive > m.Profit {
		m.LeakDetected, m.LeakAmount = true, m.NetPrive-m.Profit
	}
	return m, err
}
