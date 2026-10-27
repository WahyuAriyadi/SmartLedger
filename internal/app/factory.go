package app

import (
	"fmt"
	"log/slog"

	"smartledger/internal/config"
	"smartledger/internal/receipt"
)

// buildStorage selects receipt photo storage (server mode only). Device mode persists nothing.
func buildStorage(cfg config.Config) (receipt.Storage, error) {
	switch cfg.StorageBackend {
	case "local":
		return receipt.NewLocalDisk(cfg.StorageLocalDir)
	default:
		return nil, fmt.Errorf("STORAGE_BACKEND %q tidak dikenal", cfg.StorageBackend)
	}
}

// buildExtractor selects the OCR engine: mock (free), gemini (Gemini API, free tier available) or
// vertex (Vertex AI, paid). It is chosen via OCR_BACKEND, so the same code runs locally and on Cloud Run.
func buildExtractor(cfg config.Config) (receipt.Extractor, error) {
	switch cfg.OCRBackend {
	case "mock":
		return receipt.MockExtractor{}, nil
	case "gemini":
		slog.Warn("OCR_BACKEND=gemini: bila API key belum berbilling (free tier), Google boleh memakai foto nota untuk memperbaiki produknya. Cocok untuk demo, bukan untuk data pengguna sungguhan")
		return receipt.NewGeminiExtractor(cfg.GeminiAPIKey, cfg.GeminiModel), nil
	case "vertex":
		if cfg.VertexAccessToken != "" {
			slog.Warn("VERTEX_ACCESS_TOKEN terisi: token statis hanya untuk uji lokal, jangan dipakai di produksi")
			return receipt.NewVertexExtractorWithToken(cfg.VertexProject, cfg.VertexLocation, cfg.VertexModel, cfg.VertexAccessToken), nil
		}
		return receipt.NewVertexExtractor(cfg.VertexProject, cfg.VertexLocation, cfg.VertexModel), nil
	default:
		return nil, fmt.Errorf("OCR_BACKEND %q tidak dikenal", cfg.OCRBackend)
	}
}
