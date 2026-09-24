"""The web-chat Channel adapter (ticket 07, Seam 3).

The clarification conversation between the Agent and a Data Consumer runs
over the platform's own chat surface — the "web" Channel. The adapter's
job per turn: the consumer's message has arrived on the channel, the loop
answers, and the reply is *delivered over the channel* before it rides the
contract response home. Tests drive the adapter through the Channel
interface with the FakeChannel double, exactly as later tickets' WhatsApp/
Telegram adapters will be tested.
"""

import asyncio

import pytest

from thresh_agent.channels import Channel, ChannelMessage, DeliveryAck, FakeChannel, WebChatChannel
from thresh_agent.clarify import ClarificationLoop, run_web_chat_turn
from thresh_agent.gateway import FakeModelGateway

from test_clarify import clarify_request


def test_web_chat_turn_delivers_the_reply_over_the_channel() -> None:
    """One clarification turn over the web channel: the loop's reply is
    delivered to the request's thread, and the same body rides the
    contract response — the channel is the medium, the contract is the
    transport."""
    channel = FakeChannel("web")
    loop = ClarificationLoop(FakeModelGateway(text="Which counties?"))
    req = clarify_request()

    resp = run_web_chat_turn(loop, channel, req)

    assert channel.sent == [
        ChannelMessage(recipient="req-1", body="Which counties?")
    ]
    assert resp.reply == "Which counties?"


def test_web_chat_channel_stages_the_reply_for_the_http_response() -> None:
    """The web medium is synchronous: deliver() stages the reply, the
    waiting HTTP turn picks it up exactly once."""
    channel = WebChatChannel()

    ack = asyncio.run(
        channel.deliver(ChannelMessage(recipient="req-1", body="Which counties?"))
    )

    assert ack == DeliveryAck(delivered=True, channel="web")
    assert channel.take_reply() == ChannelMessage(recipient="req-1", body="Which counties?")
    assert channel.take_reply() is None  # staged once, picked up once

    # The consumer's inbound message surfaces exactly as sent.
    inbound = ChannelMessage(recipient="agent", body="I need yield data")
    assert asyncio.run(channel.receive(inbound)) == inbound


def test_failed_delivery_fails_the_turn() -> None:
    """A channel that refuses delivery fails the turn loudly — the reply
    must never be reported as sent when it was not."""

    class RefusingChannel(Channel):
        async def deliver(self, message: ChannelMessage) -> DeliveryAck:
            return DeliveryAck(delivered=False, channel="web")

        async def receive(self, message: ChannelMessage) -> ChannelMessage | None:
            return message

    loop = ClarificationLoop(FakeModelGateway())

    with pytest.raises(RuntimeError, match="web channel did not deliver"):
        run_web_chat_turn(loop, RefusingChannel("web"), clarify_request())


def test_clarify_endpoint_drives_the_web_channel() -> None:
    """The sidecar's clarify handler routes the turn through a fresh
    web-chat Channel per message (Seam 3 wired into the Seam 2 surface)."""
    import json
    from pathlib import Path

    from fastapi.testclient import TestClient

    from thresh_agent.app import create_app

    fixtures = Path(__file__).resolve().parents[2] / "contract" / "fixtures"
    payload = json.loads((fixtures / "request-clarify-request.json").read_text())["payload"]

    client = TestClient(create_app("web-channel-1", gateway_kwargs={"text": "Which counties?"}))
    doc = json.loads((fixtures / "request-clarify-request.json").read_text())

    resp = client.post("/message", json=doc)

    assert resp.status_code == 200
    assert resp.json()["payload"]["reply"] == "Which counties?"
