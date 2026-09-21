# 14: Connectors & Memory Providers over MCP

**What to build:** The MCP spine becomes real on the agent side: the agent's MCP client connects to admin-configured Memory Providers (mem0, Hindsight, supermemory, Honcho — external services, ADR 0003) for cross-session recall, and Connector Modules become live MCP/API connections the agent queries to enrich or cross-check answers. Admin UI to configure provider endpoints. Demoable: with a fake MCP memory server, the Agent recalls a fact from an earlier session instead of re-asking.

**Blocked by:** 07 (Requests & agent clarification over web chat), 08 (Module registry).

**Status:** ready-for-agent

- [ ] Agent MCP client boundary (Seam 4) with admin-configured Memory Provider endpoints
- [ ] Cross-session recall used in conversations (recall before re-asking)
- [ ] Connector Modules resolve to live MCP/API connections the agent can query
- [ ] Provider failures degrade gracefully (conversation continues without memory)
- [ ] In-process fake MCP servers drive tests (Seam 4); no live provider in tests
