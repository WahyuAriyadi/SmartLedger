package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLimiterRefillsOverTime(t *testing.T) {
	now := time.Now()
	l := NewLimiter(1, 2)
	l.now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		if ok, _ := l.Allow("ip"); !ok {
			t.Fatalf("burst ke-%d harus lolos", i)
		}
	}
	ok, wait := l.Allow("ip")
	if ok || wait <= 0 {
		t.Fatal("setelah burst habis harus ditolak dengan waktu tunggu")
	}
	if ok, _ := l.Allow("ip-lain"); !ok {
		t.Fatal("kunci lain tidak boleh terpengaruh")
	}
	now = now.Add(1100 * time.Millisecond)
	if ok, _ := l.Allow("ip"); !ok {
		t.Fatal("token harus terisi ulang seiring waktu")
	}
}

func TestDecodeJSONStrict(t *testing.T) {
	type in struct {
		A int `json:"a"`
	}
	do := func(ct, body string) error {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		var v in
		return DecodeJSON(httptest.NewRecorder(), r, &v)
	}
	if err := do("application/json", `{"a":1}`); err != nil {
		t.Fatalf("valid harus lolos: %v", err)
	}
	for name, c := range map[string][2]string{
		"tanpa content-type": {"", `{"a":1}`},
		"field tak dikenal":  {"application/json", `{"a":1,"admin":true}`},
		"data tambahan":      {"application/json", `{"a":1}{"a":2}`},
		"bukan integer":      {"application/json", `{"a":1.5}`},
		"terlalu besar":      {"application/json", `{"a":1,"pad":"` + strings.Repeat("x", MaxBodyBytes) + `"}`},
	} {
		if do(c[0], c[1]) == nil {
			t.Errorf("%s harus ditolak", name)
		}
	}
}

func TestClientIPTrustsOnlyConfiguredHops(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if got := ClientIP(r, 0); got != "10.0.0.1" {
		t.Fatalf("hops=0 harus memakai RemoteAddr, dapat %s", got)
	}
	if got := ClientIP(r, 1); got != "203.0.113.9" {
		t.Fatalf("hops=1 harus entri terakhir (bukan yang dipalsukan klien), dapat %s", got)
	}
}

func TestRecoverHidesPanicDetails(t *testing.T) {
	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("rahasia-internal") }), RequestID, Recover)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 500 || strings.Contains(w.Body.String(), "rahasia-internal") {
		t.Fatalf("panic harus jadi 500 generik: %d %s", w.Code, w.Body.String())
	}
}
