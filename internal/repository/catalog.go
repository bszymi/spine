package repository

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/yamlsafe"
)

// Kind is the catalog entry kind. Exactly one entry per workspace has
// kind=spine; all others are kind=code.
type Kind string

const (
	KindSpine Kind = "spine"
	KindCode  Kind = "code"
)

// PrimaryRepositoryID is the reserved ID of the workspace primary
// repository. The primary entry MUST use this ID and no other entry may.
const PrimaryRepositoryID = "spine"

// MaxIDLength caps the catalog ID length. Matches the ADR-013 contract.
const MaxIDLength = 64

// ID rules per ADR-013 / multi-repository-integration.md §2.1:
// lowercase alphanumeric with single internal hyphens. Consecutive,
// leading, or trailing hyphens are rejected.
var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// forbiddenFields are operational keys that must never appear in the
// governed catalog. They live in the runtime binding instead.
var forbiddenFields = map[string]struct{}{
	"url":         {},
	"clone_url":   {},
	"credentials": {},
	"token":       {},
	"secret_ref":  {},
	"local_path":  {},
	"path":        {},
	"status":      {},
}

// allowedFields lists the catalog keys recognised by the parser. Any
// other key is rejected so typos surface immediately.
var allowedFields = map[string]struct{}{
	"id":             {},
	"kind":           {},
	"name":           {},
	"default_branch": {},
	"role":           {},
	"description":    {},
}

// CatalogEntry is the identity-only record for one repository.
// Operational fields (clone URL, credentials, local path, status) live
// in the runtime binding, never here.
type CatalogEntry struct {
	ID            string `yaml:"id" json:"id"`
	Kind          Kind   `yaml:"kind" json:"kind"`
	Name          string `yaml:"name" json:"name"`
	DefaultBranch string `yaml:"default_branch" json:"default_branch"`
	Role          string `yaml:"role,omitempty" json:"role,omitempty"`
	Description   string `yaml:"description,omitempty" json:"description,omitempty"`
}

// PrimarySpec describes how the workspace primary repo is materialised
// outside the catalog. Operational fields for the primary live here
// (e.g. RepoPath) since the binding store has no row for the primary.
type PrimarySpec struct {
	// Name is the display name used when no catalog file exists. When
	// the catalog file is present, the primary entry's name is
	// authoritative.
	Name string

	// DefaultBranch is the authoritative branch used when no catalog
	// file exists. When the catalog file is present, the primary
	// entry's default_branch is authoritative.
	DefaultBranch string

	// LocalPath is the on-disk RepoPath for the primary repo. It is
	// always operator-supplied; the catalog never carries it.
	LocalPath string
}

// Catalog is the parsed and validated repository catalog for a
// workspace. Entries are keyed by ID; iteration via List is
// alphabetical with the primary entry pinned first.
type Catalog struct {
	primary CatalogEntry
	entries map[string]CatalogEntry
}

// ParseCatalog parses /.spine/repositories.yaml content. An empty or
// nil payload is treated as a single-repo workspace and synthesises a
// primary-only catalog from spec — backward compatible with v0.x.
//
// When data is non-empty, the file MUST contain exactly one kind=spine
// entry and at least one well-formed entry. All entries are validated
// against the ID regex, kind enum, required fields, allowed-field set,
// and the forbidden-fields list (operational keys must not appear
// here). Duplicate IDs are rejected.
func ParseCatalog(data []byte, spec PrimarySpec) (*Catalog, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return synthesizePrimaryOnly(spec), nil
	}

	root, err := yamlsafe.Decode(data)
	if err != nil {
		return nil, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("repository catalog: %v", err))
	}

	seq, err := topLevelSequence(root)
	if err != nil {
		return nil, err
	}

	entries := make([]CatalogEntry, 0, len(seq))
	for i, item := range seq {
		entry, err := decodeEntry(item, i)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}

	if err := validateEntries(entries); err != nil {
		return nil, err
	}

	cat := &Catalog{entries: make(map[string]CatalogEntry, len(entries))}
	for _, e := range entries {
		cat.entries[e.ID] = e
		if e.Kind == KindSpine {
			cat.primary = e
		}
	}
	return cat, nil
}

// Get returns the catalog entry for id and whether it exists.
func (c *Catalog) Get(id string) (CatalogEntry, bool) {
	if c == nil {
		return CatalogEntry{}, false
	}
	e, ok := c.entries[id]
	return e, ok
}

// Primary returns the primary (kind=spine) catalog entry.
func (c *Catalog) Primary() CatalogEntry {
	if c == nil {
		return CatalogEntry{}
	}
	return c.primary
}

