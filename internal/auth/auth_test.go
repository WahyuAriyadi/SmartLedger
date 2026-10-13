package auth

import "testing"

func TestNormalizePhone(t *testing.T) {
	ok := map[string]string{
		"0812-3456-7890": "081234567890", "+62 812 3456 7890": "081234567890",
		"6281234567890": "081234567890", "(0812) 3456789": "08123456789",
	}
	for in, want := range ok {
		if got, err := NormalizePhone(in); err != nil || got != want {
			t.Errorf("%q -> %q (%v), want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "12345", "0812abc3456", "0812345; DROP", "+1 555 123 4567", "08123"} {
		if _, err := NormalizePhone(in); err == nil {
			t.Errorf("%q harus ditolak", in)
		}
	}
}

func TestValidatePIN(t *testing.T) {
	for _, bad := range []string{"12345", "123456", "111111", "000000", "aaaaaa"} {
		if validatePIN(bad) == nil {
			t.Errorf("%q harus ditolak", bad)
		}
	}
	if err := validatePIN("482913"); err != nil {
		t.Errorf("PIN wajar harus lolos: %v", err)
	}
}
