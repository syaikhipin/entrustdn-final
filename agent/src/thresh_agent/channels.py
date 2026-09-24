"""Channel adapters (Seam 3, agent-side).

A Channel is a messaging medium between the agent and a person:
WhatsApp, Telegram, email, web, or voice (CONTEXT.md) — a Farmer Member on
the async channels, a Data Consumer on the web chat (ticket 07). Every
real channel implements one interface; tests drive fakes. The FakeChannel
here is the harness later tickets inherit — it records what the agent
sends and replays what the other side answers.
"""

from dataclasses import dataclass


@dataclass
class ChannelMessage:
    """One message on a Channel."""

    recipient: str
    body: str


@dataclass
class DeliveryAck:
    """Acknowledgement that a message left the platform."""

    delivered: bool
    channel: str


class Channel:
    """The adapter interface every channel — real or fake — implements.

    Real adapters translate deliver/receive into WhatsApp/Telegram/email API
    calls. Fakes record and replay. Tests assert on fakes only; no test
    touches live WhatsApp/Telegram/email APIs (spec: Testing Decisions).
    """

    def __init__(self, name: str) -> None:
        self.name = name

    async def deliver(self, message: ChannelMessage) -> DeliveryAck:
        raise NotImplementedError

    async def receive(self, message: ChannelMessage) -> ChannelMessage | None:
        raise NotImplementedError


class FakeChannel(Channel):
    """In-memory channel for tests: records outbound, replays inbound."""

    def __init__(self, name: str) -> None:
        super().__init__(name)
        self.sent: list[ChannelMessage] = []

    async def deliver(self, message: ChannelMessage) -> DeliveryAck:
        self.sent.append(message)
        return DeliveryAck(delivered=True, channel=self.name)

    async def receive(self, message: ChannelMessage) -> ChannelMessage | None:
        # A Farmer Member's reply surfaces exactly as sent.
        return message


class WebChatChannel(Channel):
    """The platform's own chat surface (ticket 07): a Channel between the
    Agent and a Data Consumer, where the medium is the request's web-chat
    thread and delivery is synchronous — the reply is staged here while the
    HTTP turn waits, then picked up exactly once to ride the contract
    response. Later asynchronous channels (WhatsApp, Telegram) implement the
    same interface with real delivery."""

    def __init__(self) -> None:
        super().__init__("web")
        self._reply: ChannelMessage | None = None

    async def deliver(self, message: ChannelMessage) -> DeliveryAck:
        """Stage the reply for the waiting HTTP turn."""
        self._reply = message
        return DeliveryAck(delivered=True, channel=self.name)

    async def receive(self, message: ChannelMessage) -> ChannelMessage | None:
        # The web medium is synchronous: the consumer's message surfaces
        # exactly as sent.
        return message

    def take_reply(self) -> ChannelMessage | None:
        """Hand the staged reply to the waiting HTTP turn, once."""
        reply, self._reply = self._reply, None
        return reply
