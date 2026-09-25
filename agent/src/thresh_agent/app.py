"""The agent sidecar's HTTP surface (Seam 2, agent side).

One endpoint — POST /message — that accepts a contract envelope and answers
typed messages. The tracer bullet answers ping.request via the LangGraph
node; ticket 07 adds request.clarify.request, answered by the clarification
loop over an injectable model gateway (faked in tests, OpenAI-compatible in
production). Ticket 11 adds conversation.start / conversation.reply: the
Member conversations over Channels, paced by the checkpointed member graph
(pause-for-days resume, ADR 0001), delivered through the channel adapters
the contact point names.
"""

from datetime import datetime, timezone
import os

from fastapi import FastAPI, HTTPException
from pydantic import ValidationError

from .clarify import ClarificationLoop, run_web_chat_turn
from .channels import DeliveryAck, WebChatChannel
from .contract import (
    CONTRACT_VERSION,
    ClarifyRequest,
    ClarifyResponse,
    ConversationReplyRequest,
    ConversationResponse,
    ConversationStartRequest,
    Envelope,
    PingRequest,
    ThreadMessage,
    make_clarify_response_envelope,
    make_conversation_response_envelope,
    make_ping_response_envelope,
)
from .gateway import FakeModelGateway, gateway_from_env
from .graph import build_ping_graph
from .member_graph import MemberConversation, checkpointer_from_env, setup_checkpointer
from .outbound import EmailChannel, LogSink, TelegramChannel, WhatsAppChannel, parse_address
from .stt import FakeSTT, stt_from_env

AGENT_VERSION = "0.1.0"

# What a keyless deployment answers clarify turns with: a loud refusal, not
# a canned completion billed as real usage (every turn is metered and
# charged by the backend).
CLARIFY_NOT_CONFIGURED = (
    "clarification is not configured: set THRESH_MODEL_GATEWAY_KEY "
    "(or OPENAI_API_KEY) to serve clarify turns"
)

# Same rule for voice notes: no STT key, no silent fake transcription of
# real audio — the voice note refuses with 503.
STT_NOT_CONFIGURED = (
    "voice transcription is not configured: set THRESH_STT_KEY "
    "(or OPENAI_API_KEY) to accept voice notes"
)


def build_channels(channels: dict | None = None) -> dict:
    """The channel adapters by name (Seam 3). Tests inject fakes wholesale;
    production builds the real adapters from env keys — WhatsApp and
    Telegram only when their credentials are configured. Email always
    exists: its dev medium is the log sink."""
    if channels is not None:
        return channels
    built: dict = {"email": EmailChannel(sink=LogSink())}
    phone_number_id = os.environ.get("THRESH_WHATSAPP_PHONE_NUMBER_ID", "")
    whatsapp_token = os.environ.get("THRESH_WHATSAPP_TOKEN", "")
    if phone_number_id and whatsapp_token:
        built["whatsapp"] = WhatsAppChannel(
            phone_number_id=phone_number_id, token=whatsapp_token
        )
    telegram_token = os.environ.get("THRESH_TELEGRAM_TOKEN", "")
    if telegram_token:
        built["telegram"] = TelegramChannel(token=telegram_token)
    return built


