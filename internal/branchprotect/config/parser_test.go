package config

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// errReader lets us exercise the io.Reader error path without depending on
// the fs or network.
type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, errors.New("boom") }

func TestParse_HappyPath(t *testing.T) {
	const body = `
version: 1
rules:
  - branch: main
    protections: [no-delete, no-direct-write]
  - branch: staging
    protections: [no-delete]
  - branch: "release/*"
    protections: [no-delete, no-direct-write]
`
	cfg, err := Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if cfg.Version != 1 {
		t.Fatalf("Version: got %d, want 1", cfg.Version)
	}
	if len(cfg.Rules) != 3 {
		t.Fatalf("Rules: got %d, want 3", len(cfg.Rules))
	}
	want := []Rule{
		{Branch: "main", Protections: []RuleKind{KindNoDelete, KindNoDirectWrite}},
		{Branch: "staging", Protections: []RuleKind{KindNoDelete}},
		{Branch: "release/*", Protections: []RuleKind{KindNoDelete, KindNoDirectWrite}},
	}
	if !reflect.DeepEqual(cfg.Rules, want) {
		t.Fatalf("Rules mismatch.\ngot:  %#v\nwant: %#v", cfg.Rules, want)
	}
}

func TestParse_Errors(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantSub string // substring required in the error message
	}{
		{
			name:    "empty input",
			body:    "",
			wantSub: "file is empty",
		},
		{
			name:    "missing version",
			body:    "rules:\n  - branch: main\n    protections: [no-delete]\n",
			wantSub: "version: required field missing",
		},
		{
			name:    "unsupported version",
			body:    "version: 2\nrules: []\n",
			wantSub: "unsupported version 2",
		},
		{
			name:    "unknown top-level key",
			body:    "version: 1\nrules: []\nextra: nope\n",
			wantSub: "field extra not found",
		},
		{
			name:    "unknown rule field",
			body:    "version: 1\nrules:\n  - branch: main\n    protections: [no-delete]\n    mode: weird\n",
			wantSub: "field mode not found",
		},
		{
			name:    "missing branch",
			body:    "version: 1\nrules:\n  - protections: [no-delete]\n",
			wantSub: "rules[0].branch: required field missing",
		},
		{
			name:    "invalid glob pattern",
			body:    "version: 1\nrules:\n  - branch: \"release/[\"\n    protections: [no-delete]\n",
			wantSub: "invalid pattern",
		},
		{
			name:    "duplicate branch",
			body:    "version: 1\nrules:\n  - branch: main\n    protections: [no-delete]\n  - branch: main\n    protections: [no-direct-write]\n",
			wantSub: "duplicate entry",
		},
		{
			name:    "empty protections",
			body:    "version: 1\nrules:\n  - branch: main\n    protections: []\n",
			wantSub: "rules[0].protections: must be non-empty",
		},
		{
			name:    "unknown rule kind",
			body:    "version: 1\nrules:\n  - branch: main\n    protections: [bogus]\n",
			wantSub: "unknown rule kind \"bogus\"",
		},
		{
			name:    "duplicate rule kind",
			body:    "version: 1\nrules:\n  - branch: main\n    protections: [no-delete, no-delete]\n",
			wantSub: "duplicate kind",
		},
		{
			name:    "malformed yaml",
			body:    "version: 1\nrules: [:::",
			wantSub: "parse branch-protection config",
		},
		{
			name:    "rules field entirely missing",
			body:    "version: 1\n",
			wantSub: "rules: required field missing",
		},
		{
			name:    "rules explicitly null",
			body:    "version: 1\nrules: null\n",
			wantSub: "rules: required field missing",
		},
		{
			name:    "recursive glob pattern",
			body:    "version: 1\nrules:\n  - branch: \"release/**\"\n    protections: [no-delete]\n",
			wantSub: "recursive",
		},
		{
			name:    "regex anchor caret",
			body:    "version: 1\nrules:\n  - branch: \"^release\"\n    protections: [no-delete]\n",
			wantSub: "not allowed in Git ref names",
		},
		{
			name:    "git-forbidden tilde",
			body:    "version: 1\nrules:\n  - branch: \"main~1\"\n    protections: [no-delete]\n",
			wantSub: "not allowed in Git ref names",
		},
		{
			name:    "git-forbidden colon",
			body:    "version: 1\nrules:\n  - branch: \"refs:head\"\n    protections: [no-delete]\n",
			wantSub: "not allowed in Git ref names",
		},
		{
			name:    "git-forbidden space",
			body:    "version: 1\nrules:\n  - branch: \"main branch\"\n    protections: [no-delete]\n",
			wantSub: "not allowed in Git ref names",
		},
		{
			name:    "trailing YAML document",
			body:    "version: 1\nrules: []\n---\nversion: 99\n",
			wantSub: "second YAML document",
		},
		{
			name:    "trailing YAML document with parse error",
			body:    "version: 1\nrules: []\n---\n  - broken: [",
			wantSub: "trailing content",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tc.body))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestParse_ExplicitEmptyRules(t *testing.T) {
	// `rules: []` is a legitimate shape — zero-rule config is meaningful
	// ("everything is allowed") and should parse cleanly. The rejection
	// only applies to absent / null.
	cfg, err := Parse(strings.NewReader("version: 1\nrules: []\n"))
	if err != nil {
		t.Fatalf("empty rules list rejected: %v", err)
	}
	if len(cfg.Rules) != 0 {
		t.Fatalf("Rules: got %d, want 0", len(cfg.Rules))
	}
}

