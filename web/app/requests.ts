// The web side of ticket 07: the backend requests API client and the
// parsers pinning its document shapes. A Request's status view carries the
// commission, the web-chat conversation, the reported existing-Asset
// matches, and the budget math. Parsers throw on unknown shapes rather than
// render wrong — same contract as credits.ts.

import { apiCall, authedHeader, errFrom } from "~/api";

export { errFrom };

export const REQUEST_STATUSES = ["clarifying", "clarified", "budget_exhausted"] as const;
export type RequestStatus = (typeof REQUEST_STATUSES)[number];

export interface ChatMessage {
  role: "consumer" | "agent";
  body: string;
  createdAt: string;
}

export interface AssetMatch {
  assetId: string;
  name: string;
  reason: string;
  reportedAt: string;
}

// Ticket 13: the module attachments a request carries. The template is the
// attachment's identity (the parsed spec stays backend-side); skills are
// module ids whose markdown the agent loads each turn.
export interface TemplateAttachment {
  moduleId: string;
}

export interface DataRequest {
  id: string;
  description: string;
  format: string;
  qualityBar: string;
  budgetMicros: number;
  spentMicros: number;
  status: RequestStatus;
  messages: ChatMessage[];
  matches: AssetMatch[];
  template: TemplateAttachment | null;
  skills: string[];
  connectors: string[];
  createdAt: string;
  updatedAt: string;
}

export interface ChatTurnResult {
  request: DataRequest;
  reply: string;
  clarified: boolean;
  chargedMicros: number;
}

function assertObject(doc: unknown, what: string): Record<string, unknown> {
  if (typeof doc !== "object" || doc === null) throw new Error(`${what} is not an object`);
  return doc as Record<string, unknown>;
}

function assertString(v: unknown, what: string): string {
  if (typeof v !== "string" || v === "") throw new Error(`${what} is missing`);
  return v;
}

function assertInt(v: unknown, what: string): number {
  if (typeof v !== "number" || !Number.isInteger(v)) throw new Error(`${what} is not an integer`);
  return v;
}

function parseStatus(v: unknown): RequestStatus {
  if (typeof v !== "string" || !REQUEST_STATUSES.includes(v as RequestStatus))
    throw new Error(`unknown request status: ${String(v)}`);
  return v as RequestStatus;
}

function parseMessage(doc: unknown): ChatMessage {
  const d = assertObject(doc, "message");
  const role = d.role;
  if (role !== "consumer" && role !== "agent") throw new Error("message carries an unknown role");
  return {
    role,
    body: assertString(d.body, "message body"),
    createdAt: assertString(d.created_at, "message created_at"),
  };
}

function parseMatch(doc: unknown): AssetMatch {
  const d = assertObject(doc, "match");
  return {
    assetId: assertString(d.asset_id, "match asset_id"),
    name: assertString(d.name, "match name"),
    reason: typeof d.reason === "string" ? d.reason : "",
    reportedAt: assertString(d.reported_at, "match reported_at"),
  };
}

export function parseRequest(doc: unknown): DataRequest {
  const d = assertObject(doc, "request");
  let template: TemplateAttachment | null = null;
  if (d.template !== undefined && d.template !== null) {
    const t = assertObject(d.template, "request template");
    template = { moduleId: assertString(t.module_id, "template module_id") };
  }
  return {
    id: assertString(d.id, "request id"),
    description: assertString(d.description, "request description"),
    format: assertString(d.format, "request format"),
    qualityBar: typeof d.quality_bar === "string" ? d.quality_bar : "",
    budgetMicros: assertInt(d.budget_micros, "request budget_micros"),
    spentMicros: assertInt(d.spent_micros, "request spent_micros"),
    status: parseStatus(d.status),
    messages: Array.isArray(d.messages) ? d.messages.map(parseMessage) : [],
    matches: Array.isArray(d.matches) ? d.matches.map(parseMatch) : [],
    template,
    skills: Array.isArray(d.skills) ? d.skills.map((s) => assertString(s, "request skill")) : [],
    connectors: Array.isArray(d.connectors)
      ? d.connectors.map((c) => assertString(c, "request connector"))
      : [],
    createdAt: assertString(d.created_at, "request created_at"),
    updatedAt: assertString(d.updated_at, "request updated_at"),
  };
}

export async function createRequest(
  backend: string,
  token: string,
  input: { description: string; format: string; qualityBar?: string; budgetMicros: number },
): Promise<DataRequest> {
  const doc = await apiCall<{ request: unknown }>(backend, "/api/v1/requests", {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({
      description: input.description,
      format: input.format,
      quality_bar: input.qualityBar ?? "",
      budget_micros: input.budgetMicros,
    }),
  });
  return parseRequest(doc.request);
}

export async function fetchMyRequests(backend: string, token: string): Promise<DataRequest[]> {
  const doc = await apiCall<{ requests: unknown[] }>(backend, "/api/v1/requests", {
    headers: authedHeader(token),
  });
  return doc.requests.map(parseRequest);
}

export async function fetchRequest(backend: string, token: string, id: string): Promise<DataRequest> {
  const doc = await apiCall<{ request: unknown }>(backend, `/api/v1/requests/${id}`, {
    headers: authedHeader(token),
  });
  return parseRequest(doc.request);
}

