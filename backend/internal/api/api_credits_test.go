package api_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/agentclient"
	"github.com/syaikhipin/entrustdn-final/backend/internal/api"
	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/mailsink"
	"github.com/syaikhipin/entrustdn-final/backend/internal/membership"
)

// Credits endpoints (ticket 03) at Seam 1: admin grant/adjust/charge and
// pricing configuration, plus the consumer's balance + entry history view.
// Every movement rides the Ledger; the consumer sees only their own scope.

func testPricingRules() credits.PricingRules {
	return credits.PricingRules{
		Inference: []credits.InferenceRule{{
			Model: "test-model", InputMicrosPer1K: 2_500,
			CachedInputMicrosPer1K: 1_250, OutputMicrosPer1K: 10_000,
		}},
		Data: credits.DataRules{CachedAssetMicrosPerUnit: 5_000_000, UniqueMicrosPerUnit: 50_000_000},
	}
}

// newCreditsServer boots the API with in-memory membership and ledger
// stores, an in-memory price book, and a provisioned admin. Returns the
// server, the admin's session token, the mail sink buffer (for verification
// tokens), and the doubles for assertions.
func newCreditsServer(t *testing.T) (*httptest.Server, string, *bytes.Buffer, *credits.MemoryStore) {
	t.Helper()
	agent := fakeAgent(t, "0.1.0")
	memStore := membership.NewMemoryStore()
	if err := memStore.PublishTOS(t.Context(), membership.TOSVersion{Version: "1.0", Body: "Be good."}); err != nil {
		t.Fatalf("seed TOS: %v", err)
	}
	ledger := credits.NewMemoryStore()
	rules := &[]credits.PricingRules{testPricingRules()}
	mail := &bytes.Buffer{}
	srv := httptest.NewServer(api.NewHandler(api.Deps{
		Agent:   agentclient.New(agent.URL),
		Version: "test-backend",
		Store:   memStore,
		Mail:    mailsink.NewLogSink(mail),
		Credits: &api.CreditsDeps{
			Store: ledger,
			Rules: func(context.Context) (credits.PricingRules, error) { return (*rules)[0], nil },
			SaveRules: func(_ context.Context, r credits.PricingRules) error {
				(*rules)[0] = r
				return nil
			},
		},
	}))
	t.Cleanup(srv.Close)

	provisionAdmin(t, memStore, "admin@thresh.dev")
	token := loginWith(t, srv, "admin@thresh.dev", "admin-pass-2026")
	return srv, token, mail, ledger
}

// newConsumer registers, verifies, and logs in a consumer; returns the ID
// and session token.
func newConsumer(t *testing.T, srv *httptest.Server, mail *bytes.Buffer, email string) (string, string) {
	t.Helper()
	if code, doc := postJSON(t, srv, "/api/v1/register", map[string]any{
		"email": email, "password": "harvest-2026", "display_name": "Consumer", "role": "data_consumer", "tos_version": "1.0",
	}); code != http.StatusCreated {
		t.Fatalf("register consumer: %d (doc: %v)", code, doc)
	}
	if code, _ := postJSON(t, srv, "/api/v1/verify", map[string]any{"token": verificationTokenFor(t, mail, email)}); code != http.StatusOK {
		t.Fatal("verify failed")
	}
	code, doc := postJSON(t, srv, "/api/v1/login", map[string]any{"email": email, "password": "harvest-2026"})
	if code != http.StatusOK {
		t.Fatalf("login consumer: %d", code)
	}
	return doc["account"].(map[string]any)["id"].(string), doc["session"].(map[string]any)["token"].(string)
}

