-- 0011_request_modules: ticket 13 — Modules consumed by Requests. A Data
-- Consumer may attach a Process Template (whose snapshot — module id plus
-- the parsed spec as it stood at attach time — rides the Request and is
-- copied onto the Collection at Create) and Agent Skills (module ids; the
-- content resolves at clarify-turn time through the registry). Both ride
-- JSONB per the single-writer whole-record pattern; a NULL template is the
-- default flow.

ALTER TABLE data_requests
    ADD COLUMN template JSONB,
    ADD COLUMN skills   JSONB NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE data_collections
    ADD COLUMN template JSONB;
