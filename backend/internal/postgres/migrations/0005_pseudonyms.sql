-- 0005_pseudonyms: ticket 05 — the pseudonym map (ADR 0005). One row per
-- pseudonymized identifier, scoped to the owning Farmer Organization. This
-- is the GDPR erasure surface: deleting an org's rows re-mints every
-- pseudonym on next use, and the rows are never exported — delivered data
-- carries pseudonyms only. The raw identifier is stored hashed, not
-- plaintext: the platform can keep the map stable without holding a
-- searchable dossier of member identities.

CREATE TABLE pseudonym_maps (
    org_id     UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    value_hash TEXT NOT NULL,
    pseudonym  TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, kind, value_hash)
);
CREATE INDEX idx_pseudonym_maps_org ON pseudonym_maps (org_id, created_at DESC);
