-- 0008_modules: ticket 08 — the Module registry. One row per version of a
-- Module: the manifest (name, kind, version, A2A capability description)
-- and the body (markdown content and/or configuration). The module's
-- identity fields are denormalized onto every version row — the registry
-- reads whole records, so version history is "every row with this
-- module_id, oldest first". system_wide is module-level and rewritten on
-- every row when the admin promotes or demotes; deprecated is
-- version-level. No executable content is ever stored — validation
-- refuses it upstream, and these are TEXT documents, not blobs.

CREATE TABLE modules (
    id           TEXT PRIMARY KEY,          -- the version record's ID
    module_id    TEXT NOT NULL,             -- the Module's stable identity
    name         TEXT NOT NULL,
    kind         TEXT NOT NULL CHECK (kind IN (
                     'agent_skill', 'process_template', 'connector')),
    author_id    UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    version      TEXT NOT NULL,
    capability   TEXT NOT NULL,
    content      TEXT NOT NULL DEFAULT '',
    config       TEXT NOT NULL DEFAULT '',
    system_wide  BOOLEAN NOT NULL DEFAULT FALSE,
    deprecated   BOOLEAN NOT NULL DEFAULT FALSE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (module_id, version)
);
CREATE INDEX idx_modules_author ON modules (author_id, created_at DESC);
CREATE INDEX idx_modules_versions ON modules (module_id, created_at);
CREATE INDEX idx_modules_system_wide ON modules (system_wide) WHERE system_wide;

-- module_grants: one row per account an author granted read access to a
-- private Module. System-wide Modules need no grants. module_id carries no
-- foreign key — the modules table holds one row per version, so module_id
-- cannot be UNIQUE there; grant integrity is the service seam's job (it
-- refuses grants on modules that do not exist).
CREATE TABLE module_grants (
    module_id  TEXT NOT NULL,
    account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (module_id, account_id)
);
CREATE INDEX idx_module_grants_account ON module_grants (account_id);
