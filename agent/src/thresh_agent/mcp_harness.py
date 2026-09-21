"""MCP harness (Seam 4, agent-side).

Memory Providers and Connectors are external MCP services (ADR 0003); tests
fake them as in-process MCP servers and assert the agent's recall/connector
behavior against them. This module is that harness: a FakeMemoryProvider
that IS a real MCP server (MCPServer, JSON-RPC over in-memory streams) with
two tools, store_memory and recall_memory — the same wire protocol a real
provider (mem0, Hindsight, supermemory, Honcho) speaks.

mcp_client() connects a real MCP ClientSession to any in-process server, so
agent code paths exercised here are the same ones a deployed provider hits.
"""

import asyncio
import json
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager

from mcp import ClientSession
from mcp.server.mcpserver import MCPServer
from mcp.shared.memory import create_client_server_memory_streams


class FakeMemoryProvider(MCPServer):
    """In-process MCP memory server: memories in a list, substring recall.

    A real MCPServer with store_memory / recall_memory tools; connect with
    mcp_client(FakeMemoryProvider()) and drive it over the MCP protocol.
    """

    def __init__(self) -> None:
        super().__init__(name="fake-memory-provider")
        self._memories: list[str] = []
        self.add_tool(
            self.store_memory,
            description="Store one memory; returns the memory id.",
        )
        self.add_tool(
            self.recall_memory,
            description="Recall memories matching the query (JSON list).",
        )

    # MCP tool: store one memory; returns the memory id.
    def store_memory(self, text: str) -> str:
        self._memories.append(text)
        return f"mem-{len(self._memories)}"

    # MCP tool: recall memories matching the query.
    def recall_memory(self, query: str) -> str:
        needle = query.lower()
        return json.dumps([m for m in self._memories if needle in m.lower()])

    def recall_all(self) -> list[str]:
        """Test helper: every stored memory."""
        return list(self._memories)


@asynccontextmanager
async def mcp_client(server: MCPServer) -> AsyncIterator[ClientSession]:
    """Connect a real MCP client session to an in-process MCP server.

    Yields an initialized ClientSession speaking JSON-RPC MCP over in-memory
    streams — no sockets, no subprocesses. The pattern tests use for every
    fake Memory Provider and Connector (Seam 4).
    """
    async with create_client_server_memory_streams() as (client_io, server_io):
        client_read, client_write = client_io
        server_read, server_write = server_io

        server_task = asyncio.create_task(
            server._lowlevel_server.run(
                server_read,
                server_write,
                server._lowlevel_server.create_initialization_options(),
            )
        )
        try:
            async with ClientSession(client_read, client_write) as session:
                await session.initialize()
                yield session
        finally:
            server_task.cancel()
            try:
                await server_task
            except BaseException:
                pass