def create_app(
    agent_version: str = AGENT_VERSION,
    clarifier: ClarificationLoop | None = None,
    gateway_kwargs: dict | None = None,
    channels: dict | None = None,
    stt=None,
    checkpointer=None,
) -> FastAPI:
    """Build the sidecar app; graphs and loops are built once and reused.

    clarifier overrides the clarification loop wholesale (tests). Otherwise
    the loop runs over the env-configured model gateway (ticket 07:
    OpenAI-compatible, env-keyed). gateway_kwargs builds the fake gateway
    explicitly (the test double — canned completions, never a bill), as does
    THRESH_MODEL_GATEWAY=fake — the explicit dev/demo opt-in. A deployment
    with no key still boots and answers pings, but clarify turns refuse
    with 503 instead of silently billing canned usage.

    channels overrides the channel adapters (tests pass fakes); production
    builds them from env (see build_channels). stt overrides the speech
    client (tests pass the fake); production builds it from env. A
    keyless deployment still boots and answers pings and typed replies,
    but a voice note refuses with 503 (STT_NOT_CONFIGURED) instead of
    silently transcribing canned demo text.
    checkpointer overrides the conversation checkpointer; the default comes
    from THRESH_CHECKPOINT_URL (Postgres, production) or in-memory (dev).
    """
    app = FastAPI(title="Thresh Agent", version=agent_version)
    ping_graph = build_ping_graph(agent_version)
    if clarifier is None:
        if gateway_kwargs is not None:
            clarifier = ClarificationLoop(FakeModelGateway(**gateway_kwargs))
        elif os.environ.get("THRESH_MODEL_GATEWAY", "").lower() == "fake":
            clarifier = ClarificationLoop(FakeModelGateway())
        else:
            try:
                clarifier = ClarificationLoop(gateway_from_env())
            except RuntimeError:
                clarifier = None  # ping-only deployment; clarify refuses

    channel_adapters = build_channels(channels)

    if stt is None:
        try:
            stt = stt_from_env()
        except RuntimeError:
            stt = None  # keyless deployment; voice notes refuse loudly

    member_checkpointer = checkpointer
    if member_checkpointer is None:
        member_checkpointer = checkpointer_from_env()
    setup_checkpointer(member_checkpointer)
    # conversations by thread ID: the pacing graph per conversation shape.
    member_graphs: dict[tuple, MemberConversation] = {}

    def member_conv(questions: tuple) -> MemberConversation:
        """One pacing graph per question-list shape, over the shared
        checkpointer: two sidecar processes with the same checkpointer
        resume each other's threads (the restart path)."""
        key = (tuple(questions), id(member_checkpointer))
        conv = member_graphs.get(key)
        if conv is None:
            conv = MemberConversation(
                questions=list(questions), checkpointer=member_checkpointer
            )
            member_graphs[key] = conv
        return conv

    def deliver(contact: str, body: str) -> DeliveryAck:
        """Deliver one message on the contact point's channel."""
        channel_name, address = parse_address(contact)
        adapter = channel_adapters.get(channel_name)
        if adapter is None:
            return DeliveryAck(delivered=False, channel=channel_name)
        from .channels import ChannelMessage

        import asyncio

        return asyncio.run(
            adapter.deliver(ChannelMessage(recipient=address, body=body))
        )

    def to_thread(msgs) -> list[ThreadMessage]:
        return [ThreadMessage(role=m.role, body=m.body) for m in msgs]

    @app.post("/message")
    def message(env: Envelope) -> Envelope:
        if env.type == "ping.request":
            req = _payload(env, PingRequest)
            result = ping_graph.invoke({"nonce": req.nonce})
            return make_ping_response_envelope(
                nonce=result["nonce"],
                sent_at=_now(),
                agent_version=result["agent_version"],
            )
        if env.type == "request.clarify.request":
            if clarifier is None:
                raise HTTPException(status_code=503, detail=CLARIFY_NOT_CONFIGURED)
            req = _payload(env, ClarifyRequest)
            # Seam 3: the turn runs over a web-chat Channel — the reply is
            # delivered on the medium, then rides the contract home.
            resp = run_web_chat_turn(clarifier, WebChatChannel(), req)
            return make_clarify_response_envelope(resp, sent_at=_now())

        if env.type == "conversation.start.request":
            req = _payload(env, ConversationStartRequest)
            conv = member_conv(tuple(req.questions))
            result = conv.start(req.conversation_id)
            question = result.question or ""
            body = _member_message(req.member_name, question, req.resume_url)
            ack = deliver(req.contact, body)
            if not ack.delivered:
                raise HTTPException(
                    status_code=502,
                    detail=f"the {ack.channel} channel refused the opening message",
                )
            return make_conversation_response_envelope(
                ConversationResponse(
                    conversation_id=req.conversation_id,
                    delivered=True,
                    status="awaiting_member",
                    thread=[ThreadMessage(role="agent", body=body)],
                    answers=[],
                ),
                sent_at=_now(),
            )

        if env.type == "conversation.reply.request":
            req = _payload(env, ConversationReplyRequest)
            # Voice notes transcribe here, agent-side, through the env-keyed
            # STT client (faked in tests): the transcript becomes the reply
            # text and rides the conversation exactly like a typed reply.
            if req.audio is not None:
                if stt is None:
                    raise HTTPException(status_code=503, detail=STT_NOT_CONFIGURED)
                import base64

                try:
                    audio_bytes = base64.b64decode(req.audio.data, validate=True)
                except (ValueError, TypeError) as err:
                    raise HTTPException(
                        status_code=422, detail=f"audio.data is not valid base64: {err}"
                    ) from err
                try:
                    transcript = stt.transcribe(audio_bytes, filename=f"voice.{req.audio.format}")
                except RuntimeError as err:
                    raise HTTPException(status_code=502, detail=f"STT failed: {err}") from err
                member_message = transcript.text
            else:
                member_message = req.message
            conv = member_conv(tuple(req.questions))
            # Cold start (no checkpoint in this process's store): rebuild
            # genuine pacing state by replaying the durable thread's member
            # messages through the graph. A live checkpoint is used as-is.
            if not conv.has_checkpoint(req.conversation_id):
                conv.replay(req.conversation_id, _member_messages_from_thread(req.thread))
            result = conv.reply(req.conversation_id, member_message)
            thread = to_thread(req.thread) + [
                ThreadMessage(role="member", body=member_message)
            ]
            if result.question:
                # A question is pending: deliver it on the Member's channel
                # and stay paused. Every follow-up re-offers the resumable
                # link (spec: members receive resumable links after
                # partial answers — not just on the opening message).
                ack = deliver(req.contact, result.question + _resume_link_suffix(req.resume_url))
                if not ack.delivered:
                    raise HTTPException(
                        status_code=502,
                        detail=f"the {ack.channel} channel refused the follow-up",
                    )
                thread.append(ThreadMessage(role="agent", body=result.question))
            status = result.status
            if status == "awaiting_member" and not result.question:
                # Nothing left to ask and nothing more to deliver: done.
                status = "completed"
            return make_conversation_response_envelope(
                ConversationResponse(
                    conversation_id=req.conversation_id,
                    delivered=True,
                    status=status,
                    thread=thread,
                    answers=result.answers,
                ),
                sent_at=_now(),
            )

        raise HTTPException(
            status_code=400,
            detail=f"unsupported message type: {env.type!r} (contract v{CONTRACT_VERSION})",
        )

    @app.get("/health")
    def health() -> dict:
        return {
            "service": "thresh-agent",
            "version": agent_version,
            "clarify": "configured" if clarifier is not None else "not-configured",
            "channels": sorted(channel_adapters),
            "stt": type(stt).__name__,
        }

    # exposed for tests that assert on what the fake stt heard.
    app.state.stt = stt
    app.state.channel_adapters = channel_adapters
    return app


