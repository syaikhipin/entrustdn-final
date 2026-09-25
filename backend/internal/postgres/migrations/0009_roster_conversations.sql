-- 0009_roster_conversations: ticket 11 — the Farmer Organization's Member
-- roster and the Agent's Member conversations. Members are contact points,
-- not accounts: no credentials, no logins. Conversations own the durable
-- thread (JSONB, rewritten whole by the single-writer reply path, same
-- pattern as data_requests) and the resumable token, which is the Member's
-- capability — unique-indexed so a token names at most one conversation.

CREATE TABLE roster_members (
    id           TEXT PRIMARY KEY,
    org_id       UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    display_name TEXT NOT NULL,
    contact      TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_roster_members_org ON roster_members (org_id, created_at);

CREATE TABLE member_conversations (
    id            TEXT PRIMARY KEY,
    org_id        UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    request_id    TEXT NOT NULL DEFAULT '',
    member_id     TEXT NOT NULL DEFAULT '',
    member_name   TEXT NOT NULL,
    contact       TEXT NOT NULL,
    topic         TEXT NOT NULL,
    questions     JSONB NOT NULL DEFAULT '[]'::jsonb,
    resume_token  TEXT NOT NULL UNIQUE,
    thread        JSONB NOT NULL DEFAULT '[]'::jsonb,
    answers       JSONB NOT NULL DEFAULT '[]'::jsonb,
    status        TEXT NOT NULL CHECK (status IN (
                      'awaiting_member', 'completed', 'stopped')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_member_conversations_org ON member_conversations (org_id, created_at DESC);
