// The web side of ticket 12: the Data Collection API client and parsers
// pinning the backend's document shapes. Parsers are the frontend half of
// the contract — unknown shapes throw rather than render wrong. Identity
// surfaces (member names, raw rounds) never appear in the consumer's
// collection documents: the backend sanitizes them, and identity-leak
// asserts on the delivery live in the backend's API tests.

import { apiCall, authedHeader, errFrom } from "~/api";

export { errFrom };

export type CollectionStatus = "collecting" | "completed" | "incomplete";
export type ItemStatus = "collecting" | "accepted" | "blocked";

export interface Round {
  answer: string;
  ok: boolean;
  reason: string;
  at: string;
}

export interface Item {
  memberId: string;
  memberName: string;
  question: string;
  status: ItemStatus;
  accepted: string;
  rounds: Round[];
  reasks: number;
  blocked: boolean;
  blockReason: string;
}

export interface Missing {
  memberName: string;
  question: string;
  reason: string;
}

export interface Collection {
  id: string;
  orgId: string;
  consumerId: string;
  requestId: string;
  deadline: string | null;
  status: CollectionStatus;
  items: Item[];
  missing: Missing[];
  missingCount: number;
  createdAt: string;
  updatedAt: string;
}

// --- Parsers ---

// parseStatus reads one of the backend's exact life-stage strings; an
// unknown value throws rather than render wrong.
function parseStatus<T extends string>(doc: unknown, allowed: readonly T[]): T {
  if (typeof doc !== "string" || !allowed.includes(doc as T)) {
    throw new Error(`unknown status: ${String(doc)}`);
  }
  return doc as T;
}

const COLLECTION_STATUSES = ["collecting", "completed", "incomplete"] as const;
const ITEM_STATUSES = ["collecting", "accepted", "blocked"] as const;

function parseRound(doc: unknown): Round {
  if (typeof doc !== "object" || doc === null) throw new Error("round is not an object");
  const d = doc as Record<string, unknown>;
  return {
    answer: typeof d.answer === "string" ? d.answer : "",
    ok: d.ok === true,
    reason: typeof d.reason === "string" ? d.reason : "",
    at: String(d.at ?? ""),
  };
}

