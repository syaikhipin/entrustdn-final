"""The sidecar answers conversation.start / conversation.reply (ticket 11,
Seam 2 agent side): driven through the FakeBackend harness exactly like the
ping and clarify pairs. The Member conversation runs over the Channel the
contact point names — a fake channel in tests; delivery is checked before
the response rides home. The restart test kills the app object entirely and
rebuilds it: the reborn app has no checkpoint, so it replays the durable
thread the backend re-sends (the backend owns the record; the Postgres
checkpointer is the production path, proven by the integration suite).
"""

import base64
import json
from pathlib import Path

import pytest
from pydantic import ValidationError

from thresh_agent.contract import CONTRACT_VERSION
from thresh_agent.channels import FakeChannel

from fake_backend import FIXTURES, FakeBackend


def fixture_payload(name: str) -> dict:
    return json.loads((FIXTURES / name).read_text())["payload"]


@pytest.fixture()
def channels():
    return {
        "whatsapp": FakeChannel("whatsapp"),
        "telegram": FakeChannel("telegram"),
        "email": FakeChannel("email"),
    }


@pytest.fixture()
def backend(channels, monkeypatch):
    """A sidecar wired to fake channels and an in-memory checkpointer."""
    monkeypatch.delenv("THRESH_CHECKPOINT_URL", raising=False)
    b = FakeBackend.with_channels(channels)
    yield b
    b.close()


QUESTIONS = [
    "What crop did you plant this season?",
    "How many hectares are under it?",
    "Was the yield better than last year?",
]
CONTACT = "whatsapp:+353860000001"


def start_payload(**overrides) -> dict:
    payload = fixture_payload("conversation-start-request.json")
    payload.update(overrides)
    return payload


def reply_payload(message: str, thread: list, **overrides) -> dict:
    payload = {
        "conversation_id": "conv-01",
        "message": message,
        "contact": CONTACT,
        "questions": QUESTIONS,
        "thread": thread,
        "resume_url": "https://thresh.dev/member/resume/tok-abc123",
    }
    payload.update(overrides)
    return payload


def test_sidecar_answers_conversation_start(backend, channels) -> None:
    env = backend.send("conversation.start.request", start_payload())

    assert env["type"] == "conversation.response"
    resp = env["payload"]
    assert resp["conversation_id"] == "conv-01"
    assert resp["delivered"] is True
    assert resp["status"] == "awaiting_member"
    # The opening question went out over the Member's channel.
    assert channels["whatsapp"].sent, "agent never delivered on WhatsApp"
    assert "crop" in channels["whatsapp"].sent[0].body.lower()


def test_sidecar_refuses_undelivered_channel(monkeypatch) -> None:
    """The medium refused the message: the turn fails loudly (HTTP 502),
    never a delivered=true reply."""

    class DeadChannel(FakeChannel):
        async def deliver(self, message):
            from thresh_agent.channels import DeliveryAck

            return DeliveryAck(delivered=False, channel=self.name)

    monkeypatch.delenv("THRESH_CHECKPOINT_URL", raising=False)
    backend = FakeBackend.with_channels({"whatsapp": DeadChannel("whatsapp")})
    try:
        resp = backend.send_raw(
            FakeBackend.envelope(
                "conversation.start.request", start_payload()
            )
        )
        assert resp.status_code == 502
    finally:
        backend.close()


def test_member_reply_resumes_from_checkpoint(backend, channels) -> None:
    backend.send("conversation.start.request", start_payload())

    env = backend.send(
        "conversation.reply.request",
        reply_payload(
            "Spring barley, twelve hectares",
            [{"role": "agent", "body": channels["whatsapp"].sent[0].body}],
        ),
    )

    resp = env["payload"]
    assert resp["delivered"] is True
    assert resp["status"] == "awaiting_member"
    assert resp["answers"] == ["Spring barley, twelve hectares"]
    # The second question followed, mid-thread.
    assert "hectares" in channels["whatsapp"].sent[-1].body.lower()


def test_follow_up_question_re_offers_the_resume_link(backend, channels) -> None:
    """'Members receive resumable links after partial answers': every
    follow-up delivery re-offers the link, not just the opening message —
    a Member who lost the first message can still find their way back."""
    url = "https://thresh.dev/member/resume/tok-abc123"
    backend.send("conversation.start.request", start_payload(resume_url=url))

    env = backend.send(
        "conversation.reply.request",
        reply_payload(
            "Spring barley",
            [{"role": "agent", "body": channels["whatsapp"].sent[0].body}],
            resume_url=url,
        ),
    )

    resp = env["payload"]
    assert resp["status"] == "awaiting_member"
    last = channels["whatsapp"].sent[-1].body
    assert "hectares" in last.lower()
    assert url in last, "the follow-up never re-offered the resumable link"


