-- 0002_membership: ticket 02 — credentials, verification, versioned TOS,
-- per-account acceptance records, sessions, and the application workflow.

-- Ticket 01 named the column `kind`; ticket 02's domain is roles.
ALTER TABLE accounts RENAME COLUMN kind TO role;

-- Credential + lifecycle columns for the root membership table.
ALTER TABLE accounts
    ADD COLUMN password_hash    TEXT NOT NULL DEFAULT '',
    ADD COLUMN status           TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'pending_approval', 'rejected')),
    ADD COLUMN verified_at      TIMESTAMPTZ;

-- One-shot email verification tokens. The token is stored as issued; it is
-- 32 hex chars of crypto/rand and never appears in an API response.
CREATE TABLE verification_tokens (
    token       TEXT PRIMARY KEY,
    account_id  UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Versioned Terms of Service. Publishing a new version makes it current;
-- accounts that accepted an older one must re-accept at next login.
CREATE TABLE tos_versions (
    version       TEXT PRIMARY KEY,
    body          TEXT NOT NULL,
    published_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Which version each account accepted, and when (story 7). Append-only:
-- re-acceptance adds a row; latest wins for the re-acceptance check.
CREATE TABLE tos_acceptances (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id   UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    tos_version  TEXT NOT NULL REFERENCES tos_versions(version),
    accepted_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_id, tos_version, accepted_at)
);
CREATE INDEX idx_tos_acceptances_account ON tos_acceptances (account_id, accepted_at DESC);

-- Opaque bearer sessions. requires_reacceptance marks a session opened
-- while a newer TOS version exists than the account last accepted: that
-- session may act only on the TOS re-acceptance flow until cleared.
CREATE TABLE sessions (
    token                   TEXT PRIMARY KEY,
    account_id              UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    requires_reacceptance   BOOLEAN NOT NULL DEFAULT FALSE
);
