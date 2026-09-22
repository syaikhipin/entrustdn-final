package credits

import (
	"context"
	"fmt"
)

// Charge carries the accounting context of an automatic charge.
type Charge struct {
	Scope     Scope  // the debited account's ledger scope
	Model     string // the model charged for (inference only)
	RequestID string
	ActorID   string // who is responsible (usually the agent on behalf of the consumer)
	Memo      string
}

// Service is the credit front door: every movement — automatic charge,
// admin grant or adjustment — prices and posts through here, so nothing
// bypasses the Ledger. The platform side of a charge lands in the platform
// scope, where Revenue Share (ticket 15) will split it out.
type Service struct {
	store Store
	rules func(ctx context.Context) (PricingRules, error)
}

// NewService returns a Service posting to store, reading pricing rules
// through rules (the api package hands it the store-backed loader).
func NewService(store Store, rules func(ctx context.Context) (PricingRules, error)) *Service {
	return &Service{store: store, rules: rules}
}

// Grant credits the account from the treasury (issue #3: admin grants for
// pilot participants). The treasury may go negative — it issues credits.
func (s *Service) Grant(ctx context.Context, to Scope, amountMicros int64, actorID, memo string) (Movement, error) {
	if amountMicros <= 0 {
		return Movement{}, fmt.Errorf("credits: grant amount must be positive")
	}
	return s.store.PostMovement(ctx, Movement{
		Kind:    KindGrant,
		ActorID: actorID,
		Memo:    memo,
		Entries: []Entry{
			{Scope: to, AmountMicros: amountMicros},
			{Scope: Scope(ScopeTreasury), AmountMicros: -amountMicros},
		},
	})
}

// Adjust posts an admin adjustment: signed amount, always balanced against
// the platform scope (issue #3: admin overrides post through the Ledger,
// never around it). A negative adjustment may not overdraw the account.
func (s *Service) Adjust(ctx context.Context, on Scope, amountMicros int64, actorID, memo string) (Movement, error) {
	if amountMicros == 0 {
		return Movement{}, fmt.Errorf("credits: adjustment amount must not be zero")
	}
	return s.store.PostMovement(ctx, Movement{
		Kind:    KindAdjustment,
		ActorID: actorID,
		Memo:    memo,
		Entries: []Entry{
			{Scope: on, AmountMicros: amountMicros},
			{Scope: Scope(ScopePlatform), AmountMicros: -amountMicros},
		},
	})
}

// ChargeInference prices one metered model call by the configured rule and
// posts the charge: the account debited, the platform credited.
func (s *Service) ChargeInference(ctx context.Context, usage InferenceUsage, ch Charge) (Movement, error) {
	if err := usage.ValidateUsage(); err != nil {
		return Movement{}, err
	}
	rules, err := s.rules(ctx)
	if err != nil {
		return Movement{}, fmt.Errorf("credits: load pricing rules: %w", err)
	}
	rule, err := rules.InferenceFor(ch.Model)
	if err != nil {
		return Movement{}, err
	}
	amount := rule.Price(usage)
	if amount <= 0 {
		return Movement{}, ErrUnbalanced // nothing metered: nothing to post
	}
	// The applied rates ride on the movement so the store persists them
	// (inference_charge_details) in the same transaction as the posting.
	detail := InferenceDetail{
		Model:                  ch.Model,
		InputTokens:            usage.InputTokens,
		CachedInputTokens:      usage.CachedInputTokens,
		OutputTokens:           usage.OutputTokens,
		InputMicrosPer1K:       rule.InputMicrosPer1K,
		CachedInputMicrosPer1K: rule.CachedInputMicrosPer1K,
		OutputMicrosPer1K:      rule.OutputMicrosPer1K,
	}
	return s.postCharge(ctx, amount, ch, KindInferenceCharge, fmt.Sprintf("model %s", ch.Model), &detail)
}

// ChargeData prices a data delivery by class and posts the charge.
func (s *Service) ChargeData(ctx context.Context, usage DataUsage, ch Charge) (Movement, error) {
	rules, err := s.rules(ctx)
	if err != nil {
		return Movement{}, fmt.Errorf("credits: load pricing rules: %w", err)
	}
	amount := rules.Data.Price(usage)
	if amount <= 0 {
		return Movement{}, ErrUnbalanced
	}
	return s.postCharge(ctx, amount, ch, KindDataCharge, string(usage.Class)+" data", nil)
}

func (s *Service) postCharge(ctx context.Context, amountMicros int64, ch Charge, kind, what string, detail *InferenceDetail) (Movement, error) {
	mov, err := s.store.PostMovement(ctx, Movement{
		Kind:      kind,
		ActorID:   ch.ActorID,
		RequestID: ch.RequestID,
		Memo:      joinMemo(ch.Memo, what),
		Inference: detail,
		Entries: []Entry{
			{Scope: ch.Scope, AmountMicros: -amountMicros},
			{Scope: Scope(ScopePlatform), AmountMicros: amountMicros},
		},
	})
	if err != nil {
		return Movement{}, err
	}
	return mov, nil
}

func joinMemo(parts ...string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += " — "
		}
		out += p
	}
	return out
}
