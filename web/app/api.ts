// Shared HTTP plumbing for the backend API clients (membership, credits):
// one fetch wrapper and one error-message extractor, imported by both.
// Unknown error shapes fall back to a generic message rather than render
// something wrong.

// errFrom extracts a human-facing message from a backend error document.
export function errFrom(doc: unknown): string {
  if (typeof doc === "object" && doc !== null && "error" in doc) {
    const msg = (doc as Record<string, unknown>).error;
    if (typeof msg === "string" && msg !== "") return msg;
  }
  return "The request failed — please try again.";
}

export async function apiCall<T>(backend: string, path: string, init: RequestInit = {}): Promise<T> {
  const resp = await fetch(`${backend}${path}`, {
    headers: { "Content-Type": "application/json", ...init.headers },
    ...init,
  });
  const doc = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new Error(errFrom(doc));
  return doc as T;
}

// authedHeader is the Authorization header for a session token.
export function authedHeader(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` };
}
