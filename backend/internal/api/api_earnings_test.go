package api_test

import (
	"net/http"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
)

// The org earnings view (ticket 15) at Seam 1: after a delivery the org
// sees its Revenue Share — the split percentage in force, total received,
// the org account's balance, and the per-Member breakdown. The flow rides
// the ticket-12 fixture end to end: org → roster → collection → delivery —
// so the postings the view reads really happened.

func TestOrgEarningsAfterDelivery(t *testing.T) {
	f := newCollectionsServer(t)
	_, orgToken := newApprovedOrg(t, f.srv, f.mail, "earn-org@example.org")
	consumerID, consumerToken := newConsumerWithRequest(t, f, "earn-consumer@example.org")
	f.seedClarifiedRequest(t, "req-1", consumerID)
	if _, err := f.credSvc.Grant(t.Context(), credits.AccountScope(consumerID), 100*credits.MicrosPerCredit, "admin", "pilot seed"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	memberID := addRosterMember(t, f.srv, orgToken, "Siobhán Ní Uaithne", "whatsapp:+353860000001")

	// Before any delivery: zero earnings, the default split in force.
	code, doc := getWithToken(t, f.srv, "/api/v1/me/earnings", orgToken)
	if code != http.StatusOK {
		t.Fatalf("earnings = %d (doc: %v)", code, doc)
	}
	if doc["revenue_share_org_percent"].(float64) != 80 {
		t.Errorf("percent = %v, want 80 (the default)", doc["revenue_share_org_percent"])
	}
	if doc["total_received_micros"].(float64) != 0 {
		t.Errorf("total = %v, want 0 before any delivery", doc["total_received_micros"])
	}
	if got := len(doc["members"].([]any)); got != 1 {
		t.Errorf("members = %d, want 1 (the rostered member)", got)
	}

	// Deliver: the premium (50 cr) splits 80/20 — the org receives 40 cr
	// and its sole contributing member earns it all.
	colID := f.seedCompletedCollection(t, orgToken, memberID, "req-1")
	dl, err := http.NewRequest(http.MethodGet, f.srv.URL+"/api/v1/consumer/collections/"+colID+"/download", nil)
	if err != nil {
		t.Fatal(err)
	}
	dl.Header.Set("Authorization", "Bearer "+consumerToken)
	httpResp, err := http.DefaultClient.Do(dl)
	if err != nil {
		t.Fatal(err)
	}
	httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("download = %d", httpResp.StatusCode)
	}

	code, doc = getWithToken(t, f.srv, "/api/v1/me/earnings", orgToken)
	if code != http.StatusOK {
		t.Fatalf("earnings after delivery = %d (doc: %v)", code, doc)
	}
	if doc["total_received_micros"].(float64) != 40_000_000 {
		t.Errorf("total received = %v, want 40_000_000 (80%% of 50 cr)", doc["total_received_micros"])
	}
	if doc["balance_micros"].(float64) != 0 {
		t.Errorf("org balance = %v, want 0 (the whole share went to the sole member)", doc["balance_micros"])
	}
	members := doc["members"].([]any)
	if len(members) != 1 {
		t.Fatalf("members = %d, want 1", len(members))
	}
	mem := members[0].(map[string]any)
	if mem["display_name"] != "Siobhán Ní Uaithne" {
		t.Errorf("member name = %v, want the roster display name", mem["display_name"])
	}
	if mem["earned_micros"].(float64) != 40_000_000 {
		t.Errorf("member earned = %v, want 40_000_000", mem["earned_micros"])
	}
}

func TestEarningsRefusesNonOrganizations(t *testing.T) {
	f := newCollectionsServer(t)
	_, orgToken := newApprovedOrg(t, f.srv, f.mail, "earn-org2@example.org")
	_, consumerToken := newConsumerWithRequest(t, f, "earn-consumer2@example.org")

	if code, _ := getWithToken(t, f.srv, "/api/v1/me/earnings", consumerToken); code != http.StatusForbidden {
		t.Errorf("consumer earnings = %d, want 403", code)
	}
	if code, _ := getWithToken(t, f.srv, "/api/v1/me/earnings", ""); code != http.StatusUnauthorized {
		t.Errorf("anonymous earnings = %d, want 401", code)
	}
	if code, doc := getWithToken(t, f.srv, "/api/v1/me/earnings", orgToken); code != http.StatusOK {
		t.Errorf("org earnings = %d (doc: %v), want 200", code, doc)
	}
}
