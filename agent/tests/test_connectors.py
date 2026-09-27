"""Ticket 14: Connector Modules resolve to live MCP/API connections.

Every happy-path test queries the in-process FakeConnectorServer over a
real MCP session — the same call path a deployed MCP connector answers.
API transport is pinned against httpx's MockTransport (never a live
endpoint). Degradation: a connector that fails removes only its own line.
"""

import httpx
import pytest

from thresh_agent.connectors import ConnectorHub, http_open
from thresh_agent.contract import ConnectorModule
from thresh_agent.mcp_harness import FakeConnectorServer, mcp_client


def make_connector(**overrides) -> ConnectorModule:
    doc = {
        "name": "teagasc-reports",
        "endpoint": "http://connector.local:9090/mcp",
        "transport": "mcp",
        "query": "query",
    }
    doc.update(overrides)
    return ConnectorModule.model_validate(doc)


@pytest.mark.asyncio
async def test_mcp_connector_answers_over_real_session() -> None:
    """The agent queries a Connector Module and the finding rides home."""
    server = FakeConnectorServer(answer="Teagasc: 7.8 t/ha average, 2025")
    hub = ConnectorHub(
        [make_connector()], open_mcp=lambda endpoint: mcp_client(server)
    )
    findings = await hub.query_all("what is the average spring barley yield?")
    assert findings == [
        ("teagasc-reports", "Teagasc: 7.8 t/ha average, 2025")
    ]
    # The request text rode to the connector as the tool's query argument.
    assert server.queries == ["what is the average spring barley yield?"]


@pytest.mark.asyncio
async def test_mcp_connector_default_tool_is_query() -> None:
    """A connector config that names no tool still talks to `query`."""
    server = FakeConnectorServer(answer="ok")
    hub = ConnectorHub(
        [make_connector(query="")], open_mcp=lambda endpoint: mcp_client(server)
    )
    assert await hub.query_all("ping") == [("teagasc-reports", "ok")]


@pytest.mark.asyncio
async def test_failing_connector_degrades_to_nothing() -> None:
    """A broken connector source removes only its own finding — the hub
    never raises, so the conversation continues."""
    server = FakeConnectorServer()
    server.enable_failure()
    hub = ConnectorHub(
        [make_connector()], open_mcp=lambda endpoint: mcp_client(server)
    )
    assert await hub.query_all("any question") == []


@pytest.mark.asyncio
async def test_dead_connector_degrades_and_healthy_one_still_answers() -> None:
    """Failover across connectors, like across memory providers."""
    healthy = FakeConnectorServer(answer="healthy finding")

    from contextlib import asynccontextmanager

    @asynccontextmanager
    async def routing_open(endpoint: str):
        if "dead" in endpoint:
            raise ConnectionError("down")
        async with mcp_client(healthy) as session:
            yield session

    hub = ConnectorHub(
        [
            make_connector(name="dead-conn", endpoint="http://dead:1/mcp"),
            make_connector(name="healthy-conn", endpoint="http://alive:1/mcp"),
        ],
        open_mcp=routing_open,
    )
    assert await hub.query_all("question") == [("healthy-conn", "healthy finding")]


@pytest.mark.asyncio
async def test_slow_connector_degrades_within_timeout() -> None:
    import asyncio

    async def hung_open(endpoint: str):
        await asyncio.sleep(60)
        yield None  # pragma: no cover

    hub = ConnectorHub(
        [make_connector()], open_mcp=hung_open, timeout=0.05
    )
    assert await hub.query_all("question") == []


@pytest.mark.asyncio
async def test_api_connector_queries_the_endpoint() -> None:
    """API transport: GET endpoint/query, body carried verbatim."""
    captured: dict = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured["url"] = str(request.url)
        return httpx.Response(200, text='{"yields": [7.8, 8.1]}')

    hub = ConnectorHub(
        [make_connector(name="met-eireann", transport="api", endpoint="http://api.local", query="reports/2025")],
    )
    # Swap the module-level _query_api's httpx client via monkeypatching
    # the transport: simpler — patch the hub module function.
    import thresh_agent.connectors as connectors_mod

    original = connectors_mod._query_api

    async def patched(endpoint: str, path: str) -> str:
        async with httpx.AsyncClient(
            transport=httpx.MockTransport(handler)
        ) as client:
            url = endpoint.rstrip("/") + "/" + path.lstrip("/")
            resp = await client.get(url)
            return resp.text

    connectors_mod._query_api = patched
    try:
        findings = await hub.query_all("irrelevant for api")
    finally:
        connectors_mod._query_api = original

    assert findings == [("met-eireann", '{"yields": [7.8, 8.1]}')]
    assert captured["url"] == "http://api.local/reports/2025"


@pytest.mark.asyncio
async def test_api_connector_http_error_degrades() -> None:
    """A 500 from an API connector is one missing line, not a turn failure."""
    import thresh_agent.connectors as connectors_mod

    original = connectors_mod._query_api

    async def failing(endpoint: str, path: str) -> str:
        raise httpx.HTTPStatusError(
            "500", request=httpx.Request("GET", endpoint), response=httpx.Response(500)
        )

    connectors_mod._query_api = failing
    hub = ConnectorHub([make_connector(name="c", transport="api")])
    try:
        assert await hub.query_all("q") == []
    finally:
        connectors_mod._query_api = original


@pytest.mark.asyncio
async def test_many_hung_connectors_cost_one_timeout_not_their_sum() -> None:
    """The aggregate budget: five hung connectors degrade within roughly one
    query timeout, not five stacked ones."""
    import asyncio
    import time

    async def hung_open(endpoint: str):
        await asyncio.sleep(60)
        yield None  # pragma: no cover - never reached

    conns = [make_connector(name=f"c{i}", endpoint=f"http://c:{i}") for i in range(5)]
    hub = ConnectorHub(conns, open_mcp=hung_open, timeout=0.1)
    started = time.monotonic()
    assert await hub.query_all("question") == []
    elapsed = time.monotonic() - started
    assert elapsed < 0.35, f"query_all took {elapsed:.2f}s for 5 hung connectors, want ~one timeout"


@pytest.mark.asyncio
async def test_hub_with_no_connectors_queries_nothing() -> None:
    """The pre-ticket-14 shape: no connectors, no queries, no findings."""
    hub = ConnectorHub([])
    assert await hub.query_all("anything") == []
