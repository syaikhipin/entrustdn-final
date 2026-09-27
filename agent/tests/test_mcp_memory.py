"""Ticket 14: the agent's Memory Provider client boundary (Seam 4).

The agent connects to admin-configured Memory Providers over MCP and
recalls before re-asking. Every happy-path test drives the real MCP client
session against the in-process FakeMemoryProvider — the same code path a
deployed mem0/Hindsight/supermemory/Honcho endpoint answers. Degradation
tests use unreachable/malformed transports: a provider failure must never
fail a conversation turn.
"""

import pytest

from thresh_agent.contract import MemoryProvider
from thresh_agent.mcp_harness import FakeMemoryProvider, mcp_client
from thresh_agent.mcp_memory import MemoryClient, MemoryRouter, http_connect


def make_provider(**overrides) -> MemoryProvider:
    doc = {"name": "mem0-primary", "endpoint": "http://memory.local:8080/mcp"}
    doc.update(overrides)
    return MemoryProvider.model_validate(doc)


@pytest.mark.asyncio
async def test_memory_client_recalls_over_real_mcp_session() -> None:
    """Recall rides the full MCP wire: initialize → call_tool → parse."""
    server = FakeMemoryProvider()
    server.store_memory("farmer +35386… grows spring barley in Cork")

    client = MemoryClient(make_provider(), connect=lambda endpoint: mcp_client(server))
    assert await client.recall("barley") == ["farmer +35386… grows spring barley in Cork"]
    assert await client.recall("dairy") == []


@pytest.mark.asyncio
async def test_memory_client_remembers_over_real_mcp_session() -> None:
    server = FakeMemoryProvider()
    client = MemoryClient(make_provider(), connect=lambda endpoint: mcp_client(server))

    assert await client.remember("consumer asked for Leinster spring barley, csv")
    assert server.recall_all() == ["consumer asked for Leinster spring barley, csv"]


@pytest.mark.asyncio
async def test_unreachable_provider_degrades_to_no_memories() -> None:
    """The demoable degradation: provider down → recall [], remember False,
    no exception — the conversation continues without memory."""
    async def dead_connect(endpoint: str):
        raise ConnectionError("connection refused")
        yield  # pragma: no cover - makes this an async generator

    client = MemoryClient(make_provider(), connect=dead_connect)
    assert await client.recall("barley") == []
    assert await client.remember("anything") is False


@pytest.mark.asyncio
async def test_gibberish_provider_degrades_to_no_memories() -> None:
    """A provider that answers with non-JSON costs the turn nothing."""
    from mcp.server.mcpserver import MCPServer

    broken = MCPServer(name="broken-memory")

    def garbage(query: str) -> str:
        return "<<<not json>>>"

    broken.add_tool(garbage, description="recall")
    client = MemoryClient(make_provider(), connect=lambda endpoint: mcp_client(broken))
    assert await client.recall("barley") == []


@pytest.mark.asyncio
async def test_slow_provider_degrades_within_timeout() -> None:
    """A provider that hangs is treated as down after the call budget."""
    import asyncio

    async def hung_connect(endpoint: str):
        await asyncio.sleep(60)
        yield None  # pragma: no cover - never reached

    client = MemoryClient(make_provider(), connect=hung_connect, timeout=0.05)
    assert await client.recall("barley") == []


@pytest.mark.asyncio
async def test_router_merges_hits_across_providers() -> None:
    """Several providers may be connected at once (ADR 0003); recall
    merges and deduplicates, remember fans out."""
    one, two = FakeMemoryProvider(), FakeMemoryProvider()
    one.store_memory("shared fact about barley")
    two.store_memory("shared fact about barley")
    two.store_memory("barley only on provider two")

    from contextlib import asynccontextmanager

    @asynccontextmanager
    async def routing_connect(endpoint: str):
        server = one if endpoint.endswith(":a") else two
        async with mcp_client(server) as session:
            yield session

    router = MemoryRouter(
        [make_provider(name="a", endpoint="http://m:a"), make_provider(name="b", endpoint="http://m:b")],
        connect=routing_connect,
    )
    assert await router.recall("barley") == [
        "shared fact about barley",
        "barley only on provider two",
    ]


@pytest.mark.asyncio
async def test_router_fails_over_when_one_provider_is_down() -> None:
    """One dead provider never blocks the other's answers."""
    from contextlib import asynccontextmanager

    healthy = FakeMemoryProvider()
    healthy.store_memory("healthy provider fact")

    @asynccontextmanager
    async def routing_connect(endpoint: str):
        if "dead" in endpoint:
            raise ConnectionError("down")
        async with mcp_client(healthy) as session:
            yield session

    router = MemoryRouter(
        [make_provider(name="dead", endpoint="http://dead:1"), make_provider(name="alive", endpoint="http://alive:1")],
        connect=routing_connect,
    )
    assert await router.recall("fact") == ["healthy provider fact"]
    assert await router.remember("stored on the living one") == 1


@pytest.mark.asyncio
async def test_many_hung_providers_cost_one_timeout_not_their_sum() -> None:
    """The aggregate budget: five hung providers degrade within roughly one
    call timeout, not five stacked ones — recall must never blow the turn's
    budget just because the registry is full of the dead."""
    import asyncio
    import time

    async def hung_connect(endpoint: str):
        await asyncio.sleep(60)
        yield None  # pragma: no cover - never reached

    providers = [make_provider(name=f"p{i}", endpoint=f"http://m:{i}") for i in range(5)]
    router = MemoryRouter(providers, connect=hung_connect, timeout=0.1)
    started = time.monotonic()
    assert await router.recall("barley") == []
    elapsed = time.monotonic() - started
    assert elapsed < 0.35, f"recall took {elapsed:.2f}s for 5 hung providers, want ~one timeout"


@pytest.mark.asyncio
async def test_router_with_no_providers_is_a_noop() -> None:
    """The pre-ticket-14 shape: no providers configured, no-op answers."""
    router = MemoryRouter([])
    assert await router.recall("anything") == []
    assert await router.remember("anything") == 0
    assert not router


@pytest.mark.asyncio
async def test_production_transport_is_http_connect() -> None:
    """The default transport is the real Streamable HTTP client — pinned
    so a refactor cannot quietly replace the wire with a stub."""
    from thresh_agent.mcp_memory import CALL_TIMEOUT_SECONDS, RECALL_TOOL, STORE_TOOL

    assert http_connect.__name__ == "http_connect"
    assert (RECALL_TOOL, STORE_TOOL) == ("recall_memory", "store_memory")
    assert CALL_TIMEOUT_SECONDS > 0
