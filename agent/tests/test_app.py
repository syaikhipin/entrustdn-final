"""Seam 2, agent side: the sidecar's HTTP surface, driven through the
contract-fake backend harness (tests/fake_backend.py) — the counterpart of
the Go side's fakeAgent tests, asserting what the sidecar answers for
fixture-shaped contract messages.
"""

from thresh_agent.contract import CONTRACT_VERSION

from fake_backend import FakeBackend


def test_message_endpoint_answers_ping_with_pong() -> None:
    backend = FakeBackend(agent_version="test-agent-1")

    payload = backend.ping("nonce-abc")

    assert payload["nonce"] == "nonce-abc"
    assert payload["pong"] is True
    assert payload["agent_version"] == "test-agent-1"


def test_fake_backend_sends_fixture_shaped_envelopes() -> None:
    """The harness itself stays honest: what it sends matches the golden
    ping-request fixture modulo the nonce."""
    doc = FakeBackend.envelope("ping.request", {"nonce": "x"})
    assert doc["version"] == CONTRACT_VERSION
    assert doc["type"] == "ping.request"
    assert set(doc["payload"].keys()) == {"nonce"}


def test_message_endpoint_rejects_wrong_version() -> None:
    backend = FakeBackend(agent_version="test-agent-1")

    body = FakeBackend.envelope("ping.request", {"nonce": "n"})
    body["version"] = 2
    resp = backend.send_raw(body)

    assert resp.status_code == 422


def test_message_endpoint_rejects_unknown_type() -> None:
    backend = FakeBackend(agent_version="test-agent-1")

    body = FakeBackend.envelope("ping.request", {"nonce": "n"})
    body["type"] = "request.lifecycle"
    resp = backend.send_raw(body)

    assert resp.status_code in (400, 422)


def test_health_endpoint_reports_service_identity() -> None:
    backend = FakeBackend(agent_version="test-agent-1")

    doc = backend.client.get("/health").json()

    assert doc["service"] == "thresh-agent"
    assert doc["version"] == "test-agent-1"


def test_graph_pings_directly() -> None:
    """The LangGraph graph itself answers a ping — the tracer-bullet node."""
    from thresh_agent.graph import answer_ping

    result = answer_ping({"nonce": "graph-nonce"}, agent_version="0.1.0")
    assert result["nonce"] == "graph-nonce"
    assert result["pong"] is True
    assert result["agent_version"] == "0.1.0"
