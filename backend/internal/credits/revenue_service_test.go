package credits_test

import (
	"context"
	"errors"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
)

// PostRevenueShare (ticket 15) at the domain seam: one balanced
// revenue_share movement carries the whole split — the platform debited its
// share, each contributing Member's scope credited pro-rata, the org
// account absorbing the pro-rata residual. Members hold no platform
// accounts; their earnings live in member-scoped entries the earnings view
// reads back.

func TestMemberScopeConvention(t *testing.T) {
	if got := credits.MemberScope("mem-1"); got != "member:mem-1" {
		t.Errorf("MemberScope = %q, want member:mem-1", got)
	}
}

func TestPostRevenueShareSplitsAndDistributes(t *testing.T) {
	ledger := credits.NewMemoryStore()
	svc := credits.NewService(ledger, func(context.Context) (credits.PricingRules, error) {
		return testPricingRulesDoc(), nil // no percentage: the 80/20 default
	})

	// The delivery charge lands first: the consumer pays 50 cr, the
	// platform scope holds the whole premium.
	if _, err := svc.Grant(t.Context(), credits.AccountScope("consumer-1"), 100*credits.MicrosPerCredit, "admin", "pilot seed"); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	if _, err := svc.ChargeData(t.Context(), credits.DataUsage{Class: credits.DataUnique, Units: 1},
		credits.Charge{Scope: credits.AccountScope("consumer-1")}); err != nil {
		t.Fatalf("seed charge: %v", err)
	}

	mov, err := svc.PostRevenueShare(t.Context(), credits.RevenueShare{
		OrgScope:  credits.AccountScope("org-1"),
		RequestID: "req-1",
		ActorID:   "consumer-1",
		Memo:      "collection delivery",
		Participants: []credits.Participant{
			{MemberID: "mem-1", Weight: 3},
			{MemberID: "mem-2", Weight: 1},
		},
	}, 50*credits.MicrosPerCredit)
	if err != nil {
		t.Fatalf("PostRevenueShare: %v", err)
	}
	if mov.Kind != credits.KindRevenueShare {
		t.Errorf("kind = %q, want %q", mov.Kind, credits.KindRevenueShare)
	}
	if mov.RequestID != "req-1" {
		t.Errorf("request id = %q, want req-1", mov.RequestID)
	}

	// 50 cr premium at 80/20: the org receives 40 cr and distributes all of
	// it (members 3:1, exact split — no residual); the platform passed the
	// 40 cr on and keeps its 10 cr.
	for scope, want := range map[credits.Scope]int64{
		credits.MemberScope("mem-1"):         30_000_000,
		credits.MemberScope("mem-2"):         10_000_000,
		credits.Scope(credits.ScopePlatform): 10_000_000, // +50 charged, −40 passed on
		credits.AccountScope("org-1"):        0,          // received 40, distributed 40
	} {
		got, err := ledger.Balance(t.Context(), scope)
		if err != nil {
			t.Fatalf("balance %s: %v", scope, err)
		}
		if got != want {
			t.Errorf("balance %s = %d, want %d", scope, got, want)
		}
	}
}

func TestPostRevenueShareResidualGoesToTheOrg(t *testing.T) {
	ledger := credits.NewMemoryStore()
	svc := credits.NewService(ledger, func(context.Context) (credits.PricingRules, error) {
		return testPricingRulesDoc(), nil
	})

	// 40 cr org share over weights 2:1 floors to 26_666_666 + 13_333_333
	// = 39_999_999: ProRata hands the indivisible micro to the largest
	// fractional part (mem-1), so members absorb every distributed micro
	// and the org account nets exactly zero — gross 40 cr in, 39_999_999
	// out to members.
	mov, err := svc.PostRevenueShare(t.Context(), credits.RevenueShare{
		OrgScope:  credits.AccountScope("org-1"),
		RequestID: "req-1",
		Participants: []credits.Participant{
			{MemberID: "mem-1", Weight: 2},
			{MemberID: "mem-2", Weight: 1},
		},
	}, 50*credits.MicrosPerCredit)
	if err != nil {
		t.Fatalf("PostRevenueShare: %v", err)
	}
	if !credits.Balanced(mov.Entries) {
		t.Fatalf("movement unbalanced: %+v", mov.Entries)
	}
	// The movement shows the gross receipt and the member distribution as
	// separate org entries.
	orgEntries := 0
	for _, e := range mov.Entries {
		if e.Scope == credits.AccountScope("org-1") {
			orgEntries++
		}
	}
	if orgEntries != 2 {
		t.Errorf("org entries = %d, want 2 (gross receipt + member distribution)", orgEntries)
	}
	bal, _ := ledger.Balance(t.Context(), credits.AccountScope("org-1"))
	if bal != 0 {
		t.Errorf("org net balance = %d, want 0", bal)
	}
	m1, _ := ledger.Balance(t.Context(), credits.MemberScope("mem-1"))
	m2, _ := ledger.Balance(t.Context(), credits.MemberScope("mem-2"))
	if m1 != 26_666_667 || m2 != 13_333_333 {
		t.Errorf("member balances = (%d, %d), want (26_666_667, 13_333_333)", m1, m2)
	}
}

