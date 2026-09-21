"""The agent's LangGraph runtime.

Ticket 01 establishes the tracer-bullet graph: one node that answers a ping.
Later tickets hang Request clarification, Channel conversations, and Memory
Provider recall off this graph — durable execution (pause-for-days/resume)
is why LangGraph is here (ADR 0001).
"""

from langgraph.graph import END, START, StateGraph
from typing_extensions import TypedDict


class PingState(TypedDict, total=False):
    """State flowing through the ping round trip."""

    nonce: str
    pong: bool
    agent_version: str


def answer_ping(state: PingState, agent_version: str = "dev") -> dict:
    """Answer a ping: echo the nonce and report the agent's version."""
    return {"nonce": state["nonce"], "pong": True, "agent_version": agent_version}


def build_ping_graph(agent_version: str = "dev"):
    """Wire the tracer-bullet graph: START → answer_ping → END."""
    graph = StateGraph(PingState)
    graph.add_node("answer_ping", lambda state: answer_ping(state, agent_version))
    graph.add_edge(START, "answer_ping")
    graph.add_edge("answer_ping", END)
    return graph.compile()
