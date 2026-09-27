// The web side of ticket 14: parsers pin the backend's Memory Provider
// document shapes — the admin registry's connection facts. Documents are
// exactly as the Go backend serializes them: snake_case, connection facts
// only (name + endpoint), never credentials.
import { describe, expect, it } from "vitest";
import { parseMemoryProvider } from "./memory";

const providerDoc = {
  id: "9f8b…",
  name: "mem0-primary",
  endpoint: "http://memory.local:8080/mcp",
  created_at: "2026-09-27T10:00:00Z",
  updated_at: "2026-09-27T10:00:00Z",
};

describe("parseMemoryProvider", () => {
  it("reads the connection fact", () => {
    expect(parseMemoryProvider(providerDoc)).toEqual({
      id: "9f8b…",
      name: "mem0-primary",
      endpoint: "http://memory.local:8080/mcp",
      createdAt: "2026-09-27T10:00:00Z",
      updatedAt: "2026-09-27T10:00:00Z",
    });
  });

  it("refuses documents without an id, name, or endpoint", () => {
    expect(() => parseMemoryProvider({ ...providerDoc, id: "" })).toThrow();
    expect(() => parseMemoryProvider({ ...providerDoc, name: 42 })).toThrow();
    expect(() => parseMemoryProvider({ ...providerDoc, endpoint: null })).toThrow();
    expect(() => parseMemoryProvider("not an object")).toThrow();
  });
});
