"""The clarification loop (ticket 07, agent side).

One turn = check the catalog first (keyword match of the request's need
against the snapshot the backend sent), then one metered model call that
either asks the next clarifying question or — reply prefixed with the
CLARIFIED sentinel — declares the need fully specified. The backend meters
and prices the reported usage; this loop only reports it honestly.
"""

import asyncio
import re

from .channels import ChannelMessage
from .contract import CatalogMatch, ClarifyRequest, ClarifyResponse, MeteredUsage
from .gateway import FakeModelGateway, OpenAICompatibleGateway

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
final summary of the request."""

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
    """The Domain Agent's per-turn behavior over a model gateway."""

    def __init__(self, gateway: FakeModelGateway | OpenAICompatibleGateway) -> None:
        self._gateway = gateway

    def turn(self, req: ClarifyRequest) -> ClarifyResponse:
        matches = _match_catalog(req)
        completion = self._gateway.complete(
            CLARIFY_SYSTEM_TEMPLATE.format(
                description=req.description,
                format=req.format,
                quality_bar=req.quality_bar or "(none stated)",
                budget_micros=req.budget_micros,
                spent_micros=req.spent_micros,
                catalog=_catalog_block(req.catalog),
                sentinel=CLARIFIED_SENTINEL,
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
