# Python agent sidecar with LangGraph

The agent runtime is a Python service using LangGraph, with Go owning the deterministic platform (auth, ledger, web, S3). The core product loop — agent asks a farmer a question, farmer replies days later over WhatsApp/Telegram/email — is a multi-day human-in-the-loop pause; durable execution with checkpointed resume is the one requirement LangGraph is explicitly engineered around. AgentScope 2.0 (permissions/sandboxes/voice) and Hermes Agent (a full environment, not an embeddable framework) were considered; AgentScope's containment and voice runtime don't outweigh losing battle-tested pause/resume, and Hermes is a product, not a library.

## Considered Options

- **LangGraph (chosen)** — durable execution via checkpointing, pause-for-human surviving days, largest ecosystem, MIT. Memory is bring-your-own: it comes from pluggable MCP memory providers (ADR 0003).
- **AgentScope 2.0** — per-tenant permissions, built-in sandboxes, native voice runtime, ReMe memory. Less central durable execution; stronger fit if the product were many sandboxed third-party agents rather than one trusted agent talking to farmers.
- **Hermes Agent** — installed personal-agent environment with channels built in. Reference design for channel integration; not embeddable as a backend runtime.
- **Go-native agents** — one runtime, but abandons the Python agent ecosystem (memory providers, evals) the thesis work lives in.
