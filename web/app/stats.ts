// The web side of the admin stats dashboard (ticket 16): the backend
// /admin/stats client and the parser pinning its document shape. Parsers
// are the frontend half of the contract — unknown shapes throw rather than
// render wrong numbers on an admin's dashboard.

import { apiCall, authedHeader } from "~/api";

export interface DayCount {
  day: string;
  count: number;
}

export interface StatusCount {
  status: string;
  count: number;
}

export interface KindFlow {
  kind: string;
  inMicros: number;
  outMicros: number;
  movements: number;
}

export interface ChannelActivity {
  channel: string;
  conversations: number;
  turns: number;
}

export interface ModuleCounts {
  agentSkill: number;
  processTemplate: number;
  connector: number;
  systemWide: number;
  private: number;
}

export interface StatsSnapshot {
  requestsOverTime: DayCount[];
  requestStatuses: StatusCount[];
  collectionStatuses: StatusCount[];
  creditsFlow: KindFlow[];
  channels: ChannelActivity[];
  modules: ModuleCounts;
}

// --- Parsers ---

function parseDayCount(doc: unknown): DayCount {
  if (typeof doc !== "object" || doc === null) throw new Error("day count is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.day !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(d.day))
    throw new Error("day count carries no YYYY-MM-DD day");
  if (typeof d.count !== "number" || !Number.isInteger(d.count) || d.count < 0)
    throw new Error("day count is not a non-negative integer");
  return { day: d.day, count: d.count };
}

function parseStatusCount(doc: unknown): StatusCount {
  if (typeof doc !== "object" || doc === null) throw new Error("status count is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.status !== "string" || d.status === "")
    throw new Error("status count carries no status");
  if (typeof d.count !== "number" || !Number.isInteger(d.count) || d.count < 0)
    throw new Error("status count is not a non-negative integer");
  return { status: d.status, count: d.count };
}

function parseKindFlow(doc: unknown): KindFlow {
  if (typeof doc !== "object" || doc === null) throw new Error("credits flow is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.kind !== "string" || d.kind === "") throw new Error("credits flow carries no kind");
  return {
    kind: d.kind,
    inMicros: parseMicros(d, "in_micros"),
    outMicros: parseMicros(d, "out_micros"),
    movements: parseMicros(d, "movements"),
  };
}

function parseChannelActivity(doc: unknown): ChannelActivity {
  if (typeof doc !== "object" || doc === null) throw new Error("channel activity is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.channel !== "string" || d.channel === "")
    throw new Error("channel activity carries no channel");
  return {
    channel: d.channel,
    conversations: parseMicros(d, "conversations"),
    turns: parseMicros(d, "turns"),
  };
}

function parseMicros(doc: Record<string, unknown>, key: string): number {
  const v = doc[key];
  if (typeof v !== "number" || !Number.isInteger(v) || v < 0)
    throw new Error(`${key} is not a non-negative integer`);
  return v;
}

function parseModuleCounts(doc: unknown): ModuleCounts {
  if (typeof doc !== "object" || doc === null) throw new Error("module counts is not an object");
  const d = doc as Record<string, unknown>;
  return {
    agentSkill: parseMicros(d, "agent_skill"),
    processTemplate: parseMicros(d, "process_template"),
    connector: parseMicros(d, "connector"),
    systemWide: parseMicros(d, "system_wide"),
    private: parseMicros(d, "private"),
  };
}

function parseArray(doc: Record<string, unknown>, key: string, item: (d: unknown) => unknown): unknown[] {
  if (!Array.isArray(doc[key])) throw new Error(`stats document carries no ${key} array`);
  return doc[key].map(item);
}

// parseStatsSnapshot reads the /admin/stats document: requests over time,
// status breakdowns, the Ledger's kind flows, channel activity, and module
// usage.
export function parseStatsSnapshot(doc: unknown): StatsSnapshot {
  if (typeof doc !== "object" || doc === null) throw new Error("stats document is not an object");
  const d = doc as Record<string, unknown>;
  return {
    requestsOverTime: parseArray(d, "requests_over_time", parseDayCount) as DayCount[],
    requestStatuses: parseArray(d, "request_statuses", parseStatusCount) as StatusCount[],
    collectionStatuses: parseArray(d, "collection_statuses", parseStatusCount) as StatusCount[],
    creditsFlow: parseArray(d, "credits_flow", parseKindFlow) as KindFlow[],
    channels: parseArray(d, "channels", parseChannelActivity) as ChannelActivity[],
    modules: parseModuleCounts(d.modules),
  };
}

// --- API client ---

// fetchAdminStats loads the Platform Admin's dashboard snapshot.
export async function fetchAdminStats(backend: string, token: string): Promise<StatsSnapshot> {
  const doc = await apiCall<unknown>(backend, "/api/v1/admin/stats", {
    headers: authedHeader(token),
  });
  return parseStatsSnapshot(doc);
}

// formatCredits renders micro-credits as a whole-or-fractional credit
// string for the flow tables.
export function formatCredits(micros: number): string {
  return (micros / 1_000_000).toLocaleString(undefined, { maximumFractionDigits: 2 });
}
