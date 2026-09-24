// Parser tests for the taxonomy & catalog documents (ticket 06): the
// frontend half of the backend contract — unknown shapes throw rather than
// render wrong, and ADR 0006 holds at the parse boundary too.

import { describe, expect, it } from "vitest";
import {
  formatConfidence,
  parseCatalog,
  parseCategory,
  parseFacets,
  parseTerm,
  parseTermList,
} from "./taxonomy";

const term = {
  id: "t1",
  category: "crop",
  value: "dairy",
  label: "Dairy",
  keywords: ["dairy", "friesian"],
  created_at: "2026-09-23T10:00:00Z",
};

describe("parseCategory", () => {
  it("accepts the six axes", () => {
    for (const c of ["crop", "region", "growth_stage", "intervention", "outcome", "data_type"]) {
      expect(parseCategory(c)).toBe(c);
    }
  });

  it("refuses unknown axes", () => {
    expect(() => parseCategory("livestock")).toThrow();
    expect(() => parseCategory(undefined)).toThrow();
  });
});

describe("parseTerm", () => {
  it("parses a seeded term", () => {
    const got = parseTerm(term);
    expect(got).toEqual({
      id: "t1",
      category: "crop",
      value: "dairy",
      label: "Dairy",
      keywords: ["dairy", "friesian"],
      createdAt: "2026-09-23T10:00:00Z",
    });
  });

  it("refuses missing identity and keywords", () => {
    expect(() => parseTerm({ ...term, id: "" })).toThrow(/no id/);
    expect(() => parseTerm({ ...term, category: "mood" })).toThrow(/unknown taxonomy category/);
    expect(() => parseTerm({ ...term, keywords: undefined })).toThrow(/keywords array/);
  });
});

describe("parseTermList", () => {
  it("maps the terms array", () => {
    expect(parseTermList({ terms: [term] })).toHaveLength(1);
    expect(() => parseTermList({ nope: [] })).toThrow(/terms array/);
  });
});

describe("parseFacets", () => {
  it("parses counts and sorts them descending", () => {
    const got = parseFacets({
      crop: [
        { term_id: "t-beef", value: "beef", label: "Beef", count: 1 },
        { term_id: "t-dairy", value: "dairy", label: "Dairy", count: 2 },
      ],
    });
    expect(got.crop).toEqual([
      { termId: "t-dairy", value: "dairy", label: "Dairy", count: 2 },
      { termId: "t-beef", value: "beef", label: "Beef", count: 1 },
    ]);
    expect(got.crop).toHaveLength(2);
    expect(got.crop![0]!.count).toBe(2);
  });

  it("refuses bad counts and unknown axes", () => {
    expect(() =>
      parseFacets({ crop: [{ term_id: "t", value: "v", label: "L", count: 1.5 }] }),
    ).toThrow(/count/);
    expect(() => parseFacets({ mood: [] })).toThrow(/unknown taxonomy category/);
  });
});

describe("parseCatalog", () => {
  const entry = {
    id: "a1",
    name: "Herd registry",
    description: "spring batch",
    size_bytes: 42,
    format: "csv",
    pipeline: ["anonymize"],
    provenance: { source: "co-op", collected_at: null, notes: "" },
    categories: [
      {
        category: "crop",
        term_id: "t-dairy",
        value: "dairy",
        label: "Dairy",
        confidence: 0.75,
        source: "classifier",
        assigned_at: "2026-09-23T10:00:00Z",
      },
    ],
    cached_price_micros: 2_500_000,
    created_at: "2026-09-23T10:00:00Z",
  };

  it("parses entries, facets, and the cached price", () => {
    const got = parseCatalog({ assets: [entry], facets: { crop: [{ term_id: "t-dairy", value: "dairy", label: "Dairy", count: 1 }] } });
    expect(got.entries).toHaveLength(1);
    expect(got.entries[0]!.cachedPriceMicros).toBe(2_500_000);
    expect(got.entries[0]!.categories[0]!.termId).toBe("t-dairy");
    expect(got.entries[0]!.provenance.source).toBe("co-op");
    expect(got.facets.crop).toHaveLength(1);
  });

  it("renders an empty catalog (nothing uploaded yet)", () => {
    const got = parseCatalog({ assets: [], facets: {} });
    expect(got.entries).toEqual([]);
    expect(got.facets).toEqual({});
  });

  it("refuses leaked internals — ADR 0006 and owner ids", () => {
    expect(() => parseCatalog({ assets: [{ ...entry, object_key: "assets/org-1/a1" }], facets: {} })).toThrow(
      /object_key/,
    );
    expect(() => parseCatalog({ assets: [{ ...entry, org_id: "org-1" }], facets: {} })).toThrow(/org_id/);
  });

  it("refuses bad prices and assignments", () => {
    expect(() => parseCatalog({ assets: [{ ...entry, cached_price_micros: -1 }], facets: {} })).toThrow(
      /cached_price_micros/,
    );
    expect(() =>
      parseCatalog({ assets: [{ ...entry, categories: [{ ...entry.categories[0], confidence: 2 }] }], facets: {} }),
    ).toThrow(/confidence/);
  });
});

describe("formatConfidence", () => {
  it("renders percent stamps", () => {
    expect(formatConfidence(0.62)).toBe("62%");
    expect(formatConfidence(1)).toBe("100%");
    expect(formatConfidence(0.075)).toBe("8%");
  });
});
