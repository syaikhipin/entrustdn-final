"""Seam 3: the Channel adapter interface (agent-side).

WhatsApp/Telegram/email/web adapters all implement one interface; tests drive
fake channels. This file establishes the harness later tickets inherit: a
FakeChannel that records outbound sends and replays inbound replies, plus a
smoke test proving the agent can converse through it.
"""

import asyncio

from thresh_agent.channels import Channel, ChannelMessage, FakeChannel


def test_fake_channel_is_a_channel() -> None:
    """The fake satisfies the adapter interface every real channel implements."""
    fake = FakeChannel(name="whatsapp")
    assert isinstance(fake, Channel)
    assert fake.name == "whatsapp"


def test_fake_channel_records_outbound_messages() -> None:
    fake = FakeChannel(name="telegram")

    asyncio.run(
        fake.deliver(
            ChannelMessage(recipient="+353860000001", body="What crop did you plant?")
        )
    )

    assert len(fake.sent) == 1
    assert fake.sent[0].recipient == "+353860000001"
    assert fake.sent[0].body == "What crop did you plant?"


def test_fake_channel_surfaces_farmer_reply() -> None:
    fake = FakeChannel(name="whatsapp")

    reply = asyncio.run(
        fake.receive(
            ChannelMessage(recipient="+353860000001", body="Spring barley, 12 ha")
        )
    )

    assert reply is not None
    assert reply.body == "Spring barley, 12 ha"


def test_agent_says_hello_through_fake_channel() -> None:
    """Smoke: the agent's conversation loop talks through a fake channel."""
    from thresh_agent.converse import open_conversation

    fake = FakeChannel(name="whatsapp")
    ack = open_conversation(
        fake,
        ChannelMessage(
            recipient="+353860000002",
            body="Hello! I'm collecting data about this season's crops. Reply anytime.",
        ),
    )

    assert ack.delivered is True
    assert fake.sent[-1].body.startswith("Hello!")
