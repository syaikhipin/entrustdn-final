"""The agent sidecar's HTTP surface (Seam 2, agent side).

One endpoint — POST /message — that accepts a contract envelope and answers
typed messages. The tracer bullet answers ping.request via the LangGraph
node; ticket 07 adds request.clarify.request, answered by the clarification
loop over an injectable model gateway (faked in tests, OpenAI-compatible in
production).
"""

from datetime import datetime, timezone
import os

from fastapi import FastAPI, HTTPException
from pydantic import ValidationError

from .clarify import ClarificationLoop, run_web_chat_turn
from .channels import WebChatChannel
from .contract import (
    CONTRACT_VERSION,
    ClarifyRequest,
    ClarifyResponse,
    Envelope,
    PingRequest,
    make_clarify_response_envelope,
    make_ping_response_envelope,
)
from .gateway import FakeModelGateway, gateway_from_env
from .graph import build_ping_graph

AGENT_VERSION = "0.1.0"

# What a keyless deployment answers clarify turns with: a loud refusal, not
# a canned completion billed as real usage (every turn is metered and
# charged by the backend).
CLARIFY_NOT_CONFIGURED = (
    "clarification is not configured: set THRESH_MODEL_GATEWAY_KEY "
    "(or OPENAI_API_KEY) to serve clarify turns"
)


def create_app(
    agent_version: str = AGENT_VERSION,
    clarifier: ClarificationLoop | None = None,
    gateway_kwargs: dict | None = None,
) -> FastAPI:
    """Build the sidecar app; graphs and loops are built once and reused.

    clarifier overrides the clarification loop wholesale (tests). Otherwise
    the loop runs over the env-configured model gateway (ticket 07:
    OpenAI-compatible, env-keyed). gateway_kwargs builds the fake gateway
    explicitly (the test double — canned completions, never a bill), as does
    THRESH_MODEL_GATEWAY=fake — the explicit dev/demo opt-in. A deployment
    with no key still boots and answers pings, but clarify turns refuse
    with 503 instead of silently billing canned usage.
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
        }

    return app


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _payload(env: Envelope, model):
    """Validate a payload against its contract model; an unusable payload is
    a 422 (the client's fault), not a 500."""
    try:
        return model.model_validate(env.payload)
    except ValidationError as err:
        raise HTTPException(status_code=422, detail=str(err)) from err


__all__ = [
    "AGENT_VERSION",
    "CLARIFY_NOT_CONFIGURED",
    "ClarifyResponse",
    "create_app",
]


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()


def _payload(env: Envelope, model):
    """Validate a payload against its contract model; an unusable payload is
    a 422 (the client's fault), not a 500."""
    try:
        return model.model_validate(env.payload)
    except ValidationError as err:
        raise HTTPException(status_code=422, detail=str(err)) from err
