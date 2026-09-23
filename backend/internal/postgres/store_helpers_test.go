package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/syaikhipin/entrustdn-final/backend/internal/postgres"
)

// openCleanTestDB connects to the test database, runs migrations, and wipes
// the membership tables so each test starts from an empty schema. The
// compose volume is persistent, so tests must not assume fresh rows.
func openCleanTestDB(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := postgres.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(pool.Close)

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	_, err = pool.Exec(ctx, `
		TRUNCATE inference_charge_details, ledger_entries, ledger_movements,
		         pricing_rules, pseudonym_maps, sessions, tos_acceptances,
		         verification_tokens, tos_versions, taxonomy_terms,
		         accounts CASCADE`)
	if err != nil {
		t.Fatalf("failed to clean membership tables: %v", err)
	}
	return pool
}
