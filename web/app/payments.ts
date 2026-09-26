// The web side of ticket 09: the backend payments API client and the
// parsers pinning its document shapes — the masked gateway config the admin
// edits, the top-up the consumer's wallet rides on. Parsers are the
// frontend half of the backend contract: unknown shapes throw rather than
// render wrong.

import { apiCall, authedHeader, errFrom } from "~/api";

export { errFrom };

// GatewayConfig is the masked configuration surface the admin sees: which
// credentials are set (never their values), the exchange rate, and whether
// top-ups are live.
export interface GatewayConfig {
  provider: string;
  apiKeySet: boolean;
  webhookSecretSet: boolean;
  currency: string;
  microsPerCent: number;
  returnBaseURL: string;
  enabled: boolean;
}

// TopUp is one attempt to buy credits: opened with the gateway, settled by
// its callback. amountMinor is cents; creditsMicros is what settlement
// credits.
export interface TopUp {
  id: string;
  accountId: string;
  reference: string;
  provider: string;
  amountMinor: number;
  currency: string;
  creditsMicros: number;
  status: "pending" | "settled" | "failed" | "cancelled";
  paymentURL: string;
  createdAt: string;
  updatedAt: string;
}

// --- Parsers ---

const STATUSES = ["pending", "settled", "failed", "cancelled"];

export function parseGatewayConfig(doc: unknown): GatewayConfig {
  if (typeof doc !== "object" || doc === null) throw new Error("gateway config is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.provider !== "string") throw new Error("gateway config carries no provider string");
  if (typeof d.api_key_set !== "boolean" || typeof d.webhook_secret_set !== "boolean")
    throw new Error("gateway config carries no credential-set flags");
  if (typeof d.micros_per_cent !== "number" || !Number.isInteger(d.micros_per_cent))
    throw new Error("gateway config micros_per_cent is not an integer");
  return {
    provider: d.provider,
    apiKeySet: d.api_key_set,
    webhookSecretSet: d.webhook_secret_set,
    currency: typeof d.currency === "string" ? d.currency : "",
    microsPerCent: d.micros_per_cent,
    returnBaseURL: typeof d.return_base_url === "string" ? d.return_base_url : "",
    enabled: d.enabled === true,
  };
}

export function parseTopUp(doc: unknown): TopUp {
  if (typeof doc !== "object" || doc === null) throw new Error("top-up is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.id !== "string" || d.id === "") throw new Error("top-up carries no id");
  if (typeof d.reference !== "string" || d.reference === "") throw new Error("top-up carries no reference");
  if (typeof d.amount_minor !== "number" || !Number.isInteger(d.amount_minor))
    throw new Error("top-up amount_minor is not an integer");
  if (typeof d.credits_micros !== "number" || !Number.isInteger(d.credits_micros))
    throw new Error("top-up credits_micros is not an integer");
  if (typeof d.status !== "string" || !STATUSES.includes(d.status))
    throw new Error(`unknown top-up status: ${String(d.status)}`);
  return {
    id: d.id,
    accountId: typeof d.account_id === "string" ? d.account_id : "",
    reference: d.reference,
    provider: typeof d.provider === "string" ? d.provider : "",
    amountMinor: d.amount_minor,
    currency: typeof d.currency === "string" ? d.currency : "",
    creditsMicros: d.credits_micros,
    status: d.status as TopUp["status"],
    paymentURL: typeof d.payment_url === "string" ? d.payment_url : "",
    createdAt: typeof d.created_at === "string" ? d.created_at : "",
    updatedAt: typeof d.updated_at === "string" ? d.updated_at : "",
  };
}

// --- Formatting ---

// formatMinor renders gateway cents the way a receipt would: 2500 → "€25.00".
export function formatMinor(amountMinor: number, currency: string): string {
  const symbols: Record<string, string> = { eur: "€", usd: "$", gbp: "£" };
  const sign = amountMinor < 0 ? "−" : "";
  const abs = Math.abs(amountMinor);
  const sym = symbols[currency.toLowerCase()] ?? `${currency.toUpperCase()} `;
  return `${sign}${sym}${Math.floor(abs / 100)}.${String(abs % 100).padStart(2, "0")}`;
}

// topUpStatusLabel translates a settlement status for the history list.
export function topUpStatusLabel(status: TopUp["status"]): string {
  const labels: Record<TopUp["status"], string> = {
    pending: "Pending",
    settled: "Settled",
    failed: "Failed",
    cancelled: "Cancelled",
  };
  return labels[status];
}

// --- API client ---

// fetchGatewayConfig reads the masked gateway configuration (admin).
export async function fetchGatewayConfig(backend: string, token: string): Promise<GatewayConfig> {
  const doc = await apiCall<unknown>(backend, "/api/v1/admin/payments/config", {
    headers: authedHeader(token),
  });
  return parseGatewayConfig(doc);
}

// saveGatewayConfig stores the gateway configuration (admin). Blank
// apiKey / webhookSecret keep the stored credential, so rotating one never
// requires re-typing the other. An empty provider disables top-ups.
export async function saveGatewayConfig(
  backend: string,
  token: string,
  body: {
    provider: string;
    apiKey?: string;
    webhookSecret?: string;
    currency: string;
    microsPerCent: number;
  },
): Promise<GatewayConfig> {
  const doc = await apiCall<unknown>(backend, "/api/v1/admin/payments/config", {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({
      provider: body.provider,
      api_key: body.apiKey ?? "",
      webhook_secret: body.webhookSecret ?? "",
      currency: body.currency,
      micros_per_cent: body.microsPerCent,
    }),
  });
  return parseGatewayConfig(doc);
}

// initiateTopUp opens a payment session for the signed-in consumer.
// Nothing is credited here — the gateway's callback settles it.
export async function initiateTopUp(
  backend: string,
  token: string,
  amountMinor: number,
): Promise<TopUp> {
  const doc = await apiCall<{ top_up?: unknown }>(backend, "/api/v1/me/topups", {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({ amount_minor: amountMinor }),
  });
  if (typeof doc.top_up !== "object" || doc.top_up === null)
    throw new Error("top-up document carries no top_up");
  return parseTopUp(doc.top_up);
}

// fetchMyTopUps loads the signed-in consumer's top-up history, newest first.
export async function fetchMyTopUps(backend: string, token: string): Promise<TopUp[]> {
  const doc = await apiCall<{ top_ups?: unknown }>(backend, "/api/v1/me/topups", {
    headers: authedHeader(token),
  });
  if (!Array.isArray(doc.top_ups)) throw new Error("top-ups document carries no top_ups array");
  return doc.top_ups.map(parseTopUp);
}
