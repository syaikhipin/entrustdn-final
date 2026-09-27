-- 0015_provider_config_retention: ticket 18 — a Payment Gateway switch or
-- disable must not strand outstanding top-ups (found in the #9 review).
-- Retain the last-known configuration per provider so the webhook can still
-- VERIFY callbacks from previously-known providers and settle their
-- outstanding sessions against the top-up's own provider column.
--
-- Money safety is unchanged: settlement still claims pending → settled
-- exactly once inside the SettlePaid transaction; retention only widens
-- which callbacks are verifiable.

CREATE TABLE payment_provider_configs (
    provider       TEXT PRIMARY KEY,
    api_key        TEXT NOT NULL,
    webhook_secret TEXT NOT NULL,
    currency       TEXT NOT NULL,
    micros_per_cent BIGINT NOT NULL CHECK (micros_per_cent > 0),
    return_base_url TEXT NOT NULL DEFAULT '',
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Backfill the mirror from the current configuration row before the
-- trigger exists: a deployment upgrading with a live gateway and
-- outstanding top-ups must be able to settle them immediately, not only
-- after an admin re-saves the config.
INSERT INTO payment_provider_configs
    (provider, api_key, webhook_secret, currency, micros_per_cent, return_base_url, updated_at)
SELECT provider, api_key, webhook_secret, currency, micros_per_cent, return_base_url, now()
FROM payment_gateway_config
WHERE provider <> ''
ON CONFLICT (provider) DO NOTHING;

-- The retention mirror. PaymentGatewayConfig rows are the current platform
-- choice (one row, possibly cleared by a disable); this table remembers
-- every provider the platform has EVER configured. Keeping the mirror in a
-- trigger means the postgres store's existing SaveConfig path needs no new
-- transactional discipline — the config write and its retention commit
-- together, or neither happens.
CREATE FUNCTION retain_provider_config() RETURNS trigger AS $$
BEGIN
    INSERT INTO payment_provider_configs
        (provider, api_key, webhook_secret, currency, micros_per_cent, return_base_url, updated_at)
    VALUES (NEW.provider, NEW.api_key, NEW.webhook_secret, NEW.currency,
            NEW.micros_per_cent, NEW.return_base_url, now())
    ON CONFLICT (provider) DO UPDATE SET
        api_key = EXCLUDED.api_key,
        webhook_secret = EXCLUDED.webhook_secret,
        currency = EXCLUDED.currency,
        micros_per_cent = EXCLUDED.micros_per_cent,
        return_base_url = EXCLUDED.return_base_url,
        updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_retain_provider_config
    AFTER INSERT OR UPDATE OF provider, api_key, webhook_secret, currency, micros_per_cent, return_base_url
    ON payment_gateway_config
    FOR EACH ROW
    WHEN (NEW.provider <> '')
    EXECUTE FUNCTION retain_provider_config();
