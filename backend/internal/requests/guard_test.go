package requests_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/requests"
)

// The budget guard's honesty under concurrency (ticket 07): turns on one
// request are serialized — the read-check-charge-update of a chat turn is
// not atomic across processes, so the service holds a per-request lock for
// the whole turn. Two turns racing for the last 550 µcr of a 1_000 µcr
// budget must not both charge.

// TestConcurrentTurnsNeverExceedTheBudget fires two chat turns at once
// against a budget that fits only the first (each turn prices 550 µcr on
// the fake's standard reply).
func TestConcurrentTurnsNeverExceedTheBudget(t *testing.T) {
	svc, ledger, _ := fixture(t, testCatalog)
	// Budget 1_000 µcr; each turn prices 550 µcr.
	r, err := svc.Create(t.Context(), "consumer-1", requests.NewRequest{
		Description: "Spring barley yields across Leinster for the 2026 season",
		Format:      "csv", BudgetMicros: 1_000,
	})
	if err != nil {
		t.Fatal(err)
	}

	const racers = 2
	var wg sync.WaitGroup
	var mu sync.Mutex
	exceeded := 0
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Chat(t.Context(), r.ID, "consumer-1", "racing turn")
			mu.Lock()
			defer mu.Unlock()
			if errors.Is(err, requests.ErrBudgetExceeded) {
				exceeded++
			}
		}()
	}
	wg.Wait()

	// Exactly one turn charged; the other was refused by the budget guard.
	got, err := svc.ByID(t.Context(), r.ID, "consumer-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.SpentMicros != 550 {
		t.Errorf("spent = %d µcr, want exactly one turn's 550 — the guard must not let racing turns both charge", got.SpentMicros)
	}
	if exceeded != racers-1 {
		t.Errorf("budget refusals = %d, want %d", exceeded, racers-1)
	}
	bal, err := ledger.Balance(t.Context(), credits.AccountScope("consumer-1"))
	if err != nil || bal != 100*credits.MicrosPerCredit-550 {
		t.Errorf("balance = (%d, %v), want 100 credits − 550 µcr (one turn only)", bal, err)
	}
}
