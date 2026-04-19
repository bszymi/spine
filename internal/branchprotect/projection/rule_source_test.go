package projection

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/branchprotect/config"
	"github.com/bszymi/spine/internal/store"
)

// fakeReader lets us exercise the adapter without a database.
type fakeReader struct {
	rows []store.BranchProtectionRuleProjection
	err  error
}

func (f fakeReader) ListBranchProtectionRules(_ context.Context) ([]store.BranchProtectionRuleProjection, error) {
	return f.rows, f.err
}

func TestRuleSource_HappyPath(t *testing.T) {
	reader := fakeReader{
		rows: []store.BranchProtectionRuleProjection{
			{
				BranchPattern: "main",
				RuleOrder:     0,
				Protections:   []byte(`["no-delete","no-direct-write"]`),
				SourceCommit:  "abc123",
			},
			{
				BranchPattern: "release/*",
				RuleOrder:     1,
				Protections:   []byte(`["no-delete"]`),
				SourceCommit:  "abc123",
			},
		},
	}
	rs := New(reader)
	got, err := rs.Rules(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []config.Rule{
		{Branch: "main", Protections: []config.RuleKind{config.KindNoDelete, config.KindNoDirectWrite}},
		{Branch: "release/*", Protections: []config.RuleKind{config.KindNoDelete}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Rules mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestRuleSource_EmptyTableReturnsEmptySliceNotNil(t *testing.T) {
	// The contract: empty table = "explicit empty config", which
	// translates to "nothing protected". The adapter must NOT return
	// (nil, nil) because that's the policy's bootstrap-fallback
	// signal; bootstrap is already written to the table by the
	// projection handler when the file is missing.
	rs := New(fakeReader{rows: nil})
	got, err := rs.Rules(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("got nil slice; want empty non-nil slice to signal explicit empty config")
	}
	if len(got) != 0 {
		t.Fatalf("got %d rules, want 0", len(got))
	}
}

func TestRuleSource_StoreErrorPropagates(t *testing.T) {
	rs := New(fakeReader{err: errors.New("db unavailable")})
	_, err := rs.Rules(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "db unavailable") {
		t.Fatalf("error does not wrap source: %v", err)
	}
	if !strings.Contains(err.Error(), "list rules") {
		t.Fatalf("error does not identify operation: %v", err)
	}
}

func TestRuleSource_MalformedProtectionsJSON(t *testing.T) {
	reader := fakeReader{
		rows: []store.BranchProtectionRuleProjection{
			{BranchPattern: "main", Protections: []byte(`not json`)},
		},
	}
	_, err := New(reader).Rules(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if !strings.Contains(err.Error(), "decode protections for \"main\"") {
		t.Fatalf("error does not identify field: %v", err)
	}
}

func TestNew_NilReaderPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("New(nil) did not panic")
		}
	}()
	New(nil)
}

func TestRuleSource_PreservesOrderingAcrossCalls(t *testing.T) {
	// The branchprotect.MatchRules contract requires source-file
	// order; the store query sorts by rule_order. Verify the adapter
	// doesn't reshuffle.
	reader := fakeReader{
		rows: []store.BranchProtectionRuleProjection{
			{BranchPattern: "main", RuleOrder: 0, Protections: []byte(`["no-delete"]`)},
			{BranchPattern: "release/*", RuleOrder: 1, Protections: []byte(`["no-delete"]`)},
			{BranchPattern: "staging", RuleOrder: 2, Protections: []byte(`["no-delete"]`)},
		},
	}
	got, err := New(reader).Rules(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.Branch)
	}
	want := []string{"main", "release/*", "staging"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("ordering changed: got %v, want %v", names, want)
	}
}
