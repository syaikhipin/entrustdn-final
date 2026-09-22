// The web side of ticket 04: parsers pin the backend's asset document
// shapes, formatBytes pins human rendering, and the ADR 0006 guard (no
// object_key anywhere) is enforced at parse time. All expected values are
// literals — independent of the backend.
import { describe, expect, it } from "vitest";
import { formatBytes, parseAsset, parseAssetList } from "./assets";

// An asset document exactly as the Go backend serializes it: snake_case
// keys, pipeline as an array of stage names.
const assetDoc = {
  id: "a1b2c3",
  name: "Herd registry",
  description: "Spring 2026 herd book",
  size_bytes: 1536,
  format: "csv",
  pipeline: ["anonymize"],
  provenance: {
    source: "Teagasc Moorepark",
    collected_at: "2026-04-15T09:00:00Z",
    notes: "validated with the co-op",
  },
  created_at: "2026-09-22T10:00:00Z",
  updated_at: "2026-09-22T11:00:00Z",
};

describe("parseAsset", () => {
  it("reads a full asset document", () => {
    const a = parseAsset(assetDoc);
    expect(a.id).toBe("a1b2c3");
    expect(a.name).toBe("Herd registry");
    expect(a.sizeBytes).toBe(1536);
    expect(a.format).toBe("csv");
    expect(a.pipeline).toEqual(["anonymize"]);
    expect(a.provenance.source).toBe("Teagasc Moorepark");
    expect(a.provenance.collectedAt).toBe("2026-04-15T09:00:00Z");
  });

  it("reads a document with no provenance collected_at", () => {
    const a = parseAsset({
      ...assetDoc,
      provenance: { source: "field notebook", collected_at: null, notes: "" },
    });
    expect(a.provenance.collectedAt).toBeNull();
  });

  it("refuses a document leaking object_key", () => {
    expect(() => parseAsset({ ...assetDoc, object_key: "assets/org-1/a1b2c3" })).toThrow();
  });

  it("refuses a document without a pipeline record", () => {
    const { pipeline, ...without } = assetDoc;
    expect(() => parseAsset(without)).toThrow();
  });

  it("refuses a non-integer size", () => {
    expect(() => parseAsset({ ...assetDoc, size_bytes: 1.5 })).toThrow();
  });
});

describe("parseAssetList", () => {
  it("reads the dashboard document", () => {
    const list = parseAssetList({ assets: [assetDoc] });
    expect(list).toHaveLength(1);
    expect(list[0].id).toBe("a1b2c3");
  });

  it("refuses a document without an assets array", () => {
    expect(() => parseAssetList({})).toThrow();
  });
});

describe("formatBytes", () => {
  it("renders bytes, KB, and MB", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1536)).toBe("1.5 KB");
    expect(formatBytes(1024 * 1024)).toBe("1.0 MB");
    expect(formatBytes(5 * 1024 * 1024)).toBe("5.0 MB");
  });
});
