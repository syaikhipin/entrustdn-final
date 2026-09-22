package credits_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
)

// The charging service (ticket 03): automatic pricing posts through the
// Ledger — a charge prices by the configured rules and posts balanced
// entries in one step; a debit beyond the balance is refused.

func testRules() credits.PricingRules {
	return credits.PricingRules{
		Inference: []credits.InferenceRule{{
			Model: "test-model", InputMicrosPer1K: 2_500,
			CachedInputMicrosPer1K: 1_250, OutputMicrosPer1K: 10_000,
		}},
		Data: credits.DataRules{CachedAssetMicrosPerUnit: 5_000_000, UniqueMicrosPerUnit: 50_000_000},
	}
}

// seededConsumer returns a service with the consumer funded at 100 credits.
func seededConsumer(t *testing.T) (*credits.Service, *credits.MemoryStore, credits.Scope) {
	t.Helper()
	store := credits.NewMemoryStore()
	svc := credits.NewService(store, func(context.Context) (credits.PricingRules, error) { return testRules(), nil })
	acct := credits.AccountScope("consumer-1")
	if _, err := svc.Grant(t.Context(), acct, 100*credits.MicrosPerCredit, "admin", "pilot seed"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	return svc, store, acct
}

func TestChargeInferencePricesAutomaticallyAndPostsBalanced(t *testing.T) {
	svc, store, acct := seededConsumer(t)

	mov, err := svc.ChargeInference(t.Context(), credits.InferenceUsage{
		InputTokens: 1_000, OutputTokens: 1_000,
	}, credits.Charge{Scope: acct, Model: "test-model", Memo: "clarification turn"})
	if err != nil {
		t.Fatalf("ChargeInference: %v", err)
	}
	if mov.Kind != credits.KindInferenceCharge {
		t.Errorf("movement kind = %q, want %q", mov.Kind, credits.KindInferenceCharge)
	}
	if !credits.Balanced(mov.Entries) {
		t.Errorf("charge posted unbalanced entries: %+v", mov.Entries)
	}

	// Worked example: 1k input @2.5µ/1k = 2_500 + 1k output @10µ/1k = 10_000.
	bal, err := store.Balance(t.Context(), acct)
	if err != nil || bal != 100*credits.MicrosPerCredit-12_500 {
		t.Errorf("balance after charge = (%d, %v), want 100 credits − 12_500 micros", bal, err)
	}
}

func TestChargeInferenceRefusesMalformedUsage(t *testing.T) {
	svc, store, acct := seededConsumer(t)

	tests := []struct {
		name  string
		usage credits.InferenceUsage
	}{
		{name: "cached tokens exceed input tokens", usage: credits.InferenceUsage{InputTokens: 100, CachedInputTokens: 200}},
		{name: "negative input", usage: credits.InferenceUsage{InputTokens: -5, OutputTokens: 10}},
		{name: "negative output", usage: credits.InferenceUsage{InputTokens: 10, OutputTokens: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mov, err := svc.ChargeInference(t.Context(), tt.usage, credits.Charge{Scope: acct, Model: "test-model"})
			if err == nil || errors.Is(err, credits.ErrInsufficientFunds) {
				t.Fatalf("ChargeInference(%+v) = (%+v, %v), want a loud refusal", tt.usage, mov, err)
			}
			bal, _ := store.Balance(t.Context(), acct)
			if bal != 100*credits.MicrosPerCredit {
				t.Errorf("refused charge changed the balance: %d", bal)
			}
		})
	}
}

func TestInferenceChargeCarriesUsageDetail(t *testing.T) {
	svc, _, acct := seededConsumer(t)

	mov, err := svc.ChargeInference(t.Context(), credits.InferenceUsage{
		InputTokens: 1_000, CachedInputTokens: 500, OutputTokens: 1_000,
	}, credits.Charge{Scope: acct, Model: "test-model"})
	if err != nil {
		t.Fatalf("ChargeInference: %v", err)
	}
	if mov.Inference == nil {
		t.Fatal("charge movement carries no inference detail")
	}
	want := credits.InferenceDetail{
		Model: "test-model", InputTokens: 1_000, CachedInputTokens: 500, OutputTokens: 1_000,
		InputMicrosPer1K: 2_500, CachedInputMicrosPer1K: 1_250, OutputMicrosPer1K: 10_000,
	}
	if *mov.Inference != want {
		t.Errorf("detail = %+v, want %+v", *mov.Inference, want)
	}
}

func TestChargeDataPricedByClass(t *testing.T) {
	svc, store, acct := seededConsumer(t)

	mov, err := svc.ChargeData(t.Context(), credits.DataUsage{Class: credits.DataCached, Units: 2},
		credits.Charge{Scope: acct, Memo: "asset download"})
	if err != nil {
		t.Fatalf("ChargeData: %v", err)
	}
	if mov.Kind != credits.KindDataCharge {
		t.Errorf("movement kind = %q, want %q", mov.Kind, credits.KindDataCharge)
	}
	bal, _ := store.Balance(t.Context(), acct)
	if bal != 100*credits.MicrosPerCredit-10_000_000 {
		t.Errorf("balance after cached charge = %d, want 100 credits − 10_000_000 micros (2×5 credits)", bal)
	}

	if _, err := svc.ChargeData(t.Context(), credits.DataUsage{Class: credits.DataUnique, Units: 1},
		credits.Charge{Scope: acct, Memo: "collection delivery"}); err != nil {
		t.Fatalf("ChargeData(unique): %v", err)
	}
	bal, _ = store.Balance(t.Context(), acct)
	if bal != 100*credits.MicrosPerCredit-60_000_000 {
		t.Errorf("balance after unique charge = %d, want 100 credits − 60_000_000 micros", bal)
	}
}

func TestChargesRefusedWithoutPricingRuleOrFunds(t *testing.T) {
	t.Run("unpriced model fails loudly and leaves the balance untouched", func(t *testing.T) {
		svc, store, acct := seededConsumer(t)
		_, err := svc.ChargeInference(t.Context(), credits.InferenceUsage{InputTokens: 10},
			credits.Charge{Scope: acct, Model: "model-nobody-priced"})
		if !errors.Is(err, credits.ErrNoPricingRule) {
			t.Fatalf("ChargeInference error = %v, want ErrNoPricingRule", err)
		}
		bal, _ := store.Balance(t.Context(), acct)
		if bal != 100*credits.MicrosPerCredit {
			t.Errorf("failed charge changed the balance: %d", bal)
		}
	})

	t.Run("empty movement is refused", func(t *testing.T) {
		svc, _, acct := seededConsumer(t)
		if _, err := svc.ChargeInference(t.Context(), credits.InferenceUsage{},
			credits.Charge{Scope: acct, Model: "test-model"}); !errors.Is(err, credits.ErrUnbalanced) {
			t.Errorf("zero-usage charge error = %v, want ErrUnbalanced (nothing to post)", err)
		}
	})

	t.Run("spending past the balance is refused", func(t *testing.T) {
		svc, store, acct := seededConsumer(t)
		_, err := svc.ChargeData(t.Context(), credits.DataUsage{Class: credits.DataUnique, Units: 3},
			credits.Charge{Scope: acct, Memo: "too expensive"})
		if !errors.Is(err, credits.ErrInsufficientFunds) {
			t.Fatalf("ChargeData error = %v, want ErrInsufficientFunds", err)
		}
		bal, _ := store.Balance(t.Context(), acct)
		if bal != 100*credits.MicrosPerCredit {
			t.Errorf("refused charge changed the balance: %d", bal)
		}
	})

	t.Run("exact balance may be spent to zero", func(t *testing.T) {
		svc, store, acct := seededConsumer(t)
		// 20 units × 5 credits = exactly the 100-credit balance.
		if _, err := svc.ChargeData(t.Context(), credits.DataUsage{Class: credits.DataCached, Units: 20},
			credits.Charge{Scope: acct}); err != nil {
			t.Fatalf("ChargeData to zero: %v", err)
		}
		bal, _ := store.Balance(t.Context(), acct)
		if bal != 0 {
			t.Errorf("balance after exact spend = %d, want 0", bal)
		}
	})
}

func TestConcurrentChargesNeverOverdraw(t *testing.T) {
	// 10 goroutines race to spend 30 credits each against a 100-credit
	// balance: at most 3 may succeed, and the final balance can never go
	// negative — the serializing store is the guard.
	svc, store, acct := seededConsumer(t)

	const workers = 10
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := svc.ChargeData(t.Context(), credits.DataUsage{Class: credits.DataCached, Units: 6},
				credits.Charge{Scope: acct, Memo: fmt.Sprintf("worker-%d", n)})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)

	var ok, refused int
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, credits.ErrInsufficientFunds):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok > 3 {
		t.Errorf("%d concurrent charges succeeded against a 100-credit balance at 6 credits each — overdrawn", ok)
	}
	if ok+refused != workers {
		t.Errorf("ok=%d refused=%d, want every charge either posted or refused", ok, refused)
	}
	bal, _ := store.Balance(t.Context(), acct)
	if bal < 0 {
		t.Errorf("final balance = %d, want ≥ 0", bal)
	}
	// Each successful charge spends 6 units × 5 credits = 30 credits.
	if bal != int64(100-30*ok)*credits.MicrosPerCredit {
		t.Errorf("final balance = %d, inconsistent with %d successful charges", bal, ok)
	}
}

