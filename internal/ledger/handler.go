package ledger

import (
	"net/http"
	"strconv"

	"smartledger/internal/identity"
	"smartledger/internal/platform/httpx"
)

type Handler struct{ svc *Service }

func NewHandler(s *Service) *Handler { return &Handler{svc: s} }

func (h *Handler) Routes(mux *http.ServeMux, protect httpx.Middleware) {
	route := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, uid string) error) {
		mux.Handle(pattern, protect(httpx.Handle(func(w http.ResponseWriter, r *http.Request) error {
			uid, err := identity.Require(r.Context())
			if err != nil {
				return err
			}
			return fn(w, r, uid)
		})))
	}
	route("POST /v1/businesses", h.createBusiness)
	route("GET /v1/businesses", h.listBusinesses)
	route("GET /v1/businesses/{bid}/wallets", h.wallets)
	route("GET /v1/businesses/{bid}/categories", h.categories)
	route("POST /v1/businesses/{bid}/transactions", h.addTransaction)
	route("GET /v1/businesses/{bid}/transactions", h.listTransactions)
	route("DELETE /v1/businesses/{bid}/transactions/{id}", h.deleteTransaction)
	route("POST /v1/businesses/{bid}/transfers", h.transfer)
	route("GET /v1/businesses/{bid}/reports/monthly", h.monthly)
}

func (h *Handler) createBusiness(w http.ResponseWriter, r *http.Request, uid string) error {
	var in struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	b, err := h.svc.CreateBusiness(r.Context(), uid, in.Name, in.Kind)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusCreated, b)
}

func (h *Handler) listBusinesses(w http.ResponseWriter, r *http.Request, uid string) error {
	v, err := h.svc.ListBusinesses(r.Context(), uid)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, map[string]any{"businesses": v})
}

func (h *Handler) wallets(w http.ResponseWriter, r *http.Request, uid string) error {
	v, err := h.svc.Wallets(r.Context(), uid, r.PathValue("bid"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, map[string]any{"wallets": v})
}

func (h *Handler) categories(w http.ResponseWriter, r *http.Request, uid string) error {
	v, err := h.svc.Categories(r.Context(), uid, r.PathValue("bid"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, map[string]any{"categories": v})
}

func (h *Handler) addTransaction(w http.ResponseWriter, r *http.Request, uid string) error {
	var in NewTransaction
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	t, created, err := h.svc.AddTransaction(r.Context(), uid, r.PathValue("bid"), in)
	if err != nil {
		return err
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	return httpx.WriteJSON(w, status, t)
}

func (h *Handler) listTransactions(w http.ResponseWriter, r *http.Request, uid string) error {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	items, next, err := h.svc.List(r.Context(), uid, r.PathValue("bid"),
		ListParams{Wallet: q.Get("wallet"), Limit: limit, Cursor: q.Get("cursor")})
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, map[string]any{"transactions": items, "next_cursor": next})
}

func (h *Handler) deleteTransaction(w http.ResponseWriter, r *http.Request, uid string) error {
	if err := h.svc.Delete(r.Context(), uid, r.PathValue("bid"), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) transfer(w http.ResponseWriter, r *http.Request, uid string) error {
	var in NewTransfer
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	items, created, err := h.svc.Transfer(r.Context(), uid, r.PathValue("bid"), in)
	if err != nil {
		return err
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	return httpx.WriteJSON(w, status, map[string]any{"transactions": items})
}

func (h *Handler) monthly(w http.ResponseWriter, r *http.Request, uid string) error {
	m, err := h.svc.Monthly(r.Context(), uid, r.PathValue("bid"), r.URL.Query().Get("month"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, m)
}