func TestPostRevenueShareZeroParticipantsCreditsTheOrg(t *testing.T) {
	ledger := credits.NewMemoryStore()
	svc := credits.NewService(ledger, func(context.Context) (credits.PricingRules, error) {
		return testPricingRulesDoc(), nil
	})

	// Nobody contributed (collection finalized incomplete): the org's share
	// still accrues to the org account — no member has earned anything.
	// The delivery charge lands first, as it always does.
	if _, err := svc.Grant(t.Context(), credits.AccountScope("consumer-1"), 100*credits.MicrosPerCredit, "admin", "pilot seed"); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	if _, err := svc.ChargeData(t.Context(), credits.DataUsage{Class: credits.DataUnique, Units: 1},
		credits.Charge{Scope: credits.AccountScope("consumer-1")}); err != nil {
		t.Fatalf("seed charge: %v", err)
	}
	_, err := svc.PostRevenueShare(t.Context(), credits.RevenueShare{
		OrgScope:  credits.AccountScope("org-1"),
		RequestID: "req-1",
	}, 50*credits.MicrosPerCredit)
	if err != nil {
		t.Fatalf("PostRevenueShare: %v", err)
	}
	org, _ := ledger.Balance(t.Context(), credits.AccountScope("org-1"))
	plat, _ := ledger.Balance(t.Context(), credits.Scope(credits.ScopePlatform))
	// After the 50 cr charge (+50) and the split (−40), the platform's
	// remaining balance is exactly its own 10 cr share.
	if org != 40_000_000 || plat != 10_000_000 {
		t.Errorf("balances = (org %d, platform %d), want (40 cr, +10 cr)", org, plat)
	}
}

func TestPostRevenueShareZeroSharePostsNothing(t *testing.T) {
	ledger := credits.NewMemoryStore()
	zero := 0 // admin keeps 100% — nothing moves
	svc := credits.NewService(ledger, func(context.Context) (credits.PricingRules, error) {
		return credits.PricingRules{
			Inference:              testPricingRulesDoc().Inference,
			RevenueShareOrgPercent: &zero,
		}, nil
	})

	mov, err := svc.PostRevenueShare(t.Context(), credits.RevenueShare{
		OrgScope: credits.AccountScope("org-1"),
	}, 50*credits.MicrosPerCredit)
	if err != nil {
		t.Fatalf("PostRevenueShare: %v", err)
	}
	if mov.ID != "" {
		t.Errorf("movement posted despite a zero org share: %+v", mov)
	}
	if balances, _ := ledger.Balances(t.Context()); len(balances) != 0 {
		t.Errorf("ledger touched: %v", balances)
	}
}

func TestPostRevenueShareRefusesNonPositivePremium(t *testing.T) {
	ledger := credits.NewMemoryStore()
	svc := credits.NewService(ledger, func(context.Context) (credits.PricingRules, error) {
		return testPricingRulesDoc(), nil
	})
	if _, err := svc.PostRevenueShare(t.Context(), credits.RevenueShare{
		OrgScope: credits.AccountScope("org-1"),
	}, 0); err == nil {
		t.Error("zero premium accepted")
	}
	if _, err := svc.PostRevenueShare(t.Context(), credits.RevenueShare{
		OrgScope: credits.AccountScope("org-1"),
	}, -5); err == nil {
		t.Error("negative premium accepted")
	}
	if movs, _ := ledger.MovementsByScope(t.Context(), credits.Scope(credits.ScopePlatform), 10); len(movs) != 0 {
		t.Errorf("refused premiums left postings: %d", len(movs))
	}
}

func TestPostRevenueShareFailsLoudlyOnRulesError(t *testing.T) {
	ledger := credits.NewMemoryStore()
	svc := credits.NewService(ledger, func(context.Context) (credits.PricingRules, error) {
		return credits.PricingRules{}, errors.New("price book unavailable")
	})
	if _, err := svc.PostRevenueShare(t.Context(), credits.RevenueShare{
		OrgScope: credits.AccountScope("org-1"),
	}, 100); err == nil {
		t.Fatal("split posted without a price book")
	}
}

func TestRevenueShareReceivedIgnoresOtherKindsAndDebits(t *testing.T) {
	ledger := credits.NewMemoryStore()
	svc := credits.NewService(ledger, func(context.Context) (credits.PricingRules, error) {
		return testPricingRulesDoc(), nil
	})
	org := credits.AccountScope("org-1")

	// A grant and a charge touch the org scope too — none of it is revenue
	// share, and the org-side distribution debit must not shrink the gross.
	if _, err := svc.Grant(t.Context(), org, 10*credits.MicrosPerCredit, "admin", "seed"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	for range 2 {
		if _, err := svc.PostRevenueShare(t.Context(), credits.RevenueShare{
			OrgScope: org, RequestID: "req-x", Memo: "collection revenue share",
			Participants: []credits.Participant{{MemberID: "mem-1", Weight: 1}},
		}, 50*credits.MicrosPerCredit); err != nil {
			t.Fatalf("PostRevenueShare: %v", err)
		}
	}
	got, err := ledger.RevenueShareReceived(t.Context(), org)
	if err != nil {
		t.Fatalf("RevenueShareReceived: %v", err)
	}
	if got != 2*40_000_000 {
		t.Errorf("gross received = %d, want 80_000_000 (two deliveries' org share)", got)
	}
}
