// Package modules implements the Module registry (ticket 08): versioned
// uploadable bundles — manifest plus markdown and/or configuration — of
// exactly one kind (Agent Skill, Process Template, or Connector). Modules
// are private to the author by default; authors grant and revoke access;
// Platform Admins review content and promote Modules system-wide. A Module
// is data, never code: the manifest documents an A2A capability
// description that is shown, not run, and executable content is refused at
// the door.
package modules

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kind is what a Module is for. Exactly three kinds exist; the registry
// refuses anything else.
type Kind string

const (
	// KindAgentSkill: instructions the agent loads to perform a
	// specialized task.
	KindAgentSkill Kind = "agent_skill"
	// KindProcessTemplate: a reusable Request workflow — questions,
	// follow-up rules, quality triggers.
	KindProcessTemplate Kind = "process_template"
	// KindConnector: configuration for an MCP or API link to an external
	// data source.
	KindConnector Kind = "connector"
)

// Kinds is the closed set, in listing order.
var Kinds = []Kind{KindAgentSkill, KindProcessTemplate, KindConnector}

// ValidKind reports whether k is one of the three kinds.
func ValidKind(k Kind) bool {
	for _, known := range Kinds {
		if k == known {
			return true
		}
	}
	return false
}

// maxPartBytes caps each of the content and config parts. Modules are
// documents, not binaries; a quarter mebibyte is generous for markdown or
// connector configuration.
const maxPartBytes = 256 << 10

// ErrNotFound is returned by lookups that find nothing.
var ErrNotFound = errors.New("modules: not found")

// ErrForbidden is returned when the caller may not touch the Module. The
// API layer maps it to 403 or 404.
var ErrForbidden = errors.New("modules: not allowed")

// ErrExists is returned when the module already holds that version.
var ErrExists = errors.New("modules: version already exists")

