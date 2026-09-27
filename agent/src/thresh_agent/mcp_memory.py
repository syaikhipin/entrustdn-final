"""The agent's Memory Provider client boundary (Seam 4, ticket 14).

Memory Providers are external MCP services (ADR 0003) the agent connects to
for cross-session recall: recall before re-asking, remember after answers.
The backend hands the admin-configured endpoints on every clarify turn
(contract field memory_providers); the agent owns the connections and every
failure they hit.

Degradation rule: a provider that is down, slow, or speaking gibberish costs
the turn nothing — recall returns no memories, remember stores nothing, and
the conversation continues without memory. Failures are logged, never
surfaced as failed turns.

Connections are per call (connect → call → close): providers may be down for
days, and a sidecar holding sockets to them is a sidecar that falls over
with them. Each call is bounded by a timeout.

The surface is async (MCP is async); the one sync boundary lives in the
clarify loop, which runs one router call under asyncio.run — the same idiom
channel delivery already uses, and safe there because FastAPI runs sync
handlers in a threadpool where no loop is running.

The transport seam is a connect(endpoint) callable returning an async
context manager yielding a ready ClientSession — production passes
http_connect (MCP over Streamable HTTP); tests pass the in-process
mcp_client harness (real MCP, no sockets). Same session type either way,
so the call paths a test exercises are the ones a deployed provider hits.
"""

import asyncio
import json
import logging

from contextlib import asynccontextmanager

from mcp import ClientSession

from .contract import MemoryProvider

log = logging.getLogger(__name__)

# One call's budget. A provider slower than this is treated as down —
# recall must never cost the turn more than the model call does.
CALL_TIMEOUT_SECONDS = 5.0

# The wire convention every Memory Provider speaks (see mcp_harness.py:
# the fake is the reference implementation of the protocol).
RECALL_TOOL = "recall_memory"
STORE_TOOL = "store_memory"


@asynccontextmanager
async def http_connect(endpoint: str):
    """Production transport: MCP over Streamable HTTP to one provider,
    yielding the initialized session."""
    from mcp.client.streamable_http import streamable_http_client

    async with streamable_http_client(endpoint) as (read, write):
        async with ClientSession(read, write) as session:
            await session.initialize()
            yield session


async def _recall_one(connect, endpoint: str, query: str) -> list[str]:
    """Recall from one provider over one connection."""
    async with connect(endpoint) as session:
        result = await session.call_tool(RECALL_TOOL, {"query": query})
        if result.is_error:
            raise RuntimeError(f"{RECALL_TOOL} returned an error")
        text = result.content[0].text if result.content else "[]"
        memories = json.loads(text)
        if not isinstance(memories, list):
            raise RuntimeError(f"{RECALL_TOOL} returned {type(memories).__name__}, want a list")
        return [str(m) for m in memories]


async def _remember_one(connect, endpoint: str, text: str) -> str:
    """Store one memory on one provider over one connection."""
    async with connect(endpoint) as session:
        result = await session.call_tool(STORE_TOOL, {"text": text})
        if result.is_error:
            raise RuntimeError(f"{STORE_TOOL} returned an error")
        return result.content[0].text if result.content else ""


class MemoryClient:
    """One admin-configured provider, spoken to over MCP. Every method
    degrades: a failure logs and returns the empty answer, never raises."""

    def __init__(
        self,
        provider: MemoryProvider,
        connect=http_connect,
        timeout: float = CALL_TIMEOUT_SECONDS,
    ) -> None:
        self.provider = provider
        self._connect = connect
        self._timeout = timeout

    async def recall(self, query: str) -> list[str]:
        """Memories matching the query; [] when the provider is unusable."""
        try:
            async with asyncio.timeout(self._timeout):
                hits = await _recall_one(self._connect, self.provider.endpoint, query)
            return list(hits)
        except Exception as err:
            log.warning(
                "memory provider %s degraded, recalling nothing: %s",
                self.provider.name,
                err,
            )
            return []

    async def remember(self, text: str) -> bool:
        """Store one memory; False when the provider is unusable."""
        try:
            async with asyncio.timeout(self._timeout):
                await _remember_one(self._connect, self.provider.endpoint, text)
            return True
        except Exception as err:
            log.warning(
                "memory provider %s degraded, remembered nothing: %s",
                self.provider.name,
                err,
            )
            return False


class MemoryRouter:
    """Recall and remember across every admin-configured provider.

    Several providers may be connected at once (ADR 0003); recall merges
    their hits (deduplicated, provider order preserved), remember fans out
    to all. A provider that fails costs the turn nothing — the others still
    answer. No providers configured is the normal pre-ticket-14 shape: no-op.
    """

    def __init__(
        self,
        providers: list[MemoryProvider],
        connect=http_connect,
        timeout: float = CALL_TIMEOUT_SECONDS,
    ) -> None:
        self._clients = [MemoryClient(p, connect=connect, timeout=timeout) for p in providers]

    async def recall(self, query: str) -> list[str]:
        # Fan out concurrently and merge in provider order: a hung provider
        # costs its own 5s timeout, never the registry's stacked total.
        batches = await asyncio.gather(
            *(client.recall(query) for client in self._clients)
        )
        seen: set[str] = set()
        out: list[str] = []
        for hits in batches:
            for hit in hits:
                if hit not in seen:
                    seen.add(hit)
                    out.append(hit)
        return out

    async def remember(self, text: str) -> int:
        """Store on every provider; returns how many accepted."""
        results = await asyncio.gather(
            *(client.remember(text) for client in self._clients)
        )
        return sum(1 for ok in results if ok)

    def __bool__(self) -> bool:
        return bool(self._clients)
