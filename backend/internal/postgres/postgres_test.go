package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/syaikhipin/entrustdn-final/backend/internal/config"
	"github.com/syaikhipin/entrustdn-final/backend/internal/postgres"
)

// ADR 0007: Postgres is the system of record; env config points at a host the
// user installs. These tests need a real Postgres and skip when none is
// reachable, so `go test ./...` stays green on any machine. The repo's
// developer workflow (compose / make dev) provides the instance.

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		url = os.Getenv("DATABASE_URL")
	}
	return url
}

func skipIfNoDatabase(t *testing.T) string {
	t.Helper()
	url := testDatabaseURL(t)
	if url == "" {
		t.Skip("TEST_DATABASE_URL / DATABASE_URL not set; Postgres-dependent tests skipped")
	}
	return url
}

func TestConnectAndPing(t *testing.T) {
	url := skipIfNoDatabase(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := postgres.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
}

func TestMigrateCreatesSchemaMigrationsTable(t *testing.T) {
	url := skipIfNoDatabase(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := postgres.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	var count int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count)
	if err != nil {
		t.Fatalf("schema_migrations table missing after Migrate(): %v", err)
	}
	if count < 1 {
		t.Errorf("schema_migrations has %d rows, want at least 1 (migrations applied)", count)
	}
}

// Config interplay: DATABASE_URL flows through Load unchanged.
func TestConfigPassesDatabaseURLThrough(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://cfgtest:cfgtest@localhost:5432/cfgtest")
	t.Setenv("AGENT_BASE_URL", "http://localhost:8001")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DatabaseURL != "postgres://cfgtest:cfgtest@localhost:5432/cfgtest" {
		t.Errorf("DatabaseURL = %q, want passthrough", cfg.DatabaseURL)
	}
}
