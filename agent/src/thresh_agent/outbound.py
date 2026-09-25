"""The real outbound channel adapters (Seam 3, ticket 11).

WhatsApp and Telegram deliver through an injected HTTP transport; email
delivers to a sink (dev: the log sink — spec: Seam 3, "Email asserts on the
dev log sink"). No test touches a live API: tests script the transport, the
same seam the model gateway (ticket 07) established on the inference side.

Channel addresses are channel-qualified strings —
"whatsapp:+353860000001", "telegram:12345", "email:member@farm.ie" — the
Member roster's contact-point format (ticket 11). `parse_address` resolves
one to its channel and bare address, which selects the adapter.
"""

from dataclasses import dataclass

import httpx

from .channels import Channel, ChannelMessage, DeliveryAck


@dataclass
class RecordedCall:
    """One HTTP call a scripted transport saw (test observation)."""

    method: str
    url: str
    headers: dict
    json: dict


class ScriptedTransport:
    """The test double for the HTTP layer: records calls, answers a fixed
    status. Real deployments get httpx's own transport."""

    def __init__(self, expected_status: int = 200) -> None:
        self.expected_status = expected_status
        self.calls: list[RecordedCall] = []

    def post(self, url: str, headers: dict, json: dict) -> int:
        self.calls.append(
            RecordedCall(method="POST", url=url, headers=headers, json=json)
        )
        return self.expected_status


class WhatsAppChannel(Channel):
    """Delivers a text message through the WhatsApp Cloud API."""

    def __init__(
        self,
        phone_number_id: str,
        token: str,
        transport: ScriptedTransport | httpx.BaseTransport | None = None,
    ) -> None:
        super().__init__("whatsapp")
        self.phone_number_id = phone_number_id
        self.token = token
        self._transport = transport

    async def deliver(self, message: ChannelMessage) -> DeliveryAck:
        url = (
            "https://graph.facebook.com/v20.0/"
            f"{self.phone_number_id}/messages"
        )
        payload = {
            "messaging_product": "whatsapp",
            "to": message.recipient,
            "type": "text",
            "text": {"body": message.body},
        }
        status = self._post(url, payload)
        return DeliveryAck(delivered=status == 200, channel=self.name)

    def _post(self, url: str, payload: dict) -> int:
        if isinstance(self._transport, ScriptedTransport):
            return self._transport.post(
                url, {"Authorization": f"Bearer {self.token}"}, payload
            )
        client = httpx.Client(
            base_url="",
            headers={"Authorization": f"Bearer {self.token}"},
            transport=self._transport,
        )
        try:
            resp = client.post(url, json=payload)
            return resp.status_code
        finally:
            client.close()


class TelegramChannel(Channel):
    """Delivers a text message through the Telegram Bot API (token in the
    URL path, the API's own convention)."""

    def __init__(
        self,
        token: str,
        transport: ScriptedTransport | httpx.BaseTransport | None = None,
    ) -> None:
        super().__init__("telegram")
        self.token = token
        self._transport = transport

    async def deliver(self, message: ChannelMessage) -> DeliveryAck:
        url = f"https://api.telegram.org/bot{self.token}/sendMessage"
        payload = {"chat_id": message.recipient, "text": message.body}
        status = self._post(url, payload)
        return DeliveryAck(delivered=status == 200, channel=self.name)

    def _post(self, url: str, payload: dict) -> int:
        if isinstance(self._transport, ScriptedTransport):
            return self._transport.post(
                url, {"Authorization": f"Bearer {self.token}"}, payload
            )
        client = httpx.Client(
            headers={"Authorization": f"Bearer {self.token}"},
            transport=self._transport,
        )
        try:
            resp = client.post(url, json=payload)
            return resp.status_code
        finally:
            client.close()


class LogSink:
    """The dev email sink: records what would have been mailed, and logs
    it — dev operators read the agent log to see what Members received."""

    def __init__(self, logger=None) -> None:
        import logging

        self._log = logger or logging.getLogger("thresh.email")
        self.sent: list[dict] = []

    def send(self, to: str, subject: str, body: str) -> bool:
        self.sent.append({"to": to, "subject": subject, "body": body})
        self._log.info(
            "[email] to=%s subject=%r body=%r", to, subject, body
        )
        return True


class EmailChannel(Channel):
    """Delivers a message as email through a sink (dev: the log)."""

    def __init__(self, sink: LogSink) -> None:
        super().__init__("email")
        self._sink = sink

    async def deliver(self, message: ChannelMessage) -> DeliveryAck:
        sent = self._sink.send(
            to=message.recipient,
            subject="A question from Thresh",
            body=message.body,
        )
        return DeliveryAck(delivered=sent, channel=self.name)


def parse_address(contact: str) -> tuple[str, str]:
    """Split a channel-qualified contact point into (channel, address)."""
    channel, sep, address = contact.partition(":")
    if not sep or not channel or not address:
        raise ValueError(
            f"contact point {contact!r} must be 'channel:address' "
            "(e.g. 'whatsapp:+353860000001')"
        )
    if channel not in ("whatsapp", "telegram", "email", "web"):
        raise ValueError(f"contact point {contact!r} names unknown channel {channel!r}")
    return channel, address
