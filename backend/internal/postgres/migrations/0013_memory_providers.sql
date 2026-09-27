-- 0013_memory_providers: ticket 14 — the admin-configured Memory Provider
-- registry (ADR 0003). Providers are platform configuration, never a
-- Module: connection facts only (name + endpoint), no credentials — the
-- agent connects over MCP with none. The unique name is the identifier the
-- contract field carries; every clarify turn dials these endpoints.

CREATE TABLE memory_providers (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL UNIQUE,
    endpoint   TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
