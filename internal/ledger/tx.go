package ledger

import (
	"context"
	"database/sql"
	"time"
)

// Blok bangunan untuk paket lain (mis. receipt) yang harus menulis ke ledger di dalam
// transaksi miliknya sendiri, supaya "konfirmasi nota" dan "pembuatan transaksi" atomik.

func RequireBusiness(ctx context.Context, tx *sql.Tx, bid string) error {
	return requireBusiness(ctx, tx, bid)
}

func WalletID(ctx context.Context, tx *sql.Tx, bid, kind string) (string, error) {
	return walletID(ctx, tx, bid, kind)
}

func Audit(ctx context.Context, tx *sql.Tx, uid, bid, action, entity, entityID string) error {
	return audit(ctx, tx, uid, bid, action, entity, entityID)
}

type ReceiptExpense struct {
	BusinessID, WalletID, UserID, ReceiptID string
	CategoryID                              string
	Amount                                  int64
	OccurredAt                              time.Time
	NoteEnc                                 any // sudah terenkripsi (ledger.AADNote) atau nil
}

// InsertReceiptExpense menulis satu pengeluaran hasil nota (source = 'ocr').
func InsertReceiptExpense(ctx context.Context, tx *sql.Tx, e ReceiptExpense) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `
		INSERT INTO transactions (business_id, wallet_id, category_id, kind, direction, amount,
		                          occurred_at, note, source, receipt_id, created_by)
		VALUES ($1, $2, $3, 'expense', 'out', $4, $5, $6, 'ocr', $7, $8)
		RETURNING id`,
		e.BusinessID, e.WalletID, e.CategoryID, e.Amount, e.OccurredAt, e.NoteEnc, e.ReceiptID, e.UserID).Scan(&id)
	return id, err
}