// namePattern: lowercase slugs only — IDs in URLs, not marketing copy.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// versionPattern: a version string with no whitespace or path tricks
// (semver-shaped is the convention, not the law).
var versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.\-+]*$`)

// NewVersion is what an author submits when uploading one version of a
// Module: the manifest (name, kind, version, capability description) plus
// the body — markdown content and/or configuration.
type NewVersion struct {
	Name       string
	Kind       Kind
	Version    string
	Capability string
	Content    string
	Config     string
}

// Validate refuses manifests that must never be stored: wrong kind, missing
// fields, or executable content — a Module is shown, not run.
func (n NewVersion) Validate() error {
	if !ValidKind(n.Kind) {
		return fmt.Errorf("modules: kind must be one of agent_skill, process_template, connector")
	}
	if !namePattern.MatchString(n.Name) {
		return fmt.Errorf("modules: name must be a lowercase slug (letters, digits, dashes)")
	}
	if !versionPattern.MatchString(n.Version) {
		return fmt.Errorf("modules: version must be a plain version string (e.g. 1.0.0)")
	}
	if strings.TrimSpace(n.Capability) == "" {
		return fmt.Errorf("modules: capability description is required")
	}
	if strings.TrimSpace(n.Content) == "" && strings.TrimSpace(n.Config) == "" {
		return fmt.Errorf("modules: content or config is required")
	}
	if len(n.Content) > maxPartBytes {
		return fmt.Errorf("modules: content exceeds the %d KiB limit", maxPartBytes>>10)
	}
	if len(n.Config) > maxPartBytes {
		return fmt.Errorf("modules: config exceeds the %d KiB limit", maxPartBytes>>10)
	}
	if hasExecutableContent(n.Content) || hasExecutableContent(n.Config) {
		return fmt.Errorf("modules: executable content is not accepted — modules are documents, not programs")
	}
	return nil
}

// hasExecutableContent reports whether s carries the tells of a program:
// a shebang line anywhere, a NUL byte, or a script tag. Modules are
// markdown and/or configuration — a document to be shown — so anything
// that reads as code to execute is refused outright.
func hasExecutableContent(s string) bool {
	if strings.ContainsRune(s, '\x00') {
		return true
	}
	lower := strings.ToLower(s)
	for _, tell := range []string{"#!", "<script", "</script", "javascript:"} {
		if strings.Contains(lower, tell) {
			return true
		}
	}
	return false
}

// Module is one stored version of a Module. The module's identity fields
// (ModuleID, Name, Kind, AuthorID, SystemWide) are denormalized onto every
// version row — the registry reads whole records, one row per version, so
// version history is just "every row with this ModuleID, oldest first".
// ID is the version's own ID.
type Module struct {
	ID         string // the version record's ID
	ModuleID   string // the Module's stable identity across versions
	Name       string
	Kind       Kind
	AuthorID   string
	Version    string
	Capability string // the A2A capability description — shown, not run
	Content    string
	Config     string
	// SystemWide marks a Module the Platform Admin promoted after review.
	// Module-level: every version of a promoted module reports true.
	SystemWide bool
	// Deprecated marks this version retired by author or admin: it stays
	// in the history and in listings, flagged.
	Deprecated bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// NewID mints an ID: 16 crypto/rand bytes, hex. A rand failure is a
// process-fatal error, matching the other registries.
func NewID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("modules: mint id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Grant is one access grant: the Module, the grantee, and when it was made.
type Grant struct {
	ModuleID  string
	AccountID string
	GrantedAt time.Time
}

// Store is the persistence seam for Modules. The postgres package
// implements it for the system of record; MemoryStore backs tests.
type Store interface {
	// CreateModule stores the first version of a Module, filling
	// timestamps when zero.
	CreateModule(ctx context.Context, m *Module) error
	// CreateModuleVersion stores a later version of an existing Module.
	CreateModuleVersion(ctx context.Context, m *Module) error
	// ModuleByID loads one version record.
	ModuleByID(ctx context.Context, id string) (Module, error)
	// LatestModuleVersion loads the newest version of a Module (by
	// CreatedAt, then ID).
	LatestModuleVersion(ctx context.Context, moduleID string) (Module, error)
	// ModuleVersions lists every version of a Module, oldest first.
	ModuleVersions(ctx context.Context, moduleID string) ([]Module, error)
	// ModulesByAuthor lists the latest version of each of the author's
	// Modules, newest first.
	ModulesByAuthor(ctx context.Context, authorID string) ([]Module, error)
	// SystemWideModules lists the latest version of every system-wide
	// Module, newest first.
	SystemWideModules(ctx context.Context) ([]Module, error)
	// SetSystemWide flips a Module's promotion flag.
	SetSystemWide(ctx context.Context, moduleID string, systemWide bool) error
	// SetVersionDeprecated flips one version's deprecation flag.
	SetVersionDeprecated(ctx context.Context, id string, deprecated bool) error

	// Grants.
	AddGrant(ctx context.Context, g Grant) error
	RemoveGrant(ctx context.Context, moduleID, accountID string) error
	GrantExists(ctx context.Context, moduleID, accountID string) (bool, error)
	GrantsForModule(ctx context.Context, moduleID string) ([]Grant, error)
}

// Compile-time check that MemoryStore satisfies Store.
var _ Store = (*MemoryStore)(nil)

// MemoryStore is the in-memory Store double used by tests.
type MemoryStore struct {
	mu       sync.Mutex
	byID     map[string]Module    // version ID → record
	modules  map[string][]string  // ModuleID → version IDs, insertion order
	grants   map[string][]Grant   // ModuleID → grants, insertion order
	moduleTS map[string]time.Time // ModuleID → created_at
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		byID:     map[string]Module{},
		modules:  map[string][]string{},
		grants:   map[string][]Grant{},
		moduleTS: map[string]time.Time{},
	}
}

func nowUTC() time.Time { return time.Now().UTC() }

func (m *MemoryStore) CreateModule(_ context.Context, mod *Module) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := nowUTC()
	if mod.CreatedAt.IsZero() {
		mod.CreatedAt = now
	}
	if mod.UpdatedAt.IsZero() {
		mod.UpdatedAt = now
	}
	m.moduleTS[mod.ModuleID] = mod.CreatedAt
	m.modules[mod.ModuleID] = []string{mod.ID}
	m.byID[mod.ID] = *mod
	return nil
}

func (m *MemoryStore) CreateModuleVersion(_ context.Context, mod *Module) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.modules[mod.ModuleID]; !ok {
		return ErrNotFound
	}
	for _, vid := range m.modules[mod.ModuleID] {
		if m.byID[vid].Version == mod.Version {
			return ErrExists
		}
	}
	now := nowUTC()
	if mod.CreatedAt.IsZero() {
		mod.CreatedAt = now
	}
	if mod.UpdatedAt.IsZero() {
		mod.UpdatedAt = now
	}
	m.modules[mod.ModuleID] = append(m.modules[mod.ModuleID], mod.ID)
	m.byID[mod.ID] = *mod
	return nil
}

func (m *MemoryStore) ModuleByID(_ context.Context, id string) (Module, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mod, ok := m.byID[id]
	if !ok {
		return Module{}, ErrNotFound
	}
	return mod, nil
}

func (m *MemoryStore) LatestModuleVersion(_ context.Context, moduleID string) (Module, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids, ok := m.modules[moduleID]
	if !ok {
		return Module{}, ErrNotFound
	}
	latest := m.byID[ids[0]]
	for _, vid := range ids[1:] {
		if v := m.byID[vid]; v.CreatedAt.After(latest.CreatedAt) ||
			(v.CreatedAt.Equal(latest.CreatedAt) && v.ID > latest.ID) {
			latest = v
		}
	}
	return latest, nil
}

func (m *MemoryStore) ModuleVersions(_ context.Context, moduleID string) ([]Module, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids, ok := m.modules[moduleID]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]Module, 0, len(ids))
	for _, vid := range ids {
		out = append(out, m.byID[vid])
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// latestOf returns the newest version record among ids (caller holds mu).
func (m *MemoryStore) latestOf(ids []string) Module {
	latest := m.byID[ids[0]]
	for _, vid := range ids[1:] {
		if v := m.byID[vid]; v.CreatedAt.After(latest.CreatedAt) ||
			(v.CreatedAt.Equal(latest.CreatedAt) && v.ID > latest.ID) {
			latest = v
		}
	}
	return latest
}

func (m *MemoryStore) ModulesByAuthor(_ context.Context, authorID string) ([]Module, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Module
	for _, ids := range m.modules {
		if m.byID[ids[0]].AuthorID != authorID {
			continue
		}
		out = append(out, m.latestOf(ids))
	}
	sortModules(out)
	return out, nil
}

func (m *MemoryStore) SystemWideModules(_ context.Context) ([]Module, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Module
	for _, ids := range m.modules {
		if latest := m.latestOf(ids); latest.SystemWide {
			out = append(out, latest)
		}
	}
	sortModules(out)
	return out, nil
}

func (m *MemoryStore) SetSystemWide(_ context.Context, moduleID string, systemWide bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids, ok := m.modules[moduleID]
	if !ok {
		return ErrNotFound
	}
	for _, vid := range ids {
		v := m.byID[vid]
		v.SystemWide = systemWide
		v.UpdatedAt = nowUTC()
		m.byID[vid] = v
	}
	return nil
}

func (m *MemoryStore) SetVersionDeprecated(_ context.Context, id string, deprecated bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.byID[id]
	if !ok {
		return ErrNotFound
	}
	v.Deprecated = deprecated
	v.UpdatedAt = nowUTC()
	m.byID[id] = v
	return nil
}

func (m *MemoryStore) AddGrant(_ context.Context, g Grant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.modules[g.ModuleID]; !ok {
		return ErrNotFound
	}
	for _, existing := range m.grants[g.ModuleID] {
		if existing.AccountID == g.AccountID {
			return ErrExists
		}
	}
	if g.GrantedAt.IsZero() {
		g.GrantedAt = nowUTC()
	}
	m.grants[g.ModuleID] = append(m.grants[g.ModuleID], g)
	return nil
}

func (m *MemoryStore) RemoveGrant(_ context.Context, moduleID, accountID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.grants[moduleID]
	for i, existing := range list {
		if existing.AccountID == accountID {
			m.grants[moduleID] = append(list[:i:i], list[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (m *MemoryStore) GrantExists(_ context.Context, moduleID, accountID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.grants[moduleID] {
		if existing.AccountID == accountID {
			return true, nil
		}
	}
	return false, nil
}

func (m *MemoryStore) GrantsForModule(_ context.Context, moduleID string) ([]Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.modules[moduleID]; !ok {
		return nil, ErrNotFound
	}
	return append([]Grant(nil), m.grants[moduleID]...), nil
}

// sortModules orders records newest first, ModuleID as the tiebreak.
func sortModules(a []Module) {
	sort.Slice(a, func(i, j int) bool {
		if !a[i].CreatedAt.Equal(a[j].CreatedAt) {
			return a[i].CreatedAt.After(a[j].CreatedAt)
		}
		return a[i].ModuleID < a[j].ModuleID
	})
}
