# Thresh

Agentic agricultural data-sharing platform.

Go backend + Python LangGraph agent sidecar + Nuxt.js frontend, monorepo
(`backend/`, `agent/`, `web/`). Domain glossary in `CONTEXT.md`; decisions in
`docs/adr/`.

## Agent skills

### Issue tracker

Issues are tracked as local markdown files under `.scratch/<feature>/`. See `docs/agents/issue-tracker.md`.

### Domain docs

Single-context: root `CONTEXT.md` + `docs/adr/`. See `docs/agents/domain.md`.