func TestAdminGrantsCreditsThroughTheLedger(t *testing.T) {
	srv, adminToken, mail, ledger := newCreditsServer(t)
	consumerID, _ := newConsumer(t, srv, mail, "granted@example.org")

	code, doc := postWithToken(t, srv, "/api/v1/admin/credits/grant", adminToken, map[string]any{
		"account_id": consumerID, "amount_micros": 100 * credits.MicrosPerCredit, "memo": "pilot seed",
	})
	if code != http.StatusCreated {
		t.Fatalf("grant = %d (doc: %v)", code, doc)
	}
	mov := doc["movement"].(map[string]any)
	if mov["kind"] != credits.KindGrant {
		t.Errorf("movement kind = %v, want grant", mov["kind"])
	}

	// The Ledger holds a balanced posting; the consumer's balance derives.
	acctBal, err := ledger.Balance(t.Context(), credits.AccountScope(consumerID))
	if err != nil || acctBal != 100*credits.MicrosPerCredit {
		t.Errorf("ledger balance = (%d, %v), want 100 credits", acctBal, err)
	}
	sums, _ := ledger.Balances(t.Context())
	if sums[credits.Scope(credits.ScopeTreasury)] != -100*credits.MicrosPerCredit {
		t.Errorf("treasury = %d, want −100 credits (grants draw the treasury)", sums[credits.Scope(credits.ScopeTreasury)])
	}

	// Granting to a ghost account is refused.
	code, _ = postWithToken(t, srv, "/api/v1/admin/credits/grant", adminToken, map[string]any{
		"account_id": "00000000-0000-0000-0000-0000000000ff", "amount_micros": 5,
	})
	if code != http.StatusNotFound {
		t.Errorf("grant to ghost = %d, want 404", code)
	}

	// Zero and negative grants are refused (adjustment is the signed path).
	for _, amount := range []int64{0, -100} {
		code, _ = postWithToken(t, srv, "/api/v1/admin/credits/grant", adminToken, map[string]any{
			"account_id": consumerID, "amount_micros": amount,
		})
		if code != http.StatusBadRequest {
			t.Errorf("grant %d = %d, want 400", amount, code)
		}
	}

	// Non-admins are refused.
	consumerToken := loginWith(t, srv, "granted@example.org", "harvest-2026")
	code, _ = postWithToken(t, srv, "/api/v1/admin/credits/grant", consumerToken, map[string]any{
		"account_id": consumerID, "amount_micros": 5,
	})
	if code != http.StatusForbidden {
		t.Errorf("consumer grant = %d, want 403", code)
	}
}

func TestAdminAdjustmentIsSignedAndOverdraftRefused(t *testing.T) {
	srv, adminToken, mail, ledger := newCreditsServer(t)
	consumerID, _ := newConsumer(t, srv, mail, "adjusted@example.org")

	// Grant 10 credits, adjust −2.5, then try −20 (overdraft refused).
	if code, _ := postWithToken(t, srv, "/api/v1/admin/credits/grant", adminToken, map[string]any{
		"account_id": consumerID, "amount_micros": 10 * credits.MicrosPerCredit,
	}); code != http.StatusCreated {
		t.Fatal("grant failed")
	}
	code, doc := postWithToken(t, srv, "/api/v1/admin/credits/adjust", adminToken, map[string]any{
		"account_id": consumerID, "amount_micros": -2_500_000, "memo": "correction",
	})
	if code != http.StatusCreated {
		t.Fatalf("adjust = %d (doc: %v)", code, doc)
	}
	if doc["movement"].(map[string]any)["kind"] != credits.KindAdjustment {
		t.Errorf("kind = %v, want adjustment", doc["movement"])
	}
	code, _ = postWithToken(t, srv, "/api/v1/admin/credits/adjust", adminToken, map[string]any{
		"account_id": consumerID, "amount_micros": -20 * credits.MicrosPerCredit,
	})
	if code != http.StatusConflict {
		t.Errorf("overdrawing adjust = %d, want 409 (insufficient funds)", code)
	}
	bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID))
	if bal != 7_500_000 {
		t.Errorf("balance = %d, want 7.5 credits", bal)
	}
}

