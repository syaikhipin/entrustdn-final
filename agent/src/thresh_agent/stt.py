"""Speech-to-text for voice notes (Seam 3, ticket 11).

Env-keyed and agent-side (spec: Implementation Decisions). Like the model
gateway, there are two implementations: FakeSTT (tests — scripted text,
recorded audio) and OpenAICompatibleSTT (any whisper-style transcription
endpoint; the request shape is pinned by tests against a scripted
transport, never a live call). A transcription rides the conversation
exactly like a typed reply.
"""

import os
from dataclasses import dataclass, field

import httpx


@dataclass
class Transcription:
    """One voice note's transcription."""

    text: str
    model: str


@dataclass
class FakeSTT:
    """The test double: scripted text, recorded audio."""

    text: str = "Spring barley, twelve hectares"
    model: str = "thresh-fake-stt"
    audio: list[bytes] = field(default_factory=list)

    def transcribe(self, audio: bytes, filename: str = "voice.ogg") -> Transcription:
        self.audio.append(audio)
        return Transcription(text=self.text, model=self.model)


class OpenAICompatibleSTT:
    """Speaks the OpenAI audio-transcription dialect to a configured
    endpoint (OpenAI's whisper endpoints and compatible gateways)."""

    def __init__(
        self,
        api_key: str,
        model: str = "whisper-1",
        base_url: str = "https://api.openai.com/v1",
        transport: httpx.BaseTransport | None = None,
        timeout: float = 60.0,
    ) -> None:
        self.model = model
        self.api_key = api_key
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

    def transcribe(self, audio: bytes, filename: str = "voice.ogg") -> Transcription:
        resp = self._client.post(
            "/audio/transcriptions",
            files={"file": (filename, audio), "model": (None, self.model)},
        )
        if resp.status_code != 200:
            raise RuntimeError(
                f"STT endpoint returned {resp.status_code}: {resp.text[:200]}"
            )
        doc = resp.json()
        return Transcription(text=doc.get("text", ""), model=self.model)


def stt_from_env() -> OpenAICompatibleSTT:
    """Build the real STT client from the environment.

    THRESH_STT_KEY (falls back to OPENAI_API_KEY), THRESH_STT_BASE_URL
    (default: OpenAI), THRESH_STT_MODEL (default: whisper-1).
    """
    api_key = os.environ.get("THRESH_STT_KEY", os.environ.get("OPENAI_API_KEY", ""))
    if not api_key:
        raise RuntimeError(
            "no STT key: set THRESH_STT_KEY (or OPENAI_API_KEY)"
        )
    return OpenAICompatibleSTT(
        api_key=api_key,
        model=os.environ.get("THRESH_STT_MODEL", "whisper-1"),
        base_url=os.environ.get("THRESH_STT_BASE_URL", "https://api.openai.com/v1"),
    )
