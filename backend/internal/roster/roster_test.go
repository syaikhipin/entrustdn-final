package roster_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/roster"
)

// The Member roster (ticket 11): a Farmer Organization maintains contact
// points for its Farmer Members — no Member accounts. A Member is one
// roster entry: a name and a channel-qualified contact point
// ('whatsapp:+353860000001') the Agent reaches them on. Members never log
// in; the resumable link is their capability.

func TestOrgAddsAndListsMembers(t *testing.T) {
	svc := roster.NewService(roster.NewMemoryStore())

	m, err := svc.Add(t.Context(), "org-1", roster.NewMember{
		DisplayName: "Siobhán",
		Contact:     "whatsapp:+353860000001",
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if m.ID == "" || m.OrgID != "org-1" {
		t.Errorf("member = %+v, want an ID and the owning org", m)
	}

	// A second member on another channel — the roster is channel-mixed.
	if _, err := svc.Add(t.Context(), "org-1", roster.NewMember{
		DisplayName: "Pádraig", Contact: "email:padraig@farm.ie",
	}); err != nil {
		t.Fatalf("Add(email member) error = %v", err)
	}

	list, err := svc.List(t.Context(), "org-1")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 2 {
		t.Errorf("List() = %d members, want 2", len(list))
	}
}

func TestRosterRefusesUnusableMembers(t *testing.T) {
	tests := []struct {
		name    string
		member  roster.NewMember
		wantErr string
	}{
		{
			name: "missing name", member: roster.NewMember{Contact: "whatsapp:+353860000001"},
			wantErr: "name",
		},
		{
			name: "bare address without channel", member: roster.NewMember{DisplayName: "S", Contact: "+353860000001"},
			wantErr: "channel:address",
		},
		{
			name: "unknown channel", member: roster.NewMember{DisplayName: "S", Contact: "smoke:signals"},
			wantErr: "unknown channel",
		},
		{
			name: "empty address", member: roster.NewMember{DisplayName: "S", Contact: "telegram:"},
			wantErr: "channel:address",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := roster.NewService(roster.NewMemoryStore())
			_, err := svc.Add(t.Context(), "org-1", tt.member)
			if err == nil || !contains(err.Error(), tt.wantErr) {
				t.Errorf("Add() error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestRosterIsScopedToTheOwningOrg(t *testing.T) {
	svc := roster.NewService(roster.NewMemoryStore())
	m, err := svc.Add(t.Context(), "org-1", roster.NewMember{DisplayName: "S", Contact: "telegram:42"})
	if err != nil {
		t.Fatal(err)
	}

	// Another org cannot see or load the member: entitlement failure.
	if _, err := svc.ByID(t.Context(), m.ID, "org-2"); !errors.Is(err, roster.ErrForbidden) {
		t.Errorf("ByID() error = %v, want ErrForbidden", err)
	}
	if list, _ := svc.List(t.Context(), "org-2"); len(list) != 0 {
		t.Errorf("List() leaked %d members across orgs", len(list))
	}
}

func TestRosterUpdatesContactPoint(t *testing.T) {
	svc := roster.NewService(roster.NewMemoryStore())
	m, _ := svc.Add(t.Context(), "org-1", roster.NewMember{DisplayName: "S", Contact: "whatsapp:+353860000001"})

	updated, err := svc.Update(t.Context(), m.ID, "org-1", roster.NewMember{
		DisplayName: "Siobhán Ní Chonchúir", Contact: "telegram:99112233",
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Contact != "telegram:99112233" {
		t.Errorf("Contact = %q, want the new contact point", updated.Contact)
	}

	got, _ := svc.ByID(t.Context(), m.ID, "org-1")
	if got.Contact != "telegram:99112233" {
		t.Errorf("stored Contact = %q, want the update to persist", got.Contact)
	}
}

func TestRosterRemovesMembers(t *testing.T) {
	svc := roster.NewService(roster.NewMemoryStore())
	m, _ := svc.Add(t.Context(), "org-1", roster.NewMember{DisplayName: "S", Contact: "email:s@farm.ie"})

	if err := svc.Remove(t.Context(), m.ID, "org-1"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := svc.ByID(t.Context(), m.ID, "org-1"); !errors.Is(err, roster.ErrNotFound) {
		t.Errorf("ByID() after Remove = %v, want ErrNotFound", err)
	}
}

func contains(hay, needle string) bool { return strings.Contains(hay, needle) }
