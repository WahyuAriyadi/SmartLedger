-- 003: pipeline nota. Item nota hasil OCR boleh dikoreksi pengguna saat konfirmasi,
-- jadi receipt_items perlu DELETE (bukan baris ledger, aman). Kolom engine mencatat
-- mesin OCR yang dipakai (mock lokal / vertex di cloud) untuk evaluasi akurasi.
ALTER TABLE receipts ADD COLUMN engine TEXT NOT NULL DEFAULT 'unknown';
GRANT DELETE ON receipt_items TO ledger_app;
