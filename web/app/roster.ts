// The web side of the Member roster (ticket 11): a read-only client for
// the org's roster — the collections UI (ticket 12) picks gathering
// targets from it. Members are contact points, never accounts.

import { apiCall, authedHeader, errFrom } from "~/api";

export { errFrom };

export interface Member {
  id: string;
  displayName: string;
  contact: string;
}

export function parseMember(doc: unknown): Member {
  if (typeof doc !== "object" || doc === null) throw new Error("member is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.id !== "string" || d.id === "") throw new Error("member carries no id");
  return {
    id: d.id,
    displayName: typeof d.display_name === "string" ? d.display_name : "",
    contact: typeof d.contact === "string" ? d.contact : "",
  };
}

export function parseMemberList(doc: unknown): Member[] {
  if (typeof doc !== "object" || doc === null) throw new Error("member list is not an object");
  const d = doc as Record<string, unknown>;
  if (!Array.isArray(d.members)) throw new Error("member list carries no members array");
  return d.members.map(parseMember);
}

// listMembers loads the org's roster.
export async function listMembers(backend: string, token: string): Promise<Member[]> {
  const doc = await apiCall<unknown>(backend, "/api/v1/members", {
    headers: authedHeader(token),
  });
  return parseMemberList(doc);
}
