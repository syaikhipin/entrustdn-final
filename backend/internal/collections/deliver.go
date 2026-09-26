package collections

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strconv"

	"github.com/syaikhipin/entrustdn-final/backend/internal/anonymize"
	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
)

// The delivery half of ticket 12 (ADR 0005): a finalized Collection
// becomes a cleaned CSV for the Data Consumer. Participant identities —
// member names, contact points, member IDs — never enter the payload;
// participants carry stable pseudonyms resolved through the org's
// pseudonym map, the same map ingest uses, so delivered data stays
// linkable within the org without identifying anyone. The cleaner is the
// anonymize package's Delivery: the payload can only cross the boundary
// as a CleanedCollection, whose constructor has already run the cleaning.

// Cleaner is the delivery-cleaning seam — *anonymize.Delivery satisfies
// it; tests wrap it.
type Cleaner interface {
	Collection(ctx context.Context, orgID, format string, payload []byte) (anonymize.CleanedCollection, error)
}

// WithCredits attaches the Ledger (mandatory for delivery; injection is
// separate so the gathering half needs no credits wiring in tests).
func (s *Service) WithCredits(cs *credits.Service) *Service { s.credits = cs; return s }

// WithCleaner attaches the delivery cleaner. Without one, delivery
// refuses rather than ship uncleaned data.
func (s *Service) WithCleaner(c Cleaner) *Service { s.cleaner = c; return s }

// WithPseudonyms attaches the org's pseudonym map — where delivery
// participants' stable pseudonyms come from.
func (s *Service) WithPseudonyms(r anonymize.Resolver) *Service { s.pseudonyms = r; return s }

// participantKind names the pseudonym-map namespace for collection
// participants; distinct from ingest's "name" kind so an org's asset
// cleaning and collection delivery mint separate tokens for separate
// purposes.
const participantKind = "collection_participant"

// Deliver finalizes delivery: build the raw payload from accepted items,
// force it through the cleaner, charge the consumer the unique-data rate,
// split the premium as Revenue Share (ticket 15), and hand back the cleaned
// bytes. Unfinalized collections refuse (ErrClosed); strangers read as
// ErrNotFound; a missing price book or an overdrawn account refuses the
// delivery — surfaced, never silent, and nothing uncleaned ever leaves.
func (s *Service) Deliver(ctx context.Context, id, consumerID string) ([]byte, error) {
	if s.credits == nil || s.cleaner == nil || s.pseudonyms == nil {
		return nil, fmt.Errorf("collections: delivery is not wired (credits, cleaner, or pseudonyms missing)")
	}
	c, err := s.ByIDForConsumer(ctx, id, consumerID)
	if err != nil {
		return nil, err
	}
	if c.Status == StatusCollecting {
		return nil, ErrClosed
	}

	raw, err := s.buildPayload(ctx, c)
	if err != nil {
		return nil, err
	}
	cleaned, err := s.cleaner.Collection(ctx, c.OrgID, "csv", raw)
	if err != nil {
		// The cleaner refuses rather than ship possibly-identifying data;
		// the delivery dies here.
		return nil, fmt.Errorf("collections: clean delivery: %w", err)
	}

	charge := credits.Charge{
		Scope:     credits.AccountScope(consumerID),
		RequestID: c.RequestID,
		ActorID:   consumerID,
		Memo:      "collection delivery",
	}
	mov, err := s.credits.ChargeData(ctx, credits.DataUsage{
		Class: credits.DataUnique, Units: 1,
	}, charge)
	if err != nil {
		return nil, fmt.Errorf("collections: charge delivery: %w", err)
	}
	// Revenue Share (ticket 15): the premium just charged now splits —
	// org + members earn their share of what the platform collected. The
	// premium is the platform-side entry of the charge; the split rides the
	// same request so the audit trail shows charge and split together.
	premium := credits.PaidTo(mov, credits.Scope(credits.ScopePlatform))
	participants := s.participants(c)
	if _, err := s.credits.PostRevenueShare(ctx, credits.RevenueShare{
		OrgScope:     credits.AccountScope(c.OrgID),
		RequestID:    c.RequestID,
		ActorID:      consumerID,
		Memo:         "collection revenue share",
		Participants: participants,
	}, premium); err != nil {
		return nil, fmt.Errorf("collections: post revenue share: %w", err)
	}
	return cleaned.Bytes(), nil
}

// participants derives the Revenue Share weights from the collection's
// accepted items: each accepted answer is one unit of participation for its
// member — the ticket's participation-weighted pro-rata. A member that
// never contributed earns nothing and needs no resolution at all.
func (s *Service) participants(c Collection) []credits.Participant {
	weights := map[string]int64{}
	var order []string
	for _, it := range c.Items {
		if it.Status != ItemAccepted {
			continue
		}
		if _, seen := weights[it.MemberID]; !seen {
			order = append(order, it.MemberID)
		}
		weights[it.MemberID]++
	}
	out := make([]credits.Participant, 0, len(order))
	for _, id := range order {
		out = append(out, credits.Participant{MemberID: id, Weight: weights[id]})
	}
	return out
}

// buildPayload renders the collection's accepted items as the raw CSV:
// the participant's stable pseudonym, the question, the accepted answer,
// and which round produced it. Member names ride nowhere.
func (s *Service) buildPayload(ctx context.Context, c Collection) ([]byte, error) {
	pseudo := map[string]string{}
	for _, it := range c.Items {
		if it.Status != ItemAccepted {
			continue
		}
		if _, done := pseudo[it.MemberID]; done {
			continue
		}
		tok, err := s.pseudonyms.Pseudonym(ctx, c.OrgID, participantKind, it.MemberID)
		if err != nil {
			return nil, fmt.Errorf("collections: resolve participant pseudonym: %w", err)
		}
		pseudo[it.MemberID] = tok
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write([]string{"participant", "question", "answer", "round"}); err != nil {
		return nil, fmt.Errorf("collections: build payload: %w", err)
	}
	for _, it := range c.Items {
		if it.Status != ItemAccepted {
			continue
		}
		round := 0
		for ri, r := range it.Rounds {
			if r.Answer == it.Accepted {
				round = ri + 1
				break
			}
		}
		if err := w.Write([]string{
			pseudo[it.MemberID], it.Question, it.Accepted, strconv.Itoa(round),
		}); err != nil {
			return nil, fmt.Errorf("collections: build payload: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("collections: build payload: %w", err)
	}
	return buf.Bytes(), nil
}
