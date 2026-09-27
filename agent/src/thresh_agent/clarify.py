"""The clarification loop (ticket 07, agent side).

One turn = check the catalog first (keyword match of the request's need
against the snapshot the backend sent), then one metered model call that
either asks the next clarifying question or — reply prefixed with the
CLARIFIED sentinel — declares the need fully specified. The backend meters
and prices the reported usage; this loop only reports it honestly.

Ticket 14 adds the memory and connector stage ahead of the model call: the
loop recalls through the turn's admin-configured Memory Providers (recall
before re-asking), queries the request's Connector Modules live, and feeds
both into the prompt as context. New consumer facts are remembered back
after the turn. Every memory/connector failure degrades to no context —
the conversation continues without them.
"""

import asyncio
import re

from .channels import ChannelMessage
from .connectors import ConnectorHub, http_open
from .contract import CatalogMatch, ClarifyRequest, ClarifyResponse, MeteredUsage
from .gateway import FakeModelGateway, OpenAICompatibleGateway
from .mcp_memory import MemoryRouter, http_connect

# The reply prefix that marks a fully-specified need. Stripped before the
# reply reaches the consumer.
CLARIFIED_SENTINEL = "CLARIFIED:"

CLARIFY_SYSTEM_TEMPLATE = """You are the Domain Agent of Thresh, an \
agricultural data-sharing platform. A Data Consumer filed this Request:

  needs: {description}
  format: {format}
  quality bar: {quality_bar}
  budget: {budget_micros} micro-credits, {spent_micros} already spent

BEFORE asking anything, check this catalog of existing Data Assets — if one \
already answers the need, say so in your reply and name it:

{catalog}

Otherwise ask the ONE next question whose answer best narrows the need \
(county, season, crop, granularity, licensing). Keep the reply to two \
sentences. Never invent assets that are not in the catalog. When the need \
is fully specified, begin the reply with "{sentinel}" followed by the \
final summary of the request.{memory_block}{connector_block}"""

# Recall from the admin-configured Memory Providers (ticket 14): context
# from earlier sessions, so the agent never re-asks what it already knows.
MEMORY_BLOCK_TEMPLATE = """

Memories from earlier conversations with this consumer (recall — do not \
re-ask what these already answer):
{memories}"""

# Live findings from the request's Connector Modules (ticket 14): external
# data queried during this turn.
CONNECTOR_BLOCK_TEMPLATE = """

Live data just queried from connected sources (may cross-check or enrich \
the catalog above):
{findings}"""

SKILLS_HEADER = """The consumer attached these Agent Skills to the Request. \
They are instructions for how to conduct this conversation and interpret \
answers — read and follow them; they are guidance, not code to run:
"""

SKILL_ROW_TEMPLATE = """--- Skill: {name}
{content}
"""


def _skills_block(skills: list) -> str:
    if not skills:
        return ""
    return SKILLS_HEADER + "\n".join(
        SKILL_ROW_TEMPLATE.format(name=s.name, content=s.content) for s in skills
    )


def _memory_block(memories: list[str]) -> str:
    if not memories:
        return ""
    return MEMORY_BLOCK_TEMPLATE.format(
        memories="\n".join(f"- {m}" for m in memories)
    )


def _connector_block(findings: list[tuple[str, str]]) -> str:
    if not findings:
        return ""
    return CONNECTOR_BLOCK_TEMPLATE.format(
        findings="\n".join(f"- [{name}] {text}" for name, text in findings)
    )

CATALOG_ROW_TEMPLATE = """- id={id} name="{name}": {description}"""


def _catalog_block(catalog: list) -> str:
    if not catalog:
        return "(the catalog is empty — no existing assets)"
    return "\n".join(
        CATALOG_ROW_TEMPLATE.format(
            id=a.id, name=a.name, description=a.description or "(no description)"
        )
        for a in catalog
    )


def _match_catalog(req: ClarifyRequest) -> list[CatalogMatch]:
    """Catalog first (ticket checklist): report snapshot assets whose
    name/description share at least two distinct meaningful words with the
    request's need — one shared word is coincidence, not a match. This is
    the deterministic floor; the model's reply adds the why."""
    stop = {
        "the", "a", "an", "for", "of", "in", "on", "and", "or", "to", "across",
        "with", "data", "need", "i", "want", "looking", "please", "just",
        "all", "any", "my", "our", "by", "per",
    }
    need_words = {
        w for w in re.findall(r"[a-z0-9]+", req.description.lower()) if w not in stop
    }
    if not need_words:
        return []
    matches = []
    for asset in req.catalog:
        hay = f"{asset.name} {asset.description}".lower()
        hay_words = set(re.findall(r"[a-z0-9]+", hay))
        overlap = need_words & hay_words
        if len(overlap) >= 2:
            matches.append(
                CatalogMatch(
                    asset_id=asset.id,
                    name=asset.name,
                    reason="catalog asset matching: " + ", ".join(sorted(overlap)),
                )
            )
    return matches


