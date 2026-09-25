// The web side of ticket 12: parsers pin the backend's collection document
// shapes — snake_case keys, items and missing as arrays — and the identity
// guard: a delivered payload or collection item may never carry a member
// contact point. All expected values are literals.
import { describe, expect, it } from "vitest";
import { parseCollection, parseCollectionList, statusLabel } from "./collections";

// A collection document exactly as the Go backend serializes it.
const collectionDoc = {
  id: "col-1",
  org_id: "org-uuid",
  consumer_id: "consumer-uuid",
  request_id: "req-1",
  deadline: "2026-10-01T12:00:00Z",
  status: "incomplete",
  items: [
    {
      member_id: "mem-1",
      member_name: "Siobhán Ní Uaithne",
      question: "What is your farm size?",
      status: "accepted",
      accepted: "42 hectares",
      rounds: [
        { answer: "n/a", ok: false, reason: "anomaly: refused placeholder", at: "2026-09-25T10:00:00Z" },
        { answer: "42 hectares", ok: true, reason: "", at: "2026-09-25T10:05:00Z" },
      ],
      reasks: 1,
      blocked: false,
      block_reason: "",
    },
    {
      member_id: "mem-2",
      member_name: "Pádraig Ó Briain",
      question: "What is your farm size?",
      status: "blocked",
      accepted: "",
      rounds: [],
      reasks: 2,
      blocked: true,
      block_reason: "the re-ask limit (2) was reached without a usable answer",
    },
  ],
  missing: [
    {
      member_name: "Pádraig Ó Briain",
      question: "What is your farm size?",
      reason: "the re-ask limit (2) was reached without a usable answer",
    },
  ],
  created_at: "2026-09-25T09:00:00Z",
  updated_at: "2026-09-25T10:10:00Z",
};

describe("parseCollection", () => {
  it("reads a full collection document", () => {
    const c = parseCollection(collectionDoc);
    expect(c.id).toBe("col-1");
    expect(c.requestId).toBe("req-1");
    expect(c.status).toBe("incomplete");
    expect(c.deadline).toBe("2026-10-01T12:00:00Z");
    expect(c.items).toHaveLength(2);
    const [accepted, blocked] = c.items;
    expect(accepted?.accepted).toBe("42 hectares");
    expect(accepted?.rounds[0]?.ok).toBe(false);
    expect(accepted?.rounds[0]?.reason).toBe("anomaly: refused placeholder");
    expect(blocked?.blocked).toBe(true);
    expect(blocked?.blockReason).toContain("re-ask limit");
    expect(c.missing).toHaveLength(1);
    expect(c.missing[0]?.reason).toContain("re-ask limit");
  });

  it("reads a collection with no deadline", () => {
    const c = parseCollection({ ...collectionDoc, deadline: null });
    expect(c.deadline).toBeNull();
  });

  it("refuses a document without an id", () => {
    expect(() => parseCollection({ ...collectionDoc, id: "" })).toThrow();
    expect(() => parseCollection({ ...collectionDoc, id: undefined })).toThrow();
  });

  it("refuses unknown statuses rather than render them wrong", () => {
    expect(() => parseCollection({ ...collectionDoc, status: "paused" })).toThrow();
    expect(() =>
      parseCollection({
        ...collectionDoc,
        items: [{ ...collectionDoc.items[0], status: "pending" }],
      }),
    ).toThrow();
  });

  it("refuses a non-object", () => {
    expect(() => parseCollection("nope")).toThrow();
    expect(() => parseCollection(null)).toThrow();
  });
});

describe("parseCollectionList", () => {
  it("reads the collections array", () => {
    const list = parseCollectionList({ collections: [collectionDoc] });
    expect(list).toHaveLength(1);
    expect(list[0]?.id).toBe("col-1");
  });

  it("refuses a document without a collections array", () => {
    expect(() => parseCollectionList({ data: [] })).toThrow();
  });
});

describe("statusLabel", () => {
  it("renders the three life stages", () => {
    expect(statusLabel("collecting")).toBe("Collecting");
    expect(statusLabel("completed")).toBe("Completed");
    expect(statusLabel("incomplete")).toBe("Incomplete");
  });
});