func TestAdminTestChargePricedAutomatically(t *testing.T) {
	srv, adminToken, mail, ledger := newCreditsServer(t)
	consumerID, _ := newConsumer(t, srv, mail, "charged@example.org")
	if code, _ := postWithToken(t, srv, "/api/v1/admin/credits/grant", adminToken, map[string]any{
		"account_id": consumerID, "amount_micros": 100 * credits.MicrosPerCredit,
	}); code != http.StatusCreated {
		t.Fatal("grant failed")
	}

	tests := []struct {
		name       string
		body       map[string]any
		wantKind   string
		wantMicros int64 // total debited from the consumer
	}{
		{
			name: "inference charge prices by rule",
			body: map[string]any{
				"account_id": consumerID,
				"inference":  map[string]any{"model": "test-model", "input_tokens": 1_000, "cached_input_tokens": 500, "output_tokens": 1_000},
			},
			wantKind:   credits.KindInferenceCharge,
			wantMicros: 1_250 + 625 + 10_000, // fresh 500@2500/1k + cached 500@1250/1k + out 1000@10000/1k
		},
		{
			name: "cached data charge",
			body: map[string]any{
				"account_id": consumerID,
				"data":       map[string]any{"class": "cached", "units": 2},
			},
			wantKind:   credits.KindDataCharge,
			wantMicros: 10_000_000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, doc := postWithToken(t, srv, "/api/v1/admin/credits/charge", adminToken, tt.body)
			if code != http.StatusCreated {
				t.Fatalf("charge = %d (doc: %v)", code, doc)
			}
			mov := doc["movement"].(map[string]any)
			if mov["kind"] != tt.wantKind {
				t.Errorf("kind = %v, want %v", mov["kind"], tt.wantKind)
			}
			if amount := doc["charged_micros"].(float64); int64(amount) != tt.wantMicros {
				t.Errorf("charged_micros = %d, want %d (automatic pricing)", int64(amount), tt.wantMicros)
			}
		})
	}

	bal, _ := ledger.Balance(t.Context(), credits.AccountScope(consumerID))
	if bal != 100*credits.MicrosPerCredit-11_875-10_000_000 {
		t.Errorf("balance after charges = %d, want 100 credits minus both charges", bal)
	}

	// Unpriced model fails loudly, nothing posted.
	code, doc := postWithToken(t, srv, "/api/v1/admin/credits/charge", adminToken, map[string]any{
		"account_id": consumerID,
		"inference":  map[string]any{"model": "ghost-model", "input_tokens": 10},
	})
	if code != http.StatusUnprocessableEntity {
		t.Errorf("unpriced charge = %d, want 422", code)
	}

	// The inference charge movement explains itself: usage and applied rates.
	code, doc = postWithToken(t, srv, "/api/v1/admin/credits/charge", adminToken, map[string]any{
		"account_id": consumerID,
		"inference":  map[string]any{"model": "test-model", "input_tokens": 500, "output_tokens": 500},
	})
	if code != http.StatusCreated {
		t.Fatalf("detail charge = %d (doc: %v)", code, doc)
	}
	detail := doc["movement"].(map[string]any)["inference"].(map[string]any)
	if detail["model"] != "test-model" ||
		detail["input_tokens"] != float64(500) ||
		detail["input_micros_per_1k"] != float64(2_500) {
		t.Errorf("inference detail = %v, want usage + applied rates", detail)
	}

	// A charge past the balance is refused with 409.
	code, _ = postWithToken(t, srv, "/api/v1/admin/credits/charge", adminToken, map[string]any{
		"account_id": consumerID,
		"data":       map[string]any{"class": "unique", "units": 99},
	})
	if code != http.StatusConflict {
		t.Errorf("overdrawing charge = %d, want 409", code)
	}
}