function parseItem(doc: unknown): Item {
  if (typeof doc !== "object" || doc === null) throw new Error("item is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.question !== "string") throw new Error("item carries no question");
  return {
    memberId: typeof d.member_id === "string" ? d.member_id : "",
    memberName: typeof d.member_name === "string" ? d.member_name : "",
    question: d.question,
    status: parseStatus(d.status, ITEM_STATUSES),
    accepted: typeof d.accepted === "string" ? d.accepted : "",
    rounds: Array.isArray(d.rounds) ? d.rounds.map(parseRound) : [],
    reasks: typeof d.reasks === "number" ? d.reasks : 0,
    blocked: d.blocked === true,
    blockReason: typeof d.block_reason === "string" ? d.block_reason : "",
  };
}

function parseMissing(doc: unknown): Missing {
  if (typeof doc !== "object" || doc === null) throw new Error("missing entry is not an object");
  const d = doc as Record<string, unknown>;
  return {
    memberName: typeof d.member_name === "string" ? d.member_name : "",
    question: typeof d.question === "string" ? d.question : "",
    reason: typeof d.reason === "string" ? d.reason : "",
  };
}

export function parseCollection(doc: unknown): Collection {
  if (typeof doc !== "object" || doc === null) throw new Error("collection is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.id !== "string" || d.id === "") throw new Error("collection carries no id");
  return {
    id: d.id,
    orgId: typeof d.org_id === "string" ? d.org_id : "",
    consumerId: typeof d.consumer_id === "string" ? d.consumer_id : "",
    requestId: typeof d.request_id === "string" ? d.request_id : "",
    deadline: typeof d.deadline === "string" ? d.deadline : null,
    status: parseStatus(d.status, COLLECTION_STATUSES),
    items: Array.isArray(d.items) ? d.items.map(parseItem) : [],
    missing: Array.isArray(d.missing) ? d.missing.map(parseMissing) : [],
    missingCount: typeof d.missing_count === "number" ? d.missing_count : 0,
    createdAt: String(d.created_at ?? ""),
    updatedAt: String(d.updated_at ?? ""),
  };
}

export function parseCollectionList(doc: unknown): Collection[] {
  if (typeof doc !== "object" || doc === null) throw new Error("collection list is not an object");
  const d = doc as Record<string, unknown>;
  if (!Array.isArray(d.collections)) throw new Error("collection list carries no collections array");
  return d.collections.map(parseCollection);
}

// --- Formatting ---

// statusLabel renders a collection's life stage for the dashboard.
export function statusLabel(status: CollectionStatus): string {
  const labels: Record<CollectionStatus, string> = {
    collecting: "Collecting",
    completed: "Completed",
    incomplete: "Incomplete",
  };
  return labels[status] ?? status;
}

// --- API client (org side) ---

// createCollection opens a Collection from a clarified Request over the
// org's roster members.
export async function createCollection(
  backend: string,
  token: string,
  input: { requestId: string; memberIds: string[]; questions: string[]; deadline: string },
): Promise<Collection> {
  const doc = await apiCall<{ collection: unknown }>(backend, "/api/v1/collections", {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({
      request_id: input.requestId,
      member_ids: input.memberIds,
      questions: input.questions,
      deadline: input.deadline, // empty string = no hard end
    }),
  });
  return parseCollection(doc.collection);
}

// listCollections loads the org's collections, newest first.
export async function listCollections(backend: string, token: string): Promise<Collection[]> {
  const doc = await apiCall<unknown>(backend, "/api/v1/collections", {
    headers: authedHeader(token),
  });
  return parseCollectionList(doc);
}

// syncCollection runs the gathering step: ingest new answers, open re-asks,
// finalize when the triggers say so. A 409 means it was already finalized —
// the body carries the standing record.
export async function syncCollection(backend: string, token: string, id: string): Promise<Collection> {
  const doc = await apiCall<{ collection?: unknown }>(backend, `/api/v1/collections/${id}/sync`, {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({}),
  });
  if (!doc.collection) throw new Error("sync response carries no collection");
  return parseCollection(doc.collection);
}

// --- Conversations (ticket 11's surface, driven by ticket 12) ---

export interface Conversation {
  id: string;
  memberId: string;
  topic: string;
  resumeToken: string;
  status: string;
}

export function parseConversation(doc: unknown): Conversation {
  if (typeof doc !== "object" || doc === null) throw new Error("conversation is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.id !== "string" || d.id === "") throw new Error("conversation carries no id");
  return {
    id: d.id,
    memberId: typeof d.member_id === "string" ? d.member_id : "",
    topic: typeof d.topic === "string" ? d.topic : "",
    resumeToken: typeof d.resume_token === "string" ? d.resume_token : "",
    status: typeof d.status === "string" ? d.status : "",
  };
}

// startGatheringConversation opens the member's conversation for one
// collection: it carries the collection's request ID so the sync matches
// it, and the questions come from the collection's items for that member.
// The resume token comes back — the org hands it to the Member (the demo
// path prints it; the agent sidecar delivers it in production).
export async function startGatheringConversation(
  backend: string,
  token: string,
  input: { memberId: string; requestId: string; questions: string[]; memberName: string },
): Promise<Conversation> {
  const doc = await apiCall<{ conversation: unknown }>(backend, "/api/v1/conversations", {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({
      member_id: input.memberId,
      topic: `Gathering for request ${input.requestId}`,
      questions: input.questions,
      request_id: input.requestId,
    }),
  });
  return parseConversation(doc.conversation);
}

// --- API client (consumer side) ---

// listConsumerCollections loads the consumer's collections — what's being
// gathered in their name.
export async function listConsumerCollections(backend: string, token: string): Promise<Collection[]> {
  const doc = await apiCall<unknown>(backend, "/api/v1/consumer/collections", {
    headers: authedHeader(token),
  });
  return parseCollectionList(doc);
}

// downloadDelivery streams the anonymized delivery via fetch so the Bearer
// session rides along, then hands the bytes to the browser as a blob.
export async function downloadDelivery(backend: string, token: string, id: string): Promise<void> {
  const resp = await fetch(`${backend}/api/v1/consumer/collections/${id}/download`, {
    headers: authedHeader(token),
  });
  if (!resp.ok) {
    const doc = await resp.json().catch(() => ({}));
    throw new Error(errFrom(doc));
  }
  const blob = await resp.blob();
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = `collection-${id}.csv`;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}
