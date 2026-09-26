package collections_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/anonymize"
	"github.com/syaikhipin/entrustdn-final/backend/internal/collections"
	"github.com/syaikhipin/entrustdn-final/backend/internal/conversations"
	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
)

// Delivery (ticket 12, ADR 0005): a finalized Collection reaches the Data
// Consumer only through the delivery cleaner — member names and contact
// points never leave the platform; participants carry pseudonyms resolved
// through the org's pseudonym map. The delivery is charged to the Ledger
// as unique data at the current price book.

// recordingCleaner wraps the real anonymize delivery and records what was
// handed in — the test asserts the raw payload was cleaned, not skipped.
type recordingCleaner struct {
	inner  *anonymize.Delivery
	gotOrg string
	gotFmt string
	got    []byte
	calls  int
}

func (r *recordingCleaner) Collection(ctx context.Context, orgID, format string, payload []byte) (anonymize.CleanedCollection, error) {
	r.calls++
	r.gotOrg, r.gotFmt, r.got = orgID, format, payload
	return r.inner.Collection(ctx, orgID, format, payload)
}

// deliveryFixture completes a one-question collection for mem-1 (mem-2
// left unanswered and the collection finalized incomplete) and wires the
// delivery path: credits at a known unique rate, a real pseudonym map, the
// recording cleaner. Returns the parts the assertions need.
type deliveryFixture struct {
	svc     *collections.Service
	credits *credits.Service
	ledger  *credits.MemoryStore
	cleaner *recordingCleaner
	// rules is the live price book the credits service reads — tests tune
	// it (e.g. the revenue-share percentage) before delivering. A pointer
	// so a mutation after fixture setup is visible to the service.
	rules *credits.PricingRules
}

func deliveryFixtureFor(t *testing.T) deliveryFixture {
	t.Helper()
	svc, _, convs, _ := fixtureShared(t)
	past := fixedNow.Add(-time.Hour)
	c, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1", MemberIDs: []string{"mem-1", "mem-2"},
		Questions: []string{farmQ}, Deadline: &past,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedConversation(t, convs, "conv-1", "mem-1", "Siobhán Ní Uaithne",
		[]string{farmQ}, []string{"42 hectares of good land"}, conversations.StatusCompleted)
	got, err := svc.Sync(t.Context(), c.ID, "org-1")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got.Status != collections.StatusIncomplete {
		t.Fatalf("fixture collection = %s, want incomplete (mem-2 never answered)", got.Status)
	}

	ledger := credits.NewMemoryStore()
	rules := testRules()
	f := deliveryFixture{svc: svc, ledger: ledger, rules: &rules}
	credSvc := credits.NewService(ledger, func(context.Context) (credits.PricingRules, error) {
		return *f.rules, nil
	})
	// Fund the consumer so the charge can land.
	if _, err := credSvc.Grant(t.Context(), credits.AccountScope("consumer-1"), 100*credits.MicrosPerCredit, "admin", "pilot seed"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	pmap := anonymize.NewMemoryMap()
	f.cleaner = &recordingCleaner{inner: anonymize.NewDelivery(pmap)}
	f.credits = credSvc
	svc.WithCredits(credSvc).WithCleaner(f.cleaner).WithPseudonyms(pmap)
	return f
}

func TestDeliveryCarriesPseudonymsNeverIdentities(t *testing.T) {
	f := deliveryFixtureFor(t)
	c, err := f.svc.ByID(t.Context(), mustCollectionID(t, f.svc), "org-1")
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}

	payload, err := f.svc.Deliver(t.Context(), c.ID, "consumer-1")
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	s := string(payload)
	if strings.Contains(s, "Siobhán") || strings.Contains(s, "Pádraig") ||
		strings.Contains(s, "+353860000001") || strings.Contains(s, "mem-1") || strings.Contains(s, "mem-2") {
		t.Fatalf("delivered payload leaks an identity:\n%s", s)
	}
	if !strings.Contains(s, "42 hectares of good land") {
		t.Errorf("delivered payload lost the accepted answer:\n%s", s)
	}
	if !strings.Contains(s, farmQ) {
		t.Errorf("delivered payload lost the question:\n%s", s)
	}
	// The cleaner ran on exactly this payload: no bypass.
	if f.cleaner.calls != 1 || !strings.EqualFold(f.cleaner.gotFmt, "csv") {
		t.Errorf("cleaner calls = %d fmt = %q, want 1 × csv", f.cleaner.calls, f.cleaner.gotFmt)
	}
}

