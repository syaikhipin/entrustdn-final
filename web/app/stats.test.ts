// The web side of ticket 16: the /admin/stats document parser — requests
// over time, status breakdowns, Ledger kind flows, channel activity, and
// module usage. All expected values are literals; unknown shapes throw.
import { describe, expect, it } from "vitest";
import { formatCredits, parseStatsSnapshot } from "./stats";

// A /admin/stats document exactly as the Go backend serializes it.
const statsDoc = {
  requests_over_time: [
    { day: "2026-09-28", count: 1 },
    { day: "2026-09-29", count: 2 },
  ],
  request_statuses: [
    { status: "clarified", count: 2 },
    { status: "clarifying", count: 1 },
  ],
  collection_statuses: [{ status: "completed", count: 1 }],
  credits_flow: [
    { kind: "inference_charge", in_micros: 4_000_000, out_micros: 0, movements: 1 },
    { kind: "top_up", in_micros: 0, out_micros: 100_000_000, movements: 1 },
  ],
  channels: [
    { channel: "telegram", conversations: 1, turns: 2 },
    { channel: "whatsapp", conversations: 1, turns: 4 },
  ],
  modules: {
    agent_skill: 2,
    process_template: 1,
    connector: 0,
    system_wide: 1,
    private: 2,
  },
};

describe("parseStatsSnapshot", () => {
  it("reads the full dashboard document", () => {
    const s = parseStatsSnapshot(statsDoc);
    expect(s.requestsOverTime).toEqual([
      { day: "2026-09-28", count: 1 },
      { day: "2026-09-29", count: 2 },
    ]);
    expect(s.requestStatuses).toEqual([
      { status: "clarified", count: 2 },
      { status: "clarifying", count: 1 },
    ]);
    expect(s.collectionStatuses).toEqual([{ status: "completed", count: 1 }]);
    expect(s.creditsFlow).toEqual([
      { kind: "inference_charge", inMicros: 4_000_000, outMicros: 0, movements: 1 },
      { kind: "top_up", inMicros: 0, outMicros: 100_000_000, movements: 1 },
    ]);
    expect(s.channels).toEqual([
      { channel: "telegram", conversations: 1, turns: 2 },
      { channel: "whatsapp", conversations: 1, turns: 4 },
    ]);
    expect(s.modules).toEqual({
      agentSkill: 2,
      processTemplate: 1,
      connector: 0,
      systemWide: 1,
      private: 2,
    });
  });

  it("reads a zero-state document for an empty platform", () => {
    const s = parseStatsSnapshot({
      requests_over_time: [],
      request_statuses: [],
      collection_statuses: [],
      credits_flow: [],
      channels: [],
      modules: { agent_skill: 0, process_template: 0, connector: 0, system_wide: 0, private: 0 },
    });
    expect(s.requestsOverTime).toEqual([]);
    expect(s.creditsFlow).toEqual([]);
    expect(s.modules.agentSkill).toBe(0);
  });

  it("refuses a missing section", () => {
    const doc = { ...statsDoc } as Record<string, unknown>;
    delete doc["credits_flow"];
    expect(() => parseStatsSnapshot(doc)).toThrow(/credits_flow/);
  });

  it("refuses a non-array section", () => {
    expect(() =>
      parseStatsSnapshot({ ...statsDoc, channels: "whatsapp" })
    ).toThrow(/channels/);
  });

  it("refuses a malformed day", () => {
    expect(() =>
      parseStatsSnapshot({
        ...statsDoc,
        requests_over_time: [{ day: "Sept 28", count: 1 }],
      })
    ).toThrow(/YYYY-MM-DD/);
  });

  it("refuses negative counts", () => {
    expect(() =>
      parseStatsSnapshot({ ...statsDoc, modules: { ...statsDoc.modules, agent_skill: -1 } })
    ).toThrow(/non-negative/);
  });

  it("refuses non-integer micros", () => {
    expect(() =>
      parseStatsSnapshot({
        ...statsDoc,
        credits_flow: [{ kind: "top_up", in_micros: 1.5, out_micros: 0, movements: 1 }],
      })
    ).toThrow(/non-negative integer/);
  });
});

describe("formatCredits", () => {
  it("renders micros as credits", () => {
    expect(formatCredits(100_000_000)).toBe("100");
    expect(formatCredits(4_250_000)).toBe("4.25");
  });
});
