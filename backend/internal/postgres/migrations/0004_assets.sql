-- 0004_assets: ticket 04 — Data Assets. One row per uploaded dataset or
-- document; the bytes live in S3-compatible storage (ADR 0006) keyed by
-- object_key, which never crosses the HTTP boundary. pipeline records the
-- ingest stages the bytes passed through (ADR 0005: anonymization as a
-- pipeline stage — the proof it ran).

CREATE TABLE data_assets (
    id           UUID PRIMARY KEY,
    org_id       UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    size_bytes   BIGINT NOT NULL,
    format       TEXT NOT NULL,
    pipeline     JSONB NOT NULL DEFAULT '[]'::jsonb,
    provenance   JSONB NOT NULL DEFAULT '{}'::jsonb,
    object_key   TEXT NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_data_assets_org ON data_assets (org_id, created_at DESC);
