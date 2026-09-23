// The web side of ticket 06: the taxonomy & catalog API client and parsers
// pinning the backend's document shapes. Parsers are the frontend half of
// the contract — unknown shapes throw rather than render wrong. The
// catalog's documents expose the asset's public face only; an object_key
// (ADR 0006) or an owner id in a catalog row throws outright.

import { apiCall, authedHeader, errFrom } from "~/api";

export { errFrom };

// The six fixed category axes (ticket 06).
export const TAXONOMY_CATEGORIES = [
  "crop",
  "region",
  "growth_stage",
  "intervention",
  "outcome",
  "data_type",
] as const;

export type TaxonomyCategory = (typeof TAXONOMY_CATEGORIES)[number];

export interface Term {
  id: string;
  category: TaxonomyCategory;
  value: string;
  label: string;
  keywords: string[];
  createdAt: string;
}

export interface Assignment {
  category: TaxonomyCategory;
  termId: string;
  value: string;
  label: string;
  confidence: number;
  source: string;
  assignedAt: string;
}

export interface FacetValue {
  termId: string;
  value: string;
  label: string;
  count: number;
}

export type Facets = Partial<Record<TaxonomyCategory, FacetValue[]>>;

export interface CatalogEntry {
  id: string;
  name: string;
  description: string;
  sizeBytes: number;
  format: string;
  pipeline: string[];
  provenance: {
    source: string;
    collectedAt: string | null;
    notes: string;
  };
  categories: Assignment[];
  cachedPriceMicros: number;
  createdAt: string;
}

export interface Catalog {
  entries: CatalogEntry[];
  facets: Facets;
}

// --- Parsers ---

// parseCategory reads one category value, refusing unknown axes.
export function parseCategory(doc: unknown): TaxonomyCategory {
  if (typeof doc !== "string" || !TAXONOMY_CATEGORIES.includes(doc as TaxonomyCategory))
    throw new Error(`unknown taxonomy category: ${String(doc)}`);
  return doc as TaxonomyCategory;
}

