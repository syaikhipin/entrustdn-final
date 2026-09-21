// The web app's slice of the tracer bullet: render the backend's answer.
// These tests pin parseStatus — the shape contract between backend and page —
// against literals independent of the implementation.
import { describe, expect, it } from "vitest";
import { parseStatus } from "./status";

describe("parseStatus", () => {
  it("reads a healthy backend document", () => {
    const doc = {
      status: "ok",
      service: "thresh-backend",
      version: "dev",
      agent: { reachable: true, version: "0.1.0" },
    };
    const s = parseStatus(doc);
    expect(s.status).toBe("ok");
    expect(s.service).toBe("thresh-backend");
    expect(s.agentVersion).toBe("0.1.0");
    expect(s.agentReachable).toBe(true);
  });

  it("reads a degraded backend document", () => {
    const doc = {
      status: "degraded",
      service: "thresh-backend",
      version: "dev",
      agent: { reachable: false, error: "agent unreachable" },
    };
    const s = parseStatus(doc);
    expect(s.status).toBe("degraded");
    expect(s.agentReachable).toBe(false);
    expect(s.agentVersion).toBeUndefined();
  });

  it("rejects a document from a different service", () => {
    const doc = {
      status: "ok",
      service: "something-else",
      version: "dev",
      agent: { reachable: true },
    };
    expect(() => parseStatus(doc)).toThrow();
  });
});
