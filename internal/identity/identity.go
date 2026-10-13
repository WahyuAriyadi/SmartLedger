// Package identity membawa ID pengguna yang sudah terautentikasi lewat context.
package identity

import (
	"context"

	"smartledger/internal/apperr"
)

type key struct{}

func With(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, key{}, userID)
}

// Require mengembalikan ID pengguna, atau error 401 bila tidak ada.
func Require(ctx context.Context) (string, error) {
	id, ok := ctx.Value(key{}).(string)
	if !ok || id == "" {
		return "", apperr.Unauthorized("silakan login terlebih dahulu")
	}
	return id, nil
}
