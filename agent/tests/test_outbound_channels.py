"""The outbound channel adapters (Seam 3, ticket 11): WhatsApp, Telegram,
and email, behind the Channel interface ticket 01 established. No test
touches a live WhatsApp/Telegram API — the transports are injected and the
tests script them (spec: cross-cutting testing decisions). Email asserts on
the log sink, the dev-mode medium (spec: Seam 3).
"""

import asyncio

from thresh_agent.channels import ChannelMessage
from thresh_agent.outbound import (
    EmailChannel,
    LogSink,
    ScriptedTransport,
    TelegramChannel,
    WhatsAppChannel,
    parse_address,
)


def test_whatsapp_delivers_through_its_transport() -> None:
    transport = ScriptedTransport(expected_status=200)
    channel = WhatsAppChannel(
        phone_number_id="pn-1", token="secret", transport=transport
    )

    ack = asyncio.run(
        channel.deliver(
            ChannelMessage(recipient="+353860000001", body="What crop did you plant?")
        )
    )

    assert ack.delivered is True
    assert ack.channel == "whatsapp"
    call = transport.calls[0]
    assert call.method == "POST"
    assert "pn-1" in call.url
    assert call.headers["Authorization"] == "Bearer secret"
    assert call.json["to"] == "+353860000001"
    assert call.json["type"] == "text"
    assert call.json["text"]["body"] == "What crop did you plant?"


def test_whatsapp_reports_transport_failure_as_undelivered() -> None:
    transport = ScriptedTransport(expected_status=500)
    channel = WhatsAppChannel(
        phone_number_id="pn-1", token="secret", transport=transport
    )

    ack = asyncio.run(
        channel.deliver(
            ChannelMessage(recipient="+353860000001", body="hello")
        )
    )

    # A failed delivery is surfaced, never silently swallowed: the caller
    # must be able to tell whether the medium has the reply.
    assert ack.delivered is False


def test_telegram_delivers_through_its_transport() -> None:
    transport = ScriptedTransport(expected_status=200)
    channel = TelegramChannel(token="tg-secret", transport=transport)

    ack = asyncio.run(
        channel.deliver(
            ChannelMessage(recipient="12345", body="Which fields are in scope?")
        )
    )

    assert ack.delivered is True
    assert ack.channel == "telegram"
    call = transport.calls[0]
    # Telegram's real API authenticates via the bot token in the URL path.
    assert call.url == "https://api.telegram.org/bottg-secret/sendMessage"
    assert call.json["chat_id"] == "12345"
    assert call.json["text"] == "Which fields are in scope?"


def test_telegram_reports_transport_failure_as_undelivered() -> None:
    channel = TelegramChannel(token="tg-secret", transport=ScriptedTransport(503))

    ack = asyncio.run(channel.deliver(ChannelMessage(recipient="12345", body="hi")))

    assert ack.delivered is False


def test_email_delivers_to_the_log_sink() -> None:
    """Email's dev medium is the log sink (spec: Seam 3 — 'Email asserts on
    the dev log sink')."""
    sink = LogSink()
    channel = EmailChannel(sink=sink)

    ack = asyncio.run(
        channel.deliver(
            ChannelMessage(
                recipient="member@farm.ie", body="Reply YES to continue the survey."
            )
        )
    )

    assert ack.delivered is True
    assert ack.channel == "email"
    assert sink.sent == [
        {"to": "member@farm.ie", "subject": "A question from Thresh", "body": "Reply YES to continue the survey."}
    ]


def test_email_reports_sink_failure_as_undelivered() -> None:
    class FailingSink:
        def send(self, to: str, subject: str, body: str) -> bool:
            return False

    channel = EmailChannel(sink=FailingSink())

    ack = asyncio.run(
        channel.deliver(ChannelMessage(recipient="member@farm.ie", body="hi"))
    )

    assert ack.delivered is False


def test_channel_address_parses_channel_and_address() -> None:
    """Roster contact points carry channel-qualified addresses
    ('whatsapp:+353860000001'); the agent resolves them to an adapter."""
    assert parse_address("whatsapp:+353860000001") == ("whatsapp", "+353860000001")
    assert parse_address("telegram:12345") == ("telegram", "12345")
    assert parse_address("email:member@farm.ie") == ("email", "member@farm.ie")


def test_channel_address_rejects_unknown_channel() -> None:
    import pytest

    with pytest.raises(ValueError):
        parse_address("smoke:signals")
