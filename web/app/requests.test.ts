// The web side of ticket 07: parsers pin the backend's request document
// shapes and formatBudget pins the budget rendering. All expected values
// are literals — independent of the backend.
import { describe, expect, it } from "vitest";
import {
  formatBudget,
  chatTurn,
  parseRequest,
  createRequest,
} from "./requests";

// A /api/v1/requests document exactly as the Go backend serializes it:
// snake_case keys, the conversation oldest first, matches stamped.
const requestDoc = {
  id: "abc123",
  description: "Spring barley yields across Leinster for the 2026 season",
  format: "csv",
  quality_bar: "Farm-level records with provenance",
  budget_micros: 10_000_000,
  spent_micros: 550,
  status: "clarifying",
  messages: [
    { role: "consumer", body: "I need yield data", created_at: "2026-09-23T10:00:00Z" },
    { role: "agent", body: "Which counties?", created_at: "2026-09-23T10:00:01Z" },
  ],
  matches: [
    {
      asset_id: "asset-01",
      name: "Leinster barley yields 2025",
      reason: "catalog asset tagged spring barley",
      reported_at: "2026-09-23T10:00:01Z",
    },
  ],
  created_at: "2026-09-23T09:59:00Z",
  updated_at: "2026-09-23T10:00:01Z",
};

describe("parseRequest", () => {
  it("parses the backend document into camelCase", () => {
    const r = parseRequest(requestDoc);
    expect(r.id).toBe("abc123");
    expect(r.qualityBar).toBe("Farm-level records with provenance");
    expect(r.budgetMicros).toBe(10_000_000);
    expect(r.spentMicros).toBe(550);
    expect(r.status).toBe("clarifying");
    expect(r.messages).toEqual([
      { role: "consumer", body: "I need yield data", createdAt: "2026-09-23T10:00:00Z" },
      { role: "agent", body: "Which counties?", createdAt: "2026-09-23T10:00:01Z" },
    ]);
    expect(r.matches[0]).toEqual({
      assetId: "asset-01",
      name: "Leinster barley yields 2025",
      reason: "catalog asset tagged spring barley",
      reportedAt: "2026-09-23T10:00:01Z",
    });
  });

  it("accepts every documented status", () => {
    for (const status of ["clarifying", "clarified", "budget_exhausted"]) {
      expect(parseRequest({ ...requestDoc, status }).status).toBe(status);
    }
  });

  it("throws on an unknown status rather than render wrong", () => {
    expect(() => parseRequest({ ...requestDoc, status: "shipped" })).toThrow("unknown request status");
  });

  it("throws on a non-integer budget", () => {
    expect(() => parseRequest({ ...requestDoc, budget_micros: 1.5 })).toThrow("not an integer");
  });

  it("throws on an unknown message role", () => {
    expect(() =>
      parseRequest({
        ...requestDoc,
        messages: [{ role: "bot", body: "hi", created_at: "2026-09-23T10:00:00Z" }],
      }),
    ).toThrow("unknown role");
  });
});

describe("formatBudget", () => {
  it("renders the exact micro-credit math", () => {
    expect(formatBudget(550, 10_000_000)).toBe("550 of 10,000,000 µcr spent");
    expect(formatBudget(0, 1_000)).toBe("0 of 1,000 µcr spent");
  });
});

describe("chatTurn", () => {
  it("posts the message and parses the turn result", async () => {
    const calls: Array<{ path: string; init: RequestInit }> = [];
    const fetchMock = (async (input: string | URL, init?: RequestInit) => {
      calls.push({ path: String(input), init: init ?? {} });
      return new Response(
        JSON.stringify({
          request: requestDoc,
          reply: "Which counties?",
          clarified: false,
          charged_micros: 550,
        }),
        { status: 200 },
      );
    }) as typeof fetch;
    const original = globalThis.fetch;
    globalThis.fetch = fetchMock;
    try {
      const got = await chatTurn("http://backend.test", "tok", "abc123", "Which counties?");
      expect(calls).toHaveLength(1);
      const call = calls[0]!;
      expect(call.path).toBe("http://backend.test/api/v1/requests/abc123/chat");
      expect(JSON.parse(String(call.init.body))).toEqual({ message: "Which counties?" });
      expect(call.init.headers).toMatchObject({ Authorization: "Bearer tok" });
      expect(got.reply).toBe("Which counties?");
      expect(got.chargedMicros).toBe(550);
      expect(got.request.id).toBe("abc123");
    } finally {
      globalThis.fetch = original;
    }
  });
});

describe("createRequest", () => {
  it("posts the commission as snake_case and parses the request", async () => {
    const calls: Array<{ path: string; init: RequestInit }> = [];
    const fetchMock = (async (input: string | URL, init?: RequestInit) => {
      calls.push({ path: String(input), init: init ?? {} });
      return new Response(JSON.stringify({ request: requestDoc }), { status: 201 });
    }) as typeof fetch;
    const original = globalThis.fetch;
    globalThis.fetch = fetchMock;
    try {
      const got = await createRequest("http://backend.test", "tok", {
        description: "Spring barley yields",
        format: "csv",
        qualityBar: "Farm-level",
        budgetMicros: 10_000_000,
      });
      expect(calls).toHaveLength(1);
      const call = calls[0]!;
      expect(call.path).toBe("http://backend.test/api/v1/requests");
      expect(JSON.parse(String(call.init.body))).toEqual({
        description: "Spring barley yields",
        format: "csv",
        quality_bar: "Farm-level",
        budget_micros: 10_000_000,
      });
      expect(got.id).toBe("abc123");
    } finally {
      globalThis.fetch = original;
    }
  });
});
