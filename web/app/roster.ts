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

// createMember adds one Member to the org's roster. The contact names the
// Channel the agent reaches them on: "email:…", "whatsapp:…", "telegram:…".
export async function createMember(
  backend: string,
  token: string,
  input: { displayName: string; contact: string },
): Promise<Member> {
  const doc = await apiCall<{ member: unknown }>(backend, "/api/v1/members", {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({ display_name: input.displayName, contact: input.contact }),
  });
  return parseMember(doc.member);
}

// deleteMember removes one Member from the roster.
export async function deleteMember(backend: string, token: string, id: string): Promise<void> {
  await apiCall(backend, `/api/v1/members/${id}`, {
    method: "DELETE",
    headers: authedHeader(token),
  });
}
