"""Go↔Agent message contract, Python side.

The single source of truth is the JSON Schema and golden fixtures in
/contract at the repo root; tests/test_contract.py pins these models to the
fixtures so the two sides cannot drift silently. Mirrors
backend/internal/contract/contract.go.
"""

from datetime import datetime
from typing import Any

from pydantic import BaseModel, ConfigDict, Field, model_validator

CONTRACT_VERSION = 1


class Envelope(BaseModel):
    """Outer shell of every backend↔agent message."""

    model_config = ConfigDict(extra="forbid")

    version: int = Field(description="Contract version; both sides reject others")
    type: str = Field(min_length=1)
    sent_at: datetime
    payload: dict[str, Any]

    @model_validator(mode="after")
    def _check_version(self) -> "Envelope":
        if self.version != CONTRACT_VERSION:
            raise ValueError(
                f"contract version {self.version}, want {CONTRACT_VERSION}"
            )
        return self


class PingRequest(BaseModel):
    """Backend→agent liveness probe payload."""

    model_config = ConfigDict(extra="forbid")

    nonce: str = Field(min_length=1)


class PingResponse(BaseModel):
    """Agent's answer to a PingRequest."""

    model_config = ConfigDict(extra="forbid")

    nonce: str = Field(min_length=1)
    pong: bool = Field(description="Always true in a well-formed response")
    agent_version: str = Field(min_length=1)


class CategoryStamp(BaseModel):
    """One taxonomy assignment as the contract carries it."""

    model_config = ConfigDict(extra="forbid")

    category: str = Field(min_length=1)
    value: str = Field(min_length=1)
    label: str = Field(min_length=1)


class CatalogAsset(BaseModel):
    """One catalog row handed to the agent: an existing Data Asset's public
    face, so the agent can check the catalog before fielding."""

    model_config = ConfigDict(extra="forbid")

    id: str = Field(min_length=1)
    name: str = Field(min_length=1)
    description: str = ""
    categories: list[CategoryStamp] = Field(default_factory=list)
    cached_price_micros: int = Field(ge=0)


class HistoryTurn(BaseModel):
    """One prior turn of a clarification conversation."""

    model_config = ConfigDict(extra="forbid")

    role: str = Field(pattern="^(consumer|agent)$")
    body: str = Field(min_length=1)


class SkillModule(BaseModel):
    """One Agent Skill loaded into the agent's context for this turn
    (ticket 13): the skill's name and its markdown instructions.
    Instructions are data the agent reads, never code it runs."""

    model_config = ConfigDict(extra="forbid")

    name: str = Field(min_length=1)
    content: str = Field(min_length=1)


class MemoryProvider(BaseModel):
    """One admin-configured Memory Provider endpoint (ticket 14, ADR 0003)
    handed over for cross-session recall: the backend owns the config, the
    agent owns the MCP connection and any failure it hits."""

    model_config = ConfigDict(extra="forbid")

    name: str = Field(min_length=1)
    endpoint: str = Field(min_length=1)


class ConnectorModule(BaseModel):
    """One Connector Module resolved live (ticket 14): an external MCP or
    API link the agent may query to enrich or cross-check an answer.
    Connection facts only — the agent treats every failure as no data."""

    model_config = ConfigDict(extra="forbid")

    name: str = Field(min_length=1)
    endpoint: str = Field(min_length=1)
    transport: str = Field(default="mcp", pattern="^(mcp|api)$")
    query: str = ""


class ClarifyRequest(BaseModel):
    """One clarification turn sent backend→agent."""

    model_config = ConfigDict(extra="forbid")

    request_id: str = Field(min_length=1)
    description: str = Field(min_length=1)
    format: str = Field(min_length=1)
    quality_bar: str = ""
    budget_micros: int = Field(ge=0)
    spent_micros: int = Field(ge=0)
    message: str = Field(min_length=1)
    history: list[HistoryTurn] = Field(default_factory=list)
    catalog: list[CatalogAsset] = Field(default_factory=list)
    skills: list[SkillModule] = Field(default_factory=list)
    memory_providers: list[MemoryProvider] = Field(default_factory=list)
    connectors: list[ConnectorModule] = Field(default_factory=list)


class CatalogMatch(BaseModel):
    """One existing Asset the agent reports as already answering the need."""

    model_config = ConfigDict(extra="forbid")

    asset_id: str = Field(min_length=1)
    name: str = Field(min_length=1)
    reason: str = Field(min_length=1)


class MeteredUsage(BaseModel):
    """One metered model call as the gateway reports it. Cached input rides
    inside input_tokens (the OpenAI convention) and bills at the cheaper
    cached rate."""

    model_config = ConfigDict(extra="forbid")

    model: str = Field(min_length=1)
    input_tokens: int = Field(ge=0)
    cached_input_tokens: int = Field(ge=0)
    output_tokens: int = Field(ge=0)

    @model_validator(mode="after")
    def _check_cached_subset(self) -> "MeteredUsage":
        if self.cached_input_tokens > self.input_tokens:
            raise ValueError(
                f"cached input tokens ({self.cached_input_tokens}) exceed "
                f"input tokens ({self.input_tokens})"
            )
        return self