export function parseTerm(doc: unknown): Term {
  if (typeof doc !== "object" || doc === null) throw new Error("term is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.id !== "string" || d.id === "") throw new Error("term carries no id");
  const category = parseCategory(d.category);
  if (typeof d.value !== "string" || d.value === "") throw new Error("term carries no value");
  if (typeof d.label !== "string" || d.label === "") throw new Error("term carries no label");
  if (!Array.isArray(d.keywords)) throw new Error("term carries no keywords array");
  return {
    id: d.id,
    category,
    value: d.value,
    label: d.label,
    keywords: d.keywords.map(String),
    createdAt: String(d.created_at ?? ""),
  };
}

export function parseTermList(doc: unknown): Term[] {
  if (typeof doc !== "object" || doc === null) throw new Error("term list is not an object");
  const d = doc as Record<string, unknown>;
  if (!Array.isArray(d.terms)) throw new Error("term list carries no terms array");
  return d.terms.map(parseTerm);
}

export function parseAssignment(doc: unknown): Assignment {
  if (typeof doc !== "object" || doc === null) throw new Error("assignment is not an object");
  const d = doc as Record<string, unknown>;
  const category = parseCategory(d.category);
  if (typeof d.term_id !== "string" || d.term_id === "") throw new Error("assignment carries no term_id");
  if (typeof d.value !== "string" || d.value === "") throw new Error("assignment carries no value");
  if (typeof d.label !== "string" || d.label === "") throw new Error("assignment carries no label");
  if (typeof d.confidence !== "number" || d.confidence < 0 || d.confidence > 1)
    throw new Error("assignment confidence is not in [0,1]");
  return {
    category,
    termId: d.term_id,
    value: d.value,
    label: d.label,
    confidence: d.confidence,
    source: String(d.source ?? ""),
    assignedAt: String(d.assigned_at ?? ""),
  };
}

export function parseFacets(doc: unknown): Facets {
  if (typeof doc !== "object" || doc === null) throw new Error("facets document is not an object");
  const out: Facets = {};
  for (const [category, values] of Object.entries(doc as Record<string, unknown>)) {
    const cat = parseCategory(category);
    if (!Array.isArray(values)) throw new Error(`facet ${category} carries no values array`);
    out[cat] = values.map((v) => {
      if (typeof v !== "object" || v === null) throw new Error("facet value is not an object");
      const f = v as Record<string, unknown>;
      if (typeof f.value !== "string" || f.value === "") throw new Error("facet value carries no value");
      if (typeof f.label !== "string" || f.label === "") throw new Error("facet value carries no label");
      if (typeof f.count !== "number" || !Number.isInteger(f.count) || f.count < 0)
        throw new Error("facet value count is not a non-negative integer");
      return {
        termId: String(f.term_id ?? ""),
        value: f.value,
        label: f.label,
        count: f.count,
      } satisfies FacetValue;
    });
    // Counts descend, ties by value — the backend's contract, pinned so a
    // reordered facet list can't silently break the filtering UI.
    out[cat]!.sort((a, b) => b.count - a.count || a.value.localeCompare(b.value));
  }
  return out;
}

export function parseCatalog(doc: unknown): Catalog {
  if (typeof doc !== "object" || doc === null) throw new Error("catalog document is not an object");
  const d = doc as Record<string, unknown>;
  if (!Array.isArray(d.assets)) throw new Error("catalog document carries no assets array");
  const entries = d.assets.map((raw): CatalogEntry => {
    if (typeof raw !== "object" || raw === null) throw new Error("catalog entry is not an object");
    const e = raw as Record<string, unknown>;
    if (typeof e.id !== "string" || e.id === "") throw new Error("catalog entry carries no id");
    if (typeof e.name !== "string") throw new Error("catalog entry carries no name");
    if (typeof e.size_bytes !== "number" || !Number.isInteger(e.size_bytes) || e.size_bytes < 0)
      throw new Error("catalog entry size_bytes is not a non-negative integer");
    if (typeof e.format !== "string" || e.format === "") throw new Error("catalog entry carries no format");
    if (!Array.isArray(e.pipeline)) throw new Error("catalog entry carries no pipeline record");
    if ("object_key" in e) throw new Error("catalog entry leaks object_key (ADR 0006 violation)");
    if ("org_id" in e) throw new Error("catalog entry leaks org_id");
    if (typeof e.cached_price_micros !== "number" || !Number.isInteger(e.cached_price_micros) || e.cached_price_micros < 0)
      throw new Error("catalog entry cached_price_micros is not a non-negative integer");
    if (!Array.isArray(e.categories)) throw new Error("catalog entry carries no categories array");
    const prov = (typeof e.provenance === "object" && e.provenance !== null ? e.provenance : {}) as Record<string, unknown>;
    return {
      id: e.id,
      name: e.name,
      description: typeof e.description === "string" ? e.description : "",
      sizeBytes: e.size_bytes,
      format: e.format,
      pipeline: e.pipeline.map(String),
      provenance: {
        source: typeof prov.source === "string" ? prov.source : "",
        collectedAt: typeof prov.collected_at === "string" ? prov.collected_at : null,
        notes: typeof prov.notes === "string" ? prov.notes : "",
      },
      categories: e.categories.map(parseAssignment),
      cachedPriceMicros: e.cached_price_micros,
      createdAt: String(e.created_at ?? ""),
    };
  });
  return { entries, facets: parseFacets(d.facets) };
}

// --- Formatting ---

// formatConfidence renders the classifier's stamp: 0.62 → "62%".
export function formatConfidence(c: number): string {
  return `${Math.round(c * 100)}%`;
}

// --- API client ---

// fetchTerms loads the public vocabulary (works signed out — facet labels
// render before login too).
export async function fetchTerms(backend: string): Promise<Term[]> {
  const doc = await apiCall<unknown>(backend, "/api/v1/taxonomy/terms", {});
  return parseTermList(doc);
}

// createTerm adds a term (admin). The backend derives the slug value from
// the label when value is omitted.
export async function createTerm(
  backend: string,
  token: string,
  input: { category: TaxonomyCategory; value?: string; label: string; keywords?: string[] },
): Promise<Term> {
  const doc = await apiCall<{ term: unknown }>(backend, "/api/v1/admin/taxonomy/terms", {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({
      category: input.category,
      ...(input.value ? { value: input.value } : {}),
      label: input.label,
      keywords: input.keywords ?? [],
    }),
  });
  return parseTerm(doc.term);
}

// updateTerm rewrites a term's label and keywords (admin).
export async function updateTerm(
  backend: string,
  token: string,
  id: string,
  patch: { label: string; keywords?: string[] },
): Promise<Term> {
  const doc = await apiCall<{ term: unknown }>(backend, `/api/v1/admin/taxonomy/terms/${id}`, {
    method: "PATCH",
    headers: authedHeader(token),
    body: JSON.stringify({ label: patch.label, keywords: patch.keywords ?? [] }),
  });
  return parseTerm(doc.term);
}

// deleteTerm removes a term nothing references (admin). An in-use term gets
// a 409 whose message names the fix.
export async function deleteTerm(backend: string, token: string, id: string): Promise<void> {
  await apiCall(backend, `/api/v1/admin/taxonomy/terms/${id}`, {
    method: "DELETE",
    headers: authedHeader(token),
  });
}

// setCategories applies the owning org's corrections: the full desired set
// replaces what the record carried (a correction is the truth, not a patch).
export async function setCategories(
  backend: string,
  token: string,
  assetId: string,
  corrections: { category: TaxonomyCategory; termId: string }[],
): Promise<AssetLike> {
  const doc = await apiCall<{ asset: unknown }>(backend, `/api/v1/assets/${assetId}/categories`, {
    method: "PUT",
    headers: authedHeader(token),
    body: JSON.stringify({
      categories: corrections.map((c) => ({ category: c.category, term_id: c.termId })),
    }),
  });
  return doc.asset as AssetLike;
}

// The corrected asset rides back as the ticket-04 asset document (which now
// carries categories); the org page re-parses it through assets.parseAsset.
export type AssetLike = Record<string, unknown>;

// fetchCatalog loads the consumer catalog: entries with categories and the
// cached price, plus the facet directory.
export async function fetchCatalog(backend: string, token: string): Promise<Catalog> {
  const doc = await apiCall<unknown>(backend, "/api/v1/catalog", {
    headers: authedHeader(token),
  });
  return parseCatalog(doc);
}
