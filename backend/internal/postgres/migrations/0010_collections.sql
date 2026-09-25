-- 0010_collections: ticket 12 — a clarified Request becomes a Data
-- Collection: items materialize over the org's roster Members × the
-- questions, gathering rounds accumulate per item as JSONB (the record is
-- only ever rewritten whole by the single-writer Sync path, same pattern
-- as data_requests and member_conversations), and an incomplete collection
-- carries the missing-data summary. The consumer_id rides its own column —
-- delivery entitlement checks it; the request FK is TEXT like its own
-- table's consumer-facing id.

CREATE TABLE data_collections (
    id          TEXT PRIMARY KEY,
    org_id      UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    consumer_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    request_id  TEXT NOT NULL,
    deadline    TIMESTAMPTZ,
    status      TEXT NOT NULL CHECK (status IN (
                    'collecting', 'completed', 'incomplete')),
    items       JSONB NOT NULL DEFAULT '[]'::jsonb,
    missing     JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_data_collections_org ON data_collections (org_id, created_at DESC);
CREATE INDEX idx_data_collections_consumer ON data_collections (consumer_id, created_at DESC);
CREATE INDEX idx_data_collections_request ON data_collections (request_id);