func TestAdjustmentAndGrantPostThroughTheLedger(t *testing.T) {
	svc, store, acct := seededConsumer(t)

	// Admin adjustment: credit back 2.5 credits.
	mov, err := svc.Adjust(t.Context(), acct, 2_500_000, "admin-7", "goodwill refund")
	if err != nil {
		t.Fatalf("Adjust: %v", err)
	}
	if mov.Kind != credits.KindAdjustment || mov.ActorID != "admin-7" {
		t.Errorf("adjustment movement = (%q, actor %q)", mov.Kind, mov.ActorID)
	}

	// Negative adjustment debits the account (and may not overdraw).
	if _, err := svc.Adjust(t.Context(), acct, -2_500_000, "admin-7", "correction"); err != nil {
		t.Fatalf("negative Adjust: %v", err)
	}
	if _, err := svc.Adjust(t.Context(), acct, -500*credits.MicrosPerCredit, "admin-7", "overdraft attempt"); !errors.Is(err, credits.ErrInsufficientFunds) {
		t.Errorf("overdrawing Adjust error = %v, want ErrInsufficientFunds", err)
	}

	bal, _ := store.Balance(t.Context(), acct)
	if bal != 100*credits.MicrosPerCredit {
		t.Errorf("balance after +2.5/−2.5 adjustments = %d, want the granted 100 credits", bal)
	}
}
