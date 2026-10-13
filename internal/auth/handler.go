package auth

import (
	"net/http"
	"strings"

	"smartledger/internal/apperr"
	"smartledger/internal/identity"
	"smartledger/internal/platform/httpx"
)

type Handler struct{ svc *Service }

func NewHandler(s *Service) *Handler { return &Handler{svc: s} }

// Routes mendaftarkan endpoint auth. `limit` adalah rate limiter ketat untuk
// endpoint yang bisa dipakai menebak PIN; `protect` mewajibkan sesi valid.
func (h *Handler) Routes(mux *http.ServeMux, limit, protect httpx.Middleware) {
	mux.Handle("POST /v1/auth/register", limit(httpx.Handle(h.register)))
	mux.Handle("POST /v1/auth/login", limit(httpx.Handle(h.login)))
	mux.Handle("POST /v1/auth/logout", protect(httpx.Handle(h.logout)))
	mux.Handle("GET /v1/me", protect(httpx.Handle(h.me)))
}

// Require memeriksa token Bearer dan menaruh user ID di context.
func (h *Handler) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(r)
		if !ok {
			httpx.WriteError(w, r, apperr.Unauthorized("silakan login terlebih dahulu"))
			return
		}
		uid, err := h.svc.Resolve(r.Context(), token)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(identity.With(r.Context(), uid)))
	})
}

func bearer(r *http.Request) (string, bool) {
	v := r.Header.Get("Authorization")
	if len(v) > 7 && strings.EqualFold(v[:7], "Bearer ") {
		return v[7:], true
	}
	return "", false
}

type credentials struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
	PIN   string `json:"pin"`
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) error {
	var in credentials
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if _, err := h.svc.Register(r.Context(), in.Name, in.Phone, in.PIN); err != nil {
		return err
	}
	// Tidak mengembalikan ID/data apa pun; pengguna login setelah daftar.
	return httpx.WriteJSON(w, http.StatusCreated, map[string]string{"status": "registered"})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) error {
	var in credentials
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	sess, err := h.svc.Login(r.Context(), in.Phone, in.PIN)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, sess)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) error {
	token, _ := bearer(r)
	if err := h.svc.Logout(r.Context(), token); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) error {
	uid, err := identity.Require(r.Context())
	if err != nil {
		return err
	}
	p, err := h.svc.Me(r.Context(), uid)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, p)
}
