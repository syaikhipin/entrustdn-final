# 02: Membership, Terms of Service & roles

**What to build:** A person registers as a Data Consumer or Farmer Organization with email verification (dev mode logs the link instead of sending), accepts the current versioned Terms of Service — the accepted version recorded per account — logs in, and lands on a role-appropriate home. Platform Admins see pending Farmer Organization applications and approve or reject them. Schema: accounts, roles, TOS versions, per-account acceptance records, org membership. Demoable end-to-end through the Nuxt UI.

**Blocked by:** 01 (Monorepo skeleton & tracer bullet).

**Status:** claimed

- [ ] Registration with email verification; verification link lands in the dev log sink
- [ ] TOS is versioned; the accepted version (and timestamp) is recorded per account; publishing a new version requires re-acceptance at next login
- [ ] Login/session; three roles: Data Consumer, Farmer Organization, Platform Admin
- [ ] Admin can approve/reject Farmer Organization applications
- [ ] Table-driven HTTP tests: registration, verification, TOS acceptance, role routing, approval flow
