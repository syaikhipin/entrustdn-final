-- 0012_payments: ticket 09 — payment gateway top-ups (ADR 0004). The
-- gateway is platform configuration (one row, server-side credentials);
-- top-ups are the money-in records whose settlement posts the balanced
-- top_up movement into the Ledger (migration 0003's kind vocabulary).

-- The platform's single Payment Gateway configuration. Credentials are
-- server-side only (ADR 0004) — the API surface masks them and this row is
-- the only place they exist. No row = top-ups disabled.
CREATE TABLE payment_gateway_config (
    id             INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    provider       TEXT NOT NULL,
    api_key        TEXT NOT NULL,
    webhook_secret TEXT NOT NULL,
    currency       TEXT NOT NULL,
    micros_per_cent BIGINT NOT NULL CHECK (micros_per_cent > 0),
    return_base_url TEXT NOT NULL DEFAULT '',
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One money-in attempt: opened at initiate, closed by the first terminal
-- callback. Append-only status history lives in the top-up itself — the
-- status column moves forward only, and the settled movement_id joins the
-- row to its balanced ledger posting. The unique reference is the
-- idempotency key: gateway replays land on the same row and credit
-- nothing extra.
CREATE TABLE top_ups (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id     UUID NOT NULL REFERENCES accounts(id),
    reference      TEXT NOT NULL UNIQUE,
    provider       TEXT NOT NULL,
    amount_minor   BIGINT NOT NULL CHECK (amount_minor > 0),
    currency       TEXT NOT NULL,
    credits_micros BIGINT NOT NULL CHECK (credits_micros > 0),
    status         TEXT NOT NULL DEFAULT 'pending' CHECK (status IN (
                       'pending', 'settled', 'failed', 'cancelled')),
    movement_id    UUID REFERENCES ledger_movements(id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_top_ups_account ON top_ups (account_id, created_at DESC);