func TestUnpricedDataChargeFailsLoudly(t *testing.T) {
	// A server whose price book is missing entirely: a data charge must
	// fail loudly (422, the same as an unpriced model), not read as a
	// server fault (500).
	agent := fakeAgent(t, "0.1.0")
	memStore := membership.NewMemoryStore()
	if err := memStore.PublishTOS(t.Context(), membership.TOSVersion{Version: "1.0", Body: "Be good."}); err != nil {
		t.Fatalf("seed TOS: %v", err)
	}
	noRules := func(context.Context) (credits.PricingRules, error) {
		return credits.PricingRules{}, fmt.Errorf("%w: no price book configured", credits.ErrNoPricingRule)
	}
	mail := &bytes.Buffer{}
	srv := httptest.NewServer(api.NewHandler(api.Deps{
		Agent:   agentclient.New(agent.URL),
		Version: "test-backend",
		Store:   memStore,
		Mail:    mailsink.NewLogSink(mail),
		Credits: &api.CreditsDeps{
			Store:     credits.NewMemoryStore(),
			Rules:     noRules,
			SaveRules: func(context.Context, credits.PricingRules) error { return nil },
		},
	}))
	t.Cleanup(srv.Close)

	provisionAdmin(t, memStore, "admin@thresh.dev")
	adminToken := loginWith(t, srv, "admin@thresh.dev", "admin-pass-2026")
	consumerID, _ := newConsumer(t, srv, mail, "unpriced@example.org")

	// A data charge against a missing price book is 422, nothing posted.
	code, doc := postWithToken(t, srv, "/api/v1/admin/credits/charge", adminToken, map[string]any{
		"account_id": consumerID,
		"data":       map[string]any{"class": "cached", "units": 1},
	})
	if code != http.StatusUnprocessableEntity {
		t.Errorf("data charge with no price book = %d (doc: %v), want 422", code, doc)
	}
}

func TestAdminPricingRulesGetAndSave(t *testing.T) {
	srv, adminToken, mail, _ := newCreditsServer(t)

	code, doc := getWithToken(t, srv, "/api/v1/admin/pricing", adminToken)
	if code != http.StatusOK {
		t.Fatalf("GET pricing = %d (doc: %v)", code, doc)
	}
	inf := doc["inference"].([]any)[0].(map[string]any)
	if inf["model"] != "test-model" || inf["input_micros_per_1k"] != float64(2_500) {
		t.Errorf("pricing document = %v", doc)
	}

	// Save a new price book: cache hits repriced, revenue share percent set.
	updated := map[string]any{
		"inference": []any{map[string]any{
			"model": "test-model", "input_micros_per_1k": 2_000,
			"cached_input_micros_per_1k": 500, "output_micros_per_1k": 9_000,
		}},
		"data":                      map[string]any{"cached_asset_micros_per_unit": 4_000_000, "unique_micros_per_unit": 40_000_000},
		"revenue_share_org_percent": 65,
	}
	code, doc = postWithToken(t, srv, "/api/v1/admin/pricing", adminToken, updated)
	if code != http.StatusOK {
		t.Fatalf("save pricing = %d (doc: %v)", code, doc)
	}
	// The saved document echoes back; the save is visible on the next GET.
	code, doc = getWithToken(t, srv, "/api/v1/admin/pricing", adminToken)
	if code != http.StatusOK {
		t.Fatalf("re-GET pricing = %d", code)
	}
	saved := doc["inference"].([]any)[0].(map[string]any)
	if saved["input_micros_per_1k"] != float64(2_000) || saved["cached_input_micros_per_1k"] != float64(500) {
		t.Errorf("rules not saved: %v", doc)
	}
	if doc["revenue_share_org_percent"].(float64) != 65 {
		t.Errorf("revenue share percent = %v, want 65", doc["revenue_share_org_percent"])
	}

	// An out-of-range revenue share percentage is refused too.
	code, _ = postWithToken(t, srv, "/api/v1/admin/pricing", adminToken, map[string]any{
		"inference": []any{map[string]any{
			"model": "test-model", "input_micros_per_1k": 1,
			"cached_input_micros_per_1k": 1, "output_micros_per_1k": 1,
		}},
		"data":                      map[string]any{"cached_asset_micros_per_unit": 1, "unique_micros_per_unit": 1},
		"revenue_share_org_percent": 101,
	})
	if code != http.StatusBadRequest {
		t.Errorf("invalid revenue share percent = %d, want 400", code)
	}

	// Invalid rule sets are refused: cached pricier than fresh.
	code, _ = postWithToken(t, srv, "/api/v1/admin/pricing", adminToken, map[string]any{
		"inference": []any{map[string]any{
			"model": "test-model", "input_micros_per_1k": 1,
			"cached_input_micros_per_1k": 2, "output_micros_per_1k": 1,
		}},
		"data": map[string]any{"cached_asset_micros_per_unit": 1, "unique_micros_per_unit": 1},
	})
	if code != http.StatusBadRequest {
		t.Errorf("invalid pricing = %d, want 400", code)
	}

	// Non-admins read nothing.
	_, consumerToken := newConsumer(t, srv, mail, "pricing-snoop@example.org")
	code, _ = getWithToken(t, srv, "/api/v1/admin/pricing", consumerToken)
	if code != http.StatusForbidden {
		t.Errorf("consumer GET pricing = %d, want 403", code)
	}
}

