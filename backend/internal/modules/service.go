package modules

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Service is the registry front door: upload with validation, versioning,
// visibility (private by default, grants, system-wide promotion), and
// deprecation. Every mutation validates before it reaches the store, so a
// malformed or executable manifest can never be stored.
type Service struct {
	store Store
}

// NewService wires the registry over its store.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// Upload stores the first version of a Module, private to its author.
// The Module's identity (ModuleID) is minted here; version history grows
// from this record.
func (s *Service) Upload(ctx context.Context, authorID string, n NewVersion) (Module, error) {
	if authorID == "" {
		return Module{}, fmt.Errorf("modules: author is required")
	}
	if err := n.Validate(); err != nil {
		return Module{}, err
	}
	moduleID, err := NewID()
	if err != nil {
		return Module{}, err
	}
	versionID, err := NewID()
	if err != nil {
		return Module{}, err
	}
	m := &Module{
		ID:         versionID,
		ModuleID:   moduleID,
		Name:       strings.TrimSpace(n.Name),
		Kind:       n.Kind,
		AuthorID:   authorID,
		Version:    strings.TrimSpace(n.Version),
		Capability: strings.TrimSpace(n.Capability),
		Content:    n.Content,
		Config:     n.Config,
	}
	if err := s.store.CreateModule(ctx, m); err != nil {
		return Module{}, err
	}
	return *m, nil
}

// Publish adds a version to an existing Module. Only the author may add
// versions, and only to a Module they still control — an admin-promoted
// module's identity stays with its author.
func (s *Service) Publish(ctx context.Context, authorID string, moduleID string, n NewVersion) (Module, error) {
	latest, err := s.store.LatestModuleVersion(ctx, moduleID)
	if err != nil {
		return Module{}, err
	}
	if latest.AuthorID != authorID {
		return Module{}, ErrForbidden
	}
	// The manifest's name and kind are identity: a new version cannot
	// masquerade as a different kind or rename the module.
	n.Name = latest.Name
	n.Kind = latest.Kind
	if err := n.Validate(); err != nil {
		return Module{}, err
	}
	// Duplicate version strings would make history unreadable.
	versions, err := s.store.ModuleVersions(ctx, moduleID)
	if err != nil {
		return Module{}, err
	}
	for _, v := range versions {
		if v.Version == strings.TrimSpace(n.Version) {
			return Module{}, fmt.Errorf("%w: %q", ErrExists, n.Version)
		}
	}
	versionID, err := NewID()
	if err != nil {
		return Module{}, err
	}
	m := &Module{
		ID:         versionID,
		ModuleID:   moduleID,
		Name:       latest.Name,
		Kind:       latest.Kind,
		AuthorID:   latest.AuthorID,
		Version:    strings.TrimSpace(n.Version),
		Capability: strings.TrimSpace(n.Capability),
		Content:    n.Content,
		Config:     n.Config,
		// SystemWide deliberately does not carry over: promotion was the
		// admin's review of the previous content, and this content has not
		// been reviewed. A new version sends the module back to private
		// until the Platform Admin promotes it again.
	}
	if err := s.store.CreateModuleVersion(ctx, m); err != nil {
		return Module{}, err
	}
	return *m, nil
}

