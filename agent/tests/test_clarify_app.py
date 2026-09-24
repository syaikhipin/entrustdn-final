"""The sidecar answers request.clarify.request (ticket 07, Seam 2 agent
side): driven through the FakeBackend harness exactly as the Go side's
fakeAgent does for ping. The model is faked at the gateway — no test
touches a live model — so what's pinned here is the wire behavior: the
envelope round trip, the echo of the request ID, and the metered usage.
"""

import json
from pathlib import Path

import pytest

from thresh_agent.contract import CONTRACT_VERSION

from fake_backend import FIXTURES, FakeBackend


def fixture_payload(name: str) -> dict:
    return json.loads((FIXTURES / name).read_text())["payload"]


def make_backend(**gateway_kwargs) -> FakeBackend:
    return FakeBackend(gateway_kwargs=gateway_kwargs)


def test_harness_sends_fixture_shaped_clarify_envelopes() -> None:
    """The harness maps the new pair onto the golden fixtures."""
    doc = FakeBackend.envelope("request.clarify.request", {"request_id": "r"})
    assert doc["version"] == CONTRACT_VERSION
    assert doc["type"] == "request.clarify.request"
    assert doc["payload"]["request_id"] == "r"


def test_message_endpoint_answers_clarify_request() -> None:
    backend = make_backend(
        text="Which counties exactly?",
        model="thresh-fake-model",
        input_tokens=120,
        cached_input_tokens=40,
        output_tokens=30,
    )
    payload = fixture_payload("request-clarify-request.json")

    env = backend.send("request.clarify.request", payload)

    assert env["type"] == "request.clarify.response"
    resp = env["payload"]
    assert resp["request_id"] == "req-clarify-01"
    assert resp["reply"] == "Which counties exactly?"
    assert resp["clarified"] is False
    # Catalog first: the fixture's asset shares "yields/barley/leinster"
    # vocabulary with the fixture's need.
    assert [m["asset_id"] for m in resp["matches"]] == ["asset-01"]
    assert resp["usage"] == {
        "model": "thresh-fake-model",
        "input_tokens": 120,
        "cached_input_tokens": 40,
        "output_tokens": 30,
    }


def test_message_endpoint_reports_clarified_replies() -> None:
    backend = make_backend(text="CLARIFIED: fully specified")

    env = backend.send(
        "request.clarify.request", fixture_payload("request-clarify-request.json")
    )

    resp = env["payload"]
    assert resp["clarified"] is True
    assert resp["reply"] == "fully specified"


def test_message_endpoint_rejects_malformed_clarify_payload() -> None:
    backend = make_backend()
    payload = fixture_payload("request-clarify-request.json")
    payload["budget_micros"] = "ten million"  # wrong type

    resp = backend.send_raw(
        FakeBackend.envelope("request.clarify.request", payload)
    )

    assert resp.status_code == 422


def test_clarify_without_a_configured_key_fails_loudly() -> None:
    """A keyless deployment must not bill for canned completions: the
    clarify endpoint refuses with 503 and says why, rather than silently
    answering from the fake gateway (the fake stays available for tests
    through the explicit gateway_kwargs escape hatch)."""
    import os

    from fastapi.testclient import TestClient

    from thresh_agent.app import create_app

    saved = {
        k: os.environ.pop(k, None)
        for k in ("THRESH_MODEL_GATEWAY_KEY", "OPENAI_API_KEY")
    }
    try:
        client = TestClient(create_app("no-key-1"))
        payload = fixture_payload("request-clarify-request.json")

        resp = client.post(
            "/message",
            json=FakeBackend.envelope("request.clarify.request", payload),
        )

        assert resp.status_code == 503
        assert "THRESH_MODEL_GATEWAY_KEY" in resp.json()["detail"]
    finally:
        for k, v in saved.items():
            if v is not None:
                os.environ[k] = v


def test_clarify_explicit_fake_gateway_env_switch() -> None:
    """THRESH_MODEL_GATEWAY=fake opts a deployment into canned completions
    on purpose (dev/demo without a bill) — the opt-in is explicit, never a
    silent fallback."""
    import os

    from fastapi.testclient import TestClient

    from thresh_agent.app import create_app

    saved = {k: os.environ.get(k) for k in ("THRESH_MODEL_GATEWAY",)}
    os.environ["THRESH_MODEL_GATEWAY"] = "fake"
    try:
        client = TestClient(create_app("fake-1"))
        payload = fixture_payload("request-clarify-request.json")

        resp = client.post(
            "/message",
            json=FakeBackend.envelope("request.clarify.request", payload),
        )

        assert resp.status_code == 200
        assert resp.json()["type"] == "request.clarify.response"
        assert client.get("/health").json()["clarify"] == "configured"
    finally:
        for k, v in saved.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v
