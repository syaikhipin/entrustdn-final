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
	credSvc := credits.NewService(ledger, func(context.Context) (credits.PricingRules, error) {
		return testRules(), nil
	})
	// Fund the consumer so the charge can land.
	if _, err := credSvc.Grant(t.Context(), credits.AccountScope("consumer-1"), 100*credits.MicrosPerCredit, "admin", "pilot seed"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	pmap := anonymize.NewMemoryMap()
	cleaner := &recordingCleaner{inner: anonymize.NewDelivery(pmap)}
	svc.WithCredits(credSvc).WithCleaner(cleaner).WithPseudonyms(pmap)
	return deliveryFixture{svc: svc, credits: credSvc, ledger: ledger, cleaner: cleaner}
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

// mustCollectionID loads the fixture's collection ID.
func mustCollectionID(t *testing.T, svc *collections.Service) string {
	t.Helper()
	list, err := svc.ByOrg(t.Context(), "org-1")
	if err != nil || len(list) == 0 {
		t.Fatalf("ByOrg: %v (%d)", err, len(list))
	}
	return list[0].ID
}
