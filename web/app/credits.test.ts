// The web side of ticket 03: parsers pin the backend's credits document
// shapes, formatCredits pins the human rendering, and the micros math stays
// exact. All expected values are literals — independent of the backend.
import { describe, expect, it } from "vitest";
import {
  formatCredits,
  formatMicros,
  parseCreditsView,
  parsePricingRules,
} from "./credits";

// A /me/credits document exactly as the Go backend serializes it: snake_case
// keys, movement ids, entries summing to zero.
const creditsDoc = {
  balance_micros: 49_987_500,
  movements: [
    {
      id: "mov_abc",
      kind: "inference_charge",
      actor_id: "admin-1",
      memo: "test charge",
      created_at: "2026-09-22T02:00:00Z",
      entries: [
        { scope: "acct:1111", amount_micros: -12_500, memo: "test-model" },
        { scope: "sys:platform", amount_micros: 12_500, memo: "test-model" },
      ],
    },
    {
      id: "mov_def",
      kind: "grant",
      actor_id: "admin-1",
      memo: "pilot seed",
      created_at: "2026-09-22T01:00:00Z",
      entries: [
        { scope: "acct:1111", amount_micros: 50_000_000, memo: "pilot seed" },
        { scope: "sys:treasury", amount_micros: -50_000_000, memo: "pilot seed" },
      ],
    },
  ],
};

describe("parseCreditsView", () => {
  it("reads balance and newest-first movements", () => {
    const view = parseCreditsView(creditsDoc);
    expect(view.balanceMicros).toBe(49_987_500);
    expect(view.movements).toHaveLength(2);
    expect(view.movements[0]!.kind).toBe("inference_charge");
    expect(view.movements[1]!.kind).toBe("grant");
  });

  it("reads entries with their scopes and memos", () => {
    const view = parseCreditsView(creditsDoc);
    expect(view.movements[0]!.entries).toEqual([
      { scope: "acct:1111", amountMicros: -12_500, memo: "test-model" },
      { scope: "sys:platform", amountMicros: 12_500, memo: "test-model" },
    ]);
  });

  it("reads an inference charge's usage detail", () => {
    const doc = {
      balance_micros: 49_987_500,
      movements: [
        {
          ...creditsDoc.movements[0],
          inference: {
            model: "test-model",
            input_tokens: 1_000,
            cached_input_tokens: 500,
            output_tokens: 1_000,
            input_micros_per_1k: 2_500,
            cached_input_micros_per_1k: 1_250,
            output_micros_per_1k: 10_000,
          },
        },
        creditsDoc.movements[1],
      ],
    };
    const view = parseCreditsView(doc);
    expect(view.movements[0]!.inference).toEqual({
      model: "test-model",
      inputTokens: 1_000,
      cachedInputTokens: 500,
      outputTokens: 1_000,
      inputMicrosPer1K: 2_500,
      cachedInputMicrosPer1K: 1_250,
      outputMicrosPer1K: 10_000,
    });
    expect(view.movements[1]!.inference).toBeUndefined();
  });

  it("refuses an inference detail with a non-integer count", () => {
    const doc = {
      balance_micros: 0,
      movements: [
        {
          ...creditsDoc.movements[0],
          inference: { model: "m", input_tokens: 1.5 },
        },
      ],
    };
    expect(() => parseCreditsView(doc)).toThrow(/non-integer/);
  });

  it("accepts an empty history", () => {
    const view = parseCreditsView({ balance_micros: 0, movements: [] });
    expect(view.balanceMicros).toBe(0);
    expect(view.movements).toEqual([]);
  });

  it("refuses a document without an integer balance", () => {
    expect(() => parseCreditsView({ movements: [] })).toThrow();
    expect(() => parseCreditsView({ balance_micros: "12.5", movements: [] })).toThrow();
    expect(() => parseCreditsView({ balance_micros: 1.5, movements: [] })).toThrow();
  });

  it("refuses a movement with an unknown kind", () => {
    const doc = {
      balance_micros: 0,
      movements: [{ ...creditsDoc.movements[0], kind: "magic_topup" }],
    };
    expect(() => parseCreditsView(doc)).toThrow(/unknown movement kind/);
  });

  it("refuses a movement without entries", () => {
    const doc = {
      balance_micros: 0,
      movements: [{ ...creditsDoc.movements[0], entries: undefined }],
    };
    expect(() => parseCreditsView(doc)).toThrow(/entries/);
  });

  it("refuses an entry without an integer amount", () => {
    const doc = {
      balance_micros: 0,
      movements: [
        {
          ...creditsDoc.movements[0],
          entries: [{ scope: "acct:1", amount_micros: 0.5 }],
        },
      ],
    };
    expect(() => parseCreditsView(doc)).toThrow(/integer/);
  });
});

const pricingDoc = {
  inference: [
    {
      model: "gpt-test",
      input_micros_per_1k: 2_500,
      cached_input_micros_per_1k: 1_250,
      output_micros_per_1k: 10_000,
    },
  ],
  data: { cached_asset_micros_per_unit: 5_000_000, unique_micros_per_unit: 50_000_000 },
};

describe("parsePricingRules", () => {
  it("reads the price book", () => {
    const rules = parsePricingRules(pricingDoc);
    expect(rules.inference).toHaveLength(1);
    expect(rules.inference[0]).toEqual({
      model: "gpt-test",
      inputMicrosPer1K: 2_500,
      cachedInputMicrosPer1K: 1_250,
      outputMicrosPer1K: 10_000,
    });
    expect(rules.data).toEqual({ cachedAssetMicrosPerUnit: 5_000_000, uniqueMicrosPerUnit: 50_000_000 });
  });

  it("refuses a negative rate", () => {
    const doc = {
      ...pricingDoc,
      inference: [{ ...pricingDoc.inference[0], input_micros_per_1k: -1 }],
    };
    expect(() => parsePricingRules(doc)).toThrow(/non-negative/);
  });

  it("refuses a document without data rules", () => {
    expect(() => parsePricingRules({ inference: [] })).toThrow(/data/);
  });

  it("refuses a rule without a model", () => {
    const doc = {
      ...pricingDoc,
      inference: [{ ...pricingDoc.inference[0], model: "" }],
    };
    expect(() => parsePricingRules(doc)).toThrow(/model/);
  });
});

describe("formatCredits", () => {
  it("renders whole credits", () => {
    expect(formatCredits(100_000_000)).toBe("100 cr");
  });

  it("renders fractions with trailing zeros trimmed", () => {
    expect(formatCredits(97_500_000)).toBe("97.5 cr");
    expect(formatCredits(1_234_567_890)).toBe("1,234.56789 cr");
  });

  it("renders zero and sub-credit dust", () => {
    expect(formatCredits(0)).toBe("0 cr");
    expect(formatCredits(250)).toBe("0.00025 cr");
  });

  it("keeps negative signs (system scopes may go under)", () => {
    expect(formatCredits(-100_000_000)).toBe("−100 cr");
  });
});

describe("formatMicros", () => {
  it("renders raw micros with grouping", () => {
    expect(formatMicros(2_500)).toBe("2,500 μcr");
    expect(formatMicros(0)).toBe("0 μcr");
  });
});