func TestDeliveryChargesUniqueDataRate(t *testing.T) {
	f := deliveryFixtureFor(t)
	c := mustCollectionID(t, f.svc)

	before, err := f.ledger.Balance(t.Context(), credits.AccountScope("consumer-1"))
	if err != nil {
		t.Fatalf("balance: %v", err)
	}

	if _, err := f.svc.Deliver(t.Context(), c, "consumer-1"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	after, err := f.ledger.Balance(t.Context(), credits.AccountScope("consumer-1"))
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	want := testRules().Data.UniqueMicrosPerUnit // one unit: this collection delivery
	if before-after != want {
		t.Errorf("charged %d µcr, want %d (unique data, one unit)", before-after, want)
	}
}

func TestDeliveryRefusesUnfinalizedAndStrangers(t *testing.T) {
	f := deliveryFixtureFor(t)

	// A fresh collection still collecting must not deliver.
	svc2, _, convs2, _ := fixtureShared(t)
	c2, err := svc2.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1", MemberIDs: []string{"mem-1"}, Questions: []string{farmQ},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = convs2
	svc2.WithCredits(f.credits).WithCleaner(f.cleaner).WithPseudonyms(anonymize.NewMemoryMap())
	if _, err := svc2.Deliver(t.Context(), c2.ID, "consumer-1"); !errors.Is(err, collections.ErrClosed) {
		t.Errorf("deliver while collecting: err = %v, want ErrClosed", err)
	}

	if _, err := f.svc.Deliver(t.Context(), mustCollectionID(t, f.svc), "consumer-999"); !errors.Is(err, collections.ErrNotFound) {
		t.Errorf("stranger delivery: err = %v, want ErrNotFound", err)
	}
}

func TestDeliveryWithoutCreditsWiringFailsLoudly(t *testing.T) {
	// A deployment without the ledger must not silently deliver free data:
	// the wiring is mandatory, and a nil credits service is a programmer
	// error caught at the seam.
	svc, _, _, _ := fixtureShared(t)
	if _, err := svc.Deliver(t.Context(), "whatever", "consumer-1"); err == nil {
		t.Fatal("delivery without credits wiring succeeded")
	}
}

// testRules prices the delivery charge: unique data at 50 credits per unit
// (matching the requests-package fixture convention — worked examples, not
// recomputations).
func testRules() credits.PricingRules {
	return credits.PricingRules{
		Inference: []credits.InferenceRule{{
			Model: "test-model", InputMicrosPer1K: 2_500,
			CachedInputMicrosPer1K: 1_250, OutputMicrosPer1K: 10_000,
		}},
		Data: credits.DataRules{CachedAssetMicrosPerUnit: 5_000_000, UniqueMicrosPerUnit: 50_000_000},
	}
}

// Revenue Share (ticket 15): a delivered collection's premium splits — the
// org and platform receive their postings, contributing members earn
// pro-rata shares. The split fires inside Deliver, right after the charge.

func TestDeliverPostsRevenueShare(t *testing.T) {
	f := deliveryFixtureFor(t)
	c := mustCollectionID(t, f.svc)

	if _, err := f.svc.Deliver(t.Context(), c, "consumer-1"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	// Premium 50 cr at the default 80/20: the org account receives 40 cr
	// and passes the whole share to its sole contributing member, netting
	// zero; the platform keeps 10 cr.
	org, err := f.ledger.Balance(t.Context(), credits.AccountScope("org-1"))
	if err != nil {
		t.Fatalf("org balance: %v", err)
	}
	if org != 0 {
		t.Errorf("org net balance = %d, want 0 (received 40 cr, distributed 40 cr to mem-1)", org)
	}
	plat, err := f.ledger.Balance(t.Context(), credits.Scope(credits.ScopePlatform))
	if err != nil {
		t.Fatalf("platform balance: %v", err)
	}
	if plat != 10_000_000 {
		t.Errorf("platform balance = %d, want 10_000_000 (80/20 of 50 cr)", plat)
	}
	mem1, err := f.ledger.Balance(t.Context(), credits.MemberScope("mem-1"))
	if err != nil {
		t.Fatalf("member balance: %v", err)
	}
	if mem1 != 40_000_000 {
		t.Errorf("mem-1 balance = %d, want 40_000_000 (sole contributor)", mem1)
	}
	if mem2, _ := f.ledger.Balance(t.Context(), credits.MemberScope("mem-2")); mem2 != 0 {
		t.Errorf("mem-2 balance = %d, want 0 (never answered)", mem2)
	}

	// The postings ride one revenue_share movement tied to the request.
	movs, err := f.ledger.MovementsByScope(t.Context(), credits.AccountScope("org-1"), 10)
	if err != nil {
		t.Fatalf("movements: %v", err)
	}
	kindRevenueShare := false
	for _, m := range movs {
		if m.Kind == credits.KindRevenueShare && m.RequestID == "req-1" {
			kindRevenueShare = true
		}
	}
	if !kindRevenueShare {
		t.Errorf("no revenue_share movement for req-1 under the org scope: %+v", movs)
	}
}

func TestDeliverRevenueShareRespectsTunedPercentage(t *testing.T) {
	f := deliveryFixtureFor(t)
	// The admin tunes the split to 50/50 before the delivery.
	fifty := 50
	f.rules.RevenueShareOrgPercent = &fifty
	c := mustCollectionID(t, f.svc)

	if _, err := f.svc.Deliver(t.Context(), c, "consumer-1"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	// At 50/50 the sole contributor takes the whole org share, netting the
	// org account zero; the platform keeps its 25 cr.
	org, _ := f.ledger.Balance(t.Context(), credits.AccountScope("org-1"))
	plat, _ := f.ledger.Balance(t.Context(), credits.Scope(credits.ScopePlatform))
	if org != 0 || plat != 25_000_000 {
		t.Errorf("balances = (org %d, platform %d), want (0, 25 cr) at 50/50", org, plat)
	}
}

func TestDeliverRevenueShareWeightsByParticipation(t *testing.T) {
	// mem-1 accepted two questions while mem-2 accepted one (the second
	// stayed unanswered and the collection finalized incomplete on its
	// deadline): weights 2:1, so the 40 cr org share splits
	// 26_666_666/13_333_333, and the largest-remainder micro goes to mem-1.
	svc, _, convs, _ := fixtureShared(t)
	past := fixedNow.Add(-time.Hour)
	c, err := svc.Create(t.Context(), "org-1", collections.NewCollection{
		RequestID: "req-1", MemberIDs: []string{"mem-1", "mem-2"},
		Questions: []string{farmQ, cropQ}, Deadline: &past,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedConversation(t, convs, "conv-1", "mem-1", "Siobhán Ní Uaithne",
		[]string{farmQ, cropQ}, []string{"42 hectares", "barley"}, conversations.StatusCompleted)
	// mem-2 answers only the first question: weights land at 2:1.
	seedConversation(t, convs, "conv-2", "mem-2", "Pádraig Ó Briain",
		[]string{farmQ, cropQ}, []string{"18 hectares"}, conversations.StatusCompleted)
	got, err := svc.Sync(t.Context(), c.ID, "org-1")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got.Status != collections.StatusIncomplete {
		t.Fatalf("Sync = %s, want incomplete (mem-2's second answer missing)", got.Status)
	}

	ledger := credits.NewMemoryStore()
	credSvc := credits.NewService(ledger, func(context.Context) (credits.PricingRules, error) {
		return testRules(), nil
	})
	if _, err := credSvc.Grant(t.Context(), credits.AccountScope("consumer-1"), 100*credits.MicrosPerCredit, "admin", "pilot seed"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	pmap := anonymize.NewMemoryMap()
	svc.WithCredits(credSvc).WithCleaner(&recordingCleaner{inner: anonymize.NewDelivery(pmap)}).WithPseudonyms(pmap)

	if _, err := svc.Deliver(t.Context(), c.ID, "consumer-1"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	m1, _ := ledger.Balance(t.Context(), credits.MemberScope("mem-1"))
	m2, _ := ledger.Balance(t.Context(), credits.MemberScope("mem-2"))
	if m1 != 26_666_667 || m2 != 13_333_333 {
		t.Errorf("member balances = (%d, %d), want (26_666_667, 13_333_333) at weights 2:1", m1, m2)
	}
}

func intPtr(i int) *int { return &i }

// mustCollectionID loads the fixture's collection ID.
func mustCollectionID(t *testing.T, svc *collections.Service) string {
	t.Helper()
	list, err := svc.ByOrg(t.Context(), "org-1")
	if err != nil || len(list) == 0 {
		t.Fatalf("ByOrg: %v (%d)", err, len(list))
	}
	return list[0].ID
}
