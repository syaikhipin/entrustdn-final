// The web side of ticket 02: parsers pin the backend's membership document
// shapes, and homeRoute pins the role-routing decision the app makes after
// login. All expected values are literals — independent of the backend.
import { describe, expect, it } from "vitest";
import {
  errFrom,
  homeRoute,
  parseLogin,
  parseMe,
  type PublicAccount,
} from "./auth";

const consumerDoc = {
  account: {
    id: "acct-1",
    email: "analyst@example.org",
    display_name: "Analyst",
    role: "data_consumer",
    status: "active",
    verified: true,
    created_at: "2026-09-22T00:00:00Z",
  },
  session: { token: "tok-123" },
  requires_tos_acceptance: false,
};

describe("parseLogin", () => {
  it("reads a clean login document", () => {
    const { account, token, requiresTosAcceptance } = parseLogin(consumerDoc);
    expect(token).toBe("tok-123");
    expect(requiresTosAcceptance).toBe(false);
    expect(account.role).toBe("data_consumer");
    expect(account.verified).toBe(true);
  });

  it("reads a TOS-flagged login", () => {
    const doc = {
      ...consumerDoc,
      requires_tos_acceptance: true,
      account: { ...consumerDoc.account, status: "pending_approval", role: "farmer_organization" },
    };
    const { requiresTosAcceptance, account } = parseLogin(doc);
    expect(requiresTosAcceptance).toBe(true);
    expect(account.status).toBe("pending_approval");
  });

  it("refuses a document without a session token", () => {
    expect(() => parseLogin({ account: consumerDoc.account })).toThrow();
  });

  it("refuses an unknown role", () => {
    const doc = {
      ...consumerDoc,
      account: { ...consumerDoc.account, role: "super_admin" },
    };
    expect(() => parseLogin(doc)).toThrow();
  });
});

describe("parseMe", () => {
  it("reads the identity plus the TOS acceptance record", () => {
    const doc = {
      account: consumerDoc.account,
      session: { requires_tos_acceptance: false },
      tos: { accepted_version: "1.0", accepted_at: "2026-09-22T01:00:00Z" },
    };
    const me = parseMe(doc);
    expect(me.account.email).toBe("analyst@example.org");
    expect(me.tos?.acceptedVersion).toBe("1.0");
    expect(me.requiresTosAcceptance).toBe(false);
  });

  it("tolerates a document without a TOS record (never accepted)", () => {
    const me = parseMe({
      account: consumerDoc.account,
      session: { requires_tos_acceptance: true },
    });
    expect(me.tos).toBeUndefined();
    expect(me.requiresTosAcceptance).toBe(true);
  });
});

describe("homeRoute — the role-appropriate home", () => {
  const acct = (over: Partial<PublicAccount>): PublicAccount => ({
    id: "a", email: "e@example.org", displayName: "E",
    role: "data_consumer", status: "active", verified: true,
    createdAt: "2026-09-22T00:00:00Z",
    ...over,
  });

  it("sends a TOS-flagged session to the contract first, whatever the role", () => {
    expect(homeRoute(acct({ role: "data_consumer" }), true)).toBe("tos");
    expect(homeRoute(acct({ role: "platform_admin" }), true)).toBe("tos");
  });

  it("routes each role to its own home", () => {
    expect(homeRoute(acct({ role: "data_consumer" }), false)).toBe("consumer");
    expect(homeRoute(acct({ role: "farmer_organization", status: "active" }), false)).toBe("organization");
    expect(homeRoute(acct({ role: "platform_admin" }), false)).toBe("admin");
  });

  it("shows a pending organization its pending state, not the dashboard", () => {
    expect(homeRoute(acct({ role: "farmer_organization", status: "pending_approval" }), false)).toBe("organization-pending");
  });

  it("shows a rejected organization the rejection notice", () => {
    expect(homeRoute(acct({ role: "farmer_organization", status: "rejected" }), false)).toBe("organization-rejected");
  });
});

describe("errFrom", () => {
  it("extracts the backend's error message", () => {
    expect(errFrom({ error: "email or password is incorrect" })).toBe("email or password is incorrect");
  });
  it("falls back to a generic message", () => {
    expect(errFrom({})).toContain("failed");
  });
});
