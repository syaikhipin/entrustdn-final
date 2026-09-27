package memoryprov_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/memoryprov"
)

// Ticket 14: Memory Providers are the admin-configured external recall
// services (mem0, Hindsight, supermemory, Honcho — ADR 0003). Like the
// payment gateway they are platform configuration, never a Module. A
// provider record is a connection fact — name and endpoint — and nothing
// else: the shape has no place for credentials, and the agent connects
// over MCP with none.

func TestServiceCreateAcceptsAWellFormedProvider(t *testing.T) {
	svc := memoryprov.NewService(memoryprov.NewMemoryStore())
	p, err := svc.Create(t.Context(), "mem0-primary", "http://memory.local:8080/mcp")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.ID == "" {
		t.Errorf("provider ID is empty")
	}
	if p.Name != "mem0-primary" || p.Endpoint != "http://memory.local:8080/mcp" {
		t.Errorf("provider = %+v", p)
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: %+v", p)
	}
}

func TestServiceCreateRefusesUnusableProviders(t *testing.T) {
	tests := []struct {
		name     string
		provName string
		endpoint string
		wantErr  string
	}{
		{"blank name", "  ", "http://memory.local:8080/mcp", "name"},
		{"bad name shape", "Mem0 Primary!", "http://memory.local:8080/mcp", "name"},
		{"blank endpoint", "mem0", "   ", "endpoint"},
		{"endpoint without scheme", "mem0", "memory.local:8080/mcp", "http(s)"},
		{"endpoint with a non-http scheme", "mem0", "ftp://memory.local/pub", "http(s)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := memoryprov.NewService(memoryprov.NewMemoryStore())
			_, err := svc.Create(t.Context(), tt.provName, tt.endpoint)
			if err == nil {
				t.Fatalf("Create(%q, %q) succeeded, want a refusal", tt.provName, tt.endpoint)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestServiceCreateRefusesDuplicateNames(t *testing.T) {
	svc := memoryprov.NewService(memoryprov.NewMemoryStore())
	if _, err := svc.Create(t.Context(), "mem0", "http://one.local/mcp"); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := svc.Create(t.Context(), "mem0", "http://two.local/mcp"); !errors.Is(err, memoryprov.ErrExists) {
		t.Errorf("second Create err = %v, want ErrExists", err)
	}
}

func TestServiceListAndDeleteRoundTrip(t *testing.T) {
	svc := memoryprov.NewService(memoryprov.NewMemoryStore())

	// An empty registry lists as an empty slice, not nil.
	got, err := svc.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("empty List = %#v, want an empty non-nil slice", got)
	}

	first, err := svc.Create(t.Context(), "mem0", "http://one.local/mcp")
	if err != nil {
		t.Fatalf("Create mem0: %v", err)
	}
	if _, err := svc.Create(t.Context(), "hindsight", "http://two.local/mcp"); err != nil {
		t.Fatalf("Create hindsight: %v", err)
	}

	got, err = svc.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List = %d providers, want 2", len(got))
	}
	if got[0].Name != "hindsight" || got[1].Name != "mem0" {
		t.Errorf("List order = [%s, %s], want newest first", got[0].Name, got[1].Name)
	}

	if err := svc.Delete(t.Context(), first.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ = svc.List(t.Context())
	if len(got) != 1 || got[0].Name != "hindsight" {
		t.Errorf("after Delete List = %+v, want only hindsight", got)
	}

	// Deleting an unknown provider is a NotFound, not a silent success.
	if err := svc.Delete(t.Context(), first.ID); !errors.Is(err, memoryprov.ErrNotFound) {
		t.Errorf("re-Delete err = %v, want ErrNotFound", err)
	}
}
