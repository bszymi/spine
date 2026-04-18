package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/bszymi/spine/internal/yamlsafe"
	"gopkg.in/yaml.v3"
)

// Parse reads a branch-protection config from r. The input is bounded by
// yamlsafe so oversized or explosion-prone files are rejected before we
// materialise them. Parse is strict: unknown top-level keys, unknown fields
// on a rule, unknown RuleKind values, duplicate branch entries, and empty
// required fields all fail here rather than silently reducing to a looser
// ruleset.
func Parse(r io.Reader) (*Config, error) {
	if r == nil {
		return nil, fmt.Errorf("parse branch-protection config: nil reader")
	}
	data, err := io.ReadAll(io.LimitReader(r, yamlsafe.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("parse branch-protection config: %w", err)
	}

	// yamlsafe.Decode bounds size, depth, node count, and alias count.
	// Catching DoS-shaped input before we hand bytes to yaml.v3's strict
	// decoder keeps the two concerns layered: yamlsafe for size/safety,
	// yaml.Decoder.KnownFields for schema strictness.
	if _, err := yamlsafe.Decode(data); err != nil {
		return nil, fmt.Errorf("parse branch-protection config: %w", err)
	}

	var raw rawConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parse branch-protection config: file is empty")
		}
		return nil, fmt.Errorf("parse branch-protection config: %w", err)
	}
	// Reject trailing documents. A strict parser does not silently drop
	// content following a `---` document separator.
	var trailing yaml.Node
	if err := dec.Decode(&trailing); err == nil {
		return nil, fmt.Errorf("parse branch-protection config: unexpected second YAML document (only one document per file)")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse branch-protection config: trailing content: %w", err)
	}

	if raw.Version == 0 {
		return nil, fmt.Errorf("parse branch-protection config: version: required field missing")
	}
	if raw.Version != SupportedVersion {
		return nil, fmt.Errorf("parse branch-protection config: version: unsupported version %d (this parser supports %d)", raw.Version, SupportedVersion)
	}
	// Distinguish missing/null `rules` from an explicit empty list. A
	// malformed file that projected to zero rules would silently disable
	// protection for the whole workspace; requiring an explicit `rules: []`
	// makes "no protections" an intentional act.
	if raw.Rules == nil {
		return nil, fmt.Errorf("parse branch-protection config: rules: required field missing (use `rules: []` if intentionally empty)")
	}

	cfg := &Config{Version: raw.Version, Rules: make([]Rule, 0, len(*raw.Rules))}

	seen := make(map[string]struct{}, len(*raw.Rules))
	for i, rr := range *raw.Rules {
		if rr.Branch == "" {
			return nil, fmt.Errorf("parse branch-protection config: rules[%d].branch: required field missing", i)
		}
		// `**` is not in path.Match semantics; the two adjacent `*` parse as
		// a single glob that does not cross `/`. Accepting it silently would
		// leave nested refs unprotected despite the user's intent.
		if strings.Contains(rr.Branch, "**") {
			return nil, fmt.Errorf("parse branch-protection config: rules[%d].branch: recursive %q pattern is not supported in v1 (use explicit segments)", i, "**")
		}
		// Reject characters Git itself disallows in ref names (per
		// git-check-ref-format). A pattern containing one of these can
		// never match a real branch, so accepting it would install a
		// silent no-op rule. `^` also catches the "regex anchor" mistake
		// (`^release/.*$` looks like regex but path.Match takes it
		// literally). Characters Git allows but that are regex-adjacent
		// (`$`, `(`, `)`, `{`, `}`, `|`, `!`) are accepted as literals —
		// some teams do use them in real branch names, and rejecting them
		// would leave those branches unprotectable.
		if idx := strings.IndexAny(rr.Branch, "^~: "); idx >= 0 {
			return nil, fmt.Errorf("parse branch-protection config: rules[%d].branch: character %q in %q is not allowed in Git ref names (see git-check-ref-format)", i, rr.Branch[idx:idx+1], rr.Branch)
		}
		// Patterns must be short branch names, not fully-qualified refs.
		// A pattern like `refs/tags/*` would either match nothing (if the
		// caller passes short names) or pull tag refs into scope (if the
		// caller forgets to strip). Tags are explicitly out of scope
		// (ADR-009 §6); non-branch ref namespaces have no place here at
		// all. Rejecting the prefix keeps the config surface unambiguous.
		if strings.HasPrefix(rr.Branch, "refs/") {
			return nil, fmt.Errorf("parse branch-protection config: rules[%d].branch: pattern %q must be a short branch name, not a fully-qualified ref (tag refs and other ref namespaces are out of scope in v1)", i, rr.Branch)
		}
		if _, err := path.Match(rr.Branch, ""); err != nil {
			return nil, fmt.Errorf("parse branch-protection config: rules[%d].branch: invalid pattern %q: %v", i, rr.Branch, err)
		}
		if _, dup := seen[rr.Branch]; dup {
			return nil, fmt.Errorf("parse branch-protection config: rules[%d].branch: duplicate entry %q (combine protections into a single rule)", i, rr.Branch)
		}
		seen[rr.Branch] = struct{}{}

		if len(rr.Protections) == 0 {
			return nil, fmt.Errorf("parse branch-protection config: rules[%d].protections: must be non-empty", i)
		}

		kinds := make([]RuleKind, 0, len(rr.Protections))
		kindSeen := make(map[RuleKind]struct{}, len(rr.Protections))
		for j, raw := range rr.Protections {
			k := RuleKind(raw)
			if !k.IsKnown() {
				return nil, fmt.Errorf("parse branch-protection config: rules[%d].protections[%d]: unknown rule kind %q (valid: %q, %q)", i, j, raw, KindNoDelete, KindNoDirectWrite)
			}
			if _, dup := kindSeen[k]; dup {
				return nil, fmt.Errorf("parse branch-protection config: rules[%d].protections: duplicate kind %q", i, k)
			}
			kindSeen[k] = struct{}{}
			kinds = append(kinds, k)
		}

		cfg.Rules = append(cfg.Rules, Rule{Branch: rr.Branch, Protections: kinds})
	}

	return cfg, nil
}

// MatchRules returns every rule whose Branch pattern matches the given
// branch name. Patterns are matched via path.Match, so globs like
// "release/*" match "release/1.0" but not "release/1.0/patch". Callers
// that need the union of protections take it across the returned slice.
// Rules are returned in the order they appear in the config.
func (c *Config) MatchRules(branch string) []Rule {
	if c == nil {
		return nil
	}
	var out []Rule
	for _, r := range c.Rules {
		matched, err := path.Match(r.Branch, branch)
		if err != nil || !matched {
			continue
		}
		out = append(out, r)
	}
	return out
}

// rawConfig is the strict-decoded shape of the config file. Keeping it
// internal means the public Config never surfaces []string for Protections;
// callers always see the typed RuleKind slice after validation.
type rawConfig struct {
	Version int        `yaml:"version"`
	Rules   *[]rawRule `yaml:"rules"` // pointer so we can distinguish absent vs empty
}

type rawRule struct {
	Branch      string   `yaml:"branch"`
	Protections []string `yaml:"protections"`
}
