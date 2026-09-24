import { describe, expect, it } from "vitest";
// The web side of ticket 08: parsers pinning the backend's module document
// shapes. Unknown shapes throw rather than render wrong — same contract as
// requests.ts and taxonomy.ts.
import {
  MODULE_KINDS,
  errFrom,
  formatModule,
  parseGrantList,
  parseModule,
  parseModuleList,
  parseVersionList,
} from "~/modules";

describe("module parsers", () => {
  const doc = {
    id: "ver-1",
    module_id: "mod-1",
    name: "barley-survey",
    kind: "process_template",
    author_id: "author-1",
    version: "1.0.0",
    capability: "Runs a spring-barley yield survey workflow for Requests.",
    content: "# Barley survey",
    config: "",
    system_wide: false,
    deprecated: false,
    created_at: "2026-09-24T10:00:00Z",
    updated_at: "2026-09-24T10:00:00Z",
  };

  it("parses a full module document", () => {
    const m = parseModule(doc);
    expect(m.id).toBe("ver-1");
    expect(m.moduleId).toBe("mod-1");
    expect(m.kind).toBe("process_template");
    expect(m.capability).toContain("survey");
    expect(m.systemWide).toBe(false);
    expect(m.deprecated).toBe(false);
  });

  it("refuses an unknown kind", () => {
    expect(() => parseModule({ ...doc, kind: "plugin" })).toThrow(/kind/);
  });

  it("refuses missing identity fields", () => {
    expect(() => parseModule({ ...doc, id: "" })).toThrow(/version id/);
    expect(() => parseModule({ ...doc, module_id: "" })).toThrow(/module_id/);
    expect(() => parseModule({ ...doc, name: "" })).toThrow(/name/);
    expect(() => parseModule({ ...doc, version: "" })).toThrow(/version/);
    expect(() => parseModule({ ...doc, capability: "" })).toThrow(/capability/);
  });

  it("refuses non-boolean flags", () => {
    expect(() => parseModule({ ...doc, system_wide: "yes" })).toThrow(/system_wide/);
    expect(() => parseModule({ ...doc, deprecated: 1 })).toThrow(/deprecated/);
  });

  it("parses lists", () => {
    expect(parseModuleList([doc]).length).toBe(1);
    expect(parseVersionList([doc]).length).toBe(1);
    expect(parseGrantList([{ module_id: "mod-1", account_id: "a-1", granted_at: "t" }]).length).toBe(1);
  });

  it("formatModule renders the manifest line", () => {
    expect(formatModule(parseModule(doc))).toBe("process_template · v1.0.0");
  });

  it("MODULE_KINDS is exactly the three kinds", () => {
    expect(MODULE_KINDS).toEqual(["agent_skill", "process_template", "connector"]);
  });
});
