// The web side of ticket 04: the Data Asset API client and parsers pinning
// the backend's document shapes. Parsers are the frontend half of the
// contract — unknown shapes throw rather than render wrong. Uploads and
// downloads ride this backend only (ADR 0006: no presigned URLs, no bucket
// paths in any document — the parsers refuse an object_key outright).

import { apiCall, authedHeader, errFrom } from "~/api";

export { errFrom };

export interface Provenance {
  source: string;
  collectedAt: string | null;
  notes: string;
}

export interface Asset {
  id: string;
  name: string;
  description: string;
  sizeBytes: number;
  format: string;
  pipeline: string[];
  provenance: Provenance;
  createdAt: string;
  updatedAt: string;
}

// --- Parsers ---

function parseProvenance(doc: unknown): Provenance {
  if (typeof doc !== "object" || doc === null) throw new Error("provenance is not an object");
  const d = doc as Record<string, unknown>;
  return {
    source: typeof d.source === "string" ? d.source : "",
    collectedAt: typeof d.collected_at === "string" ? d.collected_at : null,
    notes: typeof d.notes === "string" ? d.notes : "",
  };
}

export function parseAsset(doc: unknown): Asset {
  if (typeof doc !== "object" || doc === null) throw new Error("asset is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.id !== "string" || d.id === "") throw new Error("asset carries no id");
  if (typeof d.name !== "string") throw new Error("asset carries no name");
  if (typeof d.size_bytes !== "number" || !Number.isInteger(d.size_bytes) || d.size_bytes < 0)
    throw new Error("asset size_bytes is not a non-negative integer");
  if (typeof d.format !== "string" || d.format === "") throw new Error("asset carries no format");
  if (!Array.isArray(d.pipeline)) throw new Error("asset carries no pipeline record");
  if ("object_key" in d) throw new Error("asset document leaks object_key (ADR 0006 violation)");
  return {
    id: d.id,
    name: d.name,
    description: typeof d.description === "string" ? d.description : "",
    sizeBytes: d.size_bytes,
    format: d.format,
    pipeline: d.pipeline.map(String),
    provenance: parseProvenance(d.provenance),
    createdAt: String(d.created_at ?? ""),
    updatedAt: String(d.updated_at ?? ""),
  };
}

export function parseAssetList(doc: unknown): Asset[] {
  if (typeof doc !== "object" || doc === null) throw new Error("asset list is not an object");
  const d = doc as Record<string, unknown>;
  if (!Array.isArray(d.assets)) throw new Error("asset list carries no assets array");
  return d.assets.map(parseAsset);
}

// --- Formatting ---

// formatBytes renders a size for the dashboard: 1536 → "1.5 KB".
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB"];
  let value = n;
  let unit = "B";
  for (const next of units) {
    if (value < 1024) break;
    value /= 1024;
    unit = next;
  }
  const digits = value < 10 ? 1 : 0;
  return `${value.toFixed(digits)} ${unit}`;
}

// formatDate renders an ISO timestamp for the dashboard, or "—" when empty.
export function formatDate(iso: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

// --- API client ---

// uploadAsset streams a File through the backend. The body is FormData so
// the browser streams the file; the response is the created Asset.
export async function uploadAsset(
  backend: string,
  token: string,
  input: {
    file: File;
    name: string;
    description: string;
    source: string;
    collectedAt: string;
    notes: string;
  },
): Promise<Asset> {
  const form = new FormData();
  // Text fields go in first: the backend streams the body and hands the
  // handler the first file part open — anything appended after the file
  // would be ignored, silently dropping the asset's metadata. Empty
  // strings are sent too so the backend records the fields as cleared.
  form.append("name", input.name);
  form.append("description", input.description);
  form.append("source", input.source);
  form.append("collected_at", input.collectedAt);
  form.append("notes", input.notes);
  form.append("file", input.file);
  const resp = await fetch(`${backend}/api/v1/assets`, {
    method: "POST",
    headers: authedHeader(token), // no Content-Type: the browser sets the multipart boundary
    body: form,
  });
  const doc = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new Error(errFrom(doc));
  return parseAsset((doc as Record<string, unknown>).asset);
}

// listAssets loads the org's dashboard contents.
export async function listAssets(backend: string, token: string): Promise<Asset[]> {
  const doc = await apiCall<unknown>(backend, "/api/v1/assets", {
    headers: authedHeader(token),
  });
  return parseAssetList(doc);
}

// updateAsset rewrites an Asset's metadata (never its bytes).
export async function updateAsset(
  backend: string,
  token: string,
  id: string,
  meta: {
    name: string;
    description: string;
    source: string;
    collectedAt: string;
    notes: string;
  },
): Promise<Asset> {
  const doc = await apiCall<{ asset: unknown }>(backend, `/api/v1/assets/${id}`, {
    method: "PATCH",
    headers: authedHeader(token),
    body: JSON.stringify({
      name: meta.name,
      description: meta.description,
      source: meta.source,
      collected_at: meta.collectedAt, // empty string clears the timestamp
      notes: meta.notes,
    }),
  });
  return parseAsset(doc.asset);
}

// deleteAsset removes the Asset — backend deletes blob and record.
export async function deleteAsset(backend: string, token: string, id: string): Promise<void> {
  await apiCall(backend, `/api/v1/assets/${id}`, {
    method: "DELETE",
    headers: authedHeader(token),
  });
}

// assetDownloadURL is the backend-streamed download path — opened via a
// plain link so the browser handles progress and saving.
export function assetDownloadURL(backend: string, id: string): string {
  return `${backend}/api/v1/assets/${id}/content`;
}
