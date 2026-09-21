# Pluggable MCP memory providers, Hermes-style

The product implements no memory architecture of its own. Like Hermes Agent, the
agent runtime connects to external memory services over MCP (mem0, Hindsight,
supermemory, Honcho, …), configured by the Platform Admin and swappable per
deployment. The compatibility seam with the PhD thesis runs the other way: the
thesis's Iterative Routing algorithm, if ever wanted in the product, gets exposed
as an MCP memory server and connected like any other provider — no product changes.

## Considered Options

- **Pluggable MCP memory providers (chosen)** — the Hermes Agent pattern; matches the
  original product notes ("any existing memory, either Hindsight or anything"), keeps
  the product decoupled from research code, and lets providers compete per deployment.
- **Build the thesis three-store memory in-product** — rejected: couples the product to
  research code, duplicates mature providers, and makes the thesis a maintenance
  dependency. The thesis stays a thesis; the product ships.
- **Single hard-wired provider** — rejected: lock-in, and the notes explicitly want
  swappable memory.
