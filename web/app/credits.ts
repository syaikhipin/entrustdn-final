// The web side of the credits ledger (ticket 03): the backend credits API
// client, parsers pinning its document shapes, and the micros formatting the
// spend views share. Parsers are the frontend half of the backend contract —
// unknown shapes throw rather than render wrong.

import { apiCall, authedHeader, errFrom } from "~/api";

export { errFrom };

export interface LedgerEntry {
  scope: string;
  amountMicros: number;
  memo: string;
}

export interface LedgerMovement {
  id: string;
  kind: string;
  actorId: string;
  requestId: string;
  memo: string;
  createdAt: string;
  entries: LedgerEntry[];
  inference?: InferenceDetail;
}

// InferenceDetail explains an inference charge: the metered usage and the
// rates applied — the spend view's line-by-line justification.
export interface InferenceDetail {
  model: string;
  inputTokens: number;
  cachedInputTokens: number;
  outputTokens: number;
  inputMicrosPer1K: number;
  cachedInputMicrosPer1K: number;
  outputMicrosPer1K: number;
}

export interface CreditsView {
  balanceMicros: number;
  movements: LedgerMovement[];
}

export interface InferenceRule {
  model: string;
  inputMicrosPer1K: number;
  cachedInputMicrosPer1K: number;
  outputMicrosPer1K: number;
}

export interface PricingRules {
  inference: InferenceRule[];
  data: {
    cachedAssetMicrosPerUnit: number;
    uniqueMicrosPerUnit: number;
  };
}

// --- Parsers ---

const KINDS = [
  "grant",
  "adjustment",
  "inference_charge",
  "data_charge",
  "top_up",
  "revenue_share",
];

