-- 0016_paypal_webhook_id: ticket 17 — the PayPal gateway joins the Registry
-- behind the same Gateway seam as Stripe. PayPal's callback verification
-- signs "transmission_id|transmission_time|webhook_id|crc32(body)" with the
-- certificate PAYPAL-CERT-URL names, so the config needs PayPal's webhook
-- identifier (assigned when the listener URL is registered) as a third
-- credential. Stripe ignores it; the column is empty for Stripe configs.

ALTER TABLE payment_gateway_config
    ADD COLUMN webhook_id TEXT NOT NULL DEFAULT '';

ALTER TABLE payment_provider_configs
    ADD COLUMN webhook_id TEXT NOT NULL DEFAULT '';

-- The retention trigger must carry the new credential: a retained PayPal
-- config without its webhook id could not verify its callbacks.
CREATE OR REPLACE FUNCTION retain_provider_config() RETURNS trigger AS $$
BEGIN
    INSERT INTO payment_provider_configs
        (provider, api_key, webhook_secret, webhook_id, currency,
         micros_per_cent, return_base_url, updated_at)
    VALUES (NEW.provider, NEW.api_key, NEW.webhook_secret, NEW.webhook_id,
            NEW.currency, NEW.micros_per_cent, NEW.return_base_url, now())
    ON CONFLICT (provider) DO UPDATE SET
        api_key = EXCLUDED.api_key,
        webhook_secret = EXCLUDED.webhook_secret,
        webhook_id = EXCLUDED.webhook_id,
        currency = EXCLUDED.currency,
        micros_per_cent = EXCLUDED.micros_per_cent,
        return_base_url = EXCLUDED.return_base_url,
        updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER trg_retain_provider_config ON payment_gateway_config;
CREATE TRIGGER trg_retain_provider_config
    AFTER INSERT OR UPDATE OF provider, api_key, webhook_secret, webhook_id,
                               currency, micros_per_cent, return_base_url
    ON payment_gateway_config
    FOR EACH ROW
    WHEN (NEW.provider <> '')
    EXECUTE FUNCTION retain_provider_config();
