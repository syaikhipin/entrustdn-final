package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/stats"
)

// The Postgres implementation of stats.Source (ticket 16): every dashboard
// figure is one grouped query over the system of record — no cached
// counters, no estimates. Module counts read the latest version per module
// (DISTINCT ON), credits flow sums signed entries per movement kind, and
// channel activity splits conversations on the roster contact's
// channel-qualified prefix (the same vocabulary roster.ValidateContact
// enforces).

// StatsSource computes the admin dashboard snapshot over the shared pool.
type StatsSource struct {
	pool *pgxpool.Pool
}

// NewStatsSource returns a stats source backed by the given pool.
func NewStatsSource(pool *pgxpool.Pool) *StatsSource { return &StatsSource{pool: pool} }

// Compile-time check: the source satisfies the stats seam.
var _ stats.Source = (*StatsSource)(nil)

// Snapshot assembles the whole dashboard document.
func (s *StatsSource) Snapshot(ctx context.Context, window time.Duration) (stats.Snapshot, error) {
	since := time.Now().UTC().Add(-window)

	overTime, err := s.requestsOverTime(ctx, since)
	if err != nil {
		return stats.Snapshot{}, err
	}
	reqStatuses, err := s.requestStatuses(ctx)
	if err != nil {
		return stats.Snapshot{}, err
	}
	colStatuses, err := s.collectionStatuses(ctx)
	if err != nil {
		return stats.Snapshot{}, err
	}
	flow, err := s.creditsFlow(ctx)
	if err != nil {
		return stats.Snapshot{}, err
	}
	channels, err := s.channelActivity(ctx)
	if err != nil {
		return stats.Snapshot{}, err
	}
	mods, err := s.moduleCounts(ctx)
	if err != nil {
		return stats.Snapshot{}, err
	}

	return stats.Snapshot{
		RequestsOverTime:   overTime,
		RequestStatuses:    reqStatuses,
		CollectionStatuses: colStatuses,
		CreditsFlow:        flow,
		Channels:           channels,
		Modules:            mods,
	}, nil
}

func (s *StatsSource) requestsOverTime(ctx context.Context, since time.Time) ([]stats.DayCount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day, count(*)
		FROM data_requests
		WHERE created_at >= $1
		GROUP BY day ORDER BY day`, since)
	if err != nil {
		return nil, fmt.Errorf("stats: requests over time: %w", err)
	}
	defer rows.Close()
	out := []stats.DayCount{}
	for rows.Next() {
		var d stats.DayCount
		if err := rows.Scan(&d.Day, &d.Count); err != nil {
			return nil, fmt.Errorf("stats: scan request day: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *StatsSource) requestStatuses(ctx context.Context) ([]stats.StatusCount, error) {
	return s.statusCounts(ctx, `SELECT status, count(*) FROM data_requests GROUP BY status ORDER BY status`, "request statuses")
}

func (s *StatsSource) collectionStatuses(ctx context.Context) ([]stats.StatusCount, error) {
	return s.statusCounts(ctx, `SELECT status, count(*) FROM data_collections GROUP BY status ORDER BY status`, "collection statuses")
}

func (s *StatsSource) statusCounts(ctx context.Context, query, label string) ([]stats.StatusCount, error) {
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("stats: %s: %w", label, err)
	}
	defer rows.Close()
	out := []stats.StatusCount{}
	for rows.Next() {
		var sc stats.StatusCount
		if err := rows.Scan(&sc.Status, &sc.Count); err != nil {
			return nil, fmt.Errorf("stats: scan %s: %w", label, err)
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *StatsSource) creditsFlow(ctx context.Context) ([]stats.KindFlow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.kind,
		       COALESCE(sum(e.amount_micros) FILTER (WHERE e.amount_micros > 0), 0),
		       COALESCE(-sum(e.amount_micros) FILTER (WHERE e.amount_micros < 0), 0),
		       count(DISTINCT m.id)
		FROM ledger_movements m
		JOIN ledger_entries e ON e.movement_id = m.id
		GROUP BY m.kind ORDER BY m.kind`)
	if err != nil {
		return nil, fmt.Errorf("stats: credits flow: %w", err)
	}
	defer rows.Close()
	out := []stats.KindFlow{}
	for rows.Next() {
		var f stats.KindFlow
		if err := rows.Scan(&f.Kind, &f.InMicros, &f.OutMicros, &f.Movements); err != nil {
			return nil, fmt.Errorf("stats: scan credits flow: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *StatsSource) channelActivity(ctx context.Context) ([]stats.ChannelActivity, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT split_part(contact, ':', 1) AS channel,
		       count(*) AS conversations,
		       COALESCE(sum(jsonb_array_length(thread)), 0) AS turns
		FROM member_conversations
		GROUP BY channel ORDER BY channel`)
	if err != nil {
		return nil, fmt.Errorf("stats: channel activity: %w", err)
	}
	defer rows.Close()
	out := []stats.ChannelActivity{}
	for rows.Next() {
		var c stats.ChannelActivity
		if err := rows.Scan(&c.Channel, &c.Conversations, &c.Turns); err != nil {
			return nil, fmt.Errorf("stats: scan channel activity: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *StatsSource) moduleCounts(ctx context.Context) (stats.ModuleCounts, error) {
	// Latest version per module (DISTINCT ON, newest first, id breaks
	// created_at ties the same way the registry's listings do), then count
	// by kind and visibility.
	rows, err := s.pool.Query(ctx, `
		SELECT kind, system_wide, count(*)
		FROM (
			SELECT DISTINCT ON (module_id) module_id, kind, system_wide
			FROM modules
			ORDER BY module_id, created_at DESC, id DESC
		) latest
		GROUP BY kind, system_wide ORDER BY kind, system_wide`)
	if err != nil {
		return stats.ModuleCounts{}, fmt.Errorf("stats: module counts: %w", err)
	}
	defer rows.Close()
	var m stats.ModuleCounts
	for rows.Next() {
		var kind string
		var systemWide bool
		var n int64
		if err := rows.Scan(&kind, &systemWide, &n); err != nil {
			return stats.ModuleCounts{}, fmt.Errorf("stats: scan module counts: %w", err)
		}
		switch kind {
		case "agent_skill":
			m.AgentSkill += n
		case "process_template":
			m.ProcessTemplate += n
		case "connector":
			m.Connector += n
		}
		if systemWide {
			m.SystemWide += n
		} else {
			m.Private += n
		}
	}
	return m, rows.Err()
}
