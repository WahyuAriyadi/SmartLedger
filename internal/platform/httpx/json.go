// Package httpx berisi infrastruktur HTTP bersama: JSON ketat, penanganan error
// terpusat, middleware keamanan, dan rate limiter.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"smartledger/internal/apperr"
)

const MaxBodyBytes = 64 << 10 // 64 KiB; upload nota nanti memakai endpoint terpisah

// DecodeJSON membaca body JSON secara ketat: wajib application/json, ukuran dibatasi,
// field tak dikenal ditolak, dan tidak boleh ada data setelah objek pertama.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return &apperr.Error{Kind: apperr.KindUnsupportedMedia, Code: "unsupported_media_type", Message: "gunakan Content-Type: application/json"}
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return &apperr.Error{Kind: apperr.KindTooLarge, Code: "body_too_large", Message: "permintaan terlalu besar"}
		}
		return apperr.Validation("invalid_json", "format permintaan tidak valid")
	}
	if _, err := dec.Token(); err != io.EOF {
		return apperr.Validation("invalid_json", "format permintaan tidak valid")
	}
	return nil
}

func WriteJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

type errorBody struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id,omitempty"`
	} `json:"error"`
}

func statusOf(k apperr.Kind) int {
	switch k {
	case apperr.KindValidation:
		return http.StatusBadRequest
	case apperr.KindNotFound:
		return http.StatusNotFound
	case apperr.KindConflict:
		return http.StatusConflict
	case apperr.KindUnauthorized:
		return http.StatusUnauthorized
	case apperr.KindTooMany:
		return http.StatusTooManyRequests
	case apperr.KindTooLarge:
		return http.StatusRequestEntityTooLarge
	case apperr.KindUnsupportedMedia:
		return http.StatusUnsupportedMediaType
	default:
		return http.StatusInternalServerError
	}
}

// WriteError mengirim error dalam format seragam. Detail internal hanya masuk log
// (bersama request_id), tidak pernah ke klien.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	ae, ok := apperr.As(err)
	if !ok {
		ae = apperr.Internal(err)
	}
	rid := RequestIDFrom(r.Context())
	if ae.Kind == apperr.KindInternal {
		cause := ae.Err
		if cause == nil {
			cause = err
		}
		slog.ErrorContext(r.Context(), "kesalahan internal", "request_id", rid, "err", cause)
	}
	var body errorBody
	body.Error.Code, body.Error.Message, body.Error.RequestID = ae.Code, ae.Message, rid
	_ = WriteJSON(w, statusOf(ae.Kind), body)
}

// HandlerFunc adalah handler yang mengembalikan error; Handle mengubahnya jadi
// http.Handler dengan penanganan error terpusat.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

func Handle(h HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			WriteError(w, r, err)
		}
	})
}
