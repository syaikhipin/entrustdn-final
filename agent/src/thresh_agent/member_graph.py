"""The Member conversation graph (ticket 11) — ADR 0001's core scenario.

One LangGraph graph per conversation shape (the question list), one
checkpointed thread per conversation (the backend's conversation ID). After
every question the `ask` node returns `Command.goto("__copy__")`-free — it
simply ends; the `record` node *begins* with `interrupt()`, which pauses
the graph at a checkpoint until the Member's reply arrives as
`Command(resume=...)`. The pause lasts seconds or days; the checkpoint
survives process restarts when THRESH_CHECKPOINT_URL selects the Postgres
saver. When the backend resumes a thread the agent has no checkpoint for
(cold start), `rebuild()` replays the durable thread into a fresh graph
invocation, and the conversation continues seamlessly.

Over-survey protection (the floor of ticket 12's quality triggers): the
survey stops when the question list is exhausted, when the Member says a
done/stop word, or after ANSWER_CAP answers — whichever comes first. A
stopped or completed conversation asks nothing further.

The model gateway is not in this loop: the questions come scripted from the
backend, and this ticket's job is faithful pacing, delivery, and
durability. Model-driven clarification lives in ticket 07's web-chat loop.
"""

import os
import re
from dataclasses import dataclass, field
from typing_extensions import Annotated, TypedDict

from langgraph.checkpoint.memory import InMemorySaver
from langgraph.graph import START, StateGraph
from langgraph.types import Command, interrupt

# Replies that end the survey on the Member's terms.
STOP_WORDS = {"stop", "unsubscribe", "quit", "cancel"}
DONE_WORDS = {
    "done", "that's all", "thats all", "no more", "nothing else",
    "that's everything", "thats everything", "finished", "that is all",
}

# The over-survey cap: after this many answers the survey stops even if
# questions remain. Matches the backend's MaxQuestionsPerConversation.
ANSWER_CAP = 10


def _fragments(text: str) -> list[str]:
    """The reply split into sign-off candidates: punctuation separates
    them, case and surrounding space are normalized away."""
    return [f.strip() for f in re.split(r"[.,!?;:]+", text.strip().lower()) if f.strip()]


def _is_stop(text: str) -> bool:
    # Every fragment must be a stop word: "STOP", "stop, unsubscribe".
    fragments = _fragments(text)
    return bool(fragments) and all(f in STOP_WORDS for f in fragments)


def _is_done(text: str) -> bool:
    # Every fragment must be a done word: "that's all, no more" ends the
    # survey, while an answer that merely contains a done word ("finished
    # the harvest") is data about the farm and is recorded as one.
    fragments = _fragments(text)
    return bool(fragments) and all(f in DONE_WORDS for f in fragments)


def checkpointer_from_env():
    """Build the checkpointer from the environment.

    THRESH_CHECKPOINT_URL (a postgres:// URL) selects the Postgres saver —
    the production choice: checkpoints survive agent restarts. Without it,
    an in-memory saver serves dev and tests. All state in Thresh is
    env-configured (ADR 0007), and the checkpoint store is no exception.
    The caller owns setup; `setup_checkpointer` runs the saver's DDL.
    """
    url = os.environ.get("THRESH_CHECKPOINT_URL", "")
    if url:
        import psycopg
        from langgraph.checkpoint.postgres import PostgresSaver

        conn = psycopg.connect(url, autocommit=True, connect_timeout=5)
        return PostgresSaver(conn)
    return InMemorySaver()


def setup_checkpointer(checkpointer) -> None:
    """One-time setup (the Postgres saver needs its tables). In-memory
    savers are no-ops."""
    setup = getattr(checkpointer, "setup", None)
    if setup is not None:
        setup()


class MemberState(TypedDict, total=False):
    """State flowing through one conversation's graph."""

    questions: list[str]
    asked: int  # questions asked so far
    answers: Annotated[list[str], lambda a, b: (a or []) + (b or [])]
    status: str  # awaiting_member | completed | stopped
    # The message the graph is currently waiting to deliver on the Channel.
    pending_question: str


