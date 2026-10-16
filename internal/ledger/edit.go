package ledger

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"smartledger/internal/apperr"
	"smartledger/internal/secure"
)

// UpdateTransaction hanya memuat kolom yang BOLEH diubah. Nominal, dompet, dan jenis tidak ada di sini
// (dan ditolak trigger database); koreksi nominal dilakukan dengan menghapus lalu mencatat ulang.
type UpdateTransaction struct {
	Note       string     `json:"note"`        // selalu menggantikan; "" menghapus catatan
	CategoryID *string    `json:"category_id"` // null = tanpa kategori; diabaikan untuk transfer
	OccurredAt *time.Time `json:"occurred_at"` // null = tidak diubah
}

func (in *UpdateTransaction) Validate(now time.Time) error {
	if err := validateNote(in.Note); err != nil {
		return err
	}
	if err := validateOptionalUUID(in.CategoryID, "invalid_category", "category_id tidak valid"); err != nil {
		return err
	}
	if in.OccurredAt != nil && (in.OccurredAt.After(now.Add(24*time.Hour)) || in.OccurredAt.Before(now.AddDate(-5, 0, 0))) {
		return apperr.Validation("invalid_date", "tanggal transaksi di luar rentang yang diizinkan")
	}
	return nil
}

// Update mengubah catatan, kategori, dan tanggal. Untuk transfer, catatan dan tanggal diterapkan
// pada kedua sisi (transfer_group_id yang sama) agar tetap konsisten.
func (s *Service) Update(ctx context.Context, uid, bid, txID string, in UpdateTransaction) (Transaction, error) {
	if !secure.IsUUID(txID) {
		return Transaction{}, apperr.NotFound("transaksi tidak ditemukan")
	}
	if err := in.Validate(s.now()); err != nil {
		return Transaction{}, err
	}
	noteEnc, err := s.encryptNote(in.Note)
	if err != nil {
		return Transaction{}, err
	}
	var occurred sql.NullTime
	if in.OccurredAt != nil {
		occurred = sql.NullTime{Time: *in.OccurredAt, Valid: true}
	}

	var out Transaction
	err = s.db.InTx(ctx, uid, func(tx *sql.Tx) error {
		if err := requireBusiness(ctx, tx, bid); err != nil {
			return err
		}
		var kind, walletKind string
		var group sql.NullString
		err := tx.QueryRowContext(ctx, `
			SELECT t.kind, w.kind, t.transfer_group_id
			FROM transactions t JOIN wallets w ON w.id = t.wallet_id
			WHERE t.id = $1 AND t.business_id = $2 AND t.deleted_at IS NULL`, txID, bid).Scan(&kind, &walletKind, &group)
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.NotFound("transaksi tidak ditemukan")
		}
		if err != nil {
			return err
		}

		if kind == "transfer" {
			if _, err := tx.ExecContext(ctx, `
				UPDATE transactions SET note = $3, occurred_at = COALESCE($4::timestamptz, occurred_at)
				WHERE business_id = $1 AND transfer_group_id = $2 AND deleted_at IS NULL`,
				bid, group.String, noteEnc, occurred); err != nil {
				return err
			}
		} else {
			if in.CategoryID != nil {
				var ckind, scope string
				err := tx.QueryRowContext(ctx, "SELECT kind, scope FROM categories WHERE id = $1", *in.CategoryID).Scan(&ckind, &scope)
				if errors.Is(err, sql.ErrNoRows) {
					return apperr.Validation("invalid_category", "kategori tidak ditemukan")
				}
				if err != nil {
					return err
				}
				if ckind != kind || (scope != "any" && scope != walletKind) {
					return apperr.Validation("invalid_category", "kategori tidak cocok dengan jenis atau dompet")
				}
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE transactions SET note = $3, category_id = $4, occurred_at = COALESCE($5::timestamptz, occurred_at)
				WHERE id = $1 AND business_id = $2`, txID, bid, noteEnc, in.CategoryID, occurred); err != nil {
				return err
			}
		}
		if err := audit(ctx, tx, uid, bid, "transaction_updated", "transaction", txID); err != nil {
			return err
		}
		out, err = s.scanTx(tx.QueryRowContext(ctx, txSelect+" WHERE t.business_id = $1 AND t.id = $2", bid, txID))
		return err
	})
	return out, err
}

// ---------- ringkasan harian ----------

type DayTotal struct {
	Date    string `json:"date"` // YYYY-MM-DD (WIB)
	Income  int64  `json:"income"`
	Expense int64  `json:"expense"`
}

// Daily mengembalikan pemasukan/pengeluaran Dompet Usaha untuk N hari terakhir (termasuk hari ini),
// diisi nol pada hari tanpa transaksi. Transfer tidak dihitung: itu pemindahan, bukan penjualan/biaya.
func (s *Service) Daily(ctx context.Context, uid, bid string, days int) ([]DayTotal, error) {
	if days < 1 || days > 31 {
		return nil, apperr.Validation("invalid_days", "days harus 1-31")
	}
	today := s.now().In(s.loc).Format("2006-01-02")
	out := []DayTotal{}
	err := s.db.InTx(ctx, uid, func(tx *sql.Tx) error {
		out = out[:0]
		if err := requireBusiness(ctx, tx, bid); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `
			WITH days AS (
				SELECT generate_series($2::timestamp - make_interval(days => $3::int - 1), $2::timestamp, interval '1 day')::date AS day
			), tx AS (
				SELECT (t.occurred_at AT TIME ZONE 'Asia/Jakarta')::date AS day, t.kind, t.amount
				FROM transactions t JOIN wallets w ON w.id = t.wallet_id
				WHERE t.business_id = $1 AND t.deleted_at IS NULL AND w.kind = 'business' AND t.kind IN ('income', 'expense')
			)
			SELECT to_char(days.day, 'YYYY-MM-DD'),
			       COALESCE(SUM(tx.amount) FILTER (WHERE tx.kind = 'income'), 0),
			       COALESCE(SUM(tx.amount) FILTER (WHERE tx.kind = 'expense'), 0)
			FROM days LEFT JOIN tx ON tx.day = days.day
			GROUP BY days.day ORDER BY days.day`, bid, today, days)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d DayTotal
			if err := rows.Scan(&d.Date, &d.Income, &d.Expense); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}
