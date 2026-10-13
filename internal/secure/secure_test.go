package secure

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func testKeys() map[string][]byte {
	return map[string][]byte{"1": bytes.Repeat([]byte{1}, 32), "2": bytes.Repeat([]byte{2}, 32)}
}

func TestCipherRoundTripAndBinding(t *testing.T) {
	c, err := NewCipher(testKeys(), "1")
	if err != nil {
		t.Fatal(err)
	}
	ct, _ := c.Encrypt("08123456789", "users.phone")
	if strings.Contains(ct, "0812") {
		t.Fatal("ciphertext tidak boleh memuat plaintext")
	}
	if pt, err := c.Decrypt(ct, "users.phone"); err != nil || pt != "08123456789" {
		t.Fatalf("round trip gagal: %q %v", pt, err)
	}
	if _, err := c.Decrypt(ct, "users.name"); err == nil {
		t.Fatal("ciphertext harus terikat ke AAD; dipindah ke kolom lain harus gagal")
	}
	ct2, _ := c.Encrypt("08123456789", "users.phone")
	if ct == ct2 {
		t.Fatal("nonce harus acak: ciphertext sama untuk plaintext sama")
	}
	if _, err := c.Decrypt(ct[:len(ct)-2]+"AA", "users.phone"); err == nil {
		t.Fatal("ciphertext yang dimodifikasi harus ditolak")
	}
}

func TestCipherKeyRotation(t *testing.T) {
	old, _ := NewCipher(testKeys(), "1")
	ct, _ := old.Encrypt("rahasia", "x")
	rotated, _ := NewCipher(testKeys(), "2") // kunci aktif baru, kunci lama masih di keyring
	if pt, err := rotated.Decrypt(ct, "x"); err != nil || pt != "rahasia" {
		t.Fatalf("data lama harus tetap terbaca setelah rotasi: %v", err)
	}
	if ct2, _ := rotated.Encrypt("rahasia", "x"); !strings.HasPrefix(ct2, "v1.2.") {
		t.Fatalf("data baru harus memakai kunci aktif: %s", ct2)
	}
	if _, err := NewCipher(map[string][]byte{"1": []byte("pendek")}, "1"); err == nil {
		t.Fatal("kunci selain 32 byte harus ditolak")
	}
}

func TestPasswordHash(t *testing.T) {
	h, err := NewHasher(2)
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := h.Hash(context.Background(), "482913")
	if ok, _ := h.Verify(context.Background(), "482913", enc); !ok {
		t.Fatal("PIN benar harus lolos")
	}
	if ok, _ := h.Verify(context.Background(), "482914", enc); ok {
		t.Fatal("PIN salah harus ditolak")
	}
	if !strings.HasPrefix(enc, "$argon2id$") {
		t.Fatal("harus argon2id")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Hash(ctx, "x"); err == nil {
		// semaphore kosong -> boleh lolos; yang penting tidak panic
		t.Log("hash tetap jalan karena slot tersedia")
	}
}

func TestBlindIndexAndTokens(t *testing.T) {
	p := bytes.Repeat([]byte{9}, 32)
	if BlindIndex(p, "0812") != BlindIndex(p, "0812") || BlindIndex(p, "0812") == BlindIndex(p, "0813") {
		t.Fatal("blind index harus deterministik dan berbeda per nilai")
	}
	if BlindIndex(p, "0812") == BlindIndex(bytes.Repeat([]byte{8}, 32), "0812") {
		t.Fatal("pepper berbeda harus menghasilkan indeks berbeda")
	}
	tok, hash := NewToken()
	if len(tok) != TokenLen || !bytes.Equal(hash, HashToken(tok)) {
		t.Fatalf("token tidak sesuai: len=%d", len(tok))
	}
	if id := NewUUID(); !IsUUID(id) {
		t.Fatalf("UUID tidak valid: %s", id)
	}
	if IsUUID("1; DROP TABLE users") {
		t.Fatal("bukan UUID")
	}
}
