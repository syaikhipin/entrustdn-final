package credits

import (
	"context"
	"fmt"
	"sort"
)

// Revenue Share (ticket 15): the data premium a Consumer pays for a
// Collection delivery divides between the Farmer Organization and the
// platform, and the org's share distributes across its Members pro-rata by
// participation. The split math here is pure integer arithmetic so the
// rounding policy is explicit: no micro-credit is ever created or lost.

// DefaultRevenueShareOrgPercent is the org's share when the price book
// carries no explicit percentage — the ticket's 80/20 default.
const DefaultRevenueShareOrgPercent = 80

// SplitPremium divides the premium into (org, platform) micros at the given
// org percentage. The org side floors; the platform absorbs every rounding
// remainder, so org + platform is always exactly the premium. This mirrors
// the pricing engine's convention of keeping sub-micro drift off the
// participants' accounts (InferenceRule's ceil-division).
func SplitPremium(premium int64, orgPercent int) (org, platform int64) {
	if premium <= 0 {
		return 0, 0
	}
	org = premium * int64(orgPercent) / 100
	return org, premium - org
}

// ProRata distributes premium micros across weights (one per Member, the
// Member's contribution to the Collection — accepted answers) by largest
// remainder: every share floors at premium·w/total, then leftover micros go
// to the largest fractional parts, ties broken by position, so the result
// is deterministic and sums to at most the premium. Non-participants
// (weight ≤ 0) receive nothing; with no participants at all, nothing
// distributes. A nil weights slice returns nil.
func ProRata(premium int64, weights []int64) []int64 {
	if weights == nil {
		return nil
	}
	shares := make([]int64, len(weights))
	var total int64
	for _, w := range weights {
		if w > 0 {
			total += w
		}
	}
	if total <= 0 || premium <= 0 {
		return shares
	}

	type leftover struct {
		idx int
		rem int64
	}
	var distributed int64
	remainders := make([]leftover, 0, len(weights))
	for i, w := range weights {
		if w <= 0 {
			continue
		}
		shares[i] = premium * w / total
		distributed += shares[i]
		remainders = append(remainders, leftover{idx: i, rem: premium * w % total})
	}
	sort.Slice(remainders, func(a, b int) bool {
		if remainders[a].rem != remainders[b].rem {
			return remainders[a].rem > remainders[b].rem
		}
		return remainders[a].idx < remainders[b].idx
	})
	for i := 0; distributed < premium && i < len(remainders); i++ {
		shares[remainders[i].idx]++
		distributed++
	}
	return shares
}

// MemberScope is the ledger scope carrying a roster Member's earned share.
// Roster Members hold no platform accounts (ticket 08's roster.Store is not
// membership); their earnings live in member-scoped entries, credited only
// by Revenue Share — nothing ever debits a member scope, so no overdraft
// question ever arises.
func MemberScope(memberID string) Scope { return Scope("member:" + memberID) }

// PaidTo sums how much one movement credited a scope — the premium side of
// a charge: what the data charge paid into the platform, read off the
// movement it posted. A scope's debits don't count.
func PaidTo(mov Movement, scope Scope) int64 {
	var sum int64
	for _, e := range mov.Entries {
		if e.Scope == scope && e.AmountMicros > 0 {
			sum += e.AmountMicros
		}
	}
	return sum
}

// Participant is one Member's contribution to a Collection: their roster ID
// and the weight their participation earns (accepted answers). A zero or
// negative weight is a non-participant and earns nothing.
type Participant struct {
	MemberID string
	Weight   int64
}

// RevenueShare carries the accounting context of one premium split: the org
// account scope that receives the org share, and who/what the delivery was
// for. The premium itself rides separately (the caller extracts it from the
// charge it just posted).
type RevenueShare struct {
	OrgScope     Scope
	RequestID    string
	ActorID      string
	Memo         string
	Participants []Participant
}

// PostRevenueShare splits the premium at the configured percentage and
// posts one balanced revenue_share movement: the platform scope debited the
// whole org share, the org account credited it — then, when members
// participated, the org account debited their pro-rata distribution and
// each member's scope credited their share. The charge that earned the
// premium already credited the platform the full amount, so after this
// movement the platform's remaining balance is exactly its own share, the
// org account nets orgShare − distributed (the full share when nobody
// participated, the rounding residual otherwise), and members hold theirs. A
// zero org share posts nothing at all: the platform keeps everything, and
// there is nothing to remember. The premium must be positive — a Revenue
// Share posting follows a real charge, never a void.
func (s *Service) PostRevenueShare(ctx context.Context, rs RevenueShare, premiumMicros int64) (Movement, error) {
	if premiumMicros <= 0 {
		return Movement{}, fmt.Errorf("credits: revenue share premium must be positive, got %d", premiumMicros)
	}
	rules, err := s.rules(ctx)
	if err != nil {
		return Movement{}, fmt.Errorf("credits: load pricing rules: %w", err)
	}
	orgShare, _ := SplitPremium(premiumMicros, rules.OrgSharePercent())
	if orgShare <= 0 {
		return Movement{}, nil // the platform keeps everything: nothing to post
	}

	memberShares := ProRata(orgShare, participantWeights(rs.Participants))
	distributed := int64(0)
	memberEntries := make([]Entry, 0, len(rs.Participants))
	for i, p := range rs.Participants {
		if memberShares[i] <= 0 {
			continue
		}
		memberEntries = append(memberEntries, Entry{
			Scope:        MemberScope(p.MemberID),
			AmountMicros: memberShares[i],
			Memo:         joinMemo(rs.Memo, "member share"),
		})
		distributed += memberShares[i]
	}
	// The platform scope passes the whole org share on; the org account
	// receives it and distributes what its members earned, both visible in
	// the movement: the org's gross receipt and the member distribution are
	// separate entries. What the org nets is orgShare − distributed — the
	// full share when nobody participated, shrinking to the rounding
	// residual as members absorb the rest.
	entries := make([]Entry, 0, len(memberEntries)+3)
	entries = append(entries,
		Entry{
			Scope:        rs.OrgScope,
			AmountMicros: orgShare,
			Memo:         joinMemo(rs.Memo, "organization share"),
		},
		Entry{
			Scope:        Scope(ScopePlatform),
			AmountMicros: -orgShare,
			Memo:         joinMemo(rs.Memo, "platform share"),
		},
	)
	if distributed > 0 {
		entries = append(entries, Entry{
			Scope:        rs.OrgScope,
			AmountMicros: -distributed,
			Memo:         joinMemo(rs.Memo, "member distribution"),
		})
	}
	entries = append(entries, memberEntries...)
	mov, err := s.store.PostMovement(ctx, Movement{
		Kind:      KindRevenueShare,
		ActorID:   rs.ActorID,
		RequestID: rs.RequestID,
		Memo:      rs.Memo,
		Entries:   entries,
	})
	if err != nil {
		return Movement{}, fmt.Errorf("credits: post revenue share: %w", err)
	}
	return mov, nil
}

// participantWeights extracts the weights in participant order; ProRata
// keeps the indexes aligned.
func participantWeights(ps []Participant) []int64 {
	w := make([]int64, len(ps))
	for i, p := range ps {
		w[i] = p.Weight
	}
	return w
}
