// Package apperr mendefinisikan error domain yang tidak bergantung pada HTTP.
// Lapisan HTTP (httpx) yang memetakan Kind ke status code.
package apperr

import "errors"

type Kind int

const (
	KindInternal Kind = iota
	KindValidation
	KindNotFound
	KindConflict
	KindUnauthorized
	KindTooMany
	KindTooLarge
	KindUnsupportedMedia
)

type Error struct {
	Kind    Kind
	Code    string // stabil, untuk klien/mesin
	Message string // aman ditampilkan ke pengguna
	Err     error  // penyebab internal, TIDAK pernah dikirim ke klien
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
func (e *Error) Unwrap() error { return e.Err }

func Validation(code, msg string) *Error {
	return &Error{Kind: KindValidation, Code: code, Message: msg}
}
func NotFound(msg string) *Error       { return &Error{Kind: KindNotFound, Code: "not_found", Message: msg} }
func Conflict(code, msg string) *Error { return &Error{Kind: KindConflict, Code: code, Message: msg} }
func Unauthorized(msg string) *Error {
	return &Error{Kind: KindUnauthorized, Code: "unauthorized", Message: msg}
}
func TooMany(msg string) *Error {
	return &Error{Kind: KindTooMany, Code: "too_many_requests", Message: msg}
}

func Internal(err error) *Error {
	return &Error{Kind: KindInternal, Code: "internal", Message: "terjadi kesalahan pada server", Err: err}
}

// As mengembalikan *Error di dalam rantai err, jika ada.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
