// Roster client tests: parsers pin the backend's member shapes, and the
// create/delete calls pin the org's roster-management surface (the UI the
// collections form depends on). All expected values are literals.
import { describe, expect, it } from "vitest";
import { createMember, deleteMember, listMembers, parseMember } from "./roster";

const memberDoc = {
  id: "mem-01",
  display_name: "Máire Ní Cheallaigh",
  contact: "email:maire@example.org",
};

describe("parseMember", () => {
  it("parses the backend document into camelCase", () => {
    const m = parseMember(memberDoc);
    expect(m.id).toBe("mem-01");
    expect(m.displayName).toBe("Máire Ní Cheallaigh");
    expect(m.contact).toBe("email:maire@example.org");
  });

  it("refuses a document without an id", () => {
    expect(() => parseMember({ display_name: "x", contact: "y" })).toThrow(/no id/);
  });
});

function withFetch(mock: typeof fetch, fn: () => Promise<void>): Promise<void> {
  const original = globalThis.fetch;
  globalThis.fetch = mock;
  return fn().finally(() => {
    globalThis.fetch = original;
  });
}

describe("roster client", () => {
  it("createMember POSTs display_name and contact", async () => {
    const calls: Array<{ path: string; init?: RequestInit }> = [];
    await withFetch(
      (async (input: string | URL, init?: RequestInit) => {
        calls.push({ path: String(input), init });
        return new Response(JSON.stringify({ member: memberDoc }), { status: 201 });
      }) as typeof fetch,
      async () => {
        const m = await createMember("http://backend.test", "tok", {
          displayName: "Máire Ní Cheallaigh",
          contact: "email:maire@example.org",
        });
        expect(m.id).toBe("mem-01");
      },
    );
    expect(calls[0]!.path).toBe("http://backend.test/api/v1/members");
    expect(calls[0]!.init!.method).toBe("POST");
    expect(JSON.parse(String(calls[0]!.init!.body))).toEqual({
      display_name: "Máire Ní Cheallaigh",
      contact: "email:maire@example.org",
    });
  });

  it("deleteMember DELETEs by id; listMembers GETs the roster", async () => {
    const calls: Array<{ path: string; init?: RequestInit }> = [];
    await withFetch(
      (async (input: string | URL, init?: RequestInit) => {
        calls.push({ path: String(input), init });
        const path = String(input);
        if (path.endsWith("/api/v1/members")) {
          return new Response(JSON.stringify({ members: [memberDoc] }), { status: 200 });
        }
        return new Response(JSON.stringify({ deleted: true }), { status: 200 });
      }) as typeof fetch,
      async () => {
        await deleteMember("http://backend.test", "tok", "mem-01");
        const roster = await listMembers("http://backend.test", "tok");
        expect(roster).toHaveLength(1);
      },
    );
    expect(calls[0]!.path).toBe("http://backend.test/api/v1/members/mem-01");
    expect(calls[0]!.init!.method).toBe("DELETE");
  });
});
