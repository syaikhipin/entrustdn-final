-- 0006_taxonomy: ticket 06 — Taxonomy, auto-categorization & catalog.
-- One row per taxonomy term (the Platform Admin's controlled vocabulary);
-- Data Assets carry their category assignments as a JSONB document on the
-- existing record — assignments stamp their own provenance (source,
-- confidence, assigned_at), which is exactly the per-item metadata the
-- catalog's facets and the trust view read.

CREATE TABLE taxonomy_terms (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    category    TEXT NOT NULL CHECK (category IN
                  ('crop', 'region', 'growth_stage', 'intervention', 'outcome', 'data_type')),
    value       TEXT NOT NULL,
    label       TEXT NOT NULL,
    keywords    TEXT[] NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (category, value)
);

ALTER TABLE data_assets
    ADD COLUMN categories JSONB NOT NULL DEFAULT '[]'::jsonb;