func TestParse_NilReader(t *testing.T) {
	if _, err := Parse(nil); err == nil {
		t.Fatal("Parse(nil) = nil; want error")
	}
}

func TestParse_ReaderError(t *testing.T) {
	_, err := Parse(errReader{})
	if err == nil {
		t.Fatal("Parse with failing reader returned nil error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error %q does not include reader error", err.Error())
	}
}

func TestParse_OversizedInput(t *testing.T) {
	// yamlsafe.MaxBytes is 64 KiB. Build a body that exceeds it and verify
	// we reject it before yaml parsing.
	big := strings.Repeat("# padding\n", 8000) + "version: 1\nrules: []\n"
	_, err := Parse(strings.NewReader(big))
	if err == nil {
		t.Fatal("oversized input was accepted")
	}
	if !strings.Contains(err.Error(), "byte cap") {
		t.Fatalf("error %q does not mention byte cap", err.Error())
	}
}

func TestMatchRules(t *testing.T) {
	// Uses `*` (single-segment, per path.Match) and `release/*`
	// (single-segment under release/) to pin the documented glob
	// semantics: `*` does not cross a `/`.
	const body = `
version: 1
rules:
  - branch: main
    protections: [no-delete, no-direct-write]
  - branch: "release/*"
    protections: [no-delete]
  - branch: "*"
    protections: [no-delete]
`
	cfg, err := Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	cases := []struct {
		name        string
		branch      string
		wantBranchs []string
	}{
		{"exact matches both literal and single-segment glob", "main", []string{"main", "*"}},
		{"release glob matches release/1.0 but not bare '*'", "release/1.0", []string{"release/*"}},
		{"glob does not cross slash", "release/1.0/patch", nil},
		{"multi-segment unmatched", "feat/x", nil},
		{"empty branch name matches bare '*'", "", []string{"*"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules := cfg.MatchRules(tc.branch)
			var got []string
			for _, r := range rules {
				got = append(got, r.Branch)
			}
			if !reflect.DeepEqual(got, tc.wantBranchs) {
				t.Fatalf("got %v, want %v", got, tc.wantBranchs)
			}
		})
	}
}

func TestMatchRules_NilConfig(t *testing.T) {
	var cfg *Config
	if rules := cfg.MatchRules("main"); rules != nil {
		t.Fatalf("nil config returned %v, want nil", rules)
	}
}

func TestRuleKind_IsKnown(t *testing.T) {
	cases := []struct {
		kind RuleKind
		want bool
	}{
		{KindNoDelete, true},
		{KindNoDirectWrite, true},
		{RuleKind("no-delete"), true},
		{RuleKind("bogus"), false},
		{RuleKind(""), false},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			if got := tc.kind.IsKnown(); got != tc.want {
				t.Fatalf("IsKnown(%q) = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}

// sanity: ensure Parse returns a non-nil *Config on the happy path so
// callers can type-assert without a nil check besides err.
var _ io.Reader = strings.NewReader("")
