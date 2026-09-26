package credits_test

import (
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
)

// Revenue Share (ticket 15) split math at the pure seam: the data premium
// divides org-vs-platform at the configured percentage, and the org's share
// distributes across members pro-rata by participation. Integer money means
// rounding is a design decision, not an accident: the platform keeps every
// rounding remainder, and pro-rata leftovers go to the earliest largest
// fractional part — deterministic, order-driven, and never lost.

func TestSplitPremium(t *testing.T) {
	tests := []struct {
		name                  string
		premium               int64
		orgPercent            int
		wantOrg, wantPlatform int64
	}{
		{"worked example: 50 cr at 80/20", 50_000_000, 80, 40_000_000, 10_000_000},
		{"one micro: platform keeps the remainder", 1, 80, 0, 1},
		{"three micros at 50: org floors", 3, 50, 1, 2},
		{"zero percent: all platform", 100, 0, 0, 100},
		{"full percent: all org", 100, 100, 100, 0},
		{"irregular: 33% of 33 micros", 33, 33, 10, 23},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			org, platform := credits.SplitPremium(tt.premium, tt.orgPercent)
			if org != tt.wantOrg || platform != tt.wantPlatform {
				t.Errorf("SplitPremium(%d, %d) = (%d, %d), want (%d, %d)",
					tt.premium, tt.orgPercent, org, platform, tt.wantOrg, tt.wantPlatform)
			}
			if org+platform != tt.premium {
				t.Errorf("split lost value: %d + %d != %d", org, platform, tt.premium)
			}
		})
	}
}

func TestProRata(t *testing.T) {
	tests := []struct {
		name    string
		premium int64
		weights []int64
		want    []int64
	}{
		{"equal participation splits evenly", 40_000_000, []int64{2, 2}, []int64{20_000_000, 20_000_000}},
		{"weighted by contribution", 40_000_000, []int64{3, 1}, []int64{30_000_000, 10_000_000}},
		{"one micro, three equal sharers: leftovers in order", 10, []int64{1, 1, 1}, []int64{4, 3, 3}},
		{"two sharers, indivisible: bigger fraction wins", 5, []int64{1, 2}, []int64{2, 3}},
		{"a non-participant gets nothing", 7, []int64{0, 2}, []int64{0, 7}},
		{"nobody participated: nothing distributes", 100, []int64{0, 0}, []int64{0, 0}},
		{"no recipients at all", 100, nil, nil},
		{"all one member", 1_000, []int64{5}, []int64{1_000}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := credits.ProRata(tt.premium, tt.weights)
			if len(got) != len(tt.want) {
				t.Fatalf("ProRata(%d, %v) = %v, want %v", tt.premium, tt.weights, got, tt.want)
			}
			var sum int64
			for i, g := range got {
				sum += g
				if g < 0 {
					t.Errorf("share[%d] = %d, never negative", i, g)
				}
				if g != tt.want[i] {
					t.Errorf("share[%d] = %d, want %d (premium %d, weights %v)", i, g, tt.want[i], tt.premium, tt.weights)
				}
			}
			if sum > tt.premium {
				t.Errorf("shares sum to %d, over the premium %d", sum, tt.premium)
			}
		})
	}
}

func TestProRataLeftoverGoesToLargestFraction(t *testing.T) {
	// 100 micros over weights 50/30/20: exact shares are 50/30/20 with no
	// remainder — the no-loss case. Shift so rounding bites: 101 micros.
	got := credits.ProRata(101, []int64{50, 30, 20})
	want := []int64{51, 30, 20} // first share carries the leftover micro
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ProRata(101, [50 30 20]) = %v, want %v", got, want)
		}
	}
}

func TestOrgSharePercentDefaultsToEighty(t *testing.T) {
	// A price book predating ticket 15 carries no percentage at all: the
	// 80/20 default must hold without a migration or an admin visit.
	if got := (credits.PricingRules{}).OrgSharePercent(); got != credits.DefaultRevenueShareOrgPercent {
		t.Errorf("default org share = %d, want %d", got, credits.DefaultRevenueShareOrgPercent)
	}
	eighty := 80
	if got := (credits.PricingRules{RevenueShareOrgPercent: &eighty}).OrgSharePercent(); got != 80 {
		t.Errorf("explicit org share = %d, want 80", got)
	}
}

func TestPricingRulesValidateRefusesImpossibleShares(t *testing.T) {
	forty := 40
	hundredOne := 101
	negative := -1
	tests := []struct {
		name    string
		percent *int
		wantErr bool
	}{
		{"absent rides the default", nil, false},
		{"zero is legal (all platform)", intPtr(0), false},
		{"hundred is legal (all org)", intPtr(100), false},
		{"a middle value is legal", &forty, false},
		{"over hundred refused", &hundredOne, true},
		{"negative refused", &negative, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := testPricingRulesDoc()
			rules.RevenueShareOrgPercent = tt.percent
			err := rules.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func testPricingRulesDoc() credits.PricingRules {
	return credits.PricingRules{
		Inference: []credits.InferenceRule{{Model: "m", InputMicrosPer1K: 1, OutputMicrosPer1K: 1}},
		Data:      credits.DataRules{CachedAssetMicrosPerUnit: 5_000_000, UniqueMicrosPerUnit: 50_000_000},
	}
}

func intPtr(i int) *int { return &i }