function parseEntry(doc: unknown): LedgerEntry {
  if (typeof doc !== "object" || doc === null) throw new Error("entry is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.scope !== "string" || d.scope === "") throw new Error("entry carries no scope");
  if (typeof d.amount_micros !== "number" || !Number.isInteger(d.amount_micros))
    throw new Error("entry amount_micros is not an integer");
  return {
    scope: d.scope,
    amountMicros: d.amount_micros,
    memo: typeof d.memo === "string" ? d.memo : "",
  };
}

function parseMovement(doc: unknown): LedgerMovement {
  if (typeof doc !== "object" || doc === null) throw new Error("movement is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.id !== "string" || d.id === "") throw new Error("movement carries no id");
  if (typeof d.kind !== "string" || !KINDS.includes(d.kind))
    throw new Error(`unknown movement kind: ${String(d.kind)}`);
  if (typeof d.created_at !== "string" || d.created_at === "")
    throw new Error("movement carries no created_at");
  if (!Array.isArray(d.entries)) throw new Error("movement carries no entries array");
  let inference: InferenceDetail | undefined;
  if (typeof d.inference === "object" && d.inference !== null) {
    const i = d.inference as Record<string, unknown>;
    const int = (v: unknown): number => {
      if (typeof v !== "number" || !Number.isInteger(v)) throw new Error("inference detail carries a non-integer count");
      return v;
    };
    inference = {
      model: String(i.model ?? ""),
      inputTokens: int(i.input_tokens),
      cachedInputTokens: int(i.cached_input_tokens),
      outputTokens: int(i.output_tokens),
      inputMicrosPer1K: int(i.input_micros_per_1k),
      cachedInputMicrosPer1K: int(i.cached_input_micros_per_1k),
      outputMicrosPer1K: int(i.output_micros_per_1k),
    };
  }
  return {
    id: d.id,
    kind: d.kind,
    actorId: typeof d.actor_id === "string" ? d.actor_id : "",
    requestId: typeof d.request_id === "string" ? d.request_id : "",
    memo: typeof d.memo === "string" ? d.memo : "",
    createdAt: d.created_at,
    entries: d.entries.map(parseEntry),
    inference,
  };
}

// parseCreditsView reads the /me/credits document: the derived balance and
// the movement history (newest first, each with its balanced entries).
export function parseCreditsView(doc: unknown): CreditsView {
  if (typeof doc !== "object" || doc === null) throw new Error("credits document is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.balance_micros !== "number" || !Number.isInteger(d.balance_micros))
    throw new Error("credits document carries no integer balance_micros");
  if (!Array.isArray(d.movements)) throw new Error("credits document carries no movements array");
  return {
    balanceMicros: d.balance_micros,
    movements: d.movements.map(parseMovement),
  };
}

function parseInferenceRule(doc: unknown): InferenceRule {
  if (typeof doc !== "object" || doc === null) throw new Error("inference rule is not an object");
  const d = doc as Record<string, unknown>;
  if (typeof d.model !== "string" || d.model === "") throw new Error("inference rule carries no model");
  const rate = (key: string): number => {
    const v = d[key];
    if (typeof v !== "number" || !Number.isInteger(v) || v < 0)
      throw new Error(`inference rule ${key} is not a non-negative integer`);
    return v;
  };
  return {
    model: d.model,
    inputMicrosPer1K: rate("input_micros_per_1k"),
    cachedInputMicrosPer1K: rate("cached_input_micros_per_1k"),
    outputMicrosPer1K: rate("output_micros_per_1k"),
  };
}

// parsePricingRules reads the admin price book document.
export function parsePricingRules(doc: unknown): PricingRules {
  if (typeof doc !== "object" || doc === null) throw new Error("pricing document is not an object");
  const d = doc as Record<string, unknown>;
  if (!Array.isArray(d.inference)) throw new Error("pricing document carries no inference array");
  const data = d.data as Record<string, unknown> | undefined;
  if (typeof data !== "object" || data === null) throw new Error("pricing document carries no data rules");
  const dataRate = (key: string): number => {
    const v = data[key];
    if (typeof v !== "number" || !Number.isInteger(v) || v < 0)
      throw new Error(`data rule ${key} is not a non-negative integer`);
    return v;
  };
  return {
    inference: d.inference.map(parseInferenceRule),
    data: {
      cachedAssetMicrosPerUnit: dataRate("cached_asset_micros_per_unit"),
      uniqueMicrosPerUnit: dataRate("unique_micros_per_unit"),
    },
  };
}

// --- Formatting ---

// formatCredits renders micros as a human credit amount: 97_500_000 →
// "97.5 cr". Trailing zeros trimmed; negative balances keep their sign.
export function formatCredits(micros: number): string {
  const sign = micros < 0 ? "−" : "";
  const abs = Math.abs(micros);
  const whole = Math.floor(abs / 1_000_000);
  let frac = String(abs % 1_000_000).padStart(6, "0").replace(/0+$/, "");
  if (frac !== "") frac = `.${frac}`;
  return `${sign}${whole.toLocaleString("en-US")}${frac} cr`;
}

// formatMicros renders a raw micros amount for the price book, where exact
// integers matter.
export function formatMicros(micros: number): string {
  return `${micros.toLocaleString("en-US")} μcr`;
}

// --- API client ---

// fetchMyCredits loads the signed-in account's balance and entry history.
export async function fetchMyCredits(backend: string, token: string): Promise<CreditsView> {
  const doc = await apiCall<unknown>(backend, "/api/v1/me/credits", {
    headers: authedHeader(token),
  });
  return parseCreditsView(doc);
}

// adminGrant credits an account from the treasury (positive amounts only).
export async function adminGrant(
  backend: string,
  token: string,
  accountId: string,
  amountMicros: number,
  memo: string,
): Promise<void> {
  await apiCall(backend, "/api/v1/admin/credits/grant", {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({ account_id: accountId, amount_micros: amountMicros, memo }),
  });
}

// adminAdjust posts a signed adjustment; the backend refuses overdrafts.
export async function adminAdjust(
  backend: string,
  token: string,
  accountId: string,
  amountMicros: number,
  memo: string,
): Promise<void> {
  await apiCall(backend, "/api/v1/admin/credits/adjust", {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({ account_id: accountId, amount_micros: amountMicros, memo }),
  });
}

// adminCharge prices a test charge automatically against the price book.
export async function adminCharge(
  backend: string,
  token: string,
  body: {
    accountId: string;
    inference?: { model: string; inputTokens: number; cachedInputTokens: number; outputTokens: number };
    data?: { class: "cached" | "unique"; units: number };
    memo: string;
  },
): Promise<number> {
  const doc = await apiCall<{ charged_micros?: unknown }>(backend, "/api/v1/admin/credits/charge", {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({
      account_id: body.accountId,
      ...(body.inference
        ? {
            inference: {
              model: body.inference.model,
              input_tokens: body.inference.inputTokens,
              cached_input_tokens: body.inference.cachedInputTokens,
              output_tokens: body.inference.outputTokens,
            },
          }
        : {}),
      ...(body.data ? { data: { class: body.data.class, units: body.data.units } } : {}),
      memo: body.memo,
    }),
  });
  if (typeof doc.charged_micros !== "number") throw new Error("charge document carries no charged_micros");
  return doc.charged_micros;
}

// fetchPricing reads the current price book (admin).
export async function fetchPricing(backend: string, token: string): Promise<PricingRules> {
  const doc = await apiCall<unknown>(backend, "/api/v1/admin/pricing", {
    headers: authedHeader(token),
  });
  return parsePricingRules(doc);
}

// savePricing validates and saves a rewritten price book (admin).
export async function savePricing(backend: string, token: string, rules: PricingRules): Promise<void> {
  await apiCall(backend, "/api/v1/admin/pricing", {
    method: "POST",
    headers: authedHeader(token),
    body: JSON.stringify({
      inference: rules.inference.map((r) => ({
        model: r.model,
        input_micros_per_1k: r.inputMicrosPer1K,
        cached_input_micros_per_1k: r.cachedInputMicrosPer1K,
        output_micros_per_1k: r.outputMicrosPer1K,
      })),
      data: {
        cached_asset_micros_per_unit: rules.data.cachedAssetMicrosPerUnit,
        unique_micros_per_unit: rules.data.uniqueMicrosPerUnit,
      },
    }),
  });
}
