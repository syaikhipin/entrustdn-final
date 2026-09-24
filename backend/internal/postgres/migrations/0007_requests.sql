-- 0007_requests: ticket 07 — a Data Consumer's Request and its
-- clarification conversation. The commission (description, format, quality
-- bar, budget) is plain columns; the conversation and the reported
-- existing-Asset matches ride as JSONB arrays — they are only ever read or
-- rewritten whole by the single-writer chat path (the requests.Store seam
-- passes whole records).

CREATE TABLE data_requests (
    id             TEXT PRIMARY KEY,
    consumer_id    UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    description    TEXT NOT NULL,
    format         TEXT NOT NULL,
    quality_bar    TEXT NOT NULL DEFAULT '',
    budget_micros  BIGINT NOT NULL CHECK (budget_micros > 0),
    spent_micros   BIGINT NOT NULL DEFAULT 0 CHECK (spent_micros >= 0),
    status         TEXT NOT NULL CHECK (status IN (
                       'clarifying', 'clarified', 'budget_exhausted')),
    messages       JSONB NOT NULL DEFAULT '[]'::jsonb,
    matches        JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_data_requests_consumer ON data_requests (consumer_id, created_at DESC);
