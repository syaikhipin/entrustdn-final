// The web side of ticket 15: the earnings document parser — the split
// percentage in force, total received, the org account's balance, and the
// per-Member breakdown — plus the pricing document's optional
// revenue_share_org_percent. All expected values are literals.
import { describe, expect, it } from "vitest";
import { parseEarnings } from "./earnings";
import { parsePricingRules } from "./credits";

// A /me/earnings document exactly as the Go backend serializes it.
const earningsDoc = {
  revenue_share_org_percent: 80,
  total_received_micros: 40_000_000,
  balance_micros: 0,
  members: [
    { member_id: "mem-1", display_name: "Siobhán Ní Uaithne", earned_micros: 26_666_667 },
    { member_id: "mem-2", display_name: "Pádraig Ó Briain", earned_micros: 13_333_333 },
  ],
};

describe("parseEarnings", () => {
  it("reads the split, totals, and member lines", () => {
    const e = parseEarnings(earningsDoc);
    expect(e.revenueShareOrgPercent).toBe(80);
    expect(e.totalReceivedMicros).toBe(40_000_000);
    expect(e.balanceMicros).toBe(0);
    expect(e.members).toEqual([
      { memberId: "mem-1", displayName: "Siobhán Ní Uaithne", earnedMicros: 26_666_667 },
      { memberId: "mem-2", displayName: "Pádraig Ó Briain", earnedMicros: 13_333_333 },
    ]);
  });

  it("reads a zero-state document before any delivery", () => {
    const e = parseEarnings({
      revenue_share_org_percent: 50,
      total_received_micros: 0,
      balance_micros: 0,
      members: [],
    });
    expect(e.totalReceivedMicros).toBe(0);
    expect(e.members).toEqual([]);
  });

  it("refuses a missing percent", () => {
    const doc = { ...earningsDoc } as Record<string, unknown>;
    delete doc["revenue_share_org_percent"];
    expect(() => parseEarnings(doc)).toThrow(/percent/);
  });

  it("refuses an out-of-range percent", () => {
    expect(() => parseEarnings({ ...earningsDoc, revenue_share_org_percent: 101 })).toThrow(/percent/);
    expect(() => parseEarnings({ ...earningsDoc, revenue_share_org_percent: -1 })).toThrow(/percent/);
  });

  it("refuses non-integer micros", () => {
    expect(() => parseEarnings({ ...earningsDoc, total_received_micros: 1.5 })).toThrow(/integer/);
  });

  it("refuses a member line without a name", () => {
    const doc = {
      ...earningsDoc,
      members: [{ member_id: "mem-1", display_name: "", earned_micros: 5 }],
    };
    expect(() => parseEarnings(doc)).toThrow(/display_name/);
  });
});

describe("parsePricingRules revenue share", () => {
  it("reads an explicit revenue_share_org_percent", () => {
    const rules = parsePricingRules({
      inference: [],
      data: { cached_asset_micros_per_unit: 5_000_000, unique_micros_per_unit: 50_000_000 },
      revenue_share_org_percent: 65,
    });
    expect(rules.revenueShareOrgPercent).toBe(65);
  });

  it("leaves the percent undefined when the book predates ticket 15", () => {
    const rules = parsePricingRules({
      inference: [],
      data: { cached_asset_micros_per_unit: 5_000_000, unique_micros_per_unit: 50_000_000 },
    });
    expect(rules.revenueShareOrgPercent).toBeUndefined();
  });

  it("refuses an out-of-range percent", () => {
    expect(() =>
      parsePricingRules({
        inference: [],
        data: { cached_asset_micros_per_unit: 5_000_000, unique_micros_per_unit: 50_000_000 },
        revenue_share_org_percent: 120,
      }),
    ).toThrow(/percent/);
  });
});
