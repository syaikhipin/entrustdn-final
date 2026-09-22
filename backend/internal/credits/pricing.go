package credits

import (
	"errors"
	"fmt"
)

// ErrNoPricingRule is returned when no rule covers the requested model or
// data class. Charges never guess a price: an unpriced thing fails loudly
// and stays uncharged.
var ErrNoPricingRule = errors.New("credits: no pricing rule covers this")

// InferenceUsage is one metered model call: token counts as the gateway
// reports them. A token counts as cached when the answer draws on data that
// already exists in the platform database — serving stored data bills at
// the cheaper cached rate. When no cached data is available, the call bills
// at the standard input and output rates. CachedInputTokens is the portion
// of InputTokens served that way (OpenAI reports it inside prompt tokens),
// so it is never re-billed as fresh input.
type InferenceUsage struct {
	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
}

// InferenceRule prices one model. Rates are micro-credits per 1k tokens;
// ceil-division keeps sub-micro rounding in the platform's favor by at most
// one micro-credit — a rounding drift no pilot budget can feel.
type InferenceRule struct {
	Model                  string `json:"model"`
	InputMicrosPer1K       int64  `json:"input_micros_per_1k"`
	CachedInputMicrosPer1K int64  `json:"cached_input_micros_per_1k"`
	OutputMicrosPer1K      int64  `json:"output_micros_per_1k"`
}

// ValidateUsage refuses malformed metered usage: cached tokens are a subset
// of input tokens (the OpenAI-compatible convention), so more cached than
// input is a reporting bug, not a price — it fails loudly instead of being
// billed at whatever the mangled numbers happen to sum to.
func (u InferenceUsage) ValidateUsage() error {
	if u.InputTokens < 0 || u.CachedInputTokens < 0 || u.OutputTokens < 0 {
		return fmt.Errorf("credits: negative token count in usage %+v", u)
	}
	if u.CachedInputTokens > u.InputTokens {
		return fmt.Errorf("credits: cached input tokens (%d) exceed input tokens (%d)", u.CachedInputTokens, u.InputTokens)
	}
	return nil
}

// Price computes the charge for one call. CachedInputTokens is the portion
// of InputTokens served from the prompt cache (the OpenAI-compatible
// gateway reports it that way), so it is billed at the cached rate and the
// remainder at the fresh rate — a cached token is never billed twice.
// Malformed usage (cached > input) fails loudly via ValidateUsage; Price
// itself only clamps so a validation skip can never price negative.
func (r InferenceRule) Price(u InferenceUsage) int64 {
	fresh := u.InputTokens - u.CachedInputTokens
	if fresh < 0 {
		fresh = 0 // malformed usage: never negative-price
	}
	return ceilMicros(fresh, r.InputMicrosPer1K) +
		ceilMicros(u.CachedInputTokens, r.CachedInputMicrosPer1K) +
		ceilMicros(u.OutputTokens, r.OutputMicrosPer1K)
}

// ceilMicros prices n tokens at rate µ/1k, rounding up to whole micros.
func ceilMicros(tokens, microsPer1K int64) int64 {
	if tokens <= 0 || microsPer1K <= 0 {
		return 0
	}
	return (tokens*microsPer1K + 999) / 1000
}

// InferenceDetail is the applied pricing of one inference charge: the
// metered usage and the rates the rule charged — attached to the movement
// so the consumer's spend view can explain the charge line by line.
type InferenceDetail struct {
	Model                  string `json:"model"`
	InputTokens            int64  `json:"input_tokens"`
	CachedInputTokens      int64  `json:"cached_input_tokens"`
	OutputTokens           int64  `json:"output_tokens"`
	InputMicrosPer1K       int64  `json:"input_micros_per_1k"`
	CachedInputMicrosPer1K int64  `json:"cached_input_micros_per_1k"`
	OutputMicrosPer1K      int64  `json:"output_micros_per_1k"`
}

// DataClass distinguishes cached from unique data — the pricing axis the
// spec fixes: cached Assets are cheaper, unique Collections are premium.
type DataClass string

const (
	DataCached DataClass = "cached"
	DataUnique DataClass = "unique"
)

// DataUsage is one data charge: the class and how many units. A unit is
// whatever the caller is charging for (one Asset download, one Collection
// delivery) — the rules set the micro-credits per unit.
type DataUsage struct {
	Class DataClass
	Units int64
}

// DataRules price the two data classes.
type DataRules struct {
	CachedAssetMicrosPerUnit int64 `json:"cached_asset_micros_per_unit"`
	UniqueMicrosPerUnit      int64 `json:"unique_micros_per_unit"`
}

// Price computes the charge for one data delivery.
func (r DataRules) Price(u DataUsage) int64 {
	if u.Units <= 0 {
		return 0
	}
	switch u.Class {
	case DataCached:
		return u.Units * r.CachedAssetMicrosPerUnit
	case DataUnique:
		return u.Units * r.UniqueMicrosPerUnit
	default:
		return 0
	}
}

// PricingRules is the admin-configured price book: one rule per model, plus
// the data rates. It is stored as a single JSON document (migration 0003).
type PricingRules struct {
	Inference []InferenceRule `json:"inference"`
	Data      DataRules       `json:"data"`
}

// Validate refuses rule sets that would misprice: negative rates, cached
// input pricier than fresh input, or two rules for one model.
func (p PricingRules) Validate() error {
	seen := map[string]bool{}
	for _, r := range p.Inference {
		if r.Model == "" {
			return fmt.Errorf("pricing: inference rule with empty model")
		}
		if seen[r.Model] {
			return fmt.Errorf("pricing: duplicate inference rule for model %q", r.Model)
		}
		seen[r.Model] = true
		if r.InputMicrosPer1K < 0 || r.CachedInputMicrosPer1K < 0 || r.OutputMicrosPer1K < 0 {
			return fmt.Errorf("pricing: negative rate for model %q", r.Model)
		}
		if r.CachedInputMicrosPer1K > r.InputMicrosPer1K {
			return fmt.Errorf("pricing: cached input pricier than fresh input for model %q", r.Model)
		}
	}
	if p.Data.CachedAssetMicrosPerUnit < 0 || p.Data.UniqueMicrosPerUnit < 0 {
		return fmt.Errorf("pricing: negative data rate")
	}
	if p.Inference == nil && (p.Data.CachedAssetMicrosPerUnit == 0 && p.Data.UniqueMicrosPerUnit == 0) {
		return fmt.Errorf("pricing: rule set is empty")
	}
	return nil
}

// InferenceFor returns the rule covering the model.
func (p PricingRules) InferenceFor(model string) (InferenceRule, error) {
	for _, r := range p.Inference {
		if r.Model == model {
			return r, nil
		}
	}
	return InferenceRule{}, fmt.Errorf("%w: model %q", ErrNoPricingRule, model)
}