def _member_message(member_name: str, question: str, resume_url: str) -> str:
    """Compose the opening Channel message: greeting, question, and the
    resumable link (no account — the token is the capability)."""
    parts = [f"Hello {member_name}! I'm the Thresh agent."]
    if question:
        parts.append(question)
    return " ".join(parts) + _resume_link_suffix(resume_url)


def _resume_link_suffix(resume_url: str) -> str:
    """The link sentence appended to every question delivery — the Member
    can always reply on the channel, or continue at their link."""
    if not resume_url:
        return ""
    return f" You can reply here anytime, or continue at {resume_url}"


def _member_messages_from_thread(thread) -> list[str]:
    """The Member's messages so far, from the durable thread — the replay
    input for a cold start. Stop/done words ride along verbatim: the graph
    re-evaluates them exactly as it did live."""
    return [m.body for m in thread if m.role == "member"]


__all__ = [
    "AGENT_VERSION",
    "CLARIFY_NOT_CONFIGURED",
    "STT_NOT_CONFIGURED",
    "ClarifyResponse",
    "create_app",
]


def _now() -> datetime:
    return datetime.now(timezone.utc)


def _payload(env: Envelope, model):
    """Validate a payload against its contract model; an unusable payload is
    a 422 (the client's fault), not a 500."""
    try:
        return model.model_validate(env.payload)
    except ValidationError as err:
        raise HTTPException(status_code=422, detail=str(err)) from err
