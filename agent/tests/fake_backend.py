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
        channels: dict | None = None,
        stt=None,
    ) -> None:
        from thresh_agent.channels import FakeChannel

        self.channels = channels or {
            name: FakeChannel(name) for name in ("whatsapp", "telegram", "email")
        }
        self.client = TestClient(
            create_app(
                agent_version,
                gateway_kwargs=gateway_kwargs,
                channels=self.channels,
                stt=stt,
            )
        )

    def close(self) -> None:
        """Drop the app — the 'process restart' boundary in tests. The
        in-memory checkpointer inside the app dies here; only a shared
        checkpointer (the Postgres saver, or a test-injected saver)
        survives."""
        self.client = None

    @classmethod
    def with_channels(cls, channels: dict, **kwargs) -> "FakeBackend":
        return cls(channels=channels, **kwargs)

    def send(self, message_type: str, payload: dict[str, Any]) -> Any:
        doc = self.envelope(message_type, payload)
        resp = self.client.post("/message", json=doc)
        assert resp.status_code == 200, resp.text
        return resp.json()

    def send_raw(self, doc: dict[str, Any]):
        """Send a hand-built envelope; returns the raw response."""
        return self.client.post("/message", json=doc)

    @staticmethod
    def envelope(message_type: str, payload: dict[str, Any]) -> dict[str, Any]:
        fixtures = {
            "ping.request": "ping-request.json",
            "ping.response": "ping-response.json",
            "request.clarify.request": "request-clarify-request.json",
            "request.clarify.response": "request-clarify-response.json",
            "conversation.start.request": "conversation-start-request.json",
            "conversation.reply.request": "conversation-reply-request.json",
            "conversation.response": "conversation-reply-response.json",
        }
        doc = __import__("json").loads((FIXTURES / fixtures[message_type]).read_text())
        doc["payload"] = payload
        return doc

    def ping(self, nonce: str) -> dict[str, Any]:
        env = self.send("ping.request", {"nonce": nonce})
        assert env["type"] == "ping.response"
        return env["payload"]
