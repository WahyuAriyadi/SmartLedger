package receipt

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	"smartledger/internal/apperr"
	"smartledger/internal/identity"
	"smartledger/internal/platform/httpx"
)

const (
	uploadReadWindow  = 40 * time.Second // koneksi seluler lambat: 5 MB bisa butuh puluhan detik
	uploadWriteWindow = 60 * time.Second
	ocrTimeout        = 25 * time.Second
)

type Handler struct{ svc *Service }

func NewHandler(s *Service) *Handler { return &Handler{svc: s} }

// Routes: `upload` membatasi laju unggahan per pengguna (OCR cloud berbiaya per panggilan).
func (h *Handler) Routes(mux *http.ServeMux, protect, upload httpx.Middleware) {
	route := func(pattern string, extra httpx.Middleware, fn func(w http.ResponseWriter, r *http.Request, uid string) error) {
		var hd http.Handler = httpx.Handle(func(w http.ResponseWriter, r *http.Request) error {
			uid, err := identity.Require(r.Context())
			if err != nil {
				return err
			}
			return fn(w, r, uid)
		})
		if extra != nil {
			hd = extra(hd)
		}
		mux.Handle(pattern, protect(hd))
	}
	route("POST /v1/businesses/{bid}/receipts", upload, h.upload)
	route("GET /v1/businesses/{bid}/receipts/{rid}", nil, h.get)
	route("GET /v1/businesses/{bid}/receipts/{rid}/image", nil, h.image)
	route("POST /v1/businesses/{bid}/receipts/{rid}/confirm", nil, h.confirm)
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request, uid string) error {
	// Perpanjang batas waktu khusus endpoint ini (server-wide hanya 10-15 detik).
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(uploadReadWindow))
	_ = rc.SetWriteDeadline(time.Now().Add(uploadWriteWindow))

	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadBytes+(64<<10))
	mr, err := r.MultipartReader()
	if err != nil {
		return apperr.Validation("invalid_upload", "kirim sebagai multipart/form-data dengan field 'image'")
	}
	var raw []byte
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return uploadErr(err)
		}
		if part.FormName() != "image" {
			part.Close()
			continue
		}
		raw, err = readPart(part)
		if err != nil {
			return uploadErr(err)
		}
		break
	}
	if raw == nil {
		return apperr.Validation("invalid_upload", "field 'image' wajib ada")
	}

	// Lepas dari batas 10 detik milik request, tapi tetap dibatasi OCR timeout sendiri.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), ocrTimeout)
	defer cancel()
	rec, err := h.svc.Upload(ctx, uid, r.PathValue("bid"), raw)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusCreated, rec)
}

func readPart(p *multipart.Part) ([]byte, error) {
	defer p.Close()
	b, err := io.ReadAll(io.LimitReader(p, MaxUploadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxUploadBytes {
		return nil, errTooLarge
	}
	return b, nil
}

var errTooLarge = errors.New("upload terlalu besar")

func uploadErr(err error) error {
	var mbe *http.MaxBytesError
	if errors.Is(err, errTooLarge) || errors.As(err, &mbe) {
		return &apperr.Error{Kind: apperr.KindTooLarge, Code: "image_too_large", Message: "ukuran foto maksimal 5 MB"}
	}
	return apperr.Validation("invalid_upload", "unggahan tidak valid")
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, uid string) error {
	rec, err := h.svc.Get(r.Context(), uid, r.PathValue("bid"), r.PathValue("rid"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, rec)
}

func (h *Handler) image(w http.ResponseWriter, r *http.Request, uid string) error {
	b, err := h.svc.Image(r.Context(), uid, r.PathValue("bid"), r.PathValue("rid"))
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "image/jpeg") // selalu JPEG: sudah di-encode ulang saat unggah
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "private, no-store")
	_, err = w.Write(b)
	return err
}

func (h *Handler) confirm(w http.ResponseWriter, r *http.Request, uid string) error {
	var in ConfirmInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	res, err := h.svc.Confirm(r.Context(), uid, r.PathValue("bid"), r.PathValue("rid"), in)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, res)
}
