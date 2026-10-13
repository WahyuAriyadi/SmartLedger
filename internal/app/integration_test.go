package app

// Tes end-to-end ke PostgreSQL asli. Dilewati bila variabel env tidak diisi:
//   TEST_MIGRATE_DATABASE_URL  pemilik skema (menjalankan migrasi)
//   TEST_DATABASE_URL          role ledger_app (yang dipakai API; terkena RLS)

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"math/big"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"smartledger/internal/config"
	"smartledger/internal/platform/db"
	"smartledger/internal/platform/httpx"
	"smartledger/migrations"
)

type api struct {
	t    *testing.T
	base string
}

func (a api) do(method, path, token string, body any) (int, map[string]any) {
	a.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, a.base+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}

func (a api) expect(want int, method, path, token string, body any) map[string]any {
	a.t.Helper()
	code, out := a.do(method, path, token, body)
	if code != want {
		a.t.Fatalf("%s %s: status %d, mau %d, body %v", method, path, code, want, out)
	}
	return out
}

func randPhone() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(10_000_000_000))
	return fmt.Sprintf("08%010d", n)
}

func randKey() []byte { b := make([]byte, 32); _, _ = rand.Read(b); return b }

func setup(t *testing.T) (api, *sql.DB, *sql.DB, *db.DB) {
	appURL, ownerURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_MIGRATE_DATABASE_URL")
	if appURL == "" || ownerURL == "" {
		t.Skip("TEST_DATABASE_URL / TEST_MIGRATE_DATABASE_URL tidak diisi")
	}
	ctx := context.Background()
	owner, err := sql.Open("postgres", ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Close() })
	if err := db.Migrate(ctx, owner, migrations.FS, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	appDB, err := db.Open(ctx, appURL, db.Options{MaxOpen: 5, MaxIdle: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { appDB.Close() })

	var ready atomic.Bool
	ready.Store(true)
	cfg := config.Config{
		DataKeys: map[string][]byte{"1": randKey()}, ActiveKeyID: "1", PhonePepper: randKey(),
		SessionTTL: time.Hour, MaxFailedLogins: 5, LockDuration: 15 * time.Minute,
		RequestTimeout: 10 * time.Second, HashConcurrency: 4, RateLimitRPS: 1000, RateLimitBurst: 1000,
		StorageBackend: "local", StorageLocalDir: t.TempDir(), OCRBackend: "mock",
	}
	h, err := New(Deps{Cfg: cfg, DB: appDB, Ready: &ready, AuthLimiter: httpx.NewLimiter(1000, 1000)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return api{t: t, base: srv.URL}, owner, nil, appDB
}

func (a api) signup(name string) (phone, token string) {
	phone = randPhone()
	a.expect(201, "POST", "/v1/auth/register", "", map[string]string{"name": name, "phone": phone, "pin": "482913"})
	out := a.expect(200, "POST", "/v1/auth/login", "", map[string]string{"phone": phone, "pin": "482913"})
	return phone, out["token"].(string)
}

func TestEndToEnd(t *testing.T) {
	a, owner, _, appDB := setup(t)
	phoneA, tokA := a.signup("Bu Sari")
	_, tokB := a.signup("Pak Budi")

	// --- usaha + dompet otomatis ---
	biz := a.expect(201, "POST", "/v1/businesses", tokA, map[string]string{"name": "Warung Bu Sari", "kind": "warung_makan"})
	bid := biz["id"].(string)
	w := a.expect(200, "GET", "/v1/businesses/"+bid+"/wallets", tokA, nil)["wallets"].([]any)
	if len(w) != 2 {
		t.Fatalf("harus ada 2 dompet otomatis, dapat %d", len(w))
	}

	// --- isolasi: pengguna B tidak boleh menyentuh usaha A ---
	for _, p := range []string{"/wallets", "/categories", "/transactions", "/reports/monthly"} {
		a.expect(404, "GET", "/v1/businesses/"+bid+p, tokB, nil)
	}
	a.expect(404, "POST", "/v1/businesses/"+bid+"/transactions", tokB,
		map[string]any{"wallet": "business", "kind": "income", "amount": 1000})
	if list := a.expect(200, "GET", "/v1/businesses", tokB, nil)["businesses"].([]any); len(list) != 0 {
		t.Fatalf("B tidak boleh melihat usaha A: %v", list)
	}
	a.expect(401, "GET", "/v1/businesses", "", nil)
	a.expect(401, "GET", "/v1/businesses", strings.Repeat("a", 43), nil)

	// --- transaksi + idempotensi ---
	cats := a.expect(200, "GET", "/v1/businesses/"+bid+"/categories", tokA, nil)["categories"].([]any)
	var bahanBaku string
	for _, c := range cats {
		if m := c.(map[string]any); m["name"] == "Bahan baku" {
			bahanBaku = m["id"].(string)
		}
	}
	cid := "7f1c2d3e-0a1b-4c5d-8e9f-0123456789ab"
	inc := map[string]any{"wallet": "business", "kind": "income", "amount": 9_800_000, "client_id": cid, "note": "penjualan minggu 1"}
	first := a.expect(201, "POST", "/v1/businesses/"+bid+"/transactions", tokA, inc)
	replay := a.expect(200, "POST", "/v1/businesses/"+bid+"/transactions", tokA, inc)
	if first["id"] != replay["id"] || first["note"] != "penjualan minggu 1" {
		t.Fatalf("replay client_id harus mengembalikan transaksi yang sama: %v vs %v", first, replay)
	}
	a.expect(201, "POST", "/v1/businesses/"+bid+"/transactions", tokA,
		map[string]any{"wallet": "business", "kind": "expense", "amount": 7_800_000, "category_id": bahanBaku, "note": "beli beras"})
	// kategori expense tidak boleh dipakai untuk income
	a.expect(400, "POST", "/v1/businesses/"+bid+"/transactions", tokA,
		map[string]any{"wallet": "business", "kind": "income", "amount": 1000, "category_id": bahanBaku})

	// --- transfer (prive) + deteksi kebocoran ---
	tr := map[string]any{"direction": "to_personal", "amount": 2_450_000, "client_id": "11111111-2222-4333-8444-555555555555"}
	out := a.expect(201, "POST", "/v1/businesses/"+bid+"/transfers", tokA, tr)
	a.expect(200, "POST", "/v1/businesses/"+bid+"/transfers", tokA, tr) // replay tidak menggandakan
	group := out["transactions"].([]any)[0].(map[string]any)["id"].(string)

	bal := map[string]float64{}
	for _, x := range a.expect(200, "GET", "/v1/businesses/"+bid+"/wallets", tokA, nil)["wallets"].([]any) {
		m := x.(map[string]any)
		bal[m["kind"].(string)] = m["balance"].(float64)
	}
	if bal["business"] != -450_000 || bal["personal"] != 2_450_000 {
		t.Fatalf("saldo salah (replay mungkin menggandakan): %v", bal)
	}
	rep := a.expect(200, "GET", "/v1/businesses/"+bid+"/reports/monthly", tokA, nil)
	if rep["profit"] != 2_000_000.0 || rep["net_prive"] != 2_450_000.0 || rep["leak_detected"] != true || rep["leak_amount"] != 450_000.0 {
		t.Fatalf("laporan salah: %v", rep)
	}

	// --- paginasi keyset ---
	p1 := a.expect(200, "GET", "/v1/businesses/"+bid+"/transactions?limit=2", tokA, nil)
	if len(p1["transactions"].([]any)) != 2 || p1["next_cursor"] == "" {
		t.Fatalf("halaman 1 salah: %v", p1)
	}
	p2 := a.expect(200, "GET", "/v1/businesses/"+bid+"/transactions?limit=2&cursor="+p1["next_cursor"].(string), tokA, nil)
	if len(p2["transactions"].([]any)) != 2 || p2["next_cursor"] != "" {
		t.Fatalf("halaman 2 salah: %v", p2)
	}
	a.expect(400, "GET", "/v1/businesses/"+bid+"/transactions?cursor=ngawur", tokA, nil)

	// --- hapus lunak transfer memulihkan saldo di kedua dompet ---
	a.expect(204, "DELETE", "/v1/businesses/"+bid+"/transactions/"+group, tokA, nil)
	for _, x := range a.expect(200, "GET", "/v1/businesses/"+bid+"/wallets", tokA, nil)["wallets"].([]any) {
		m := x.(map[string]any)
		if (m["kind"] == "business" && m["balance"] != 2_000_000.0) || (m["kind"] == "personal" && m["balance"] != 0.0) {
			t.Fatalf("saldo setelah hapus transfer salah: %v", m)
		}
	}
	a.expect(404, "DELETE", "/v1/businesses/"+bid+"/transactions/"+group, tokB, nil)

	// --- logout mencabut sesi ---
	a.expect(204, "POST", "/v1/auth/logout", tokB, nil)
	a.expect(401, "GET", "/v1/me", tokB, nil)
	me := a.expect(200, "GET", "/v1/me", tokA, nil)
	if me["phone"] != phoneA || me["name"] != "Bu Sari" {
		t.Fatalf("/v1/me salah: %v", me)
	}

	// ====== pemeriksaan langsung di database ======
	t.Run("data sensitif tersimpan terenkripsi", func(t *testing.T) {
		rows, err := owner.Query("SELECT note FROM transactions WHERE business_id = $1 AND note IS NOT NULL", bid)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			var note string
			_ = rows.Scan(&note)
			n++
			if !strings.HasPrefix(note, "v1.") || strings.Contains(note, "beras") || strings.Contains(note, "penjualan") {
				t.Fatalf("catatan tidak terenkripsi: %q", note)
			}
		}
		if n != 2 {
			t.Fatalf("harus ada 2 catatan terenkripsi, dapat %d", n)
		}
		var phoneEnc, name string
		if err := owner.QueryRow("SELECT phone_enc, name FROM users WHERE phone_enc LIKE 'v1.%' ORDER BY created_at DESC LIMIT 1").Scan(&phoneEnc, &name); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(phoneEnc, "0812") || strings.Contains(name, "Budi") || strings.Contains(name, "Sari") {
			t.Fatal("PII tersimpan terbaca")
		}
		var sessionsWithRawToken int
		_ = owner.QueryRow("SELECT count(*) FROM sessions WHERE octet_length(token_hash) <> 32").Scan(&sessionsWithRawToken)
		if sessionsWithRawToken != 0 {
			t.Fatal("sesi harus menyimpan hash SHA-256, bukan token")
		}
	})

	t.Run("ledger immutable", func(t *testing.T) {
		_, err := owner.Exec("UPDATE transactions SET amount = 1 WHERE business_id = $1", bid)
		if err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("mengubah nominal harus ditolak trigger, dapat: %v", err)
		}
	})

	t.Run("role aplikasi dibatasi", func(t *testing.T) {
		if _, err := appDB.Exec("SELECT count(*) FROM sessions"); err == nil {
			t.Fatal("ledger_app tidak boleh membaca sessions")
		}
		if _, err := appDB.Exec("DELETE FROM transactions"); err == nil {
			t.Fatal("ledger_app tidak boleh DELETE transaksi")
		}
		if _, err := appDB.Exec("UPDATE users SET pin_hash = 'x'"); err == nil {
			t.Fatal("ledger_app tidak boleh mengubah users langsung")
		}
		var n int
		_ = appDB.QueryRow("SELECT count(*) FROM transactions").Scan(&n)
		if n != 0 {
			t.Fatalf("tanpa konteks pengguna, RLS harus menyembunyikan semua baris, terlihat %d", n)
		}
		_ = appDB.QueryRow("SELECT count(*) FROM users").Scan(&n)
		if n != 0 {
			t.Fatalf("tanpa konteks pengguna, users harus kosong, terlihat %d", n)
		}
		// SQL injection pada konteks pengguna tidak membuka data pengguna lain
		err := appDB.InTx(context.Background(), "00000000-0000-4000-8000-000000000000", func(tx *sql.Tx) error {
			return tx.QueryRow("SELECT count(*) FROM transactions").Scan(&n)
		})
		if err != nil || n != 0 {
			t.Fatalf("pengguna fiktif tidak boleh melihat transaksi: n=%d err=%v", n, err)
		}
	})
}

func TestAllTablesHaveRLS(t *testing.T) {
	_, owner, _, _ := setup(t)
	rows, err := owner.Query(`
		SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind = 'r' AND NOT c.relrowsecurity AND c.relname <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		t.Errorf("tabel %q belum mengaktifkan Row Level Security", name)
	}
}

func TestLoginLockoutAndRateLimit(t *testing.T) {
	a, _, _, _ := setup(t)
	phone, _ := a.signup("Pengguna Uji")
	for i := 0; i < 5; i++ {
		a.expect(401, "POST", "/v1/auth/login", "", map[string]string{"phone": phone, "pin": "999000"})
	}
	// PIN benar pun ditolak selama akun terkunci
	a.expect(429, "POST", "/v1/auth/login", "", map[string]string{"phone": phone, "pin": "482913"})
	// nomor yang tidak terdaftar mendapat jawaban yang sama dengan PIN salah (tidak membocorkan keberadaan akun)
	code, body := a.do("POST", "/v1/auth/login", "", map[string]string{"phone": randPhone(), "pin": "482913"})
	if code != 401 || body["error"].(map[string]any)["message"] != "nomor HP atau PIN salah" {
		t.Fatalf("respons nomor tak terdaftar harus identik dengan PIN salah: %d %v", code, body)
	}
	// field tak dikenal ditolak (mass assignment)
	a.expect(400, "POST", "/v1/auth/register", "", map[string]any{"name": "X Y", "phone": randPhone(), "pin": "482913", "is_admin": true})

	// batas default endpoint auth per IP: 10 burst
	var ready atomic.Bool
	ready.Store(true)
	appDB, _ := db.Open(context.Background(), os.Getenv("TEST_DATABASE_URL"), db.Options{MaxOpen: 3, MaxIdle: 1})
	defer appDB.Close()
	h, _ := New(Deps{Cfg: config.Config{DataKeys: map[string][]byte{"1": randKey()}, ActiveKeyID: "1", PhonePepper: randKey(),
		SessionTTL: time.Hour, MaxFailedLogins: 5, LockDuration: time.Minute, RequestTimeout: 5 * time.Second,
		HashConcurrency: 2, RateLimitRPS: 1000, RateLimitBurst: 1000,
		StorageBackend: "local", StorageLocalDir: t.TempDir(), OCRBackend: "mock"}, DB: appDB, Ready: &ready})
	srv := httptest.NewServer(h)
	defer srv.Close()
	limited := false
	for i := 0; i < 15 && !limited; i++ {
		code, _ := api{t: t, base: srv.URL}.do("POST", "/v1/auth/login", "", map[string]string{"phone": randPhone(), "pin": "482913"})
		limited = code == 429
	}
	if !limited {
		t.Fatal("brute force login harus kena rate limit per IP")
	}
}

func TestHealthEndpoints(t *testing.T) {
	a, _, _, _ := setup(t)
	for _, p := range []string{"/healthz", "/readyz"} {
		if code, _ := a.do("GET", p, "", nil); code != 200 {
			t.Errorf("%s: %d", p, code)
		}
	}
	req, _ := http.NewRequest("GET", a.base+"/v1/businesses", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for _, h := range []string{"X-Content-Type-Options", "Content-Security-Policy", "Strict-Transport-Security", "X-Request-Id"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("header keamanan %s hilang", h)
		}
	}
}

// ---------- alur nota ----------

func testJPEG(t *testing.T, marker string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()
	if marker == "" {
		return raw
	}
	payload := []byte(marker) // segmen komentar JPEG berisi data "rahasia" (stand-in metadata GPS)
	seg := []byte{0xFF, 0xFE, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	seg = append(seg, payload...)
	out := append([]byte{}, raw[:2]...)
	out = append(out, seg...)
	return append(out, raw[2:]...)
}

func (a api) upload(token, bid string, content []byte) (int, map[string]any) {
	a.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("image", "nota.jpg")
	_, _ = fw.Write(content)
	_ = mw.Close()
	req, _ := http.NewRequest("POST", a.base+"/v1/businesses/"+bid+"/receipts", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func (a api) getRaw(path, token string) (int, []byte) {
	a.t.Helper()
	req, _ := http.NewRequest("GET", a.base+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestReceiptFlow(t *testing.T) {
	a, owner, _, _ := setup(t)
	_, tokA := a.signup("Bu Sari")
	_, tokB := a.signup("Pak Budi")
	bid := a.expect(201, "POST", "/v1/businesses", tokA, map[string]string{"name": "Warung Bu Sari"})["id"].(string)

	// --- unggahan berbahaya ditolak ---
	if code, out := a.upload(tokA, bid, []byte("<html><script>alert(1)</script></html>"+strings.Repeat(" ", 600))); code != 400 {
		t.Fatalf("file HTML menyamar sebagai gambar harus 400, dapat %d %v", code, out)
	}
	if code, _ := a.upload(tokA, bid, bytes.Repeat([]byte{0}, 6<<20)); code != 413 {
		t.Fatalf("file > 5 MB harus 413, dapat %d", code)
	}
	if code, _ := a.upload(tokB, bid, testJPEG(t, "")); code != 404 {
		t.Fatalf("pengguna lain tidak boleh mengunggah ke usaha ini, dapat %d", code)
	}

	// --- unggah valid ---
	code, rec := a.upload(tokA, bid, testJPEG(t, "GPS-SECRET-LOCATION -6.2,106.8"))
	if code != 201 || rec["status"] != "needs_review" || rec["engine"] != "mock" || len(rec["items"].([]any)) == 0 {
		t.Fatalf("unggah valid salah: %d %v", code, rec)
	}
	rid := rec["id"].(string)

	// foto tersimpan sudah bersih dari metadata
	code, img := a.getRaw("/v1/businesses/"+bid+"/receipts/"+rid+"/image", tokA)
	if code != 200 || len(img) < 100 || img[0] != 0xFF || img[1] != 0xD8 || bytes.Contains(img, []byte("GPS-SECRET")) {
		t.Fatalf("foto harus JPEG bersih metadata: %d len=%d", code, len(img))
	}

	// --- isolasi antar pengguna ---
	for _, p := range []string{"", "/image"} {
		if code, _ := a.getRaw("/v1/businesses/"+bid+"/receipts/"+rid+p, tokB); code != 404 {
			t.Fatalf("pengguna B tidak boleh melihat nota A%s: %d", p, code)
		}
	}
	a.expect(404, "POST", "/v1/businesses/"+bid+"/receipts/"+rid+"/confirm", tokB,
		map[string]any{"items": []any{map[string]any{"name": "x", "qty": 1, "unit_price": 1000}}})

	// --- konfirmasi dengan koreksi pengguna ---
	var bahan, kemasan, penjualan string
	for _, c := range a.expect(200, "GET", "/v1/businesses/"+bid+"/categories", tokA, nil)["categories"].([]any) {
		m := c.(map[string]any)
		switch m["name"] {
		case "Bahan baku":
			bahan = m["id"].(string)
		case "Kemasan":
			kemasan = m["id"].(string)
		case "Penjualan":
			penjualan = m["id"].(string)
		}
	}
	items := []any{
		map[string]any{"name": "Beras 25 kg", "qty": 1, "unit_price": 310000, "category_id": bahan},
		map[string]any{"name": "Minyak goreng 5 L", "qty": 1, "unit_price": 95000, "category_id": bahan},
		map[string]any{"name": "Plastik kemasan", "qty": 2.5, "unit_price": 8000, "category_id": kemasan},
	}
	// kategori pemasukan tidak boleh dipakai untuk nota
	a.expect(400, "POST", "/v1/businesses/"+bid+"/receipts/"+rid+"/confirm", tokA, map[string]any{
		"items": []any{map[string]any{"name": "x", "qty": 1, "unit_price": 1000, "category_id": penjualan}}})
	// nominal tidak valid
	a.expect(400, "POST", "/v1/businesses/"+bid+"/receipts/"+rid+"/confirm", tokA, map[string]any{
		"items": []any{map[string]any{"name": "x", "qty": -1, "unit_price": 1000}}})

	res := a.expect(200, "POST", "/v1/businesses/"+bid+"/receipts/"+rid+"/confirm", tokA,
		map[string]any{"merchant": "Toko Sumber Rejeki", "date": "2026-10-05", "items": items})
	if res["total"] != 425000.0 || len(res["transactions"].([]any)) != 2 {
		t.Fatalf("konfirmasi harus menghasilkan total Rp425.000 dalam 2 transaksi (per kategori): %v", res)
	}
	// konfirmasi ganda ditolak: tidak boleh membuat transaksi dobel
	a.expect(409, "POST", "/v1/businesses/"+bid+"/receipts/"+rid+"/confirm", tokA,
		map[string]any{"items": items})

	for _, x := range a.expect(200, "GET", "/v1/businesses/"+bid+"/wallets", tokA, nil)["wallets"].([]any) {
		if m := x.(map[string]any); m["kind"] == "business" && m["balance"] != -425000.0 {
			t.Fatalf("saldo usaha harus -425000, dapat %v", m["balance"])
		}
	}
	got := a.expect(200, "GET", "/v1/businesses/"+bid+"/receipts/"+rid, tokA, nil)
	if got["status"] != "confirmed" || got["merchant"] != "Toko Sumber Rejeki" || got["total"] != 425000.0 {
		t.Fatalf("status nota setelah konfirmasi salah: %v", got)
	}

	// --- pemeriksaan langsung di database ---
	var merchant, itemName, raw string
	var viaOCR, withReceipt int
	if err := owner.QueryRow("SELECT merchant, ocr_raw::text FROM receipts WHERE id = $1", rid).Scan(&merchant, &raw); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow("SELECT name FROM receipt_items WHERE receipt_id = $1 ORDER BY position LIMIT 1", rid).Scan(&itemName); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(merchant, "Sumber") || strings.Contains(itemName, "Beras") || !strings.HasPrefix(raw, `"v1.`) {
		t.Fatalf("data nota harus terenkripsi di DB: merchant=%q item=%q raw=%.12q", merchant, itemName, raw)
	}
	_ = owner.QueryRow("SELECT count(*), count(receipt_id) FROM transactions WHERE business_id = $1 AND source = 'ocr'", bid).Scan(&viaOCR, &withReceipt)
	if viaOCR != 2 || withReceipt != 2 {
		t.Fatalf("transaksi hasil nota harus bersumber 'ocr' dan menaut ke nota: %d/%d", viaOCR, withReceipt)
	}
}
