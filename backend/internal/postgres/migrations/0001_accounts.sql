-- 0001_accounts: the root membership table every later ticket builds on.
-- Roles and verification arrive in ticket 02; this establishes the shape.

CREATE TABLE accounts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email           TEXT NOT NULL UNIQUE,
    kind            TEXT NOT NULL CHECK (kind IN ('data_consumer', 'farmer_organization', 'platform_admin')),
    display_name    TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
