package workflow

import (
	"math"
	"strings"
	"testing"
)

// unencodableCases are values as yaml.v3 decodes them into `any`. want is
// the substring refuseUnencodable's error must carry, or "" when the
// value must pass (INIT-024 TASK-014).
var unencodableCases = []struct {
	name string
	v    any
	want string
}{
	{"plain tree", map[string]any{"a": []any{"x", 1, int64(2), uint64(3), 1.5, math.Copysign(0, -1), true, nil, map[string]any{"b": "c"}, []any{}}}, ""},
	{"empty mapping", map[string]any{}, ""},
	{"top-level map[any]any", map[any]any{"a": "x"}, "the top-level mapping has a key that is not a plain YAML string"},
	{"string keys only, still refused", map[string]any{"commit": map[any]any{"hi": "x"}}, `the mapping at "commit" has a key`},
	{"integer key", map[string]any{"commit": map[any]any{1: "x"}}, `the mapping at "commit" has a key`},
	{"boolean key", map[string]any{"commit": map[any]any{true: "x"}}, `the mapping at "commit" has a key`},
	{"null key", map[string]any{"commit": map[any]any{nil: "x"}}, `the mapping at "commit" has a key`},
	{"float key", map[string]any{"commit": map[any]any{1.5: "x"}}, `the mapping at "commit" has a key`},
	{"inside a list", map[string]any{"steps": []any{map[string]any{"commit": map[any]any{1: "x"}}}}, `the mapping at "steps"[0]."commit" has a key`},
	{"NaN", map[string]any{"name": math.NaN()}, `"name" is a non-finite number`},
	{"+Inf in a list", []any{1.0, math.Inf(1)}, "the document[1] is a non-finite number"},
	{"-Inf", map[string]any{"x": map[string]any{"y": math.Inf(-1)}}, `"x"."y" is a non-finite number`},
	{"the first key in sorted order is reported", map[string]any{"b": map[any]any{1: "x"}, "a": map[any]any{2: "y"}}, `the mapping at "a" has a key`},
}

// TestRefuseUnencodable pins the refusal on the walker itself, with no
// JSON encoder involved, so it holds under every encoding/json engine.
func TestRefuseUnencodable(t *testing.T) {
	for _, tc := range unencodableCases {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseUnencodable(tc.v, "")
			if tc.want == "" {
				if err != nil {
					t.Fatalf("expected acceptance, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected an error containing %q, got %v", tc.want, err)
			}
		})
	}
}
