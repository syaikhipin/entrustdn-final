"""The clarification loop (ticket 07, agent side).

The Domain Agent's job on each turn: check the catalog FIRST and report
existing assets that already answer the need, then ask the consumer the
next clarifying question. The loop is deterministic and the model call is
faked — what these tests pin is the loop's own logic: catalog-first
matching, the request ID echo, and the metered usage riding home.
"""

from thresh_agent.clarify import ClarificationLoop
from thresh_agent.contract import (
    CatalogAsset,
    ClarifyRequest,
    MeteredUsage,
)
from thresh_agent.gateway import Completion, FakeModelGateway


def catalog(*ids: str) -> list[CatalogAsset]:
    names = {
        "asset-01": ("Leinster spring barley yields 2025", "Farm-level barley yields, county tagged"),
        "asset-02": ("Munster dairy herd census", "Herd sizes by county"),
    }
    return [
        CatalogAsset(
            id=i,
            name=names.get(i, (i, ""))[0],
            description=names.get(i, ("", i))[1],
            cached_price_micros=5_000_000,
        )
        for i in ids
    ]


def clarify_request(**overrides) -> ClarifyRequest:
    payload = {
        "request_id": "req-1",
        "description": "Spring barley yields across Leinster",
        "format": "csv",
        "quality_bar": "",
        "budget_micros": 10_000_000,
        "spent_micros": 0,
        "message": "I need yield data",
        "history": [],
        "catalog": catalog("asset-01"),
    }
    payload.update(overrides)
    return ClarifyRequest(**payload)


class TestClarificationLoop:
    def test_reports_catalog_match_and_asks_next_question(self) -> None:
        gw = FakeModelGateway(
            text="Which counties exactly?", input_tokens=100, output_tokens=20
        )
        loop = ClarificationLoop(gateway=gw)

        resp = loop.turn(clarify_request())

        assert resp.request_id == "req-1"
        assert resp.reply == "Which counties exactly?"
        assert resp.clarified is False
        # Catalog first: the snapshot's barley-yields asset is reported.
        assert [m.asset_id for m in resp.matches] == ["asset-01"]
        assert resp.matches[0].name == "Leinster spring barley yields 2025"
        assert "barley" in resp.matches[0].reason
        # The model call is metered with the gateway's usage.
        assert resp.usage == MeteredUsage(
            model="thresh-fake-model", input_tokens=100,
            cached_input_tokens=40, output_tokens=20,
        )

    def test_match_requires_the_need_words_in_the_description(self) -> None:
        """The catalog check is keyword-based on the loop level: an asset
        whose description shares no word with the request's need is not
        reported as a match."""
        gw = FakeModelGateway()
        loop = ClarificationLoop(gateway=gw)

        resp = loop.turn(
            clarify_request(
                description="Monthly rainfall totals for Munster",
                catalog=catalog("asset-01"),  # barley yields — no overlap
            )
        )

        assert resp.matches == []

    def test_clarified_flag_comes_from_the_model_text(self) -> None:
        """The model marks a fully-specified need by starting its reply with
        the sentinel; the loop turns that into clarified=true and strips it
        from the consumer-facing reply."""
        gw = FakeModelGateway(
            text="CLARIFIED: csv, Leinster, 2026 spring barley, farm-level",
            output_tokens=25,
        )
        loop = ClarificationLoop(gateway=gw)

        resp = loop.turn(clarify_request())

        assert resp.clarified is True
        assert resp.reply == "csv, Leinster, 2026 spring barley, farm-level"
        assert not resp.reply.startswith("CLARIFIED")

    def test_prompt_carries_the_request_context_and_history(self) -> None:
        gw = FakeModelGateway()
        loop = ClarificationLoop(gateway=gw)
        req = clarify_request(
            history=[
                {"role": "consumer", "body": "Hello"},
                {"role": "agent", "body": "What do you need?"},
            ],
            spent_micros=550,
        )

        loop.turn(req)

        system, user = gw.calls[0]
        assert "Spring barley yields across Leinster" in system
        assert "csv" in system
        assert "spent" in system
        assert "asset-01" in system  # the catalog snapshot to check first
        assert "Hello" in user and "What do you need?" in user
        assert "I need yield data" in user

    def test_empty_catalog_means_no_matches(self) -> None:
        gw = FakeModelGateway()
        loop = ClarificationLoop(gateway=gw)

        resp = loop.turn(clarify_request(catalog=[]))

        assert resp.matches == []
        assert resp.reply  # still answers with the next question


def test_match_requires_more_than_one_shared_word() -> None:
    """One shared word is not a match: 'dairy census data' must not report
    the Munster dairy herd census asset on the strength of 'census' alone —
    the floor is at least two distinct shared words, so the consumer is
    never told an asset 'already answers the need' on thin overlap."""
    gw = FakeModelGateway()
    loop = ClarificationLoop(gateway=gw)

    resp = loop.turn(
        clarify_request(
            description="National agricultural census data",
            catalog=catalog("asset-02"),  # "Munster dairy herd census"
        )
    )

    assert resp.matches == []

    # Two shared words — 'dairy' and 'census' — still match.
    resp = loop.turn(
        clarify_request(
            description="Dairy census counts",
            catalog=catalog("asset-02"),
        )
    )
    assert [m.asset_id for m in resp.matches] == ["asset-02"]
