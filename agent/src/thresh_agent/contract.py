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
