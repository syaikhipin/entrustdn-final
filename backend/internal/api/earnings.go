package api

import (
	"context"
	"net/http"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/roster"
)

// EarningsDeps carries the collaborators the org earnings view (ticket 15)
// needs: the Ledger to derive member-scope balances from, and the roster to
// name members. The price book loader is resolved once in NewHandler from
// the credits deps — the earnings view only reads it.
type EarningsDeps struct {
	Store  credits.Store
	Roster roster.Store
}

// earningsHandlers holds the resolved collaborators for the earnings route.
type earningsHandlers struct {
	deps  *EarningsDeps
	rules func(ctx context.Context) (credits.PricingRules, error)
}

// registerEarningsRoutes wires the org earnings endpoint.
func (h *Handler) registerEarningsRoutes(deps *EarningsDeps, rules func(ctx context.Context) (credits.PricingRules, error)) {
	h.earnings = &earningsHandlers{deps: deps, rules: rules}
	h.mux.HandleFunc("GET /api/v1/me/earnings", h.handleMyEarnings)
}

// earningsDoc is the org's earnings view: the split in force, the total
// Revenue Share ever received (gross), the org account's current balance,
// and the per-Member breakdown.
type earningsDoc struct {
	RevenueShareOrgPercent int              `json:"revenue_share_org_percent"`
	TotalReceivedMicros    int64            `json:"total_received_micros"`
	BalanceMicros          int64            `json:"balance_micros"`
	Members                []memberEarnings `json:"members"`
}

// memberEarnings is one Member's line: who they are on the roster and what
// their participation has earned them.
type memberEarnings struct {
	MemberID     string `json:"member_id"`
	DisplayName  string `json:"display_name"`
	EarnedMicros int64  `json:"earned_micros"`
}

// handleMyEarnings serves the signed-in Farmer Organization's earnings:
// shares received, per-Member breakdown. Members hold no accounts — their
// earnings live in member-scoped ledger entries (credited only by Revenue
// Share), read back derived like every balance.
func (h *Handler) handleMyEarnings(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.requireOrg(w, r)
	if !ok {
		return
	}
	deps := h.earnings.deps

	rules, err := h.earnings.rules(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to load pricing rules")
		return
	}

	orgScope := credits.AccountScope(acct.ID)
	balance, err := deps.Store.Balance(r.Context(), orgScope)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to derive the org balance")
		return
	}

	total, err := deps.Store.RevenueShareReceived(r.Context(), orgScope)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to sum the org's revenue share")
		return
	}

	members, err := deps.Roster.MembersByOrg(r.Context(), acct.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "failed to load the roster")
		return
	}
	lines := make([]memberEarnings, 0, len(members))
	for _, m := range members {
		earned, err := deps.Store.Balance(r.Context(), credits.MemberScope(m.ID))
		if err != nil {
			apiError(w, http.StatusInternalServerError, "failed to derive a member's earnings")
			return
		}
		lines = append(lines, memberEarnings{
			MemberID: m.ID, DisplayName: m.DisplayName, EarnedMicros: earned,
		})
	}

	writeJSON(w, http.StatusOK, earningsDoc{
		RevenueShareOrgPercent: rules.OrgSharePercent(),
		TotalReceivedMicros:    total,
		BalanceMicros:          balance,
		Members:                lines,
	})
}
