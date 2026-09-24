// The web side of ticket 08: the backend modules API client and the
// parsers pinning its document shapes. A Module is a versioned bundle —
// manifest (kind, version, A2A capability description) plus markdown
// and/or configuration. The capability description is shown, not run;
// parsers throw on unknown shapes rather than render wrong — same contract
// as requests.ts.

import { apiCall, authedHeader, errFrom } from "~/api";

export { errFrom };

export const MODULE_KINDS = ["agent_skill", "process_template", "connector"] as const;
export type ModuleKind = (typeof MODULE_KINDS)[number];

export interface StoredModule {
  id: string;
  moduleId: string;
  name: string;
  kind: ModuleKind;
  authorId: string;
  version: string;
  capability: string;
  content: string;
  config: string;
  systemWide: boolean;
  deprecated: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface ModuleGrant {
  moduleId: string;
  accountId: string;
  grantedAt: string;
}

function assertObject(doc: unknown, what: string): Record<string, unknown> {
  if (typeof doc !== "object" || doc === null) throw new Error(`${what} is not an object`);
  return doc as Record<string, unknown>;
}

function assertString(v: unknown, what: string): string {
  if (typeof v !== "string" || v === "") throw new Error(`${what} is missing`);
  return v;
}

function assertBool(v: unknown, what: string): boolean {
  if (typeof v !== "boolean") throw new Error(`${what} is not a boolean`);
  return v;
}

function parseKind(v: unknown): ModuleKind {
  if (typeof v !== "string" || !MODULE_KINDS.includes(v as ModuleKind))
    throw new Error(`unknown module kind: ${String(v)}`);
  return v as ModuleKind;
}

export function parseModule(doc: unknown): StoredModule {
  const d = assertObject(doc, "module");
  return {
    id: assertString(d.id, "version id"),
    moduleId: assertString(d.module_id, "module_id"),
    name: assertString(d.name, "module name"),
    kind: parseKind(d.kind),
    authorId: assertString(d.author_id, "module author_id"),
    version: assertString(d.version, "module version"),
    capability: assertString(d.capability, "module capability"),
    content: typeof d.content === "string" ? d.content : "",
    config: typeof d.config === "string" ? d.config : "",
    systemWide: assertBool(d.system_wide, "system_wide"),
    deprecated: assertBool(d.deprecated, "deprecated"),
    createdAt: assertString(d.created_at, "module created_at"),
    updatedAt: assertString(d.updated_at, "module updated_at"),
  };
}

export function parseModuleList(docs: unknown): StoredModule[] {
  if (!Array.isArray(docs)) throw new Error("module list is not an array");
  return docs.map(parseModule);
}

export const parseVersionList = parseModuleList;

export function parseGrantList(docs: unknown): ModuleGrant[] {
  if (!Array.isArray(docs)) throw new Error("grant list is not an array");
  return docs.map((doc) => {
    const d = assertObject(doc, "grant");
    return {
      moduleId: assertString(d.module_id, "grant module_id"),
      accountId: assertString(d.account_id, "grant account_id"),
      grantedAt: assertString(d.granted_at, "grant granted_at"),
    };
  });
}

// formatModule renders the manifest line for lists: "process_template ·
// v1.0.0".
export function formatModule(m: StoredModule): string {
  return `${m.kind} · v${m.version}`;
}

// --- API calls ---

export async function uploadModule(
  backend: string,
  token: string,
  input: {
    name: string;
    kind: ModuleKind;
    version: string;
    capability: string;
    content?: string;
    config?: string;
  },
): Promise<StoredModule> {
  const doc = await apiCall<{ module: unknown }>(backend, "/api/v1/modules", {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({
      name: input.name,
      kind: input.kind,
      version: input.version,
      capability: input.capability,
      content: input.content ?? "",
      config: input.config ?? "",
    }),
  });
  return parseModule(doc.module);
}

export async function fetchMyModules(backend: string, token: string): Promise<StoredModule[]> {
  const doc = await apiCall<{ modules: unknown[] }>(backend, "/api/v1/modules/mine", {
    headers: authedHeader(token),
  });
  return parseModuleList(doc.modules);
}

export async function fetchSystemModules(backend: string, token: string): Promise<StoredModule[]> {
  const doc = await apiCall<{ modules: unknown[] }>(backend, "/api/v1/modules/system", {
    headers: authedHeader(token),
  });
  return parseModuleList(doc.modules);
}

export async function fetchModuleVersions(
  backend: string,
  token: string,
  moduleId: string,
): Promise<StoredModule[]> {
  const doc = await apiCall<{ versions: unknown[] }>(
    backend,
    `/api/v1/modules/${moduleId}/versions`,
    { headers: authedHeader(token) },
  );
  return parseVersionList(doc.versions);
}

export async function publishModuleVersion(
  backend: string,
  token: string,
  moduleId: string,
  input: { version: string; capability: string; content?: string; config?: string },
): Promise<StoredModule> {
  const doc = await apiCall<{ module: unknown }>(
    backend,
    `/api/v1/modules/${moduleId}/versions`,
    {
      method: "POST",
      headers: authedHeader(token),
      body: JSON.stringify({
        version: input.version,
        capability: input.capability,
        content: input.content ?? "",
        config: input.config ?? "",
      }),
    },
  );
  return parseModule(doc.module);
}

export async function grantModule(
  backend: string,
  token: string,
  moduleId: string,
  email: string,
): Promise<void> {
  await apiCall(backend, `/api/v1/modules/${moduleId}/grants`, {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({ email }),
  });
}

export async function revokeModule(
  backend: string,
  token: string,
  moduleId: string,
  email: string,
): Promise<void> {
  await apiCall(backend, `/api/v1/modules/${moduleId}/grants/${encodeURIComponent(email)}`, {
    method: "DELETE",
    headers: authedHeader(token),
  });
}

export async function fetchModuleGrants(
  backend: string,
  token: string,
  moduleId: string,
): Promise<ModuleGrant[]> {
  const doc = await apiCall<{ grants: unknown[] }>(
    backend,
    `/api/v1/modules/${moduleId}/grants`,
    { headers: authedHeader(token) },
  );
  return parseGrantList(doc.grants);
}

export async function promoteModule(
  backend: string,
  token: string,
  moduleId: string,
  systemWide: boolean,
): Promise<void> {
  await apiCall(backend, `/api/v1/admin/modules/${moduleId}/promote`, {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({ system_wide: systemWide }),
  });
}

export async function deprecateModuleVersion(
  backend: string,
  token: string,
  moduleId: string,
  versionId: string,
  deprecated: boolean,
): Promise<StoredModule> {
  const doc = await apiCall<{ module: unknown }>(
    backend,
    `/api/v1/modules/${moduleId}/versions/${versionId}/deprecate`,
    {
      method: "POST",
      headers: authedHeader(token),
      body: JSON.stringify({ deprecated }),
    },
  );
  return parseModule(doc.module);
}
