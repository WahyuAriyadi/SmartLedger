package app

import (
	"fmt"

	"smartledger/internal/config"
	"smartledger/internal/receipt"
)

// buildStorage memilih penyimpanan foto nota. Lokal = disk; cloud = Cloud Storage.
func buildStorage(cfg config.Config) (receipt.Storage, error) {
	switch cfg.StorageBackend {
	case "local":
		return receipt.NewLocalDisk(cfg.StorageLocalDir)
	// CLOUD: case "gcs":
	// CLOUD: 	return receipt.NewGCSStorage(cfg.GCSBucket), nil
	default:
		return nil, fmt.Errorf("STORAGE_BACKEND %q tidak dikenal atau belum diaktifkan di kode", cfg.StorageBackend)
	}
}

// buildExtractor memilih mesin OCR. Lokal = tiruan (tanpa biaya); cloud = Gemini di Vertex AI.
func buildExtractor(cfg config.Config) (receipt.Extractor, error) {
	switch cfg.OCRBackend {
	case "mock":
		return receipt.MockExtractor{}, nil
	// CLOUD: case "vertex":
	// CLOUD: 	return receipt.NewVertexExtractor(cfg.VertexProject, cfg.VertexLocation, cfg.VertexModel), nil
	default:
		return nil, fmt.Errorf("OCR_BACKEND %q tidak dikenal atau belum diaktifkan di kode", cfg.OCRBackend)
	}
}
