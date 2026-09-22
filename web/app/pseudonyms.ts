// The web side of ticket 05: the Farmer Organization's view of its
// pseudonym map. The backend exposes kinds and opaque pseudonyms only —
// raw identifiers do not exist in any document (ADR 0005) — and the
// parser refuses a document that carries one. Erase is the GDPR surface:
// one call, org-scoped, irreversible.

import { apiCall, authedHeader, errFrom } from "~/api";

export { errFrom };

export interface PseudonymEntry {
  kind: string;
  pseudonym: string;
}

export interface PseudonymList {
  entries: PseudonymEntry[];
  total: number;
}

// --- Parsers ---

function parsePseudonymRow(doc: unknown): PseudonymEntry {
  if (typeof doc !== "object" || doc === null) throw new Error("pseudonym row is not an object");
  const d = doc as Record<string, unknown>;
  // ADR 0005 guard, as an allowlist: a row may carry only kind and
  // pseudonym. Anything else — especially a raw identifier field — is a
  // backend leak, refused at parse time.
  const allowed = new Set(["kind", "pseudonym"]);
  for (const key of Object.keys(d)) {
    if (!allowed.has(key)) throw new Error(`pseudonym row carries unexpected field "${key}" — ADR 0005 violation`);
  }
  if (typeof d.kind !== "string" || d.kind === "") throw new Error("pseudonym row carries no kind");
  if (typeof d.pseudonym !== "string" || d.pseudonym === "")
    throw new Error("pseudonym row carries no pseudonym token");
  return { kind: d.kind, pseudonym: d.pseudonym };
}

export function parsePseudonymList(doc: unknown): PseudonymList {
  if (typeof doc !== "object" || doc === null) throw new Error("pseudonym list is not an object");
  const d = doc as Record<string, unknown>;
  if (!Array.isArray(d.pseudonyms)) throw new Error("pseudonym list carries no pseudonyms array");
  if (typeof d.total !== "number" || !Number.isInteger(d.total) || d.total < 0)
    throw new Error("pseudonym list total is not a non-negative integer");
  return {
    entries: d.pseudonyms.map(parsePseudonymRow),
    total: d.total,
  };
}

// --- API client ---

// listPseudonyms loads the org's pseudonym map (kinds + tokens only).
export async function listPseudonyms(backend: string, token: string): Promise<PseudonymList> {
  const doc = await apiCall<unknown>(backend, "/api/v1/pseudonyms", {
    headers: authedHeader(token),
  });
  return parsePseudonymList(doc);
}

// erasePseudonyms wipes the org's whole pseudonym map — the GDPR erasure
// surface. Erased identifiers re-mint on next use.
export async function erasePseudonyms(backend: string, token: string): Promise<void> {
  await apiCall(backend, "/api/v1/pseudonyms", {
    method: "DELETE",
    headers: authedHeader(token),
  });
}
