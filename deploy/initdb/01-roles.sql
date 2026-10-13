-- HANYA UNTUK DEV LOKAL (docker compose). Di produksi role dibuat oleh Terraform
-- dan password-nya diambil dari Secret Manager, bukan ditulis di repo.
CREATE ROLE ledger_app LOGIN PASSWORD 'ledger_app_dev' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
GRANT CONNECT ON DATABASE ledger TO ledger_app;
