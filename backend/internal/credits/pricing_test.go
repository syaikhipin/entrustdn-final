package credits_test

import (
	"errors"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
)

// Automatic pricing (ticket 03): rules per model and per data class compute
// charges; the Platform Admin configures the rule, the engine does the math.
// All amounts are micro-credits; expected values are worked examples, not
// recomputations.

func TestInferenceChargeMath(t *testing.T) {
	gptRule := credits.InferenceRule{
		Model:                  "gpt-5.2",
		InputMicrosPer1K:       2_500,  // 2.5 credits per 1k input tokens
		CachedInputMicrosPer1K: 1_250,  // cache hits are half price
		OutputMicrosPer1K:      10_000, // 10 credits per 1k output tokens
	}
	cheapRule := credits.InferenceRule{
		Model:                  "haiku",
		InputMicrosPer1K:       400,
		CachedInputMicrosPer1K: 100,
		OutputMicrosPer1K:      2_000,
	}

	tests := []struct {
		name string
		rule credits.InferenceRule
		req  credits.InferenceUsage
		want int64
	}{
		{
			name: "input + output, no cache",
			rule: gptRule,
			req:  credits.InferenceUsage{InputTokens: 1_000, OutputTokens: 1_000},
			want: 2_500 + 10_000, // 12_500
		},
		{
			name: "cache-hit input billed at the cached rate",
			// Subset semantics, as the OpenAI-compatible gateway reports it:
			// of 1_000 input tokens, 1_000 hit the cache — fresh pays zero.
			rule: gptRule,
			req:  credits.InferenceUsage{InputTokens: 1_000, CachedInputTokens: 1_000, OutputTokens: 1_000},
			want: 1_250 + 10_000,
		},
		{
			name: "mixed cached and fresh input rounds each part up to whole micros",
			// Worked example: of 10 input tokens, 3 are cached →
			// ceil(7*2500/1000)=18 fresh + ceil(3*1250/1000)=4 cached,
			// plus ceil(5*10000/1000)=50 output.
			rule: gptRule,
			req:  credits.InferenceUsage{InputTokens: 10, CachedInputTokens: 3, OutputTokens: 5},
			want: 18 + 4 + 50,
		},
		{
			name: "Price itself never goes negative even on malformed usage",
			// The clamp is a last-resort invariant, not a license: the
			// service layer refuses this usage before pricing (see
			// TestChargeInferenceRefusesMalformedUsage).
			rule: gptRule,
			req:  credits.InferenceUsage{InputTokens: 100, CachedInputTokens: 200, OutputTokens: 0},
			want: 250, // ceil(200*1250/1000)
		},
		{
			name: "zero usage prices zero",
			rule: gptRule,
			req:  credits.InferenceUsage{},
			want: 0,
		},
		{
			name: "cheap model stays cheap",
			// Worked example: of 10_000 input tokens, 10_000 are cached →
			// fresh pays 0, cached pays 1_000, output 2_500@2000 pays 5_000.
			rule: cheapRule,
			req:  credits.InferenceUsage{InputTokens: 10_000, CachedInputTokens: 10_000, OutputTokens: 2_500},
			want: 1_000 + 5_000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.rule.Price(tt.req)
			if got != tt.want {
				t.Errorf("Price(%+v) = %d micros, want %d", tt.req, got, tt.want)
			}
		})
	}
}

func TestDataChargeMath(t *testing.T) {
	rules := credits.DataRules{
		CachedAssetMicrosPerUnit: 5_000_000,  // 5 credits per cached Asset unit
		UniqueMicrosPerUnit:      50_000_000, // 50 credits per unique Collection unit
	}
	tests := []struct {
		name string
		req  credits.DataUsage
		want int64
	}{
		{name: "cached asset: cheaper", req: credits.DataUsage{Class: credits.DataCached, Units: 3}, want: 15_000_000},
		{name: "unique collection: premium", req: credits.DataUsage{Class: credits.DataUnique, Units: 1}, want: 50_000_000},
		{name: "zero units price zero", req: credits.DataUsage{Class: credits.DataCached, Units: 0}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rules.Price(tt.req)
			if got != tt.want {
				t.Errorf("Price(%+v) = %d micros, want %d", tt.req, got, tt.want)
			}
		})
	}
}

func TestPricingRulesValidate(t *testing.T) {
	tests := []struct {
		name    string
		rules   credits.PricingRules
		wantErr bool
	}{
		{
			name: "well-formed rules pass",
			rules: credits.PricingRules{
				Inference: []credits.InferenceRule{{
					Model: "gpt-5.2", InputMicrosPer1K: 1, CachedInputMicrosPer1K: 1, OutputMicrosPer1K: 1,
				}},
				Data: credits.DataRules{CachedAssetMicrosPerUnit: 1, UniqueMicrosPerUnit: 1},
			},
			wantErr: false,
		},
		{
			name:    "empty rules are refused",
			rules:   credits.PricingRules{},
			wantErr: true,
		},
		{
			name: "negative rate is refused",
			rules: credits.PricingRules{
				Data: credits.DataRules{CachedAssetMicrosPerUnit: -1, UniqueMicrosPerUnit: 1},
			},
			wantErr: true,
		},
		{
			name: "cached input pricier than fresh input is refused",
			rules: credits.PricingRules{
				Inference: []credits.InferenceRule{{
					Model: "gpt-5.2", InputMicrosPer1K: 1, CachedInputMicrosPer1K: 2, OutputMicrosPer1K: 1,
				}},
			},
			wantErr: true,
		},
		{
			name: "duplicate model rule is refused",
			rules: credits.PricingRules{
				Inference: []credits.InferenceRule{
					{Model: "gpt-5.2", InputMicrosPer1K: 1, CachedInputMicrosPer1K: 1, OutputMicrosPer1K: 1},
					{Model: "gpt-5.2", InputMicrosPer1K: 2, CachedInputMicrosPer1K: 2, OutputMicrosPer1K: 2},
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.rules.Validate()
			if tt.wantErr && err == nil {
				t.Errorf("Validate() = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestInferenceRuleForModel(t *testing.T) {
	rules := credits.PricingRules{
		Inference: []credits.InferenceRule{{
			Model: "gpt-5.2", InputMicrosPer1K: 1, CachedInputMicrosPer1K: 1, OutputMicrosPer1K: 1,
		}},
		Data: credits.DataRules{CachedAssetMicrosPerUnit: 1, UniqueMicrosPerUnit: 1},
	}
	if _, err := rules.InferenceFor("gpt-5.2"); err != nil {
		t.Errorf("InferenceFor(configured model) = %v, want nil", err)
	}
	if _, err := rules.InferenceFor("unknown-model"); !errors.Is(err, credits.ErrNoPricingRule) {
		t.Errorf("InferenceFor(unknown) = %v, want ErrNoPricingRule", err)
	}
}
