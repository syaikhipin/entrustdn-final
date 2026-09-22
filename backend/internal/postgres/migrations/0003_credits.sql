-- 0003_credits: ticket 03 — the double-entry Ledger, automatic pricing
-- rules, and the rule-based grant/adjustment paths (ADRs 0002, 0004: credits
-- not payments; no external money until ticket 09's gateway).

-- Every credit movement is one ledger_movements row: kind, the responsible
-- actor, and the balanced entries below. Append-only: nothing UPDATEs or
-- DELETEs here. Balances are never stored — they are the SUM of entries.
CREATE TABLE ledger_movements (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind        TEXT NOT NULL CHECK (kind IN (
                    'grant', 'adjustment', 'inference_charge', 'data_charge',
                    'top_up', 'revenue_share')),
    actor_id    UUID REFERENCES accounts(id),
    request_id  TEXT,
    memo        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One leg of a movement. amount_micros is signed: positive credits the
-- scope, negative debits it. scope is 'acct:<account id>' for accounts or
-- 'sys:treasury' / 'sys:platform' for the platform-internal scopes. A
-- CHECK-verified balanced set is unenforceable per row; the store refuses
-- unbalanced postings in the same transaction that writes them.
CREATE TABLE ledger_entries (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    movement_id   UUID NOT NULL REFERENCES ledger_movements(id) ON DELETE CASCADE,
    scope         TEXT NOT NULL,
    amount_micros BIGINT NOT NULL,
    memo          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_ledger_entries_scope ON ledger_entries (scope, movement_id);

-- Charges that came from a metered model call record their usage so the
-- consumer's spend view can explain a charge down to tokens and rates.
CREATE TABLE inference_charge_details (
    movement_id          UUID PRIMARY KEY REFERENCES ledger_movements(id) ON DELETE CASCADE,
    model                TEXT NOT NULL,
    input_tokens         BIGINT NOT NULL,
    cached_input_tokens  BIGINT NOT NULL,
    output_tokens        BIGINT NOT NULL,
    input_micros_per_1k       BIGINT NOT NULL,
    cached_input_micros_per_1k BIGINT NOT NULL,
    output_micros_per_1k      BIGINT NOT NULL
);

-- The admin-configured price book: a single JSON document, one row.
-- Automatic pricing reads it; the admin surface (POST /admin/pricing) rewrites it.
CREATE TABLE pricing_rules (
    id          INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    document    JSONB NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
