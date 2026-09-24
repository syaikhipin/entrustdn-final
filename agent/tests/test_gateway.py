"""The model gateway (the LLM seam, agent side).

Every clarification turn is one metered gateway call: the fake gateway is
the test double (no test touches a live model); the OpenAI-compatible
gateway is the real one, and its usage parsing is pinned here against a
scripted transport — no network.
"""

import json

import httpx

from thresh_agent.gateway import (
    Completion,
    FakeModelGateway,
    OpenAICompatibleGateway,
)


class TestFakeModelGateway:
    def test_returns_canned_completion_with_metered_usage(self) -> None:
        gw = FakeModelGateway(
            text="Which counties?",
            model="thresh-fake-model",
            input_tokens=120,
            cached_input_tokens=40,
            output_tokens=30,
        )

        got = gw.complete("be terse", "I need yield data")

        assert isinstance(got, Completion)
        assert got.text == "Which counties?"
        assert got.model == "thresh-fake-model"
        assert (got.input_tokens, got.cached_input_tokens, got.output_tokens) == (
            120,
            40,
            30,
        )

    def test_records_every_call_for_inspection(self) -> None:
        gw = FakeModelGateway()

        gw.complete("system-one", "user-one")
        gw.complete("system-two", "user-two")

        assert gw.calls == [("system-one", "user-one"), ("system-two", "user-two")]


class TestOpenAICompatibleGateway:
    def test_parses_usage_including_cached_subset(self) -> None:
        """Cached tokens ride inside prompt tokens (OpenAI convention) and
        surface under prompt_tokens_details."""
        captured: dict = {}

        def handler(request: httpx.Request) -> httpx.Response:
            captured["body"] = json.loads(request.content)
            return httpx.Response(
                200,
                json={
                    "choices": [
                        {"message": {"content": "Which season exactly?"}}
                    ],
                    "model": "gpt-4o-mini",
                    "usage": {
                        "prompt_tokens": 120,
                        "completion_tokens": 30,
                        "prompt_tokens_details": {"cached_tokens": 40},
                    },
                },
            )

        gw = OpenAICompatibleGateway(
            api_key="test-key",
            model="gpt-4o-mini",
            transport=httpx.MockTransport(handler),
        )

        got = gw.complete("be terse", "I need yield data")

        assert got.text == "Which season exactly?"
        assert got.model == "gpt-4o-mini"
        assert (got.input_tokens, got.cached_input_tokens, got.output_tokens) == (
            120,
            40,
            30,
        )
        # The call went out as a chat completion with both messages.
        assert captured["body"]["model"] == "gpt-4o-mini"
        assert [m["role"] for m in captured["body"]["messages"]] == [
            "system",
            "user",
        ]

    def test_missing_cached_details_reads_as_zero(self) -> None:
        def handler(request: httpx.Request) -> httpx.Response:
            return httpx.Response(
                200,
                json={
                    "choices": [{"message": {"content": "ok"}}],
                    "model": "m",
                    "usage": {"prompt_tokens": 10, "completion_tokens": 5},
                },
            )

        gw = OpenAICompatibleGateway(
            api_key="test-key",
            model="m",
            transport=httpx.MockTransport(handler),
        )

        got = gw.complete("s", "u")

        assert (got.input_tokens, got.cached_input_tokens, got.output_tokens) == (
            10,
            0,
            5,
        )

    def test_http_failure_surfaces_loudly(self) -> None:
        def handler(request: httpx.Request) -> httpx.Response:
            return httpx.Response(500, json={"error": "boom"})

        gw = OpenAICompatibleGateway(
            api_key="test-key",
            model="m",
            transport=httpx.MockTransport(handler),
        )

        try:
            gw.complete("s", "u")
            raise AssertionError("gateway swallowed an HTTP 500")
        except RuntimeError as err:
            assert "500" in str(err)
