"""Pin the Python-side contract types to the golden fixtures in /contract.

The fixtures are the single source of truth for the Go↔Agent message contract
(Seam 2, agent side). If the fixtures and the Pydantic models drift, this
suite fails — same deal as backend/internal/contract/contract_test.go.
"""

import json
from pathlib import Path

import pytest
from jsonschema import Draft202012Validator

from thresh_agent.contract import (
    CONTRACT_VERSION,
    ClarifyRequest,
    ClarifyResponse,
    ConversationReplyRequest,
    ConversationResponse,
    ConversationStartRequest,
    Envelope,
    PingRequest,
    PingResponse,
)

FIXTURES = Path(__file__).resolve().parents[2] / "contract" / "fixtures"
SCHEMAS = Path(__file__).resolve().parents[2] / "contract" / "schemas"


def load_fixture(name: str) -> dict:
    return json.loads((FIXTURES / name).read_text())


class TestEnvelope:
    def test_rejects_wrong_version(self) -> None:
        doc = load_fixture("ping-request.json")
        doc["version"] = 2
        with pytest.raises(Exception):
            Envelope.model_validate(doc)

    def test_rejects_missing_type(self) -> None:
        doc = load_fixture("ping-request.json")
        del doc["type"]
        with pytest.raises(Exception):
            Envelope.model_validate(doc)


class TestPingPair:
    def test_request_round_trip(self) -> None:
        doc = load_fixture("ping-request.json")
        env = Envelope.model_validate(doc)
        assert env.type == "ping.request"
        req = PingRequest.model_validate(env.payload)
        assert req.nonce == "tracer-bullet-01"

    def test_response_round_trip(self) -> None:
        doc = load_fixture("ping-response.json")
        env = Envelope.model_validate(doc)
        assert env.type == "ping.response"
        resp = PingResponse.model_validate(env.payload)
        assert resp.nonce == "tracer-bullet-01"
        assert resp.pong is True
        assert resp.agent_version == "0.1.0"

    def test_response_rejects_empty_agent_version(self) -> None:
        doc = load_fixture("ping-response.json")
        doc["payload"]["agent_version"] = ""
        env = Envelope.model_validate(doc)
        with pytest.raises(Exception):
            PingResponse.model_validate(env.payload)


def test_contract_version_is_one() -> None:
    assert CONTRACT_VERSION == 1


class TestClarifyPair:
    """Ticket 07's contract pair: request.clarify.request/response, pinned
    to the golden fixtures exactly like the ping pair."""

    def test_request_round_trip(self) -> None:
        doc = load_fixture("request-clarify-request.json")
        env = Envelope.model_validate(doc)
        assert env.type == "request.clarify.request"
        req = ClarifyRequest.model_validate(env.payload)
        assert req.request_id == "req-clarify-01"
        assert req.format == "csv"
        assert req.budget_micros == 10_000_000
        assert req.spent_micros == 0
        assert len(req.history) == 2
        assert req.history[0].role == "consumer"
        assert req.catalog[0].id == "asset-01"
        assert req.catalog[0].cached_price_micros == 5_000_000

    def test_response_round_trip(self) -> None:
        doc = load_fixture("request-clarify-response.json")
        env = Envelope.model_validate(doc)
        assert env.type == "request.clarify.response"
        resp = ClarifyResponse.model_validate(env.payload)
        assert resp.request_id == "req-clarify-01"
        assert resp.clarified is False
        assert resp.matches[0].asset_id == "asset-01"
        assert resp.usage.input_tokens == 120
        assert resp.usage.cached_input_tokens == 40
        assert resp.usage.output_tokens == 30

    def test_request_rejects_unknown_fields(self) -> None:
        doc = load_fixture("request-clarify-request.json")
        doc["payload"]["extra"] = 1
        with pytest.raises(Exception):
            ClarifyRequest.model_validate(doc["payload"])

    def test_response_rejects_unknown_fields(self) -> None:
        doc = load_fixture("request-clarify-response.json")
        doc["payload"]["extra"] = 1
        with pytest.raises(Exception):
            ClarifyResponse.model_validate(doc["payload"])

    def test_response_rejects_cached_exceeding_input(self) -> None:
        doc = load_fixture("request-clarify-response.json")
        doc["payload"]["usage"]["cached_input_tokens"] = 999
        with pytest.raises(Exception):
            ClarifyResponse.model_validate(doc["payload"])

    def test_schemas_validate_clarify_fixtures(self) -> None:
        validator = Draft202012Validator(
            json.loads((SCHEMAS / "request-clarify-pair.schema.json").read_text())
        )
        env_validator = self._envelope_validator()
        for name in ("request-clarify-request.json", "request-clarify-response.json"):
            doc = load_fixture(name)
            env_validator.validate(doc)
            validator.validate(doc["payload"])

    def _envelope_validator(self) -> Draft202012Validator:
        return Draft202012Validator(json.loads((SCHEMAS / "envelope.schema.json").read_text()))