class ClarifyResponse(BaseModel):
    """Agent's answer for one clarification turn."""

    model_config = ConfigDict(extra="forbid")

    request_id: str = Field(min_length=1)
    reply: str = Field(min_length=1)
    clarified: bool
    matches: list[CatalogMatch] = Field(default_factory=list)
    usage: MeteredUsage


def make_clarify_response_envelope(
    response: ClarifyResponse, sent_at: datetime
) -> Envelope:
    """Build a request.clarify.response envelope."""
    return Envelope(
        version=CONTRACT_VERSION,
        type="request.clarify.response",
        sent_at=sent_at,
        payload=response.model_dump(),
    )


def make_ping_response_envelope(
    nonce: str, sent_at: datetime, agent_version: str
) -> Envelope:
    """Build a ping.response envelope."""
    return Envelope(
        version=CONTRACT_VERSION,
        type="ping.response",
        sent_at=sent_at,
        payload=PingResponse(
            nonce=nonce, pong=True, agent_version=agent_version
        ).model_dump(),
    )


class ThreadMessage(BaseModel):
    """One turn of a Member conversation (ticket 11)."""

    model_config = ConfigDict(extra="forbid")

    role: str = Field(pattern="^(agent|member)$")
    body: str = Field(min_length=1)


class ConversationStartRequest(BaseModel):
    """Backend→agent: open a conversation with one Farmer Member over a
    Channel. The agent asks the first question over the medium, then pauses
    on a checkpoint until the Member replies (ADR 0001's pause-for-days)."""

    model_config = ConfigDict(extra="forbid")

    conversation_id: str = Field(min_length=1)
    # Empty when the conversation is not (yet) tied to a Request.
    request_id: str = ""
    member_name: str = Field(min_length=1)
    # Channel-qualified contact point from the roster:
    # 'whatsapp:+353860000001', 'telegram:12345', 'email:member@farm.ie'.
    contact: str = Field(min_length=1)
    topic: str = Field(min_length=1)
    questions: list[str] = Field(min_length=1)
    # The Member's resumable link (no account — the token is the
    # capability); appended to Channel messages. Empty when the deployment
    # has no public URL.
    resume_url: str = ""


class ConversationAudio(BaseModel):
    """A Member's voice note: base64-encoded bytes plus a format hint that
    rides the transcription upload (ticket 11: STT is env-keyed, agent-side)."""

    model_config = ConfigDict(extra="forbid")

    data: str = Field(min_length=1, description="base64-encoded audio bytes")
    format: str = Field(min_length=1, description="container hint, e.g. 'ogg'")


class ConversationReplyRequest(BaseModel):
    """Backend→agent: a Member's reply arrived — resume the conversation
    mid-thread from the checkpoint. The reply is typed text ('message') or
    a voice note ('audio', transcribed by the agent's env-keyed STT client)
    — exactly one of the two. Thread, contact, and questions ride along:
    the backend owns the durable record, and re-sending it lets a cold
    agent rebuild context, deliver on the right channel, and pace the same
    question list."""

    model_config = ConfigDict(extra="forbid")

    conversation_id: str = Field(min_length=1)
    message: str = Field(default="", description="typed reply; exactly one of message / audio")
    audio: ConversationAudio | None = None
    thread: list[ThreadMessage] = Field(default_factory=list)
    contact: str = Field(min_length=1)
    questions: list[str] = Field(min_length=1)
    # The Member's resumable link, re-offered after partial answers.
    # Empty when the deployment has no public URL.
    resume_url: str = ""

    @model_validator(mode="after")
    def _check_message_xor_audio(self) -> "ConversationReplyRequest":
        if (self.message != "") == (self.audio is not None):
            raise ValueError("a reply carries message or audio, not both (and not neither)")
        return self


class ConversationResponse(BaseModel):
    """Agent→backend after any conversation turn: delivery acknowledgement,
    status, the full thread, and the answers gathered so far."""

    model_config = ConfigDict(extra="forbid")

    conversation_id: str = Field(min_length=1)
    delivered: bool
    status: str = Field(pattern="^(awaiting_member|completed|stopped)$")
    thread: list[ThreadMessage] = Field(default_factory=list)
    answers: list[str] = Field(default_factory=list)


def make_conversation_response_envelope(
    response: ConversationResponse, sent_at: datetime
) -> Envelope:
    """Build a conversation.response envelope."""
    return Envelope(
        version=CONTRACT_VERSION,
        type="conversation.response",
        sent_at=sent_at,
        payload=response.model_dump(),
    )
