// The backend status document, as the web app consumes it. Mirrors the
// backend's /api/v1/status response shape (Seam 1 as seen by the frontend).
export interface BackendStatus {
  status: "ok" | "degraded";
  service: string;
  version: string;
  agentReachable: boolean;
  agentVersion?: string;
}

export function parseStatus(doc: unknown): BackendStatus {
  if (typeof doc !== "object" || doc === null) {
    throw new Error("status document is not an object");
  }
  const d = doc as Record<string, unknown>;
  if (d.service !== "thresh-backend") {
    throw new Error(`unexpected service: ${String(d.service)}`);
  }
  if (d.status !== "ok" && d.status !== "degraded") {
    throw new Error(`unexpected status: ${String(d.status)}`);
  }
  const agent = d.agent as Record<string, unknown> | undefined;
  const agentReachable = agent?.reachable === true;
  const agentVersion =
    typeof agent?.version === "string" ? (agent.version as string) : undefined;
  return {
    status: d.status,
    service: d.service as string,
    version: String(d.version ?? ""),
    agentReachable,
    agentVersion,
  };
}

export async function fetchStatus(
  backendBaseURL: string,
): Promise<BackendStatus> {
  const resp = await fetch(`${backendBaseURL}/api/v1/status`);
  if (!resp.ok) {
    throw new Error(`backend status endpoint returned ${resp.status}`);
  }
  return parseStatus(await resp.json());
}
