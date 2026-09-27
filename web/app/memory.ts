// The web side of ticket 14: the backend's Memory Provider registry API
// client and the parser pinning its document shape — the admin registry of
// external recall services (mem0, Hindsight, supermemory, Honcho). A
// provider is a connection fact only: name + endpoint, never credentials —
// the agent connects over MCP with none. Parsers are the frontend half of
// the backend contract: unknown shapes throw rather than render wrong.

import { apiCall, authedHeader } from "~/api";

// MemoryProvider is one configured recall service: an identifier the
// contract carries and the MCP endpoint the agent dials.
export interface MemoryProvider {
  id: string;
  name: string;
  endpoint: string;
  createdAt: string;
  updatedAt: string;
}

export function parseMemoryProvider(doc: unknown): MemoryProvider {
  if (typeof doc !== "object" || doc === null) throw new Error("memory provider is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.id !== "string" || d.id === "") throw new Error("memory provider carries no id");
  if (typeof d.name !== "string" || d.name === "") throw new Error("memory provider carries no name");
  if (typeof d.endpoint !== "string" || d.endpoint === "")
    throw new Error("memory provider carries no endpoint");
  if (typeof d.created_at !== "string" || d.created_at === "")
    throw new Error("memory provider carries no created_at");
  if (typeof d.updated_at !== "string" || d.updated_at === "")
    throw new Error("memory provider carries no updated_at");
  return {
    id: d.id,
    name: d.name,
    endpoint: d.endpoint,
    createdAt: d.created_at,
    updatedAt: d.updated_at,
  };
}

// fetchMemoryProviders lists the configured providers, newest first (admin).
export async function fetchMemoryProviders(backend: string, token: string): Promise<MemoryProvider[]> {
  const doc = await apiCall<{ memory_providers?: unknown }>(backend, "/api/v1/admin/memory-providers", {
    headers: authedHeader(token),
  });
  if (!Array.isArray(doc.memory_providers)) throw new Error("memory providers document carries no memory_providers array");
  return doc.memory_providers.map(parseMemoryProvider);
}

// createMemoryProvider registers one provider (admin).
export async function createMemoryProvider(
  backend: string,
  token: string,
  body: { name: string; endpoint: string },
): Promise<MemoryProvider> {
  const doc = await apiCall<{ memory_provider?: unknown }>(backend, "/api/v1/admin/memory-providers", {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({ name: body.name, endpoint: body.endpoint }),
  });
  if (typeof doc.memory_provider !== "object" || doc.memory_provider === null)
    throw new Error("memory provider document carries no memory_provider");
  return parseMemoryProvider(doc.memory_provider);
}

// deleteMemoryProvider removes one provider by id (admin).
export async function deleteMemoryProvider(backend: string, token: string, id: string): Promise<void> {
  await apiCall<unknown>(backend, `/api/v1/admin/memory-providers/${encodeURIComponent(id)}`, {
    method: "DELETE",
    headers: authedHeader(token),
  });
}