def _ask(state: MemberState) -> dict:
    """Ask the next question — or finish the survey. Termination: question
    list exhausted (completed), the Member opted out or hit the answer cap
    (stopped), or said done early (completed, set by the record node)."""
    asked = state.get("asked", 0)
    answers = state.get("answers", [])
    questions = state["questions"]
    status = state.get("status")

    if status in ("stopped", "completed"):
        return {"pending_question": "", "status": status}
    if asked >= len(questions):
        return {"pending_question": "", "status": "completed"}
    if len(answers) >= ANSWER_CAP:
        return {"pending_question": "", "status": "stopped"}

    question = questions[asked]
    return {"pending_question": question, "asked": asked + 1, "status": "awaiting_member"}


def _record(state: MemberState) -> dict | Command:
    """Pause here until the Member replies (interrupt → checkpoint); the
    resumed value is their message."""
    if not state.get("pending_question"):
        return {}  # nothing was asked: the graph is wrapping up

    member_message = interrupt({"question": state["pending_question"]})
    message = str(member_message).strip()

    if _is_stop(message):
        # Over-survey protection, member side: opt out ends it outright.
        return {"status": "stopped", "pending_question": ""}
    if _is_done(message):
        return {"status": "completed", "pending_question": ""}
    # Loop back to ask for the next question; ask sees the new answer count.
    return Command(goto="ask", update={"answers": [message]})


def build_member_graph(checkpointer=None):
    """Wire the conversation graph: ask → record —(resume)→ ask → ….
    The checkpointer is what makes the pause durable."""
    graph = StateGraph(MemberState)
    graph.add_node("ask", _ask)
    graph.add_node("record", _record)
    graph.add_edge(START, "ask")
    graph.add_edge("ask", "record")
    return graph.compile(checkpointer=checkpointer)


@dataclass
class TurnResult:
    """One turn's outcome: what to deliver on the Channel, and where the
    conversation now stands."""

    question: str | None
    answers: list[str] = field(default_factory=list)
    status: str = "awaiting_member"  # awaiting_member | completed | stopped
    done: bool = False


class MemberConversation:
    """Drives one conversation's checkpointed graph.

    thread_id is the backend's conversation ID, so backend and agent name
    the pause the same way. The checkpointer is shared across 'restarts':
    a fresh MemberConversation over the same checkpointer resumes
    mid-thread.
    """

    def __init__(self, questions: list[str], checkpointer=None) -> None:
        if not questions:
            raise ValueError("a conversation needs at least one question")
        self.questions = list(questions)
        self.checkpointer = checkpointer if checkpointer is not None else InMemorySaver()
        self._graph = build_member_graph(checkpointer=self.checkpointer)

    def start(self, thread_id: str) -> TurnResult:
        """Open the conversation: ask question one, then pause."""
        config = {"configurable": {"thread_id": thread_id}}
        state = self._graph.invoke(
            {"questions": self.questions, "answers": []}, config
        )
        return TurnResult(
            question=state.get("pending_question") or None,
            answers=list(state.get("answers", [])),
            status=state.get("status", "awaiting_member"),
            done=state.get("status") in ("completed", "stopped"),
        )

    def reply(self, thread_id: str, message: str) -> TurnResult:
        """Resume from the checkpoint with the Member's message."""
        config = {"configurable": {"thread_id": thread_id}}
        state = self._graph.invoke(Command(resume=message), config)
        return TurnResult(
            question=state.get("pending_question") or None,
            answers=list(state.get("answers", [])),
            status=state.get("status", "awaiting_member"),
            done=state.get("status") in ("completed", "stopped"),
        )

    def has_checkpoint(self, thread_id: str) -> bool:
        """Whether this conversation already has checkpoint state here."""
        config = {"configurable": {"thread_id": thread_id}}
        try:
            return self.checkpointer.get(config) is not None
        except Exception:
            return False

    def replay(self, thread_id: str, member_messages: list[str]) -> None:
        """Cold start: rebuild genuine checkpoint state by re-running the
        graph through the backend's durable member messages — the same
        code path a live conversation takes, so the pause lands exactly
        where the thread says it should. No delivery happens here; the
        agent only re-walks its own pacing."""
        config = {"configurable": {"thread_id": thread_id}}
        self._graph.invoke({"questions": self.questions, "answers": []}, config)
        for message in member_messages:
            self._graph.invoke(Command(resume=message), config)
