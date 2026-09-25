"""The Member conversation graph (ticket 11, ADR 0001's core scenario).

The agent walks a Member through a scripted question list over a Channel,
pausing on a LangGraph checkpoint after every question — the pause-for-days
interrupt — and resuming mid-thread from the checkpoint (or, cold, from the
thread the backend re-sends) when the Member replies. Over-survey
protection: questions stop when the list is exhausted, when the Member says
done/stop, or when the answer cap is hit.

The model is not involved in this ticket's flow: the questions come from
the backend, and the agent's job is faithful pacing, delivery, and
durability. (Model-driven clarification is ticket 07's web-chat loop; the
two coexist.)
"""

import pytest

from thresh_agent.member_graph import (
    ANSWER_CAP,
    DONE_WORDS,
    STOP_WORDS,
    MemberConversation,
    checkpointer_from_env,
)


def make_conv(questions=None, checkpointer=None) -> MemberConversation:
    return MemberConversation(
        questions=questions
        or ["What crop did you plant this season?", "How many hectares?"],
        checkpointer=checkpointer,
    )


def test_first_turn_asks_the_first_question() -> None:
    conv = make_conv()

    result = conv.start("conv-1")

    assert result.question == "What crop did you plant this season?"
    assert result.status == "awaiting_member"
    assert result.done is False


def test_reply_records_answer_and_asks_next_question() -> None:
    conv = make_conv()
    conv.start("conv-1")

    result = conv.reply("conv-1", "Spring barley, twelve hectares")

    assert result.answers == ["Spring barley, twelve hectares"]
    assert result.question == "How many hectares?"
    assert result.status == "awaiting_member"


def test_checkpoint_survives_a_fresh_object() -> None:
    """Durable pause (ADR 0001): a brand-new conversation object — the
    restarted agent process — resumes mid-thread from the shared
    checkpointer. InMemorySaver serializes threads by ID, which is exactly
    the contract; the Postgres saver swaps in via env for production."""
    checkpointer = shared_in_memory_saver()
    conv = make_conv(checkpointer=checkpointer)
    conv.start("conv-88")
    conv.reply("conv-88", "spring barley")

    # The restart: a fresh object (and fresh graph) over the same saver.
    reborn = make_conv(checkpointer=checkpointer)
    result = reborn.reply("conv-88", "twelve hectares")

    assert result.answers == ["spring barley", "twelve hectares"]
    assert result.question is None  # out of questions
    assert result.status == "completed"
    assert result.done is True


def test_replay_rebuilds_a_cold_thread() -> None:
    """Cold start (no checkpoint here): replay() re-walks the durable
    thread the backend re-sends, so the next reply lands exactly where a
    live conversation would have paused."""
    conv = make_conv()
    conv.replay("conv-99", ["spring barley"])

    result = conv.reply("conv-99", "twelve hectares")

    assert result.answers == ["spring barley", "twelve hectares"]
    assert result.status == "completed"


def test_member_saying_done_completes_early() -> None:
    conv = make_conv()
    conv.start("conv-1")

    result = conv.reply("conv-1", "that's all, no more")

    assert result.done is True
    assert result.status == "completed"
    assert result.question is None


def test_a_sentence_mentioning_a_done_word_is_an_answer() -> None:
    """The done matcher is for sign-offs, not answers that happen to
    contain one: 'finished the harvest' is data about the farm, and the
    survey must keep its next question."""
    conv = make_conv()
    conv.start("conv-1")

    result = conv.reply("conv-1", "finished the harvest")

    assert result.done is False
    assert result.status == "awaiting_member"
    assert result.answers == ["finished the harvest"]
    assert result.question is not None

    result = conv.reply("conv-1", "Spring barley, mostly")
    assert result.answers == ["finished the harvest", "Spring barley, mostly"]


def test_member_saying_stop_marks_declined() -> None:
    conv = make_conv()
    conv.start("conv-1")

    result = conv.reply("conv-1", "STOP")

    assert result.status == "stopped"
    assert result.done is True


def test_answer_cap_stops_the_survey() -> None:
    """A question list longer than the cap truncates: after ANSWER_CAP
    answers the survey stops — the Member is not kept on the hook."""
    questions = [f"Question {i}?" for i in range(ANSWER_CAP + 1)]
    conv = make_conv(questions=questions)
    conv.start("conv-1")

    result = None
    for i in range(ANSWER_CAP):
        result = conv.reply("conv-1", f"answer {i}")
        assert result.status == "awaiting_member" or i == ANSWER_CAP - 1

    # The last in-cap answer ends the survey before the final question.
    assert result is not None
    assert result.status == "stopped"
    assert result.done is True
    assert len(result.answers) == ANSWER_CAP


def test_stop_done_words_are_deterministic() -> None:
    assert "stop" in STOP_WORDS and "unsubscribe" in STOP_WORDS
    assert "done" in DONE_WORDS and "no more" in DONE_WORDS


def test_checkpointer_from_env_fails_loudly_on_a_dead_url(monkeypatch) -> None:
    # Eager connect: a bad checkpoint URL must fail at boot, not at the
    # first resume days later. Port 1 on localhost refuses immediately.
    monkeypatch.setenv("THRESH_CHECKPOINT_URL", "postgresql://x:y@localhost:1/z")
    import psycopg
    import pytest as _pytest

    with _pytest.raises(psycopg.OperationalError):
        checkpointer_from_env()


def test_checkpointer_from_env_defaults_to_memory() -> None:
    import os

    monkeypatch = pytest.MonkeyPatch()
    monkeypatch.delenv("THRESH_CHECKPOINT_URL", raising=False)
    checkpointer = checkpointer_from_env()
    assert checkpointer is not None
    monkeypatch.undo()


# The restart test's saver: one InMemorySaver instance shared by both
# conversation objects — the same deal a shared Postgres checkpoint store
# gives two agent processes.
def shared_in_memory_saver():
    from langgraph.checkpoint.memory import InMemorySaver

    return InMemorySaver()
