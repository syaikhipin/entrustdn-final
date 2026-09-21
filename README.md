# Thresh

Agentic agricultural data-sharing platform. Monorepo, three services:

- **`backend/`** — Go. The system of record: membership, catalog, Requests,
  Ledger, anonymization, storage, admin. Public API at `/api/v1/*`.
- **`agent/`** — Python + LangGraph sidecar. The agentic loop: Request
  clarification, Channel conversations, MCP memory/connectors.
- **`web/`** — Nuxt.js frontend.
- **`contract/`** — the Go↔Agent message contract: JSON Schema + golden
  fixtures, pinned by tests on both sides.

Domain vocabulary: `CONTEXT.md`. Decisions: `docs/adr/`.

## Quick start

```bash
make dev   # boots Postgres (docker), agent (:8001), backend (:8080), web (:3000)
```

Then open http://localhost:3000 — the page fetches the backend's status live,
and the status includes a ping round trip to the agent over the contract.

Stop everything: `make stop`.

## Tests

```bash
make test          # all three suites
make test-backend  # go test -race ./... (Postgres tests skip without a DB)
make test-backend-pg  # same, against the compose Postgres
make test-agent    # pytest
make test-web      # vitest
```

Testing happens at four agreed seams (spec, Testing Decisions):
backend public HTTP API, Go↔Agent contract (fake agent / fake backend),
Channel adapter interface (fake channels), and the MCP client boundary
(in-process fake MCP servers). No test touches live external APIs.

## Layout

```
backend/
  cmd/api/            entrypoint
  internal/api/       Seam 1 handlers
  internal/agentclient/  backend→agent contract client (Seam 2)
  internal/contract/  Go-side contract types, pinned to /contract fixtures
  internal/config/    env config (ADR 0007)
  internal/postgres/  pool + embedded migrations
  internal/mailsink/  dev log mail sink
agent/
  src/thresh_agent/
    contract.py       Pydantic contract, pinned to /contract fixtures
    app.py            FastAPI /message + /health
    graph.py          LangGraph runtime (tracer-bullet ping node)
    channels.py       Channel adapter interface + FakeChannel (Seam 3)
    mcp_harness.py    in-process MCP fake memory provider (Seam 4)
    converse.py       agent-side conversation helpers
web/
  app/app.vue         status page
  app/status.ts       backend status parsing
contract/
  schemas/            envelope + ping-pair JSON Schema
  fixtures/           golden messages both sides test against
```
