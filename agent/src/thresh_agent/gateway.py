"""The model gateway (the LLM seam, agent side).

Ticket 07: inference is metered per token — input, output, and cached
input (cheaper). The gateway returns a Completion carrying that usage
alongside the text, so the clarification loop can report it on the contract
and the backend can price it against the Request's budget.

Two implementations: FakeModelGateway (tests — canned text, scripted
usage, recorded calls) and OpenAICompatibleGateway (any OpenAI-style
chat-completions endpoint; the usage parsing is pinned by tests against a
scripted transport, never a live call).
"""

import os
from dataclasses import dataclass, field

import httpx


@dataclass
class Completion:
    """One model call's outcome: the text and what it cost in tokens.

    Cached input rides inside input_tokens (the OpenAI convention); the
    backend bills the cached subset at the cheaper cached rate.
    """

    text: str
    model: str
    input_tokens: int
    cached_input_tokens: int
    output_tokens: int


@dataclass
class FakeModelGateway:
    """The test double: canned completion, scripted usage, recorded calls."""

    text: str = "Which counties exactly do you need?"
    model: str = "thresh-fake-model"
    input_tokens: int = 120
    cached_input_tokens: int = 40
    output_tokens: int = 30
    calls: list[tuple[str, str]] = field(default_factory=list)

    def complete(self, system: str, user: str) -> Completion:
        self.calls.append((system, user))
        return Completion(
            text=self.text,
            model=self.model,
            input_tokens=self.input_tokens,
            cached_input_tokens=self.cached_input_tokens,
            output_tokens=self.output_tokens,
        )


class OpenAICompatibleGateway:
    """Speaks the OpenAI chat-completions dialect to a configured endpoint.

    Works with OpenAI itself and any compatible gateway (vLLM, Ollama's
    OpenAI shim, Azure's compatible surface). base_url points at the
    endpoint root; api_key rides as a bearer token.
    """

    def __init__(
        self,
        api_key: str,
        model: str,
        base_url: str = "https://api.openai.com/v1",
        transport: httpx.BaseTransport | None = None,
        timeout: float = 60.0,
    ) -> None:
        self.model = model
        self.base_url = base_url.rstrip("/")
        headers = {"Authorization": f"Bearer {api_key}"}
        if transport is not None:
            self._client = httpx.Client(
                base_url=self.base_url, headers=headers, transport=transport
            )
        else:
            self._client = httpx.Client(
                base_url=self.base_url, headers=headers, timeout=timeout
            )

    def complete(self, system: str, user: str) -> Completion:
        resp = self._client.post(
            "/chat/completions",
            json={
                "model": self.model,
                "messages": [
                    {"role": "system", "content": system},
                    {"role": "user", "content": user},
                ],
            },
        )
        if resp.status_code != 200:
            raise RuntimeError(
                f"model gateway returned {resp.status_code}: {resp.text[:200]}"
            )
        doc = resp.json()
        usage = doc.get("usage", {})
        cached = (
            usage.get("prompt_tokens_details", {}).get("cached_tokens", 0) or 0
        )
        text = doc["choices"][0]["message"]["content"] or ""
        return Completion(
            text=text,
            model=doc.get("model", self.model),
            input_tokens=usage.get("prompt_tokens", 0),
            cached_input_tokens=cached,
            output_tokens=usage.get("completion_tokens", 0),
        )


def gateway_from_env() -> OpenAICompatibleGateway:
    """Build the real gateway from the environment.

    THRESH_MODEL_GATEWAY_BASE_URL (default: OpenAI), THRESH_MODEL_GATEWAY_KEY
    (falls back to OPENAI_API_KEY), THRESH_MODEL (default: gpt-4o-mini).
    """
    api_key = os.environ.get(
        "THRESH_MODEL_GATEWAY_KEY", os.environ.get("OPENAI_API_KEY", "")
    )
    if not api_key:
        raise RuntimeError(
            "no model gateway key: set THRESH_MODEL_GATEWAY_KEY "
            "(or OPENAI_API_KEY)"
        )
    return OpenAICompatibleGateway(
        api_key=api_key,
        model=os.environ.get("THRESH_MODEL", "gpt-4o-mini"),
        base_url=os.environ.get(
            "THRESH_MODEL_GATEWAY_BASE_URL", "https://api.openai.com/v1"
        ),
    )