// List returns all catalog entries sorted alphabetically by ID with
// the primary entry pinned first. The returned slice is freshly
// allocated; callers may mutate it.
func (c *Catalog) List() []CatalogEntry {
	if c == nil {
		return nil
	}
	ids := make([]string, 0, len(c.entries))
	for id := range c.entries {
		if id == PrimaryRepositoryID {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]CatalogEntry, 0, len(c.entries))
	out = append(out, c.primary)
	for _, id := range ids {
		out = append(out, c.entries[id])
	}
	return out
}

func synthesizePrimaryOnly(spec PrimarySpec) *Catalog {
	primary := CatalogEntry{
		ID:            PrimaryRepositoryID,
		Kind:          KindSpine,
		Name:          spec.Name,
		DefaultBranch: spec.DefaultBranch,
	}
	return &Catalog{
		primary: primary,
		entries: map[string]CatalogEntry{PrimaryRepositoryID: primary},
	}
}

func topLevelSequence(root *yaml.Node) ([]*yaml.Node, error) {
	if root == nil || len(root.Content) == 0 {
		return nil, domain.NewError(domain.ErrInvalidParams,
			"repository catalog must be a YAML sequence of entries")
	}
	doc := root.Content[0]
	if doc.Kind != yaml.SequenceNode {
		return nil, domain.NewError(domain.ErrInvalidParams,
			"repository catalog must be a YAML sequence of entries")
	}
	if len(doc.Content) == 0 {
		return nil, domain.NewError(domain.ErrInvalidParams,
			"repository catalog must contain at least one entry")
	}
	return doc.Content, nil
}

func decodeEntry(item *yaml.Node, index int) (CatalogEntry, error) {
	if item.Kind != yaml.MappingNode {
		return CatalogEntry{}, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("repository catalog entry [%d] must be a mapping", index))
	}
	if len(item.Content)%2 != 0 {
		return CatalogEntry{}, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("repository catalog entry [%d] is malformed", index))
	}

	for i := 0; i < len(item.Content); i += 2 {
		key := item.Content[i].Value
		if _, banned := forbiddenFields[key]; banned {
			return CatalogEntry{}, domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("repository catalog entry [%d]: operational field %q is forbidden in the governed catalog (it belongs in the runtime binding)", index, key))
		}
		if _, ok := allowedFields[key]; !ok {
			return CatalogEntry{}, domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("repository catalog entry [%d]: unknown field %q", index, key))
		}
	}

	var entry CatalogEntry
	if err := item.Decode(&entry); err != nil {
		return CatalogEntry{}, domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("repository catalog entry [%d]: %v", index, err))
	}
	return entry, nil
}

func validateEntries(entries []CatalogEntry) error {
	var primaryCount int
	seen := make(map[string]struct{}, len(entries))

	for i, e := range entries {
		if e.ID == "" {
			return domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("repository catalog entry [%d]: missing required field %q", i, "id"))
		}
		if len(e.ID) > MaxIDLength {
			return domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("repository catalog entry %q: id exceeds %d-character limit", e.ID, MaxIDLength))
		}
		if !idPattern.MatchString(e.ID) {
			return domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("repository catalog entry %q: id must match %s", e.ID, idPattern.String()))
		}
		if _, dup := seen[e.ID]; dup {
			return domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("repository catalog: duplicate id %q", e.ID))
		}
		seen[e.ID] = struct{}{}

		switch e.Kind {
		case KindSpine:
			primaryCount++
			if e.ID != PrimaryRepositoryID {
				return domain.NewError(domain.ErrInvalidParams,
					fmt.Sprintf("repository catalog entry %q: kind=%q is reserved for id %q", e.ID, KindSpine, PrimaryRepositoryID))
			}
		case KindCode:
			if e.ID == PrimaryRepositoryID {
				return domain.NewError(domain.ErrInvalidParams,
					fmt.Sprintf("repository catalog entry %q: id is reserved for the primary (kind=%q)", e.ID, KindSpine))
			}
		default:
			return domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("repository catalog entry %q: kind must be %q or %q (got %q)", e.ID, KindSpine, KindCode, e.Kind))
		}

		if e.Name == "" {
			return domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("repository catalog entry %q: missing required field %q", e.ID, "name"))
		}
		if e.DefaultBranch == "" {
			return domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("repository catalog entry %q: missing required field %q", e.ID, "default_branch"))
		}
	}

	switch primaryCount {
	case 0:
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("repository catalog must contain exactly one kind=%q entry; found 0", KindSpine))
	case 1:
		return nil
	default:
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("repository catalog must contain exactly one kind=%q entry; found %d", KindSpine, primaryCount))
	}
}
