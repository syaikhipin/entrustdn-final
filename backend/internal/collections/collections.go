// Package collections implements ticket 12: a clarified Request becomes a
// completed, delivered Data Collection. The Agent gathers Farmer Member
// answers over Channels (ticket 11's conversations carry the durable
// threads); the default quality triggers (completeness, anomaly — see
// triggers.go; template-sourced triggers are ticket 13) evaluate every
// answer and drive re-asks; a Collection completes when every (member,
// question) item holds an accepted answer, or flags incomplete when the
// deadline passes, the request's budget is exhausted, or a member has no
// path left to a good answer (stop word, re-ask cap hit).
//
// The collection record itself is single-writer: only Sync rewrites it,
// whole, after reading the org's conversations — the same whole-record
// pattern the requests and conversations packages use.
package collections

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/anonymize"
	"github.com/syaikhipin/entrustdn-final/backend/internal/conversations"
	"github.com/syaikhipin/entrustdn-final/backend/internal/credits"
	"github.com/syaikhipin/entrustdn-final/backend/internal/requests"
	"github.com/syaikhipin/entrustdn-final/backend/internal/roster"
)

// MaxReasksPerItem caps how many times one item's bad answer may trigger a
// re-ask. Past the cap the item escalates: it can no longer reach
// completion, and the collection flags incomplete.
const MaxReasksPerItem = 2

// Status is where a Collection sits in its life.
type Status string

const (
	// StatusCollecting: the agent is gathering answers.
	StatusCollecting Status = "collecting"
	// StatusCompleted: every item holds an accepted answer.
	StatusCompleted Status = "completed"
	// StatusIncomplete: the deadline, budget, or escalation path ended the
	// gathering with gaps — the Consumer sees what's missing.
	StatusIncomplete Status = "incomplete"
)

// ItemStatus is where one (member, question) item stands.
type ItemStatus string

const (
	// ItemCollecting: open — no accepted answer yet, and a path may still
	// produce one.
	ItemCollecting ItemStatus = "collecting"
	// ItemAccepted: a quality trigger passed an answer for this item.
	ItemAccepted ItemStatus = "accepted"
	// ItemBlocked: no path to completion — the Member opted out, or the
	// re-ask cap was hit. Escalation, surfaced.
	ItemBlocked ItemStatus = "blocked"
)

var (
	// ErrNotFound is returned for unknown collections — and, per the
	// platform convention, for collections that exist but belong to
	// someone else: existence is not disclosed.
	ErrNotFound = errors.New("collections: not found")
	// ErrClosed is returned when Sync runs on a finalized collection.
	ErrClosed = errors.New("collections: collection is finalized")
)

// Round is one answer attempt for an item, with the trigger verdict.
type Round struct {
	ConversationID string    `json:"conversation_id"`
	Answer         string    `json:"answer"`
	OK             bool      `json:"ok"`
	Reason         string    `json:"reason,omitempty"`
	At             time.Time `json:"at"`
}

// Item is one (member, question) slot of a Collection.
type Item struct {
	MemberID   string     `json:"member_id"`
	MemberName string     `json:"member_name"`
	Question   string     `json:"question"`
	Status     ItemStatus `json:"status"`
	// Accepted is the answer a trigger passed, empty until one does.
	Accepted string `json:"accepted,omitempty"`
	// Rounds is every answer attempt, in arrival order — the audit of what
	// was gathered and why it was refused. One round per (conversation,
	// question): re-ask conversations each contribute their own.
	Rounds []Round `json:"rounds"`
	// Reasks counts the re-ask conversations opened for this item — the
	// cap is on asks, not on answers, so re-running Sync cannot open more.
	Reasks int `json:"reasks"`
	// Blocked carries why an item can no longer complete (stop word,
	// re-ask cap). True only when Status is blocked.
	Blocked     bool   `json:"blocked"`
	BlockReason string `json:"blocked_reason,omitempty"`
}

