"""Seam 4: the MCP client boundary (agent-side).

Memory Providers and Connectors are faked as in-process MCP servers; tests
assert the agent's recall/connector behavior against them (spec: Testing
Decisions). This file is the harness later tickets inherit: a
FakeMemoryProvider MCP server (real JSON-RPC MCP over in-memory streams)
plus a smoke test proving the agent can store and recall a memory through
the full MCP client session.
"""

import pytest

from thresh_agent.mcp_harness import FakeMemoryProvider, mcp_client


@pytest.mark.asyncio
async def test_fake_memory_provider_lists_memory_tools() -> None:
    """The fake is a real MCP server exposing the memory tool surface."""
    async with mcp_client(FakeMemoryProvider()) as session:
        tools = await session.list_tools()
        names = {t.name for t in tools.tools}
        assert "store_memory" in names
        assert "recall_memory" in names


@pytest.mark.asyncio
async def test_agent_remember_and_recall_round_trip() -> None:
    """Smoke: store a memory via MCP, recall it back via MCP."""
    async with mcp_client(FakeMemoryProvider()) as session:
        store = await session.call_tool(
            "store_memory", {"text": "farmer +35386… grows spring barley in Cork"}
        )
        assert not store.is_error

        hits = await session.call_tool("recall_memory", {"query": "barley"})
        text = hits.content[0].text if hits.content else ""
        assert "barley" in text


@pytest.mark.asyncio
async def test_recall_misses_return_nothing() -> None:
    async with mcp_client(FakeMemoryProvider()) as session:
        await session.call_tool("store_memory", {"text": "farmer grows wheat in Wexford"})

        hits = await session.call_tool("recall_memory", {"query": "dairy"})
        text = hits.content[0].text if hits.content else ""
        assert text.strip() in ("", "[]")


def test_fake_memory_provider_starts_empty() -> None:
    provider = FakeMemoryProvider()
    assert provider.recall_all() == []
