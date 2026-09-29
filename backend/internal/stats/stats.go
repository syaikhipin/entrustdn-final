// Package stats aggregates the Platform Admin's dashboard numbers (ticket
// 16) from the platform's systems of record: requests, collections, the
// Ledger, conversations, and the module registry. Every figure is derived
// from real platform data — nothing here invents or estimates a number.
//
// The reads go through one Source seam so the API layer stays at Seam 1
// (in-memory doubles in tests) while Postgres computes the aggregations in
// SQL for the system of record.
package stats

import (
	"context"
	"time"
)

// DayCount is one day's worth of a quantity over time — a bucket in a
// "requests over time" series. Day is the UTC date, formatted YYYY-MM-DD.
type DayCount struct {
	Day   string `json:"day"`
	Count int64  `json:"count"`
}

// StatusCount is one status bucket of a record set — e.g. clarifying vs
// clarified vs budget_exhausted requests, or completed vs incomplete
// collections.
type StatusCount struct {
	Status string `json:"status"`
	Count  int64  `json:"count"`
}

// KindFlow is one Ledger movement kind's totals: how much moved out of
// accounts and how much moved in. For top-ups (credits issued) the in side
// dominates; for charges the out side does. Summing signed entries per kind
// gives both directions honestly from the double-entry record.
type KindFlow struct {
	Kind      string `json:"kind"`
	InMicros  int64  `json:"in_micros"`
	OutMicros int64  `json:"out_micros"`
	Movements int64  `json:"movements"`
}

// ChannelActivity is one Channel's conversation load — messages per channel
// is the ticket's phrasing; the durable proxy is turns (thread entries) per
// channel, derived from conversations' Contact prefix.
type ChannelActivity struct {
	Channel       string `json:"channel"`
	Conversations int64  `json:"conversations"`
	Turns         int64  `json:"turns"`
}

// ModuleCounts is the module usage line: system-wide and private counts,
// per kind, latest version each.
type ModuleCounts struct {
	AgentSkill      int64 `json:"agent_skill"`
	ProcessTemplate int64 `json:"process_template"`
	Connector       int64 `json:"connector"`
	SystemWide      int64 `json:"system_wide"`
	Private         int64 `json:"private"`
}

// Snapshot is the whole dashboard document: one request to the Source, one
// JSON body to the admin page.
type Snapshot struct {
	RequestsOverTime   []DayCount        `json:"requests_over_time"`
	RequestStatuses    []StatusCount     `json:"request_statuses"`
	CollectionStatuses []StatusCount     `json:"collection_statuses"`
	CreditsFlow        []KindFlow        `json:"credits_flow"`
	Channels           []ChannelActivity `json:"channels"`
	Modules            ModuleCounts      `json:"modules"`
}

// Source is the read seam the dashboard aggregates through. One call
// returns the whole snapshot; the postgres implementation computes it in a
// handful of grouped queries. The window bounds the over-time series (the
// statuses, flows, channels, and module counts are whole-history).
type Source interface {
	Snapshot(ctx context.Context, window time.Duration) (Snapshot, error)
}