// Missing is one gap in a delivered Collection: the question that went
// unanswered (or unaccepted), who it was for, and why.
type Missing struct {
	MemberName string `json:"member_name"`
	Question   string `json:"question"`
	Reason     string `json:"reason"`
}

// Collection is the durable record of one data gathering effort.
type Collection struct {
	ID         string
	OrgID      string
	ConsumerID string
	RequestID  string
	// Deadline, when set, is the hard end: Sync past it finalizes the
	// collection incomplete.
	Deadline *time.Time
	Status   Status
	Items    []Item
	// Missing is the missing-data summary of an incomplete collection.
	Missing   []Missing
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewCollection is what the org submits to open a collection.
type NewCollection struct {
	RequestID string
	MemberIDs []string
	Questions []string
	// Deadline is optional; nil gathers without a hard end.
	Deadline *time.Time
}

// Validate refuses records that must never be stored.
func (n NewCollection) Validate() error {
	if len(n.Questions) == 0 {
		return fmt.Errorf("collections: at least one question is required")
	}
	if len(n.MemberIDs) == 0 {
		return fmt.Errorf("collections: at least one member is required")
	}
	for _, q := range n.Questions {
		if strings.TrimSpace(q) == "" {
			return fmt.Errorf("collections: questions must not be blank")
		}
	}
	return nil
}

// Opener opens a (re-ask) conversation with one member — the
// conversations.Service satisfies it. Split from reading so Sync never
// needs the whole conversation service.
type Opener interface {
	Start(ctx context.Context, st conversations.Start) (conversations.Conversation, error)
}

// Deps carries the collaborators the collection service needs.
type Deps struct {
	Store         Store
	Conversations conversations.Store
	Opener        Opener
	Roster        roster.Store
	Requests      requests.Store
	// Now is the injected clock; tests pin it, production uses time.Now.
	Now func() time.Time
}

// Service is the collection front door: creation, the gathering sync, and
// the entitlement-checked reads. Delivery's collaborators (credits,
// cleaner, pseudonyms) attach through the With* setters — see deliver.go.
type Service struct {
	store  Store
	convs  conversations.Store
	opener Opener
	roster roster.Store
	reqs   requests.Store
	now    func() time.Time

	credits    *credits.Service
	cleaner    Cleaner
	pseudonyms anonymize.Resolver

	mu sync.Mutex // serializes Sync per process (single-writer rule)
}

// NewService wires the collection service.
func NewService(d Deps) *Service {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &Service{store: d.Store, convs: d.Conversations, opener: d.Opener, roster: d.Roster, reqs: d.Requests, now: now}
}

// WithOpener replaces the re-ask opener — tests swap in fakes that
// simulate partial channel outages.
func (s *Service) WithOpener(o Opener) *Service { s.opener = o; return s }

// NewID mints a collection ID.
func NewID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("collections: mint id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Store is the persistence seam. Postgres implements it for the system of
// record; MemoryStore backs tests.
type Store interface {
	CreateCollection(ctx context.Context, c *Collection) error
	CollectionByID(ctx context.Context, id string) (Collection, error)
	CollectionsByOrg(ctx context.Context, orgID string) ([]Collection, error)
	CollectionsByConsumer(ctx context.Context, consumerID string) ([]Collection, error)
	UpdateCollection(ctx context.Context, c Collection) error
}

// Compile-time check that MemoryStore satisfies Store.
var _ Store = (*MemoryStore)(nil)

// MemoryStore is the in-memory Store double used by tests.
type MemoryStore struct {
	mu   sync.Mutex
	cols map[string]Collection
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{cols: map[string]Collection{}}
}

func (m *MemoryStore) CreateCollection(_ context.Context, c *Collection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.cols[c.ID]; dup {
		return fmt.Errorf("collections: id %s already exists", c.ID)
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = now
	}
	m.cols[c.ID] = *c
	return nil
}

func (m *MemoryStore) CollectionByID(_ context.Context, id string) (Collection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.cols[id]
	if !ok {
		return Collection{}, ErrNotFound
	}
	return c, nil
}

func (m *MemoryStore) CollectionsByOrg(_ context.Context, orgID string) ([]Collection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.list(func(c Collection) bool { return c.OrgID == orgID }), nil
}

func (m *MemoryStore) CollectionsByConsumer(_ context.Context, consumerID string) ([]Collection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.list(func(c Collection) bool { return c.ConsumerID == consumerID }), nil
}

func (m *MemoryStore) UpdateCollection(_ context.Context, c Collection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.cols[c.ID]; !ok {
		return ErrNotFound
	}
	c.UpdatedAt = time.Now().UTC()
	m.cols[c.ID] = c
	return nil
}

// list collects matching collections newest first.
func (m *MemoryStore) list(match func(Collection) bool) []Collection {
	out := []Collection{}
	for _, c := range m.cols {
		if match(c) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Create opens a Collection: a clarified Request, the org's roster members,
// and the questions to gather — items materialize as the cross product.
func (s *Service) Create(ctx context.Context, orgID string, n NewCollection) (Collection, error) {
	if err := n.Validate(); err != nil {
		return Collection{}, err
	}
	r, err := s.reqs.RequestByID(ctx, n.RequestID)
	if err != nil {
		if errors.Is(err, requests.ErrNotFound) {
			return Collection{}, ErrNotFound
		}
		return Collection{}, fmt.Errorf("collections: load request: %w", err)
	}
	if r.Status != requests.StatusClarified {
		return Collection{}, fmt.Errorf("collections: request %s is %s, not clarified — field it first", r.ID, r.Status)
	}
	members, err := s.roster.MembersByOrg(ctx, orgID)
	if err != nil {
		return Collection{}, fmt.Errorf("collections: load roster: %w", err)
	}
	byID := map[string]roster.Member{}
	for _, m := range members {
		byID[m.ID] = m
	}
	id, err := NewID()
	if err != nil {
		return Collection{}, err
	}
	c := Collection{
		ID: id, OrgID: orgID, ConsumerID: r.ConsumerID, RequestID: r.ID,
		Deadline: n.Deadline, Status: StatusCollecting,
		Items: []Item{}, Missing: []Missing{},
	}
	for _, mid := range n.MemberIDs {
		m, ok := byID[mid]
		if !ok || m.OrgID != orgID {
			return Collection{}, fmt.Errorf("collections: member %s is not on the org's roster", mid)
		}
		for _, q := range n.Questions {
			c.Items = append(c.Items, Item{
				MemberID: m.ID, MemberName: m.DisplayName, Question: q,
				Status: ItemCollecting, Rounds: []Round{},
			})
		}
	}
	if err := s.store.CreateCollection(ctx, &c); err != nil {
		return Collection{}, err
	}
	return c, nil
}

// ByID returns one collection to its org after the entitlement check.
func (s *Service) ByID(ctx context.Context, id, orgID string) (Collection, error) {
	c, err := s.store.CollectionByID(ctx, id)
	if err != nil {
		return Collection{}, err
	}
	if c.OrgID != orgID {
		return Collection{}, ErrNotFound // existence is not disclosed
	}
	return c, nil
}

// ByIDForConsumer returns one collection to its Data Consumer — the payer
// of the request sees its gathering.
func (s *Service) ByIDForConsumer(ctx context.Context, id, consumerID string) (Collection, error) {
	c, err := s.store.CollectionByID(ctx, id)
	if err != nil {
		return Collection{}, err
	}
	if c.ConsumerID != consumerID {
		return Collection{}, ErrNotFound
	}
	return c, nil
}

// ByOrg lists the org's collections, newest first.
func (s *Service) ByOrg(ctx context.Context, orgID string) ([]Collection, error) {
	return s.store.CollectionsByOrg(ctx, orgID)
}

// ByConsumer lists the Data Consumer's collections — the payer's view of
// what's being gathered in their name.
func (s *Service) ByConsumer(ctx context.Context, consumerID string) ([]Collection, error) {
	return s.store.CollectionsByConsumer(ctx, consumerID)
}

// Sync is the gathering step: read the org's member conversations for this
// collection's request, run the quality triggers over every answer not yet
// ingested, open re-asks for bad answers, and finalize when the paths say
// so. Idempotent — a re-run ingests only what is new.
func (s *Service) Sync(ctx context.Context, id, orgID string) (Collection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, err := s.ByID(ctx, id, orgID)
	if err != nil {
		return Collection{}, err
	}
	if c.Status != StatusCollecting {
		return Collection{}, ErrClosed
	}

	// The org's conversations for this collection's request, by member.
	convList, err := s.convs.ConversationsByOrg(ctx, orgID)
	if err != nil {
		return Collection{}, fmt.Errorf("collections: load conversations: %w", err)
	}
	byMember := map[string]conversations.Conversation{}
	for _, cv := range convList {
		if cv.RequestID != c.RequestID {
			continue
		}
		// A member may hold several conversations (initial + re-asks);
		// keep them all, newest wins for status.
		byMember[cv.MemberID+"|"+cv.ID] = cv
	}

	now := s.now()

	for ii := range c.Items {
		it := &c.Items[ii]
		if it.Status == ItemAccepted || it.Status == ItemBlocked {
			continue
		}
		// Ingest every not-yet-recorded answer for this member & question,
		// from any conversation whose question list carries the question.
		for _, cv := range ordered(byMember, it.MemberID) {
			qi := slices.Index(cv.Questions, it.Question)
			if qi < 0 {
				continue
			}
			// One round per (conversation, question): count this
			// conversation's already-recorded rounds and ingest only what
			// is new — the n-th answer answers the n-th question the
			// conversation asked.
			seen := 0
			for _, r := range it.Rounds {
				if r.ConversationID == cv.ID {
					seen++
				}
			}
			for ai := seen; ai < len(cv.Answers); ai++ {
				if ai != qi {
					continue
				}
				ans := cv.Answers[ai]
				v := Evaluate(it.Question, ans)
				it.Rounds = append(it.Rounds, Round{
					ConversationID: cv.ID, Answer: ans,
					OK: v.OK, Reason: v.Reason, At: now,
				})
				if v.OK {
					it.Status = ItemAccepted
					it.Accepted = ans
					it.Blocked = false
					it.BlockReason = ""
				}
			}
			if it.Status == ItemAccepted {
				break
			}
		}
		if it.Status == ItemAccepted {
			continue
		}

		// Blocked paths: the member opted out anywhere on their threads.
		if memberStopped(byMember, it.MemberID) {
			it.Status = ItemBlocked
			it.Blocked = true
			it.BlockReason = "the member opted out of this conversation"
			continue
		}

		// Re-ask on a bad answer, unless the cap has been reached.
		badRounds := 0
		for _, r := range it.Rounds {
			if !r.OK {
				badRounds++
			}
		}
		if badRounds == 0 {
			continue // nothing to judge yet: still waiting on the member
		}
		if badRounds <= it.Reasks {
			continue // every bad answer already has its re-ask in flight
		}
		if it.Reasks >= MaxReasksPerItem {
			it.Status = ItemBlocked
			it.Blocked = true
			it.BlockReason = fmt.Sprintf("the re-ask limit (%d) was reached without a usable answer", MaxReasksPerItem)
			continue
		}
		// Open as planned, persist as sent: each open's success is counted
		// (Reasks++) and stored before the next one is attempted. A channel
		// outage mid-batch leaves every sent re-ask counted and the unsent
		// one not — the retry re-derives only what never went out.
		st := conversations.Start{
			OrgID:      c.OrgID,
			RequestID:  c.RequestID,
			MemberID:   it.MemberID,
			MemberName: it.MemberName,
			Contact:    s.memberContact(ctx, c.OrgID, it.MemberID),
			Topic:      "A follow-up on your earlier answer",
			Questions:  []string{it.Question},
		}
		if _, err := s.opener.Start(ctx, st); err != nil {
			if perr := s.store.UpdateCollection(ctx, c); perr != nil {
				return Collection{}, fmt.Errorf("collections: open re-ask for %s: %w (and persist what was sent: %v)", st.MemberID, err, perr)
			}
			return Collection{}, fmt.Errorf("collections: open re-ask for %s: %w", st.MemberID, err)
		}
		it.Reasks++ // counted only once the conversation actually opened
		if err := s.store.UpdateCollection(ctx, c); err != nil {
			return Collection{}, err
		}
	}

	// Finalization: complete when every item accepted; incomplete when the
	// deadline passed, the budget is gone, or every open item is blocked.
	if c.Status == StatusCollecting {
		if allAccepted(c) {
			c.Status = StatusCompleted
		} else if deadlinePassed(c, now) || s.budgetGone(ctx, c) || allBlockedOrNoPath(c) {
			c.Status = StatusIncomplete
		}
	}
	if c.Status != StatusCollecting {
		c.Missing = summarize(c)
	}
	if err := s.store.UpdateCollection(ctx, c); err != nil {
		return Collection{}, err
	}
	return c, nil
}

// ordered returns one member's conversations oldest first (by ID order of
// insertion into the map is unspecified, so sort by CreatedAt then ID).
func ordered(byMember map[string]conversations.Conversation, memberID string) []conversations.Conversation {
	var out []conversations.Conversation
	for _, cv := range byMember {
		if cv.MemberID == memberID {
			out = append(out, cv)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// memberStopped reports whether any of the member's conversations for this
// request ended on their opt-out.
func memberStopped(byMember map[string]conversations.Conversation, memberID string) bool {
	for _, cv := range byMember {
		if cv.MemberID == memberID && cv.Status == conversations.StatusStopped {
			return true
		}
	}
	return false
}

// allAccepted reports whether every item holds an accepted answer.
func allAccepted(c Collection) bool {
	for _, it := range c.Items {
		if it.Status != ItemAccepted {
			return false
		}
	}
	return len(c.Items) > 0
}

// deadlinePassed reports whether the collection's hard end has gone by.
func deadlinePassed(c Collection, now time.Time) bool {
	return c.Deadline != nil && now.After(*c.Deadline)
}

// budgetGone reports whether the request can no longer fund gathering.
func (s *Service) budgetGone(ctx context.Context, c Collection) bool {
	r, err := s.reqs.RequestByID(ctx, c.RequestID)
	if err != nil {
		return false // the request record governs; unreadable means not gone
	}
	return r.Status == requests.StatusBudgetExhausted
}

// allBlockedOrNoPath reports whether every non-accepted item is blocked or
// has exhausted its re-ask budget — no path left to completion.
func allBlockedOrNoPath(c Collection) bool {
	if len(c.Items) == 0 {
		return false
	}
	for _, it := range c.Items {
		switch it.Status {
		case ItemAccepted:
			continue
		case ItemBlocked:
			continue
		case ItemCollecting:
			// A re-ask may still be in flight and come back good.
			if it.Reasks < MaxReasksPerItem {
				return false
			}
		}
	}
	return true
}

// summarize derives the missing-data summary of a non-collecting
// collection: every non-accepted item, with the honest reason.
func summarize(c Collection) []Missing {
	out := []Missing{}
	for _, it := range c.Items {
		if it.Status == ItemAccepted {
			continue
		}
		reason := it.BlockReason
		if reason == "" {
			reason = "no usable answer arrived"
			if len(it.Rounds) > 0 {
				reason = "no answer passed the quality checks: " + it.Rounds[len(it.Rounds)-1].Reason
			}
		}
		out = append(out, Missing{MemberName: it.MemberName, Question: it.Question, Reason: reason})
	}
	return out
}

// memberContact loads the member's contact point for a re-ask; a roster
// removal between rounds degrades to an empty contact, which the
// conversation layer refuses — surfaced, never silent.
func (s *Service) memberContact(ctx context.Context, orgID, memberID string) string {
	m, err := s.roster.MemberByID(ctx, memberID)
	if err != nil || m.OrgID != orgID {
		return ""
	}
	return m.Contact
}
