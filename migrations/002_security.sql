-- 002: pengamanan tingkat database.
--
-- Prinsip: kalau ada bug di kode aplikasi (atau SQL injection), database
-- tetap menolak membaca data milik pengguna lain.
--   1. Row Level Security (RLS) pada semua tabel, berdasarkan app.user_id.
--   2. Role aplikasi `ledger_app` bukan pemilik tabel, tanpa hak DELETE.
--   3. Pra-autentikasi (register/login/sesi) hanya lewat fungsi SECURITY DEFINER.
--   4. PII (nomor HP, nama, catatan) dienkripsi di aplikasi; DB hanya melihat ciphertext.
--   5. Transaksi immutable: nominal/dompet/jenis tidak bisa diubah setelah dibuat.
--
-- ATURAN MAINTENANCE: setiap tabel baru WAJIB ENABLE ROW LEVEL SECURITY, punya
-- policy, dan GRANT eksplisit. Tes integrasi TestAllTablesHaveRLS akan gagal kalau lupa.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ledger_app') THEN
        RAISE EXCEPTION 'role ledger_app belum dibuat (lihat deploy/initdb/01-roles.sql)';
    END IF;
END $$;

-- ---------- 1. Kolom PII terenkripsi + proteksi brute force ----------
-- Catatan: diasumsikan tabel users masih kosong (belum ada deployment).
ALTER TABLE users RENAME COLUMN phone TO phone_enc;
ALTER TABLE users DROP CONSTRAINT users_phone_key;
ALTER TABLE users ADD COLUMN phone_idx       TEXT NOT NULL;      -- HMAC(pepper, nomor HP), untuk pencarian
ALTER TABLE users ADD COLUMN failed_attempts INT  NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN locked_until    TIMESTAMPTZ;
CREATE UNIQUE INDEX users_phone_idx_uq ON users (phone_idx);

CREATE TABLE sessions (
    token_hash    BYTEA PRIMARY KEY,                              -- SHA-256 dari token; token asli tidak disimpan
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL,
    revoked_at    TIMESTAMPTZ
);
CREATE INDEX sessions_user_idx   ON sessions (user_id);
CREATE INDEX sessions_expiry_idx ON sessions (expires_at);

