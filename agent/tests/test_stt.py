"""Voice notes → STT transcription (Seam 3, ticket 11). The STT client is
env-keyed and agent-side (spec: Implementation Decisions); tests drive the
fake — no test touches a live STT API. The transcription's only job is to
turn one voice note into text that rides the conversation like a typed
reply.
"""

import pytest

from thresh_agent.stt import FakeSTT, Transcription, stt_from_env


def test_fake_stt_transcribes_to_scripted_text() -> None:
    stt = FakeSTT(text="Spring barley, twelve hectares")

    result = stt.transcribe(b"fake-audio-bytes")

    assert isinstance(result, Transcription)
    assert result.text == "Spring barley, twelve hectares"
    assert result.model == "thresh-fake-stt"


def test_fake_stt_records_audio_it_saw() -> None:
    stt = FakeSTT(text="twelve hectares of spring barley")

    stt.transcribe(b"clip-001")
    stt.transcribe(b"clip-002")

    assert stt.audio == [b"clip-001", b"clip-002"]


def test_env_stt_requires_a_key(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("THRESH_STT_KEY", raising=False)
    monkeypatch.delenv("OPENAI_API_KEY", raising=False)

    with pytest.raises(RuntimeError):
        stt_from_env()


def test_env_stt_falls_back_to_openai_key(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("THRESH_STT_KEY", raising=False)
    monkeypatch.setenv("OPENAI_API_KEY", "sk-openai")

    client = stt_from_env()

    assert client.api_key == "sk-openai"


def test_env_stt_prefers_its_own_key(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("THRESH_STT_KEY", "sk-stt")
    monkeypatch.setenv("OPENAI_API_KEY", "sk-openai")

    client = stt_from_env()

    assert client.api_key == "sk-stt"