def test_conversation_survives_agent_process_restart(channels) -> None:
    """ADR 0001's demo: chat, kill the agent process, restart, reply — the
    conversation resumes mid-thread. 'Kill and restart' here is dropping
    the whole app object (its in-memory checkpointer with it) and building
    a new one: the reborn app cold-starts by replaying the durable thread
    the backend re-sends on every reply. The checkpointed variant — the
    same restart with a Postgres checkpointer surviving — is the
    integration suite's whole job."""
    monkeypatch = pytest.MonkeyPatch()
    monkeypatch.delenv("THRESH_CHECKPOINT_URL", raising=False)
    try:
        first = FakeBackend.with_channels(channels)
        first.send("conversation.start.request", start_payload())
        first.send(
            "conversation.reply.request",
            reply_payload(
                "Spring barley",
                [{"role": "agent", "body": channels["whatsapp"].sent[0].body}],
            ),
        )
        first.close()  # ---- the process dies ----

        reborn = FakeBackend.with_channels(channels)
        env = reborn.send(
            "conversation.reply.request",
            reply_payload(
                "Twelve hectares",
                [
                    {"role": "agent", "body": channels["whatsapp"].sent[0].body},
                    {"role": "member", "body": "Spring barley"},
                    {"role": "agent", "body": channels["whatsapp"].sent[-1].body},
                ],
            ),
        )
        resp = env["payload"]
        # The conversation resumed mid-thread after the restart: both
        # answers are on record and the third question followed.
        assert resp["status"] == "awaiting_member"
        assert resp["answers"] == ["Spring barley", "Twelve hectares"]
        assert "yield" in channels["whatsapp"].sent[-1].body.lower()
    finally:
        monkeypatch.undo()


def test_member_stop_word_ends_the_conversation(backend, channels) -> None:
    backend.send("conversation.start.request", start_payload())
    env = backend.send(
        "conversation.reply.request",
        reply_payload(
            "STOP",
            [{"role": "agent", "body": channels["whatsapp"].sent[0].body}],
        ),
    )
    resp = env["payload"]
    assert resp["status"] == "stopped"
    # Nothing more was asked: the WhatsApp outbox ends with the opener.
    assert len(channels["whatsapp"].sent) == 1


def test_voice_note_transcribed_then_answered(channels, monkeypatch) -> None:
    """A voice-note reply rides the contract as base64 audio; the agent
    transcribes it through its injected STT client (env-keyed in
    production, the fake here) and the transcript becomes the reply text."""
    from thresh_agent.stt import FakeSTT

    stt = FakeSTT(text="Spring barley on twelve hectares")
    monkeypatch.delenv("THRESH_CHECKPOINT_URL", raising=False)
    b = FakeBackend.with_channels(channels, stt=stt)
    try:
        b.send("conversation.start.request", start_payload())
        payload = reply_payload(
            "",
            [{"role": "agent", "body": channels["whatsapp"].sent[0].body}],
        )
        del payload["message"]
        payload["audio"] = {
            "data": base64.b64encode(b"ogg-bytes").decode(),
            "format": "ogg",
        }
        env = b.send("conversation.reply.request", payload)

        resp = env["payload"]
        # The transcript, not the audio, entered the conversation: the
        # answer on record and the next question are the transcript's work.
        assert resp["answers"] == ["Spring barley on twelve hectares"]
        assert "hectares" in channels["whatsapp"].sent[-1].body.lower()
        # The agent's STT client saw exactly the decoded audio.
        assert stt.audio == [b"ogg-bytes"]
    finally:
        b.close()


def test_voice_note_without_stt_configured_refuses_loudly(channels, monkeypatch) -> None:
    """No STT key: a voice note is a loud 503, never a silent fake
    transcription of real audio (the same rule clarify turns follow)."""
    monkeypatch.delenv("THRESH_CHECKPOINT_URL", raising=False)
    b = FakeBackend.with_channels(channels, stt=None)
    try:
        payload = reply_payload("", [])
        del payload["message"]
        payload["audio"] = {"data": base64.b64encode(b"x").decode(), "format": "ogg"}
        resp = b.send_raw(FakeBackend.envelope("conversation.reply.request", payload))
        assert resp.status_code == 503
        assert "STT_KEY" in resp.json()["detail"]
    finally:
        b.close()


def test_reply_refuses_message_and_audio_together() -> None:
    """The exactly-one-of rule: message and audio are mutually exclusive,
    and a reply carrying neither is rejected."""
    import pytest as _pytest
    from thresh_agent.contract import ConversationReplyRequest

    base = dict(conversation_id="c", contact=CONTACT, questions=["q"])
    with _pytest.raises(ValidationError):
        ConversationReplyRequest(
            message="hi", audio={"data": "aGk=", "format": "ogg"}, **base
        )
    with _pytest.raises(ValidationError):
        ConversationReplyRequest(message="", **base)  # neither


def test_production_channels_build_from_env(monkeypatch) -> None:
    """All states are env-configured (ADR 0007), channel credentials
    included: with WhatsApp and Telegram keys set, production boots those
    adapters alongside email; with none, email (the dev log sink) alone."""
    from fastapi.testclient import TestClient

    from thresh_agent.app import create_app

    monkeypatch.delenv("THRESH_CHECKPOINT_URL", raising=False)
    monkeypatch.setenv("THRESH_WHATSAPP_PHONE_NUMBER_ID", "pn-42")
    monkeypatch.setenv("THRESH_WHATSAPP_TOKEN", "wa-secret")
    monkeypatch.setenv("THRESH_TELEGRAM_TOKEN", "tg-secret")
    app = create_app(gateway_kwargs={})
    health = TestClient(app).get("/health").json()
    assert health["channels"] == ["email", "telegram", "whatsapp"]

    monkeypatch.delenv("THRESH_WHATSAPP_PHONE_NUMBER_ID")
    monkeypatch.delenv("THRESH_WHATSAPP_TOKEN")
    monkeypatch.delenv("THRESH_TELEGRAM_TOKEN")
    app = create_app(gateway_kwargs={})
    health = TestClient(app).get("/health").json()
    assert health["channels"] == ["email"]
