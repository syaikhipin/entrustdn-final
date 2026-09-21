"""Pin the Python-side contract types to the golden fixtures in /contract.

The fixtures are the single source of truth for the Go↔Agent message contract
(Seam 2, agent side). If the fixtures and the Pydantic models drift, this
suite fails — same deal as backend/internal/contract/contract_test.go.
"""

import json
from pathlib import Path

import pytest
from jsonschema import Draft202012Validator

from thresh_agent.contract import CONTRACT_VERSION, Envelope, PingRequest, PingResponse

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


class TestSchemaEnforcement:
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
