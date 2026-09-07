ALTER TABLE users
    ADD COLUMN IF NOT EXISTS plan               TEXT        NOT NULL DEFAULT 'free',
    ADD COLUMN IF NOT EXISTS is_admin           BOOLEAN     NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS suspended_at       TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS stripe_customer_id TEXT        UNIQUE;

CREATE TABLE IF NOT EXISTS stripe_events (
    id           TEXT        PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
