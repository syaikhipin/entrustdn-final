"""Contract-fake backend harness (Seam 2, agent side).

The spec: "contract-fake backend tests the sidecar's [side]". This module is
the reusable counterpart to the Go side's fakeAgent: it drives the real
sidecar app over HTTP with fixture-shaped contract messages and asserts on
the envelopes that come back. Later tickets inherit it for
request.lifecycle, channel.reply, and the pause/resume path.
"""

from pathlib import Path
from typing import Any

from fastapi.testclient import TestClient

from thresh_agent.app import create_app

FIXTURES = Path(__file__).resolve().parents[2] / "contract" / "fixtures"


class FakeBackend:
    """Drives the real sidecar the way the backend will: typed contract
    messages built from the golden fixtures."""

    def __init__(
        self,
        agent_version: str = "fake-tested-1",
        gateway_kwargs: dict | None = None,
    ) -> None:
        self.client = TestClient(
            create_app(agent_version, gateway_kwargs=gateway_kwargs)
        )

    def send(self, message_type: str, payload: dict[str, Any]) -> Any:
        doc = self.envelope(message_type, payload)
        resp = self.client.post("/message", json=doc)
        assert resp.status_code == 200, resp.text
        return resp.json()

    def send_raw(self, doc: dict[str, Any]) -> Any:
        """Send a hand-built envelope; returns the raw response."""
        return self.client.post("/message", json=doc)

    @staticmethod
    def envelope(message_type: str, payload: dict[str, Any]) -> dict[str, Any]:
        base = {
            "ping.request": "ping-request.json",
            "ping.response": "ping-response.json",
            "request.clarify.request": "request-clarify-request.json",
            "request.clarify.response": "request-clarify-response.json",
        }[message_type]
        doc = __import__("json").loads((FIXTURES / base).read_text())
        doc["payload"] = payload
        return doc

    def ping(self, nonce: str) -> dict[str, Any]:
        env = self.send("ping.request", {"nonce": nonce})
        assert env["type"] == "ping.response"
        return env["payload"]
