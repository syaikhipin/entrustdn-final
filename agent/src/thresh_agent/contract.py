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
