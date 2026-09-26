// The web side of ticket 09: parsers pin the backend's payments document
// shapes and formatMinor pins the money rendering. Documents are exactly as
// the Go backend serializes them — snake_case, masked credentials.
import { describe, expect, it } from "vitest";
import { formatMinor, parseGatewayConfig, parseTopUp, topUpStatusLabel } from "./payments";

// The masked config document as the backend renders it: credential VALUES
// never appear — only *_set flags.
const configDoc = {
  provider: "stripe",
  api_key_set: true,
  webhook_secret_set: true,
  currency: "eur",
  micros_per_cent: 10_000,
  return_base_url: "https://thresh.example",
  enabled: true,
};

// A disabled (cleared) config: the admin pressed "disable".
const clearedConfigDoc = {
  provider: "",
  api_key_set: false,
  webhook_secret_set: false,
  currency: "",
  micros_per_cent: 0,
  return_base_url: "",
  enabled: false,
};

// One pending top-up as POST /me/topups returns it: reference from the
// gateway, credits derived at the configured rate, payment URL attached.
const pendingTopUpDoc = {
  id: "tu_1",
  account_id: "acct-9",
  reference: "cs_test_123",
  provider: "stripe",
  amount_minor: 2500,
  currency: "eur",
  credits_micros: 25_000_000,
  status: "pending",
  movement_id: "",
  payment_url: "https://checkout.stripe.com/c/pay/cs_test_123",
  created_at: "2026-09-27T10:00:00Z",
  updated_at: "2026-09-27T10:00:00Z",
};

describe("parseGatewayConfig", () => {
  it("reads the masked config", () => {
    const cfg = parseGatewayConfig(configDoc);
    expect(cfg).toEqual({
      provider: "stripe",
      apiKeySet: true,
      webhookSecretSet: true,
      currency: "eur",
      microsPerCent: 10_000,
      returnBaseURL: "https://thresh.example",
      enabled: true,
    });
  });

  it("reads a cleared config as disabled", () => {
    const cfg = parseGatewayConfig(clearedConfigDoc);
    expect(cfg.enabled).toBe(false);
    expect(cfg.apiKeySet).toBe(false);
    expect(cfg.provider).toBe("");
  });

  it("refuses a config without credential flags", () => {
    expect(() => parseGatewayConfig({ provider: "stripe", currency: "eur" })).toThrow(/credential-set flags/);
  });

  it("refuses a non-integer exchange rate", () => {
    expect(() => parseGatewayConfig({ ...configDoc, micros_per_cent: 1.5 })).toThrow(/not an integer/);
  });
});

describe("parseTopUp", () => {
  it("reads a pending top-up with its payment URL", () => {
    const tu = parseTopUp(pendingTopUpDoc);
    expect(tu.reference).toBe("cs_test_123");
    expect(tu.amountMinor).toBe(2500);
    expect(tu.creditsMicros).toBe(25_000_000);
    expect(tu.status).toBe("pending");
    expect(tu.paymentURL).toContain("checkout.stripe.com");
  });

  it("accepts every settlement status", () => {
    for (const status of ["settled", "failed", "cancelled"]) {
      expect(parseTopUp({ ...pendingTopUpDoc, status }).status).toBe(status);
    }
  });

  it("refuses an unknown status", () => {
    expect(() => parseTopUp({ ...pendingTopUpDoc, status: "refunded" })).toThrow(/unknown top-up status/);
  });

  it("refuses a top-up without a reference", () => {
    expect(() => parseTopUp({ ...pendingTopUpDoc, reference: "" })).toThrow(/no reference/);
  });
});

describe("formatMinor", () => {
  it("renders euro cents as a receipt", () => {
    expect(formatMinor(2500, "eur")).toBe("€25.00");
    expect(formatMinor(5, "eur")).toBe("€0.05");
    expect(formatMinor(10_000_000_00, "eur")).toBe("€10000000.00");
  });

  it("falls back to the ISO code for unknown currencies", () => {
    expect(formatMinor(990, "sek")).toBe("SEK 9.90");
  });
});

describe("topUpStatusLabel", () => {
  it("translates every status", () => {
    expect(topUpStatusLabel("pending")).toBe("Pending");
    expect(topUpStatusLabel("settled")).toBe("Settled");
    expect(topUpStatusLabel("failed")).toBe("Failed");
    expect(topUpStatusLabel("cancelled")).toBe("Cancelled");
  });
});
