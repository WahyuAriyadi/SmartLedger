package ledger

import (
	"strings"
	"testing"
	"time"

	"smartledger/internal/secure"
)

func TestTransactionValidation(t *testing.T) {
	now := time.Now()
	good := NewTransaction{Wallet: "business", Kind: "income", Amount: 50_000}
	if err := good.Validate(now); err != nil {
		t.Fatalf("input valid ditolak: %v", err)
	}
	future, past := now.Add(72*time.Hour), now.AddDate(-6, 0, 0)
	badID := "bukan-uuid"
	cases := map[string]NewTransaction{
		"dompet salah":         {Wallet: "x", Kind: "income", Amount: 1},
		"jenis salah":          {Wallet: "business", Kind: "transfer", Amount: 1},
		"nominal nol":          {Wallet: "business", Kind: "income", Amount: 0},
		"nominal negatif":      {Wallet: "business", Kind: "expense", Amount: -5},
		"nominal raksasa":      {Wallet: "business", Kind: "income", Amount: MaxAmount + 1},
		"catatan panjang":      {Wallet: "business", Kind: "income", Amount: 1, Note: strings.Repeat("a", MaxNoteLen+1)},
		"client id rusak":      {Wallet: "business", Kind: "income", Amount: 1, ClientID: &badID},
		"tanggal masa dpn":     {Wallet: "business", Kind: "income", Amount: 1, OccurredAt: &future},
		"tanggal terlalu lama": {Wallet: "business", Kind: "income", Amount: 1, OccurredAt: &past},
	}
	for name, in := range cases {
		if in.Validate(now) == nil {
			t.Errorf("%s harus ditolak", name)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	id := secure.NewUUID()
	ts := time.Date(2026, 10, 8, 7, 0, 0, 123456000, time.UTC)
	gotT, gotID, err := decodeCursor(encodeCursor(ts, id))
	if err != nil || !gotT.Equal(ts) || gotID != id {
		t.Fatalf("cursor tidak round-trip: %v %v %v", gotT, gotID, err)
	}
	for _, bad := range []string{"", "!!!", "YWJj", encodeCursor(ts, id) + "x"} {
		if _, _, err := decodeCursor(bad); err == nil {
			t.Errorf("cursor %q harus ditolak", bad)
		}
	}
}
