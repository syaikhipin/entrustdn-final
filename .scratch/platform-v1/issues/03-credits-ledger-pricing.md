# 03: Credits Ledger & automatic pricing

**What to build:** The double-entry Ledger exists and works: every credit movement (grant, adjustment, inference charge, data charge, top-up, Revenue Share posting) creates balanced entries; balances are derived, never stored as a single mutable number. Automatic pricing rules (per model, cached vs unique data) compute charges; Platform Admins can override any amount or grant Credits; Data Consumers see their balance and a spend view. No external money in this ticket — top-ups arrive in ticket 09. Demoable: admin grants credits, a test charge prices automatically, consumer sees the entries.

**Blocked by:** 02 (Membership, Terms of Service & roles).

**Status:** ready-for-agent

- [ ] Double-entry postings are always balanced; violating attempts fail loudly in tests
- [ ] Balances derived from entries; no mutable balance column to corrupt
- [ ] Automatic pricing rules (per model, cached/unique) configurable by admin
- [ ] Admin override and grant paths post through the Ledger, never bypass it
- [ ] Consumer-facing balance + entry history view
- [ ] Table-driven tests: pricing math, balanced postings, override/grant, derivation of balances
