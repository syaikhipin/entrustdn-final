"""Connector Modules as live connections (ticket 14).

A Connector Module's config resolves to an external MCP server or plain API
endpoint the agent queries during a clarify turn to enrich or cross-check
its answer. The backend resolves the attached modules and hands the
connection facts on the contract (connectors field); the agent speaks to
them and owns every failure.

Degradation rule, same as memory: a connector that is down, slow, or
malformed contributes nothing to the turn — findings come back empty and
the conversation continues. A failed connector never fails a turn.

Two transports:
- mcp: MCP over Streamable HTTP; `query` names the tool to call, the
  request text rides as the tool's `query` argument (the same convention
  the memory providers' tools follow).
- api: plain HTTP GET; `query` is appended to the endpoint as a path/query
  string. The response body is carried verbatim as the finding text.

The transport seam mirrors mcp_memory's: an open_mcp callable returning an
async context manager that yields a ready ClientSession — production passes
http_open (MCP over Streamable HTTP); tests pass the in-process mcp_client
harness, so the paths tests exercise are the ones a deployed connector
answers.
"""

import asyncio
import logging

from contextlib import asynccontextmanager

import httpx

from mcp import ClientSession

from .contract import ConnectorModule

log = logging.getLogger(__name__)

# One query's budget, same reasoning as memory's CALL_TIMEOUT_SECONDS.
QUERY_TIMEOUT_SECONDS = 5.0

# MCP tool-call argument convention: the request text rides as `query`.
MCP_QUERY_ARGUMENT = "query"

# The tool an MCP connector exposes when its config names no tool.
DEFAULT_MCP_TOOL = "query"


@asynccontextmanager
async def http_open(endpoint: str):
    """Production MCP transport: one initialized session per query."""
    from mcp.client.streamable_http import streamable_http_client

    async with streamable_http_client(endpoint) as (read, write):
        async with ClientSession(read, write) as session:
            await session.initialize()
            yield session


def _text_of(result) -> str:
    """The tool result's text content."""
    return result.content[0].text if result.content else ""


async def _query_mcp(endpoint: str, tool: str, request: str, open_mcp) -> str:
    async with open_mcp(endpoint) as session:
        result = await session.call_tool(tool, {MCP_QUERY_ARGUMENT: request})
        if result.is_error:
            raise RuntimeError(f"tool {tool!r} returned an error")
        return _text_of(result)


async def _query_api(endpoint: str, path: str) -> str:
    async with httpx.AsyncClient(timeout=QUERY_TIMEOUT_SECONDS) as client:
        url = endpoint.rstrip("/") + ("/" + path.lstrip("/") if path else "")
        resp = await client.get(url)
        resp.raise_for_status()
        return resp.text


class ConnectorHub:
    """Queries the turn's Connector Modules, degrading per connector.

    A connector that answers contributes a (name, finding) pair; one that
    fails removes only its own line. The hub with no connectors queries
    nothing — the normal pre-ticket-14 shape.
    """

    def __init__(
        self,
        connectors: list[ConnectorModule],
        open_mcp=http_open,
        timeout: float = QUERY_TIMEOUT_SECONDS,
    ) -> None:
        self._connectors = list(connectors)
        self._open = open_mcp
        self._timeout = timeout

    async def query_all(self, request: str) -> list[tuple[str, str]]:
        # Fan out concurrently: a hung connector costs its own 5s timeout,
        # never the stack of every connector's.
        async def guarded(conn: ConnectorModule) -> str | None:
            try:
                async with asyncio.timeout(self._timeout):
                    return await self._query_one(conn, request)
            except Exception as err:
                log.warning("connector %s degraded, skipped: %s", conn.name, err)
                return None

        texts = await asyncio.gather(*(guarded(conn) for conn in self._connectors))
        return [
            (conn.name, text)
            for conn, text in zip(self._connectors, texts)
            if text is not None and text.strip()
        ]

    async def _query_one(self, conn: ConnectorModule, request: str) -> str:
        if conn.transport == "api":
            return await _query_api(conn.endpoint, conn.query)
        tool = conn.query or DEFAULT_MCP_TOOL
        return await _query_mcp(conn.endpoint, tool, request, self._open)