class ClarificationLoop:
    """The Domain Agent's per-turn behavior over a model gateway.

    memory_router and connector_hub are the ticket-14 seams; both optional
    (an empty turn rides with no memory stage, exactly the pre-ticket-14
    shape). When set they are driven per turn from the request's
    memory_providers / connectors contract fields.
    """

    def __init__(
        self,
        gateway: FakeModelGateway | OpenAICompatibleGateway,
        memory_connect=http_connect,
        connector_open=http_open,
    ) -> None:
        self._gateway = gateway
        self._memory_connect = memory_connect
        self._connector_open = connector_open

    def turn(self, req: ClarifyRequest) -> ClarifyResponse:
        matches = _match_catalog(req)
        memories, findings = self._gather_context(req)
        completion = self._gateway.complete(
            CLARIFY_SYSTEM_TEMPLATE.format(
                description=req.description,
                format=req.format,
                quality_bar=req.quality_bar or "(none stated)",
                budget_micros=req.budget_micros,
                spent_micros=req.spent_micros,
                catalog=_catalog_block(req.catalog),
                sentinel=CLARIFIED_SENTINEL,
                memory_block=_memory_block(memories),
                connector_block=_connector_block(findings),
            )
            + _skills_block(req.skills),
            self._user_turn(req),
        )
        text = completion.text.strip()
        clarified = text.upper().startswith(CLARIFIED_SENTINEL)
        if clarified:
            text = text[len(CLARIFIED_SENTINEL):].strip()
        if not text:
            # An empty model reply is unusable on the contract; fall back to
            # a question rather than fail the turn.
            text = "Could you say more about the data you need?"
        return ClarifyResponse(
            request_id=req.request_id,
            reply=text,
            clarified=clarified,
            matches=matches,
            usage=MeteredUsage(
                model=completion.model,
                input_tokens=completion.input_tokens,
                cached_input_tokens=completion.cached_input_tokens,
                output_tokens=completion.output_tokens,
            ),
        )

    def _gather_context(self, req: ClarifyRequest) -> tuple[list[str], list[tuple[str, str]]]:
        """Recall + query + remember ahead of the model call, all degrading
        quietly and all concurrent — a hung provider or connector costs its
        own timeout, never the registry's stacked total, and remember (which
        stores the consumer's message, needing no reply) never adds a second
        fan-out on the response path. Runs under one asyncio.run (the sync
        boundary: FastAPI runs sync handlers in a threadpool, no loop running
        here)."""

        async def gather() -> tuple[list[str], list[tuple[str, str]]]:
            router = MemoryRouter(req.memory_providers, connect=self._memory_connect)
            hub = ConnectorHub(req.connectors, open_mcp=self._connector_open)
            memories, findings, _ = await asyncio.gather(
                router.recall(req.message),
                hub.query_all(req.message),
                # The other half of recall-before-re-asking: store the
                # consumer's new fact alongside the reads, so the write
                # cannot stack its own fan-out after the model call.
                router.remember(f"request {req.request_id}: consumer said: {req.message}"),
            )
            return memories, findings

        try:
            return asyncio.run(gather())
        except Exception:
            # The gather itself must not fail the turn — belt and braces on
            # top of the per-provider/per-connector degradation.
            return [], []

    @staticmethod
    def _user_turn(req: ClarifyRequest) -> str:
        lines = []
        for turn in req.history:
            who = "Consumer" if turn.role == "consumer" else "You"
            lines.append(f"{who}: {turn.body}")
        lines.append(f"Consumer: {req.message}")
        return "\n".join(lines)


def run_web_chat_turn(
    loop: "ClarificationLoop", channel, req: ClarifyRequest
) -> ClarifyResponse:
    """Drive one clarification turn over a web-chat Channel (Seam 3): run
    the loop, deliver the reply over the channel, and return the response
    that rides the contract home. A delivery the channel did not
    acknowledge fails the turn — a reply is only ever reported as sent once
    the medium has it."""
    resp = loop.turn(req)
    ack = asyncio.run(
        channel.deliver(
            ChannelMessage(recipient=req.request_id, body=resp.reply)
        )
    )
    if not ack.delivered:
        raise RuntimeError(
            f"web channel did not deliver the reply to {req.request_id}"
        )
    return resp