class TestConversationPairs:
    """Ticket 11's contract pairs: conversation.start (open a Member
    conversation over a Channel) and conversation.reply (resume
    mid-thread, possibly days later). Pinned to the golden fixtures exactly
    like the ping and clarify pairs; both share the conversation.response."""

    def test_start_request_round_trip(self) -> None:
        doc = load_fixture("conversation-start-request.json")
        env = Envelope.model_validate(doc)
        assert env.type == "conversation.start.request"
        req = ConversationStartRequest.model_validate(env.payload)
        assert req.conversation_id == "conv-01"
        assert req.request_id == "req-clarify-01"
        assert req.contact == "whatsapp:+353860000001"
        assert len(req.questions) == 3

    def test_reply_request_round_trip(self) -> None:
        doc = load_fixture("conversation-reply-request.json")
        env = Envelope.model_validate(doc)
        assert env.type == "conversation.reply.request"
        req = ConversationReplyRequest.model_validate(env.payload)
        assert req.conversation_id == "conv-01"
        assert req.message == "Spring barley, twelve hectares"
        assert req.contact == "whatsapp:+353860000001"
        assert len(req.questions) == 3
        assert req.thread[0].role == "agent"

    def test_response_round_trip(self) -> None:
        for name in ("conversation-start-response.json", "conversation-reply-response.json"):
            doc = load_fixture(name)
            env = Envelope.model_validate(doc)
            assert env.type == "conversation.response"
            resp = ConversationResponse.model_validate(env.payload)
            assert resp.conversation_id == "conv-01"
            assert resp.delivered is True
            assert resp.status == "awaiting_member"
            assert resp.thread[0].role == "agent"

    def test_requests_reject_unknown_fields(self) -> None:
        for name, model in (
            ("conversation-start-request.json", ConversationStartRequest),
            ("conversation-reply-request.json", ConversationReplyRequest),
        ):
            doc = load_fixture(name)
            doc["payload"]["extra"] = 1
            with pytest.raises(Exception):
                model.model_validate(doc["payload"])

    def test_response_rejects_unknown_status(self) -> None:
        doc = load_fixture("conversation-start-response.json")
        doc["payload"]["status"] = "maybe"
        with pytest.raises(Exception):
            ConversationResponse.model_validate(doc["payload"])

    def test_schemas_validate_conversation_fixtures(self) -> None:
        start = Draft202012Validator(
            json.loads((SCHEMAS / "conversation-start-pair.schema.json").read_text())
        )
        reply = Draft202012Validator(
            json.loads((SCHEMAS / "conversation-reply-pair.schema.json").read_text())
        )
        env_validator = self._envelope_validator()
        doc = load_fixture("conversation-start-request.json")
        env_validator.validate(doc)
        start.validate(doc["payload"])
        doc = load_fixture("conversation-start-response.json")
        env_validator.validate(doc)
        start.validate(doc["payload"])
        doc = load_fixture("conversation-reply-request.json")
        env_validator.validate(doc)
        reply.validate(doc["payload"])
        doc = load_fixture("conversation-reply-response.json")
        env_validator.validate(doc)
        reply.validate(doc["payload"])
    """The JSON Schemas in /contract are the contract's source of truth —
    every golden fixture must validate against them, and hand-built
    violations must fail, so schema and typed models cannot drift."""

    def _envelope_validator(self) -> Draft202012Validator:
        schema = json.loads((SCHEMAS / "envelope.schema.json").read_text())
        return Draft202012Validator(schema)

    def _ping_validator(self) -> Draft202012Validator:
        schema = json.loads((SCHEMAS / "ping-pair.schema.json").read_text())
        return Draft202012Validator(schema)

    def test_fixtures_validate_against_schemas(self) -> None:
        env_validator = self._envelope_validator()
        ping_validator = self._ping_validator()
        for name in ("ping-request.json", "ping-response.json"):
            doc = load_fixture(name)
            env_validator.validate(doc)
            ping_validator.validate(doc["payload"])

    def test_schema_rejects_extra_envelope_fields(self) -> None:
        doc = load_fixture("ping-request.json")
        doc["extra"] = "field"
        with pytest.raises(Exception):
            self._envelope_validator().validate(doc)

    def test_schema_rejects_missing_sent_at(self) -> None:
        doc = load_fixture("ping-request.json")
        del doc["sent_at"]
        with pytest.raises(Exception):
            self._envelope_validator().validate(doc)
