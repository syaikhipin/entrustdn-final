// The web side of the org earnings view (ticket 15): the backend /me/earnings
// client and the parser pinning its document shape. Parsers are the frontend
// half of the contract — unknown shapes throw rather than render wrong.

import { apiCall, authedHeader } from "~/api";

export interface MemberEarnings {
  memberId: string;
  displayName: string;
  earnedMicros: number;
}

export interface EarningsView {
  revenueShareOrgPercent: number;
  totalReceivedMicros: number;
  balanceMicros: number;
  members: MemberEarnings[];
}

// --- Parsers ---

function parseIntMicros(doc: Record<string, unknown>, key: string): number {
  const v = doc[key];
  if (typeof v !== "number" || !Number.isInteger(v))
    throw new Error(`earnings document ${key} is not an integer`);
  return v;
}

function parseMemberEarnings(doc: unknown): MemberEarnings {
  if (typeof doc !== "object" || doc === null) throw new Error("member earnings is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.member_id !== "string" || d.member_id === "")
    throw new Error("member earnings carries no member_id");
  if (typeof d.display_name !== "string" || d.display_name === "")
    throw new Error("member earnings carries no display_name");
  if (typeof d.earned_micros !== "number" || !Number.isInteger(d.earned_micros))
    throw new Error("member earnings earned_micros is not an integer");
  return {
    memberId: d.member_id,
    displayName: d.display_name,
    earnedMicros: d.earned_micros,
  };
}

// parseEarnings reads the /me/earnings document: the split percentage in
// force (0–100), the gross Revenue Share received, the org account's
// balance, and the per-Member breakdown.
export function parseEarnings(doc: unknown): EarningsView {
  if (typeof doc !== "object" || doc === null) throw new Error("earnings document is not an object");
  const d = doc as Record<string, unknown>;
  const percent = d.revenue_share_org_percent;
  if (typeof percent !== "number" || !Number.isInteger(percent) || percent < 0 || percent > 100)
    throw new Error("earnings document carries no revenue_share_org_percent in 0–100");
  if (!Array.isArray(d.members)) throw new Error("earnings document carries no members array");
  return {
    revenueShareOrgPercent: percent,
    totalReceivedMicros: parseIntMicros(d, "total_received_micros"),
    balanceMicros: parseIntMicros(d, "balance_micros"),
    members: d.members.map(parseMemberEarnings),
  };
}

// --- API client ---

// fetchMyEarnings loads the signed-in org's earnings view.
export async function fetchMyEarnings(backend: string, token: string): Promise<EarningsView> {
  const doc = await apiCall<unknown>(backend, "/api/v1/me/earnings", {
    headers: authedHeader(token),
  });
  return parseEarnings(doc);
}
