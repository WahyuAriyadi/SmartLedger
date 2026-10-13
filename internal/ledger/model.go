// Package ledger: dompet usaha/pribadi, transaksi, transfer (prive), dan laporan.
package ledger

import (
	"encoding/base64"
	"strings"
	"time"
	"unicode/utf8"

	"smartledger/internal/apperr"
	"smartledger/internal/secure"
)

const (
	MaxAmount  = 10_000_000_000 // Rp10 miliar per transaksi; batas kewajaran, mencegah overflow dan salah ketik
	MaxNoteLen = 280
	AADNote    = "transactions.note"
)

type Business struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

type Wallet struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Balance int64  `json:"balance"`
}

type Category struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Scope string `json:"scope"`
	Name  string `json:"name"`
}

type Transaction struct {
	ID              string    `json:"id"`
	Wallet          string    `json:"wallet"`
	Kind            string    `json:"kind"`
	Direction       string    `json:"direction"`
	Amount          int64     `json:"amount"`
	CategoryID      *string   `json:"category_id"`
	CategoryName    *string   `json:"category_name"`
	Note            string    `json:"note"`
	OccurredAt      time.Time `json:"occurred_at"`
	Source          string    `json:"source"`
	TransferGroupID *string   `json:"transfer_group_id"`
	CreatedAt       time.Time `json:"created_at"`
}

type MonthlySummary struct {
	Month        string `json:"month"`
	Revenue      int64  `json:"revenue"`
	Expenses     int64  `json:"expenses"`
	Profit       int64  `json:"profit"`
	Prive        int64  `json:"prive"`
	CapitalIn    int64  `json:"capital_in"`
	NetPrive     int64  `json:"net_prive"`
	LeakDetected bool   `json:"leak_detected"` // uang yang diambil pribadi melebihi untung
	LeakAmount   int64  `json:"leak_amount"`
}

var businessKinds = map[string]bool{
	"warung_makan": true, "toko_kelontong": true, "pedagang_pasar": true, "jasa": true, "lainnya": true,
}

// ---------- input ----------

type NewTransaction struct {
	Wallet     string     `json:"wallet"` // business | personal
	Kind       string     `json:"kind"`   // income | expense
	CategoryID *string    `json:"category_id"`
	Amount     int64      `json:"amount"`
	OccurredAt *time.Time `json:"occurred_at"`
	Note       string     `json:"note"`
	ClientID   *string    `json:"client_id"` // UUID buatan klien; membuat pengiriman ulang aman (idempoten)
}

func (in *NewTransaction) Validate(now time.Time) error {
	if in.Wallet != "business" && in.Wallet != "personal" {
		return apperr.Validation("invalid_wallet", "dompet harus 'business' atau 'personal'")
	}
	if in.Kind != "income" && in.Kind != "expense" {
		return apperr.Validation("invalid_kind", "jenis harus 'income' atau 'expense'")
	}
	if err := validateAmount(in.Amount); err != nil {
		return err
	}
	if err := validateNote(in.Note); err != nil {
		return err
	}
	if err := validateOptionalUUID(in.CategoryID, "invalid_category", "category_id tidak valid"); err != nil {
		return err
	}
	if err := validateOptionalUUID(in.ClientID, "invalid_client_id", "client_id harus UUID"); err != nil {
		return err
	}
	if in.OccurredAt != nil && (in.OccurredAt.After(now.Add(24*time.Hour)) || in.OccurredAt.Before(now.AddDate(-5, 0, 0))) {
		return apperr.Validation("invalid_date", "tanggal transaksi di luar rentang yang diizinkan")
	}
	return nil
}

type NewTransfer struct {
	Direction string  `json:"direction"` // to_personal (prive) | to_business (setor modal)
	Amount    int64   `json:"amount"`
	Note      string  `json:"note"`
	ClientID  *string `json:"client_id"`
}

func (in *NewTransfer) Validate() error {
	if in.Direction != "to_personal" && in.Direction != "to_business" {
		return apperr.Validation("invalid_direction", "arah harus 'to_personal' atau 'to_business'")
	}
	if err := validateAmount(in.Amount); err != nil {
		return err
	}
	if err := validateNote(in.Note); err != nil {
		return err
	}
	return validateOptionalUUID(in.ClientID, "invalid_client_id", "client_id harus UUID")
}

func validateAmount(a int64) error {
	if a < 1 || a > MaxAmount {
		return apperr.Validation("invalid_amount", "nominal harus antara Rp1 dan Rp10.000.000.000")
	}
	return nil
}

func validateNote(n string) error {
	if utf8.RuneCountInString(n) > MaxNoteLen {
		return apperr.Validation("note_too_long", "catatan maksimal 280 karakter")
	}
	return nil
}

func validateOptionalUUID(v *string, code, msg string) error {
	if v != nil && !secure.IsUUID(*v) {
		return apperr.Validation(code, msg)
	}
	return nil
}

// ---------- kursor paginasi (keyset) ----------

func encodeCursor(t time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeCursor(c string) (time.Time, string, error) {
	bad := apperr.Validation("invalid_cursor", "cursor tidak valid")
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, "", bad
	}
	ts, id, ok := strings.Cut(string(raw), "|")
	if !ok || !secure.IsUUID(id) {
		return time.Time{}, "", bad
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, "", bad
	}
	return t, id, nil
}
