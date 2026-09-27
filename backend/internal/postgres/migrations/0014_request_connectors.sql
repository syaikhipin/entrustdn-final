-- 0014_request_connectors: ticket 14 — Connector Modules attached to
-- Requests. Like skills, the Request carries module IDs; the connection
-- fact resolves at clarify-turn time through the registry, so the latest
-- version always rides the wire. JSONB per the single-writer whole-record
-- pattern.

ALTER TABLE data_requests
    ADD COLUMN connectors JSONB NOT NULL DEFAULT '[]'::jsonb;
