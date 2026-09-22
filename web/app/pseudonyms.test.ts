// The web side of ticket 05: the pseudonym-map document parser pins the
// backend's shape (kinds + opaque tokens only — the raw identifier does
// not exist in any document), the ADR 0005 guard refuses anything that
// looks like a raw identifier or a map export, and the GDPR erase surface
// is one call.
import { describe, expect, it, vi } from "vitest";
import { erasePseudonyms, listPseudonyms, parsePseudonymList } from "./pseudonyms";

const listDoc = {
  pseudonyms: [
    { kind: "name", pseudonym: "a".repeat(32) },
    { kind: "email", pseudonym: "b".repeat(32) },
  ],
  total: 2,
};

describe("parsePseudonymList", () => {
  it("reads kinds and opaque pseudonyms with a total", () => {
    const { entries, total } = parsePseudonymList(listDoc);
    expect(total).toBe(2);
    expect(entries).toHaveLength(2);
    const first = entries[0]!;
    expect(first.kind).toBe("name");
    expect(first.pseudonym).toBe("a".repeat(32));
  });

  it("reads an empty map", () => {
    const { entries, total } = parsePseudonymList({ pseudonyms: [], total: 0 });
    expect(entries).toEqual([]);
    expect(total).toBe(0);
  });

  it("refuses a document without the pseudonyms array", () => {
    expect(() => parsePseudonymList({ total: 3 })).toThrow();
  });

  it("refuses a row missing its kind or token", () => {
    expect(() => parsePseudonymList({ pseudonyms: [{ pseudonym: "x".repeat(32) }], total: 1 })).toThrow();
    expect(() => parsePseudonymList({ pseudonyms: [{ kind: "name" }], total: 1 })).toThrow();
  });

  it("refuses a row carrying a raw identifier field (ADR 0005)", () => {
    expect(() =>
      parsePseudonymList({ pseudonyms: [{ kind: "name", pseudonym: "x".repeat(32), value: "Mary Byrne" }], total: 1 }),
    ).toThrow();
  });
});

describe("client calls", () => {
  it("listPseudonyms GETs the map endpoint", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(listDoc),
    });
    vi.stubGlobal("fetch", fetchMock);
    try {
      const got = await listPseudonyms("http://backend.test", "tok-1");
      expect(got.entries).toHaveLength(2);
      expect(fetchMock).toHaveBeenCalledWith(
        "http://backend.test/api/v1/pseudonyms",
        expect.objectContaining({ headers: expect.objectContaining({ Authorization: "Bearer tok-1" }) }),
      );
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("erasePseudonyms DELETEs the map endpoint", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ erased: true }),
    });
    vi.stubGlobal("fetch", fetchMock);
    try {
      await erasePseudonyms("http://backend.test", "tok-1");
      expect(fetchMock).toHaveBeenCalledWith("http://backend.test/api/v1/pseudonyms", {
        method: "DELETE",
        headers: expect.objectContaining({ Authorization: "Bearer tok-1" }),
      });
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