CREATE TABLE audit_log (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_id      UUID,
    business_id  UUID,
    action       TEXT NOT NULL,
    entity       TEXT,
    entity_id    UUID,
    meta         JSONB NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_business_idx ON audit_log (business_id, at DESC);

-- ---------- 2. Immutability transaksi ----------
CREATE FUNCTION transactions_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.business_id <> OLD.business_id OR NEW.wallet_id <> OLD.wallet_id
       OR NEW.kind <> OLD.kind OR NEW.direction <> OLD.direction OR NEW.amount <> OLD.amount
       OR NEW.transfer_group_id IS DISTINCT FROM OLD.transfer_group_id
       OR NEW.client_id IS DISTINCT FROM OLD.client_id
       OR NEW.receipt_id IS DISTINCT FROM OLD.receipt_id
       OR NEW.created_at <> OLD.created_at
       OR NEW.created_by IS DISTINCT FROM OLD.created_by THEN
        RAISE EXCEPTION 'transaksi immutable: hanya catatan, kategori, tanggal, dan penghapusan lunak yang boleh diubah'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS DISTINCT FROM OLD.deleted_at THEN
        RAISE EXCEPTION 'transaksi yang sudah dihapus tidak dapat dipulihkan'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER transactions_guard BEFORE UPDATE ON transactions
    FOR EACH ROW EXECUTE FUNCTION transactions_guard();

-- ---------- 3. View harus mengikuti RLS milik pemanggil ----------
DROP VIEW monthly_summary;
DROP VIEW wallet_balances;

CREATE VIEW wallet_balances WITH (security_invoker = true) AS
SELECT w.id AS wallet_id, w.business_id, w.kind,
       w.opening_balance
         + COALESCE(SUM(CASE t.direction WHEN 'in' THEN t.amount ELSE -t.amount END), 0) AS balance
FROM wallets w
LEFT JOIN transactions t ON t.wallet_id = w.id AND t.deleted_at IS NULL
GROUP BY w.id;

CREATE VIEW monthly_summary WITH (security_invoker = true) AS
SELECT s.*, s.revenue - s.expenses AS profit, s.prive - s.capital_in AS net_prive
FROM (
    SELECT t.business_id,
           date_trunc('month', t.occurred_at AT TIME ZONE 'Asia/Jakarta')::date AS month,
           COALESCE(SUM(t.amount) FILTER (WHERE t.kind = 'income'  AND w.kind = 'business'), 0) AS revenue,
           COALESCE(SUM(t.amount) FILTER (WHERE t.kind = 'expense' AND w.kind = 'business'), 0) AS expenses,
           COALESCE(SUM(t.amount) FILTER (WHERE t.kind = 'transfer' AND t.direction = 'out' AND w.kind = 'business'), 0) AS prive,
           COALESCE(SUM(t.amount) FILTER (WHERE t.kind = 'transfer' AND t.direction = 'in'  AND w.kind = 'business'), 0) AS capital_in
    FROM transactions t
    JOIN wallets w ON w.id = t.wallet_id
    WHERE t.deleted_at IS NULL
    GROUP BY 1, 2
) s;

-- ---------- 4. Row Level Security ----------
CREATE FUNCTION app_user_id() RETURNS uuid LANGUAGE sql STABLE AS
$$ SELECT NULLIF(current_setting('app.user_id', true), '')::uuid $$;

CREATE FUNCTION owns_business(b uuid) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS
$$ SELECT EXISTS (SELECT 1 FROM businesses WHERE id = b AND owner_id = app_user_id()) $$;

ALTER TABLE users          ENABLE ROW LEVEL SECURITY;
ALTER TABLE businesses     ENABLE ROW LEVEL SECURITY;
ALTER TABLE wallets        ENABLE ROW LEVEL SECURITY;
ALTER TABLE categories     ENABLE ROW LEVEL SECURITY;
ALTER TABLE receipts       ENABLE ROW LEVEL SECURITY;
ALTER TABLE receipt_items  ENABLE ROW LEVEL SECURITY;
ALTER TABLE transactions   ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_rules    ENABLE ROW LEVEL SECURITY;
ALTER TABLE notifications  ENABLE ROW LEVEL SECURITY;
ALTER TABLE sessions       ENABLE ROW LEVEL SECURITY;   -- tanpa policy = tertutup untuk ledger_app
ALTER TABLE audit_log      ENABLE ROW LEVEL SECURITY;

CREATE POLICY users_self      ON users      FOR SELECT USING (id = app_user_id());
CREATE POLICY businesses_own  ON businesses FOR ALL USING (owner_id = app_user_id()) WITH CHECK (owner_id = app_user_id());
CREATE POLICY wallets_own     ON wallets    FOR ALL USING (owns_business(business_id)) WITH CHECK (owns_business(business_id));
CREATE POLICY categories_read ON categories FOR SELECT USING (business_id IS NULL OR owns_business(business_id));
CREATE POLICY categories_write ON categories FOR ALL
    USING (business_id IS NOT NULL AND owns_business(business_id))
    WITH CHECK (business_id IS NOT NULL AND owns_business(business_id));
CREATE POLICY receipts_own    ON receipts   FOR ALL USING (owns_business(business_id)) WITH CHECK (owns_business(business_id));
CREATE POLICY receipt_items_own ON receipt_items FOR ALL
    USING (EXISTS (SELECT 1 FROM receipts r WHERE r.id = receipt_id AND owns_business(r.business_id)))
    WITH CHECK (EXISTS (SELECT 1 FROM receipts r WHERE r.id = receipt_id AND owns_business(r.business_id)));
CREATE POLICY transactions_own ON transactions FOR ALL USING (owns_business(business_id)) WITH CHECK (owns_business(business_id));
CREATE POLICY alert_rules_own ON alert_rules FOR ALL USING (owns_business(business_id)) WITH CHECK (owns_business(business_id));
CREATE POLICY notifications_own ON notifications FOR ALL USING (owns_business(business_id)) WITH CHECK (owns_business(business_id));
CREATE POLICY audit_insert    ON audit_log  FOR INSERT WITH CHECK (user_id IS NULL OR user_id = app_user_id());

-- ---------- 5. Fungsi pra-autentikasi (satu-satunya jalan ke users/sessions) ----------
CREATE FUNCTION auth_register(p_phone_enc TEXT, p_phone_idx TEXT, p_name TEXT, p_pin_hash TEXT)
RETURNS UUID LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS $$
    INSERT INTO users (phone_enc, phone_idx, name, pin_hash)
    VALUES (p_phone_enc, p_phone_idx, p_name, p_pin_hash) RETURNING id;
$$;

CREATE FUNCTION auth_get_credentials(p_phone_idx TEXT)
RETURNS TABLE (id UUID, pin_hash TEXT, failed_attempts INT, locked_until TIMESTAMPTZ)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
    SELECT u.id, u.pin_hash, u.failed_attempts, u.locked_until FROM users u WHERE u.phone_idx = p_phone_idx;
$$;

CREATE FUNCTION auth_record_failure(p_user UUID, p_max INT, p_lock_seconds INT)
RETURNS TIMESTAMPTZ LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE v_until TIMESTAMPTZ;
BEGIN
    UPDATE users
       SET failed_attempts = failed_attempts + 1,
           locked_until = CASE WHEN failed_attempts + 1 >= p_max
                               THEN now() + make_interval(secs => p_lock_seconds)
                               ELSE locked_until END
     WHERE id = p_user
    RETURNING locked_until INTO v_until;
    INSERT INTO audit_log (user_id, action) VALUES (p_user, 'login_failed');
    RETURN v_until;
END $$;

CREATE FUNCTION auth_record_success(p_user UUID)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
BEGIN
    UPDATE users SET failed_attempts = 0, locked_until = NULL WHERE id = p_user;
    INSERT INTO audit_log (user_id, action) VALUES (p_user, 'login_ok');
END $$;

CREATE FUNCTION auth_create_session(p_user UUID, p_hash BYTEA, p_expires TIMESTAMPTZ)
RETURNS void LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS $$
    INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (p_hash, p_user, p_expires);
$$;

CREATE FUNCTION auth_resolve_session(p_hash BYTEA)
RETURNS UUID LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
DECLARE v_user UUID;
BEGIN
    SELECT user_id INTO v_user FROM sessions
     WHERE token_hash = p_hash AND revoked_at IS NULL AND expires_at > now();
    IF v_user IS NOT NULL THEN
        UPDATE sessions SET last_used_at = now()
         WHERE token_hash = p_hash AND last_used_at < now() - interval '1 minute';
    END IF;
    RETURN v_user;
END $$;

CREATE FUNCTION auth_revoke_session(p_hash BYTEA)
RETURNS void LANGUAGE sql SECURITY DEFINER SET search_path = public, pg_temp AS $$
    UPDATE sessions SET revoked_at = now() WHERE token_hash = p_hash AND revoked_at IS NULL;
$$;

-- ---------- 6. Hak akses minimum untuk ledger_app ----------
REVOKE ALL ON ALL TABLES    IN SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO ledger_app;

GRANT SELECT, INSERT, UPDATE ON businesses, wallets, categories, receipts, receipt_items,
                                transactions, alert_rules, notifications TO ledger_app;   -- tanpa DELETE
GRANT SELECT ON users TO ledger_app;
GRANT INSERT ON audit_log TO ledger_app;
GRANT SELECT ON wallet_balances, monthly_summary TO ledger_app;

GRANT EXECUTE ON FUNCTION app_user_id(), owns_business(uuid),
    auth_register(text, text, text, text), auth_get_credentials(text),
    auth_record_failure(uuid, int, int), auth_record_success(uuid),
    auth_create_session(uuid, bytea, timestamptz), auth_resolve_session(bytea),
    auth_revoke_session(bytea) TO ledger_app;
