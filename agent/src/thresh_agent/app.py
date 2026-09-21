"""The agent sidecar's HTTP surface (Seam 2, agent side).

One endpoint — POST /message — that accepts a contract envelope and answers
typed messages. The tracer bullet answers ping.request via the LangGraph
node; later message types land here as new handlers, contract-first.
"""

from datetime import datetime, timezone

from fastapi import FastAPI, HTTPException

from .contract import (
    CONTRACT_VERSION,
    Envelope,
    PingRequest,
    make_ping_response_envelope,
)
from .graph import build_ping_graph

AGENT_VERSION = "0.1.0"


def create_app(agent_version: str = AGENT_VERSION) -> FastAPI:
    """Build the sidecar app; the graph is built once and reused."""
    app = FastAPI(title="Thresh Agent", version=agent_version)
    ping_graph = build_ping_graph(agent_version)

    @app.post("/message")
    def message(env: Envelope) -> Envelope:
        if env.type == "ping.request":
            req = PingRequest.model_validate(env.payload)
            result = ping_graph.invoke({"nonce": req.nonce})
            return make_ping_response_envelope(
                nonce=result["nonce"],
                sent_at=_now(),
                agent_version=result["agent_version"],
            )
        raise HTTPException(
            status_code=400,
            detail=f"unsupported message type: {env.type!r} (contract v{CONTRACT_VERSION})",
        )

    @app.get("/health")
    def health() -> dict:
        return {"service": "thresh-agent", "version": agent_version}

    return app


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()