func TestConsumerSeesBalanceAndEntryHistory(t *testing.T) {
	srv, adminToken, mail, _ := newCreditsServer(t)
	consumerID, consumerToken := newConsumer(t, srv, mail, "spender@example.org")

	// Empty ledger: zero balance, no movements.
	code, doc := getWithToken(t, srv, "/api/v1/me/credits", consumerToken)
	if code != http.StatusOK {
		t.Fatalf("me/credits = %d (doc: %v)", code, doc)
	}
	if doc["balance_micros"] != float64(0) {
		t.Errorf("fresh balance = %v, want 0", doc["balance_micros"])
	}

	if code, _ := postWithToken(t, srv, "/api/v1/admin/credits/grant", adminToken, map[string]any{
		"account_id": consumerID, "amount_micros": 50 * credits.MicrosPerCredit, "memo": "seed",
	}); code != http.StatusCreated {
		t.Fatal("grant failed")
	}
	if code, _ := postWithToken(t, srv, "/api/v1/admin/credits/charge", adminToken, map[string]any{
		"account_id": consumerID,
		"inference":  map[string]any{"model": "test-model", "input_tokens": 1_000, "output_tokens": 1_000},
	}); code != http.StatusCreated {
		t.Fatal("charge failed")
	}

	code, doc = getWithToken(t, srv, "/api/v1/me/credits", consumerToken)
	if code != http.StatusOK {
		t.Fatalf("me/credits = %d", code)
	}
	if doc["balance_micros"] != float64(50*credits.MicrosPerCredit-12_500) {
		t.Errorf("balance = %v, want 50 credits − 12_500 micros", doc["balance_micros"])
	}
	movs := doc["movements"].([]any)
	if len(movs) != 2 {
		t.Fatalf("got %d movements, want 2 (grant + charge)", len(movs))
	}
	newest := movs[0].(map[string]any)
	if newest["kind"] != credits.KindInferenceCharge {
		t.Errorf("newest movement = %v, want the inference charge", newest["kind"])
	}
	// Every listed movement carries its balanced entries (the spend view's
	// audit trail).
	for _, m := range movs {
		entries := m.(map[string]any)["entries"].([]any)
		var sum float64
		for _, e := range entries {
			sum += e.(map[string]any)["amount_micros"].(float64)
		}
		if sum != 0 {
			t.Errorf("movement %v shows unbalanced entries (sum %f)", m, sum)
		}
	}

	// Another consumer's movements never appear in this scope.
	otherID, otherToken := newConsumer(t, srv, mail, "bystander@example.org")
	_, _ = otherID, otherToken
	code, doc = getWithToken(t, srv, "/api/v1/me/credits", otherToken)
	if doc["balance_micros"] != float64(0) {
		t.Errorf("bystander balance = %v, want 0 (scope isolation)", doc["balance_micros"])
	}
	if movs := doc["movements"].([]any); len(movs) != 0 {
		t.Errorf("bystander sees %d movements, want 0", len(movs))
	}

	// No session, no view.
	code, _ = getWithToken(t, srv, "/api/v1/me/credits", "")
	if code != http.StatusUnauthorized {
		t.Errorf("anonymous me/credits = %d, want 401", code)
	}
}