// Versions lists a Module's version history, oldest first. The caller must
// be able to see the Module (see canView).
func (s *Service) Versions(ctx context.Context, callerID string, moduleID string) ([]Module, error) {
	latest, err := s.store.LatestModuleVersion(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	if !s.canView(ctx, latest, callerID) {
		return nil, ErrForbidden
	}
	return s.store.ModuleVersions(ctx, moduleID)
}

// ByAuthor lists the caller's own Modules (latest version each), newest
// first. Private modules included — it is their registry view.
func (s *Service) ByAuthor(ctx context.Context, authorID string) ([]Module, error) {
	return s.store.ModulesByAuthor(ctx, authorID)
}

// SystemWide lists the system-wide Modules (latest version each), newest
// first — any signed-in account may browse what the platform offers.
func (s *Service) SystemWide(ctx context.Context) ([]Module, error) {
	return s.store.SystemWideModules(ctx)
}

// canView reports whether callerID may see a Module: its author, a granted
// account, or anyone at all once system-wide.
func (s *Service) canView(ctx context.Context, latest Module, callerID string) bool {
	if latest.SystemWide || latest.AuthorID == callerID {
		return true
	}
	granted, err := s.store.GrantExists(ctx, latest.ModuleID, callerID)
	if err != nil {
		return false
	}
	return granted
}

// requireVisible loads the Module and refuses callers who may not see it.
func (s *Service) requireVisible(ctx context.Context, callerID, moduleID string) (Module, error) {
	latest, err := s.store.LatestModuleVersion(ctx, moduleID)
	if err != nil {
		return Module{}, err
	}
	if !s.canView(ctx, latest, callerID) {
		return Module{}, ErrForbidden
	}
	return latest, nil
}

// Get loads one version of a Module after the visibility check — a
// stranger's private Module is ErrForbidden, which the API renders as 404.
// The version must belong to the named Module; a mismatched pair is
// ErrNotFound.
func (s *Service) Get(ctx context.Context, callerID, moduleID, versionID string) (Module, error) {
	m, err := s.store.ModuleByID(ctx, versionID)
	if err != nil {
		return Module{}, err
	}
	if m.ModuleID != moduleID {
		return Module{}, ErrNotFound
	}
	latest, err := s.store.LatestModuleVersion(ctx, m.ModuleID)
	if err != nil {
		return Module{}, err
	}
	if !s.canView(ctx, latest, callerID) {
		return Module{}, ErrForbidden
	}
	return m, nil
}

// Grant gives accountID read access to a private Module. Only the author
// grants; system-wide Modules need no grants.
func (s *Service) Grant(ctx context.Context, callerID, moduleID, accountID string) error {
	latest, err := s.store.LatestModuleVersion(ctx, moduleID)
	if err != nil {
		return err
	}
	if latest.AuthorID != callerID {
		return ErrForbidden
	}
	if latest.SystemWide {
		return fmt.Errorf("modules: a system-wide module is visible to everyone")
	}
	if accountID == callerID {
		return fmt.Errorf("modules: the author already sees their own module")
	}
	return s.store.AddGrant(ctx, Grant{ModuleID: moduleID, AccountID: accountID, GrantedAt: time.Now().UTC()})
}

// Revoke removes accountID's read access. Only the author revokes.
func (s *Service) Revoke(ctx context.Context, callerID, moduleID, accountID string) error {
	latest, err := s.store.LatestModuleVersion(ctx, moduleID)
	if err != nil {
		return err
	}
	if latest.AuthorID != callerID {
		return ErrForbidden
	}
	return s.store.RemoveGrant(ctx, moduleID, accountID)
}

// Grants lists a Module's access grants. Only the author inspects them.
func (s *Service) Grants(ctx context.Context, callerID, moduleID string) ([]Grant, error) {
	latest, err := s.store.LatestModuleVersion(ctx, moduleID)
	if err != nil {
		return nil, err
	}
	if latest.AuthorID != callerID {
		return nil, ErrForbidden
	}
	return s.store.GrantsForModule(ctx, moduleID)
}

// Promote makes a Module system-wide after Platform Admin review. The
// admin's judgement is recorded as the review: no separate state machine —
// promotion is reversible (demote) while the pilot is small. Returns the
// module's latest version as it now stands.
func (s *Service) Promote(ctx context.Context, isAdmin bool, moduleID string, systemWide bool) (Module, error) {
	if !isAdmin {
		return Module{}, ErrForbidden
	}
	if _, err := s.store.LatestModuleVersion(ctx, moduleID); err != nil {
		return Module{}, err
	}
	if err := s.store.SetSystemWide(ctx, moduleID, systemWide); err != nil {
		return Module{}, err
	}
	return s.store.LatestModuleVersion(ctx, moduleID)
}

// Deprecate flags one version retired. The author may deprecate any
// version of their Module; an admin may deprecate any version at all. The
// version must belong to the named Module; a mismatched pair is
// ErrNotFound.
func (s *Service) Deprecate(ctx context.Context, callerID string, isAdmin bool, moduleID, versionID string, deprecated bool) (Module, error) {
	m, err := s.Get(ctx, callerID, moduleID, versionID)
	if err != nil {
		// The author/admin gate: Get's visibility is necessary but not
		// sufficient — a non-admin stranger must not flag anyone's version,
		// even one they were granted read access to.
		if errors.Is(err, ErrForbidden) && isAdmin {
			m, err = s.store.ModuleByID(ctx, versionID)
			if err != nil {
				return Module{}, err
			}
			if m.ModuleID != moduleID {
				return Module{}, ErrNotFound
			}
		} else {
			return Module{}, err
		}
	}
	if m.AuthorID != callerID && !isAdmin {
		return Module{}, ErrForbidden
	}
	if err := s.store.SetVersionDeprecated(ctx, versionID, deprecated); err != nil {
		return Module{}, err
	}
	return s.store.ModuleByID(ctx, versionID)
}
