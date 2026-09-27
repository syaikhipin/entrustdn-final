"""Ticket 14: recall-before-re-asking in the clarification loop, and
connector findings riding into the prompt — driven end to end through the
in-process MCP fakes (Seam 4), with the fake model gateway recording the
prompt it saw. Degradation tests pin the rule: a provider or connector
failure never fails a turn.
"""

import pytest

from thresh_agent.clarify import ClarificationLoop
from thresh_agent.contract import (
    ClarifyRequest,
    ConnectorModule,
    MemoryProvider,
)
from thresh_agent.gateway import FakeModelGateway
from thresh_agent.mcp_harness import FakeConnectorServer, FakeMemoryProvider, mcp_client

from contextlib import asynccontextmanager


def make_request(**overrides) -> ClarifyRequest:
    doc = {
        "request_id": "req-14",
        "description": "Spring barley yields across Leinster for 2026",
        "format": "csv",
        "budget_micros": 10_000_000,
        "spent_micros": 0,
        "message": "I need spring barley yield data for Leinster",
    }
    doc.update(overrides)
    return ClarifyRequest.model_validate(doc)


def loop_with_memory(
    gateway: FakeModelGateway,
    server: FakeMemoryProvider,
):
    """A loop whose memory transport reaches the in-process fake."""
    @asynccontextmanager
    async def connect(endpoint: str):
        async with mcp_client(server) as session:
            yield session

    return ClarificationLoop(gateway, memory_connect=connect)


def test_clarify_turn_with_no_memory_or_connectors_is_unchanged() -> None:
    """The pre-ticket-14 shape: no memory blocks in the prompt at all."""
    gateway = FakeModelGateway()
    loop = ClarificationLoop(gateway)
    resp = loop.turn(make_request())
    assert resp.request_id == "req-14"
    system, _ = gateway.calls[0]
    assert "Memories from earlier" not in system
    assert "Live data just queried" not in system


def test_recalled_memory_lands_in_the_prompt() -> None:
    """The demoable behavior: with a fake MCP memory server holding an
    earlier-session fact, the loop recalls it instead of re-asking — the
    memory appears in the prompt the model gateway receives."""
    server = FakeMemoryProvider()
    server.store_memory("consumer already confirmed: Leinster only, counties Carlow and Kilkenny")
    gateway = FakeModelGateway()
    loop = loop_with_memory(gateway, server)

    resp = loop.turn(make_request(memory_providers=[
        MemoryProvider(name="mem0", endpoint="http://fake/mcp"),
    ]))

    system, user = gateway.calls[0]
    assert "Carlow and Kilkenny" in system
    assert "do not re-ask" in system
    assert resp.reply  # the turn rode home normally


def test_turn_remembers_the_consumers_new_fact() -> None:
    """After the turn, the consumer's message is stored for next time."""
    server = FakeMemoryProvider()
    gateway = FakeModelGateway()
    loop = loop_with_memory(gateway, server)

    loop.turn(make_request(memory_providers=[
        MemoryProvider(name="mem0", endpoint="http://fake/mcp"),
    ]))

    assert len(server.recall_all()) == 1
    assert "I need spring barley yield data for Leinster" in server.recall_all()[0]


def test_down_provider_degrades_to_normal_turn() -> None:
    """The memory provider is unreachable: the turn is identical to a
    no-memory turn — no exception, no memory block, reply intact."""
    @asynccontextmanager
    async def dead_connect(endpoint: str):
        raise ConnectionError("down")
        yield  # pragma: no cover

    gateway = FakeModelGateway()
    loop = ClarificationLoop(gateway, memory_connect=dead_connect)
    resp = loop.turn(make_request(memory_providers=[
        MemoryProvider(name="mem0", endpoint="http://dead/mcp"),
    ]))

    system, _ = gateway.calls[0]
    assert "Memories from earlier" not in system
    assert resp.reply == gateway.text


def test_connector_findings_land_in_the_prompt() -> None:
    """A live Connector Module's answer rides into the prompt, tagged with
    the connector's name."""
    server = FakeConnectorServer(answer="Met Eireann: growth stage GS31 across Leinster")
    @asynccontextmanager
    async def open_mcp(endpoint: str):
        async with mcp_client(server) as session:
            yield session

    gateway = FakeModelGateway()
    loop = ClarificationLoop(gateway, connector_open=open_mcp)
    loop.turn(make_request(connectors=[
        ConnectorModule(name="met-eireann", endpoint="http://fake/mcp", query="query"),
    ]))

    system, _ = gateway.calls[0]
    assert "[met-eireann] Met Eireann: growth stage GS31 across Leinster" in system
    # The consumer's message was the query the connector received.
    assert server.queries == ["I need spring barley yield data for Leinster"]


def test_down_connector_degrades_to_normal_turn() -> None:
    @asynccontextmanager
    async def dead_open(endpoint: str):
        raise ConnectionError("down")
        yield  # pragma: no cover

    gateway = FakeModelGateway()
    loop = ClarificationLoop(gateway, connector_open=dead_open)
    resp = loop.turn(make_request(connectors=[
        ConnectorModule(name="dead", endpoint="http://dead/mcp"),
    ]))

    system, _ = gateway.calls[0]
    assert "Live data just queried" not in system
    assert resp.reply == gateway.text
