# 09: Payment gateway top-ups

**What to build:** Platform Admins connect a Payment Gateway (Stripe or PayPal) by configuring its API key; a Data Consumer initiates a top-up, pays, and Credits post to the Ledger (ADR 04: money-in only; Revenue Shares stay internal). Gateway callbacks verify and settle; failed/cancelled payments never credit. Gateway choice is platform config, never a Module. In tests, a fake gateway exercises success, failure, and replay paths.

**Blocked by:** 03 (Credits Ledger & automatic pricing).

**Status:** ready-for-agent

- [ ] Admin configures gateway (Stripe/PayPal) by API key; config is server-side platform config
- [ ] Consumer top-up flow: initiate → pay → Credits post to the Ledger
- [ ] Callback verification; forged/unverified callbacks rejected; replay of a settled callback credits nothing
- [ ] Failed and cancelled payments never credit
- [ ] Tests with a fake gateway: success, failure, cancel, replay, callback forgery
