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
import os

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

# Hindsight uses different tool names; try these as fallback.
HINDSIGHT_RECALL_TOOL = "recall"
HINDSIGHT_STORE_TOOL = "retain"


def _auth_headers(endpoint: str) -> dict:
    """Return auth headers for providers that need them."""
    # Hindsight requires Bearer auth via HINDSIGHT_API_KEY env var.
    if "hindsight" in endpoint.lower() or "vectorize.io" in endpoint:
        api_key = os.getenv("HINDSIGHT_API_KEY", "")
        if api_key:
            return {"Authorization": f"Bearer {api_key}"}
    return {}


@asynccontextmanager
async def http_connect(endpoint: str):
    """Production transport: MCP over Streamable HTTP to one provider,
    yielding the initialized session."""
    from mcp.client.streamable_http import streamable_http_client

    headers = _auth_headers(endpoint)
    kwargs = {"headers": headers} if headers else {}
    async with streamable_http_client(endpoint, **kwargs) as (read, write):
        async with ClientSession(read, write) as session:
            await session.initialize()
            yield session


async def _recall_one(connect, endpoint: str, query: str) -> list[str]:
    """Recall from one provider over one connection."""
    async with connect(endpoint) as session:
        # Try standard tool name first, then Hindsight's convention.
        for tool_name in (RECALL_TOOL, HINDSIGHT_RECALL_TOOL):
            try:
                # Hindsight's recall takes different params
                params = {"query": query} if tool_name == RECALL_TOOL else {"query": query, "limit": 5}
                result = await session.call_tool(tool_name, params)
                if result.is_error:
                    continue
                text = result.content[0].text if result.content else "[]"
                try:
                    memories = json.loads(text)
                except json.JSONDecodeError:
                    # Hindsight returns structured data, not JSON list
                    return [text] if text else []
                if not isinstance(memories, list):
                    return [str(memories)] if memories else []
                return [str(m) for m in memories]
            except Exception:
                continue
        return []


async def _remember_one(connect, endpoint: str, text: str) -> str:
    """Store one memory on one provider over one connection."""
    async with connect(endpoint) as session:
        # Try standard tool name first, then Hindsight's convention.
        for tool_name, params in (
            (STORE_TOOL, {"text": text}),
            (HINDSIGHT_STORE_TOOL, {"content": text}),
        ):
            try:
                result = await session.call_tool(tool_name, params)
                if result.is_error:
                    continue
                return result.content[0].text if result.content else ""
            except Exception:
                continue
        return ""


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
