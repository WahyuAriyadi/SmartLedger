-- Smart Micro-Ledger (Buku Kas Pintar) - PostgreSQL schema v0.1
-- Aturan emas: uang disimpan sebagai BIGINT dalam rupiah (tanpa desimal),
-- saldo TIDAK disimpan, selalu dihitung dari transaksi (lihat view wallet_balances).


-- ============================================================
-- 1. Akun dan usaha
-- ============================================================
CREATE TABLE users (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    phone       TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    pin_hash    TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE businesses (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL DEFAULT 'lainnya',  -- warung makan, toko kelontong, pedagang pasar, dll.
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX businesses_owner_idx ON businesses (owner_id);

-- ============================================================
-- 2. Dompet: tepat satu Dompet Usaha dan satu Dompet Pribadi per usaha
-- ============================================================
CREATE TABLE wallets (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id      UUID NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    kind             TEXT NOT NULL CHECK (kind IN ('business', 'personal')),
    name             TEXT NOT NULL,
    opening_balance  BIGINT NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (business_id, kind),
    UNIQUE (id, business_id)   -- dipakai FK komposit dari transactions
);

-- ============================================================
-- 3. Kategori (business_id NULL = kategori bawaan sistem)
-- ============================================================
CREATE TABLE categories (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id  UUID REFERENCES businesses(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL CHECK (kind IN ('income', 'expense', 'transfer')),
    scope        TEXT NOT NULL DEFAULT 'business' CHECK (scope IN ('business', 'personal', 'any')),
    name         TEXT NOT NULL,
    sort_order   INT NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX categories_name_uq
    ON categories (COALESCE(business_id, '00000000-0000-0000-0000-000000000000'::uuid), kind, name);

-- ============================================================
-- 4. Nota belanja dan hasil OCR
-- ============================================================
CREATE TABLE receipts (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id   UUID NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    uploaded_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    image_path    TEXT NOT NULL,                       -- path objek di Cloud Storage, bukan URL publik
    status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'processing', 'needs_review', 'confirmed', 'failed')),
    merchant      TEXT,
    receipt_date  DATE,
    total         BIGINT CHECK (total >= 0),
    confidence    NUMERIC(4,3) CHECK (confidence BETWEEN 0 AND 1),
    ocr_raw       JSONB,                               -- respons mentah model, untuk debugging dan evaluasi
    error         TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    confirmed_at  TIMESTAMPTZ
);
CREATE INDEX receipts_business_idx ON receipts (business_id, created_at DESC);

CREATE TABLE receipt_items (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    receipt_id   UUID NOT NULL REFERENCES receipts(id) ON DELETE CASCADE,
    position     INT NOT NULL DEFAULT 0,
    name         TEXT NOT NULL,
    qty          NUMERIC(12,3) NOT NULL DEFAULT 1 CHECK (qty > 0),
    unit_price   BIGINT NOT NULL CHECK (unit_price >= 0),
    subtotal     BIGINT NOT NULL CHECK (subtotal >= 0),
    category_id  UUID REFERENCES categories(id)         -- saran kategori dari AI, bisa diubah pengguna
);
CREATE INDEX receipt_items_receipt_idx ON receipt_items (receipt_id);

-- ============================================================
-- 5. Transaksi (buku besar)
--    - income  -> direction 'in'
--    - expense -> direction 'out'
--    - transfer antar dompet = DUA baris dengan transfer_group_id yang sama
--      (usaha -> pribadi = "prive", pribadi -> usaha = "setor modal")
--    - nota yang dikonfirmasi menghasilkan satu transaksi per kategori
-- ============================================================
CREATE TABLE transactions (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id        UUID NOT NULL,
    wallet_id          UUID NOT NULL,
    category_id        UUID REFERENCES categories(id),
    kind               TEXT NOT NULL CHECK (kind IN ('income', 'expense', 'transfer', 'adjustment')),
    direction          TEXT NOT NULL CHECK (direction IN ('in', 'out')),
    amount             BIGINT NOT NULL CHECK (amount > 0),
    occurred_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    note               TEXT,
    source             TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'ocr', 'voice')),
    receipt_id         UUID REFERENCES receipts(id) ON DELETE SET NULL,
    transfer_group_id  UUID,
    client_id          UUID,                            -- dibuat klien (PWA offline) agar sinkronisasi idempoten
    created_by         UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at         TIMESTAMPTZ,                     -- hapus lunak, jangan pernah DELETE baris keuangan

    FOREIGN KEY (wallet_id, business_id) REFERENCES wallets (id, business_id),
    CHECK ((kind = 'transfer') = (transfer_group_id IS NOT NULL)),
    CHECK (kind IN ('transfer', 'adjustment')
           OR (kind = 'income'  AND direction = 'in')
           OR (kind = 'expense' AND direction = 'out')),
    UNIQUE (business_id, client_id)
);
CREATE INDEX tx_business_time_idx ON transactions (business_id, occurred_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX tx_wallet_time_idx   ON transactions (wallet_id, occurred_at DESC)   WHERE deleted_at IS NULL;
CREATE INDEX tx_transfer_idx      ON transactions (transfer_group_id)             WHERE transfer_group_id IS NOT NULL;

-- ============================================================
-- 6. Aturan alert dan notifikasi
-- ============================================================
CREATE TABLE alert_rules (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id  UUID NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL CHECK (kind IN ('low_cash', 'prive_exceeds_profit', 'profit_milestone')),
    threshold    JSONB NOT NULL DEFAULT '{}',
    enabled      BOOLEAN NOT NULL DEFAULT true,
    UNIQUE (business_id, kind)
);

CREATE TABLE notifications (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    business_id  UUID NOT NULL REFERENCES businesses(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL,
    title        TEXT NOT NULL,
    body         TEXT NOT NULL,
    payload      JSONB NOT NULL DEFAULT '{}',
    dedupe_key   TEXT NOT NULL,                         -- mis. 'low_cash:2026-10-08' agar tidak dobel
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at      TIMESTAMPTZ,
    UNIQUE (business_id, dedupe_key)
);
CREATE INDEX notifications_unread_idx ON notifications (business_id, created_at DESC) WHERE read_at IS NULL;

-- ============================================================
-- 7. Otomatisasi: usaha baru langsung punya 2 dompet dan aturan alert bawaan
-- ============================================================
CREATE FUNCTION create_business_defaults() RETURNS trigger AS $$
BEGIN
    INSERT INTO wallets (business_id, kind, name) VALUES
        (NEW.id, 'business', 'Dompet Usaha'),
        (NEW.id, 'personal', 'Dompet Pribadi');
    INSERT INTO alert_rules (business_id, kind, threshold) VALUES
        (NEW.id, 'low_cash', '{"days_of_spending": 4}'),
        (NEW.id, 'prive_exceeds_profit', '{}'),
        (NEW.id, 'profit_milestone', '{"save_percent": 20}');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER businesses_defaults
    AFTER INSERT ON businesses
    FOR EACH ROW EXECUTE FUNCTION create_business_defaults();

-- ============================================================
-- 8. View: saldo dan ringkasan bulanan
-- ============================================================
CREATE VIEW wallet_balances AS
SELECT w.id AS wallet_id,
       w.business_id,
       w.kind,
       w.opening_balance
         + COALESCE(SUM(CASE t.direction WHEN 'in' THEN t.amount ELSE -t.amount END), 0) AS balance
FROM wallets w
LEFT JOIN transactions t ON t.wallet_id = w.id AND t.deleted_at IS NULL
GROUP BY w.id;

-- Untung = penjualan - biaya usaha. Prive (uang diambil pribadi) sengaja dipisah
-- agar bisa dibandingkan dengan untung: itu inti deteksi "kebocoran kas".
CREATE VIEW monthly_summary AS
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

-- ============================================================
-- 9. Data awal: kategori bawaan
-- ============================================================
INSERT INTO categories (business_id, kind, scope, name, sort_order) VALUES
    (NULL, 'income',   'business', 'Penjualan',               1),
    (NULL, 'income',   'business', 'Pendapatan lain',         2),
    (NULL, 'expense',  'business', 'Bahan baku',              1),
    (NULL, 'expense',  'business', 'Kemasan',                 2),
    (NULL, 'expense',  'business', 'Gaji karyawan',           3),
    (NULL, 'expense',  'business', 'Sewa tempat',             4),
    (NULL, 'expense',  'business', 'Listrik, air, dan gas',   5),
    (NULL, 'expense',  'business', 'Transportasi',            6),
    (NULL, 'expense',  'business', 'Pemasaran',               7),
    (NULL, 'expense',  'business', 'Perawatan alat',          8),
    (NULL, 'expense',  'business', 'Lain-lain (usaha)',       9),
    (NULL, 'expense',  'personal', 'Kebutuhan rumah tangga',  1),
    (NULL, 'expense',  'personal', 'Pendidikan',              2),
    (NULL, 'expense',  'personal', 'Kesehatan',               3),
    (NULL, 'expense',  'personal', 'Lain-lain (pribadi)',     4),
    (NULL, 'transfer', 'any',      'Ambil pribadi (prive)',   1),
    (NULL, 'transfer', 'any',      'Setor modal',             2);