// chatTurn sends one clarification message. The backend answers 402 when the
// budget cannot cover the turn (carrying the updated request in the error
// document); that surfaces as a plain Error here — the page refetches the
// status view to show the surfaced state.
export async function chatTurn(
  backend: string,
  token: string,
  id: string,
  message: string,
): Promise<ChatTurnResult> {
  const doc = await apiCall<{
    request: unknown;
    reply: string;
    clarified: boolean;
    charged_micros: number;
  }>(backend, `/api/v1/requests/${id}/chat`, {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({ message }),
  });
  return {
    request: parseRequest(doc.request),
    reply: doc.reply,
    clarified: doc.clarified === true,
    chargedMicros: assertInt(doc.charged_micros, "charged_micros"),
  };
}

// formatBudget renders the budget math for the status view: "550 µcr of
// 10,000 µcr spent". Micro-credits are the ledger's exact unit.
export function formatBudget(spentMicros: number, budgetMicros: number): string {
  return `${spentMicros.toLocaleString("en-IE")} of ${budgetMicros.toLocaleString("en-IE")} µcr spent`;
}

// attachTemplate pins a Process Template module onto the request (ticket
// 13): its questions and triggers drive the collection conversation.
export async function attachTemplate(
  backend: string,
  token: string,
  id: string,
  moduleId: string,
): Promise<DataRequest> {
  const doc = await apiCall<{ request: unknown }>(backend, `/api/v1/requests/${id}/template`, {
    method: "PUT",
    headers: authedHeader(token),
    body: JSON.stringify({ module_id: moduleId }),
  });
  return parseRequest(doc.request);
}

export async function detachTemplate(backend: string, token: string, id: string): Promise<DataRequest> {
  const doc = await apiCall<{ request: unknown }>(backend, `/api/v1/requests/${id}/template`, {
    method: "DELETE",
    headers: authedHeader(token),
  });
  return parseRequest(doc.request);
}

export async function attachSkill(
  backend: string,
  token: string,
  id: string,
  moduleId: string,
): Promise<DataRequest> {
  const doc = await apiCall<{ request: unknown }>(backend, `/api/v1/requests/${id}/skills`, {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({ module_id: moduleId }),
  });
  return parseRequest(doc.request);
}

export async function detachSkill(
  backend: string,
  token: string,
  id: string,
  moduleId: string,
): Promise<DataRequest> {
  const doc = await apiCall<{ request: unknown }>(backend, `/api/v1/requests/${id}/skills/${moduleId}`, {
    method: "DELETE",
    headers: authedHeader(token),
  });
  return parseRequest(doc.request);
}

// Ticket 14: Connector Modules attach like skills — the resolved
// connection fact rides every clarify turn, letting the agent query the
// live source. Connection facts only, never credentials.
export async function attachConnector(
  backend: string,
  token: string,
  id: string,
  moduleId: string,
): Promise<DataRequest> {
  const doc = await apiCall<{ request: unknown }>(backend, `/api/v1/requests/${id}/connectors`, {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({ module_id: moduleId }),
  });
  return parseRequest(doc.request);
}

export async function detachConnector(
  backend: string,
  token: string,
  id: string,
  moduleId: string,
): Promise<DataRequest> {
  const doc = await apiCall<{ request: unknown }>(backend, `/api/v1/requests/${id}/connectors/${moduleId}`, {
    method: "DELETE",
    headers: authedHeader(token),
  });
  return parseRequest(doc.request);
}

// --- Org-facing view (the collections form's data source) ---

// FieldableRequest is the org's face of one clarified Request: the
// commission only — the consumer's private chat never rides this view.
export interface FieldableRequest {
  id: string;
  description: string;
  format: string;
  qualityBar: string;
  budgetMicros: number;
  status: RequestStatus;
  template: TemplateAttachment | null;
  createdAt: string;
  updatedAt: string;
}

function parseFieldableRequest(doc: unknown): FieldableRequest {
  const d = assertObject(doc, "fieldable request");
  // The org view must never carry the consumer's private chat — a backend
  // regression that leaks it fails loudly here rather than rendering.
  if ("messages" in d) throw new Error("fieldable request leaks the private chat");
  if ("spent_micros" in d) throw new Error("fieldable request leaks the consumer's spend");
  let template: TemplateAttachment | null = null;
  if (d.template !== undefined && d.template !== null) {
    const t = assertObject(d.template, "fieldable request template");
    template = { moduleId: assertString(t.module_id, "fieldable request template module_id") };
  }
  return {
    id: assertString(d.id, "fieldable request id"),
    description: assertString(d.description, "fieldable request description"),
    format: assertString(d.format, "fieldable request format"),
    qualityBar: typeof d.quality_bar === "string" ? d.quality_bar : "",
    budgetMicros: assertInt(d.budget_micros, "fieldable request budget_micros"),
    status: parseStatus(d.status),
    template,
    createdAt: assertString(d.created_at, "fieldable request created_at"),
    updatedAt: assertString(d.updated_at, "fieldable request updated_at"),
  };
}

// fetchFieldableRequests lists every clarified Request the org may field.
export async function fetchFieldableRequests(backend: string, token: string): Promise<FieldableRequest[]> {
  const doc = await apiCall<{ requests: unknown[] }>(backend, "/api/v1/org/requests", {
    headers: authedHeader(token),
  });
  return doc.requests.map(parseFieldableRequest);
}
