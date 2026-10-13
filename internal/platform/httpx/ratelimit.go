package httpx

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"smartledger/internal/apperr"
)

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter adalah token bucket per kunci (mis. IP). State ada di memori per instance;
// untuk batas global lintas instance gunakan Cloud Armor di depan. Tidak ada goroutine
// latar: pembersihan dilakukan oportunistik agar tidak ada kebocoran.
type Limiter struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	rate      float64 // token per detik
	burst     float64
	maxKeys   int
	lastClean time.Time
	now       func() time.Time
}

func NewLimiter(ratePerSec float64, burst int) *Limiter {
	return &Limiter{buckets: map[string]*bucket{}, rate: ratePerSec, burst: float64(burst), maxKeys: 100_000, now: time.Now}
}

// Allow mengembalikan (true, 0) bila diizinkan, atau (false, tunggu) bila ditolak.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastClean) > time.Minute || len(l.buckets) > l.maxKeys {
		l.clean(now)
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

// clean membuang bucket yang sudah penuh kembali (tidak ada informasi yang hilang).
func (l *Limiter) clean(now time.Time) {
	l.lastClean = now
	for k, b := range l.buckets {
		if math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate) >= l.burst {
			delete(l.buckets, k)
		}
	}
}

func (l *Limiter) Middleware(key func(*http.Request) string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ok, wait := l.Allow(key(r)); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
				WriteError(w, r, apperr.TooMany("terlalu banyak permintaan, coba lagi sebentar"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
