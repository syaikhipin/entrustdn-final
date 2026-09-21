# Pluggable payment gateway, credits first

Credits remain the internal unit of value, but the platform now accepts external
money via an admin-configured Payment Gateway (Stripe, PayPal) connected by API
key — Top-ups convert money to Credits; the Ledger records everything. This
amends ADR 0002: the pilot can still run on granted credits (grants, automatic
pricing rules), but the gateway is built in from the start rather than deferred,
so org onboarding and revenue payouts aren't blocked later. Gateway choice is
platform configuration, not a user-uploadable Module — money flows are not
third-party extensible.

## Consequences

- Gateway credentials live in server-side platform config; Module Authors can
  never touch payment paths.
- Automatic credit pricing (per model, cached vs. unique data) remains the
  default; Platform Admins can override any credit amount manually.
- Amends [0002-credits-not-payments.md](0002-credits-not-payments.md): external
  money enters via Top-up only; Revenue Shares still settle internally.
