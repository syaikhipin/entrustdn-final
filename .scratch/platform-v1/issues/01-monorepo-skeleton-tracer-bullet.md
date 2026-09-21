# 01: Monorepo skeleton & tracer bullet

**What to build:** The repo runs three cooperating services end-to-end: a Go backend exposing a health/status API, a Python LangGraph agent sidecar that answers one typed message (e.g. a ping/echo lifecycle event) over the Go↔Agent contract, and a Nuxt page that renders the backend's answer. This is the tracer bullet that proves the monorepo, the Go↔Agent message contract, and every test harness the later tickets inherit: contract fakes for the agent (Seam 2), Channel adapters (Seam 3), and MCP boundary (Seam 4), plus the backend API test harness (Seam 1). Postgres connection wired via env (ADR 0007); dev email log sink stubbed. Demoable: run three processes, open the page, see the round trip.

**Blocked by:** None (can start immediately).

**Status:** ready-for-agent

- [ ] `backend/` Go service serves a health/status endpoint; table-driven tests drive it at the HTTP boundary
- [ ] `agent/` Python/LangGraph service consumes and answers one typed contract message from the backend
- [ ] `web/` Nuxt app renders a value fetched from the backend
- [ ] Go↔Agent message contract defined once and shared (typed on both sides); contract-fake harness exists for both directions (Seam 2)
- [ ] Test harnesses for Channel adapter fakes (Seam 3) and in-process MCP fake servers (Seam 4) exist and are exercised by at least one smoke test each
- [ ] Postgres reachable via env config (ADR 0007); migrations/generation path established
- [ ] All three services boot from one command (compose/justfile/Makefile)
- [ ] CI-green: `go test -race ./...` and the agent/web suites pass
