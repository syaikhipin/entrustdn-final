// The web side of membership (ticket 02): the backend membership API client,
// parsers pinning its document shapes, and the role-routing decision the app
// makes after login. Parsers are the frontend half of the backend contract —
// unknown shapes throw rather than render wrong.

import { apiCall, authedHeader, errFrom } from "~/api";

export { errFrom };

export type Role = "data_consumer" | "farmer_organization" | "platform_admin";
export type AccountStatus = "active" | "pending_approval" | "rejected";

export interface PublicAccount {
  id: string;
  email: string;
  displayName: string;
  role: Role;
  status: AccountStatus;
  verified: boolean;
  createdAt: string;
}

export interface TosRecord {
  acceptedVersion: string;
  acceptedAt: string;
}

export interface Me {
  account: PublicAccount;
  requiresTosAcceptance: boolean;
  tos?: TosRecord;
}

// --- Parsers ---

const ROLES: Role[] = ["data_consumer", "farmer_organization", "platform_admin"];
const STATUSES: AccountStatus[] = ["active", "pending_approval", "rejected"];

function parseAccount(doc: unknown): PublicAccount {
  if (typeof doc !== "object" || doc === null) throw new Error("account is not an object");
  const d = doc as Record<string, unknown>;
  const role = d.role as Role;
  const status = d.status as AccountStatus;
  if (!ROLES.includes(role)) throw new Error(`unknown role: ${String(d.role)}`);
  if (!STATUSES.includes(status)) throw new Error(`unknown status: ${String(d.status)}`);
  return {
    id: String(d.id ?? ""),
    email: String(d.email ?? ""),
    displayName: String(d.display_name ?? ""),
    role,
    status,
    verified: d.verified === true,
    createdAt: String(d.created_at ?? ""),
  };
}

// parseLogin reads the login (and register) response: the account, the
// bearer token, and whether the session is flagged for TOS re-acceptance.
export function parseLogin(doc: unknown): { account: PublicAccount; token: string; requiresTosAcceptance: boolean } {
  if (typeof doc !== "object" || doc === null) throw new Error("login document is not an object");
  const d = doc as Record<string, unknown>;
  const session = d.session as Record<string, unknown> | undefined;
  const token = typeof session?.token === "string" ? session.token : "";
  if (token === "") throw new Error("login document carries no session token");
  return {
    account: parseAccount(d.account),
    token,
    requiresTosAcceptance: d.requires_tos_acceptance === true,
  };
}

// parseMe reads the /me document: identity plus the provable TOS record.
export function parseMe(doc: unknown): Me {
  if (typeof doc !== "object" || doc === null) throw new Error("me document is not an object");
  const d = doc as Record<string, unknown>;
  const session = d.session as Record<string, unknown> | undefined;
  let tos: TosRecord | undefined;
  if (typeof d.tos === "object" && d.tos !== null) {
    const t = d.tos as Record<string, unknown>;
    tos = {
      acceptedVersion: String(t.accepted_version ?? ""),
      acceptedAt: String(t.accepted_at ?? ""),
    };
  }
  return {
    account: parseAccount(d.account),
    requiresTosAcceptance: session?.requires_tos_acceptance === true,
    tos,
  };
}

// homeRoute decides which home a logged-in account lands on: the TOS
// contract page first if flagged, then the role-appropriate home — with
// pending/rejected organizations seeing their application state.
export function homeRoute(acct: PublicAccount, requiresTosAcceptance: boolean): string {
  if (requiresTosAcceptance) return "tos";
  if (acct.role === "data_consumer") return "consumer";
  if (acct.role === "platform_admin") return "admin";
  // Farmer organizations: their application status picks the view.
  if (acct.status === "active") return "organization";
  if (acct.status === "rejected") return "organization-rejected";
  return "organization-pending";
}

// --- API client ---

export async function register(
  backend: string,
  body: { email: string; password: string; displayName: string; role: string; tosVersion: string },
): Promise<PublicAccount> {
  const doc = await apiCall<{ account: unknown }>(backend, "/api/v1/register", {
    method: "POST",
    body: JSON.stringify({
      email: body.email,
      password: body.password,
      display_name: body.displayName,
      role: body.role,
      tos_version: body.tosVersion,
    }),
  });
  return parseAccount(doc.account);
}

export async function verify(backend: string, token: string): Promise<void> {
  await apiCall(backend, "/api/v1/verify", { method: "POST", body: JSON.stringify({ token }) });
}

export async function login(backend: string, email: string, password: string): Promise<ReturnType<typeof parseLogin>> {
  const doc = await apiCall<unknown>(backend, "/api/v1/login", {
    method: "POST",
    body: JSON.stringify({ email, password }),
  });
  return parseLogin(doc);
}

export async function fetchMe(backend: string, token: string): Promise<Me> {
  const doc = await apiCall<unknown>(backend, "/api/v1/me", {
    headers: { Authorization: `Bearer ${token}` },
  });
  return parseMe(doc);
}

export async function acceptTos(backend: string, token: string, version: string): Promise<void> {
  await apiCall(backend, "/api/v1/tos/accept", {
    method: "POST",
    headers: { Authorization: `Bearer ${token}` },
    body: JSON.stringify({ version }),
  });
}

export async function logout(backend: string, token: string): Promise<void> {
  await apiCall(backend, "/api/v1/logout", {
    method: "POST",
    headers: { Authorization: `Bearer ${token}` },
    body: JSON.stringify({}),
  });
}

export interface Application extends PublicAccount {}

export async function listApplications(backend: string, token: string): Promise<Application[]> {
  const doc = await apiCall<{ applications: unknown[] }>(backend, "/api/v1/admin/applications", {
    headers: { Authorization: `Bearer ${token}` },
  });
  return doc.applications.map(parseAccount);
}

export async function decideApplication(
  backend: string,
  token: string,
  accountId: string,
  decision: "approve" | "reject",
): Promise<void> {
  await apiCall(backend, "/api/v1/admin/applications/decide", {
    method: "POST",
    headers: { Authorization: `Bearer ${token}` },
    body: JSON.stringify({ account_id: accountId, decision }),
  });
}

export async function fetchCurrentTos(backend: string): Promise<{ version: string; body: string }> {
  const doc = await apiCall<{ version: unknown; body: unknown }>(backend, "/api/v1/tos/current");
  return { version: String(doc.version ?? ""), body: String(doc.body ?? "") };
}

export async function publishTos(backend: string, token: string, version: string, body: string): Promise<void> {
  await apiCall(backend, "/api/v1/admin/tos", {
    method: "POST",
    headers: { Authorization: `Bearer ${token}` },
    body: JSON.stringify({ version, body }),
  });
}
