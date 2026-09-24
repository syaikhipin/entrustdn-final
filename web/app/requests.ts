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
