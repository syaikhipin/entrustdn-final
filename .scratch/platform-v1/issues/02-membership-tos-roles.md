# 02: Membership, Terms of Service & roles

**What to build:** A person registers as a Data Consumer or Farmer Organization with email verification (dev mode logs the link instead of sending), accepts the current versioned Terms of Service — the accepted version recorded per account — logs in, and lands on a role-appropriate home. Platform Admins see pending Farmer Organization applications and approve or reject them. Schema: accounts, roles, TOS versions, per-account acceptance records, org membership. Demoable end-to-end through the Nuxt UI.

**Blocked by:** 01 (Monorepo skeleton & tracer bullet).

**Status:** resolved

- [x] Registration with email verification; verification link lands in the dev log sink
- [x] TOS is versioned; the accepted version (and timestamp) is recorded per account; publishing a new version requires re-acceptance at next login
- [x] Login/session; three roles: Data Consumer, Farmer Organization, Platform Admin
- [x] Admin can approve/reject Farmer Organization applications
- [x] Table-driven HTTP tests: registration, verification, TOS acceptance, role routing, approval flow

## Comments

**2026-09-22 — implemented in `8630bf9`, review fixes in `1ff9072`.**

Scope delivered, per the seam-1 plan agreed with the user:

- **Membership core** (`backend/internal/membership`): `Account`/`Role`/`Status`, PBKDF2-SHA256 password hashing (stdlib `crypto/pbkdf2`, 210k iterations, per-hash salt), one-shot 48h verification tokens, `MemoryStore` test double. `DummyCheck` equalizes PBKDF2 work on the unknown-email login path (review finding; live probe: ~30 ms both ways).
- **HTTP API** (`backend/internal/api`): register (consumer active, org → `pending_approval`, refuses the admin role), verify (one-shot, 404 on reuse/expiry), login (uniform 401, 403 unverified, session flagged when latest TOS acceptance ≠ current version), `/me`, accept-TOS (clears the flag), logout (allowed even while flagged — session hygiene; the gate lives on the admin surface), admin queue + approve/reject (409 on double decision, 403 for non-admins and TOS-flagged sessions), TOS publish (monotonic versions, immutable bodies, 409 duplicate).
- **Schema** (`migrations/0002_membership.sql`): `accounts.kind` → `role` rename, `password_hash`/`status`/`verified_at`, `verification_tokens`, `tos_versions`, append-only `tos_acceptances` (latest wins), `sessions.requires_reacceptance`. Postgres store conforms to the same `membership.Store` contract the memory double implements (live-PG conformance suite in `backend/internal/postgres`).
- **Cold start**: idempotent env-driven bootstrap (`cmd/api/bootstrap.go`) publishes TOS 1.0 and provisions the first Platform Admin before listening — solves the registration-needs-TOS / publish-needs-admin circularity. Pilot defaults in `.env.example`; override `BOOTSTRAP_*` for real deployments.
- **Web** (`web/`): register (role select + inline TOS + accept checkbox), verify, login, TOS re-acceptance interstitial, role homes (consumer / organization with pending+rejected states / admin queue + publish form). Session composable restores from sessionStorage via `/me`; page guards wait for the restore to finish (review finding — the eager guard stranded refreshing users at /login).
- **Tests**: table-driven HTTP suite at the seam (registration, verification, TOS acceptance, role routing, approval flow) plus password/token/bootstrap unit tests — 116 Go tests green under `-race` with live Postgres attached (conformance suite skips cleanly without `TEST_DATABASE_URL`); 23 Python (agent, untouched but re-run); 15 web (vitest + vue-tsc typecheck + nuxt build clean).

*Note on "org membership": read as the account *being* the org (a Farmer Organization account with display name and status). Individual Farmer Members remain accountless by design — the member contact lists arrive with the Channels ticket (11).*

Demoed end-to-end live: register org → verify via logged link → login → admin approves → org home; TOS publish → re-login flagged → re-accept → flag cleared.
