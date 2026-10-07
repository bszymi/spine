package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/yamlsafe"
)

// ErrProjectionRoundTrip reports a workflow body that strict Parse
// accepts but whose projected JSON definition does not decode — through
// UnmarshalProjectionDefinition, the decoder every projection-backed
// loader shares — to exactly the parsed definition. It also covers every
// failure after Parse in the projection encoder (the `any` decode,
// json.Marshal, the strict JSON decode) and bodies carrying U+0000,
// which PostgreSQL refuses in jsonb and text columns.
//
// Callers distinguish it from a parse failure with errors.Is: the
// projection reports it as check "round_trip", the write path maps it to
// the "projection_round_trip" validation rule (INIT-023 TASK-011).
var ErrProjectionRoundTrip = errors.New("workflow: projected definition does not round-trip to the parsed definition")

// ProjectionDefinition is the only way a workflow body becomes a
// projection row, and the write path's parity check (INIT-023 TASK-011,
// audit P3-13).
//
// Why it exists: the governed path reads a workflow with strict Parse
// (exact, case-sensitive yaml.v3 keys; unknown and duplicate keys fail),
// but the projection stores a JSON rendering that every loader decodes
// with encoding/json, which matches object keys case-insensitively with
// Unicode simple folding ("ſteps" U+017F reads as "steps"). A file can
// therefore be rejected by Parse, shown benign by a YAML viewer and
// executed differently from the projection. Closing the differential
// at the encoder — rather than in the six loaders across two
// repositories — keeps the stored shape unchanged for every accepted
// file.
//
// Steps:
//  1. wf := Parse(path, content): yamlsafe bounds, strict keys, and the
//     yaml:"-" fields path / commit_sha refused. A failure is returned
//     unwrapped (a parse error, never ErrProjectionRoundTrip).
//  2. def := json.Marshal of yamlsafe.Decode(content) decoded into `any`
//     — byte-for-byte the encoder the projection has always used, so an
//     accepted file stores unchanged JSONB.
//  3. Refuse U+0000 in any key or string value (walked on the decoded
//     value, never a byte search: the six-character text `\u0000` in a
//     description is legitimate).
//  4. got := UnmarshalProjectionDefinition(def), normalised the way the
//     loaders normalise it (Path set from the row, CommitSHA empty, an
//     empty Mode read as "execution"); reflect.DeepEqual(got, wf) must
//     hold. Decoding successfully is not enough: `version: 2026-10-06`
//     decodes, but as "2026-10-06T00:00:00Z".
//
// Every failure after step 1 wraps ErrProjectionRoundTrip.
func ProjectionDefinition(path string, content []byte) (*domain.WorkflowDefinition, []byte, error) {
	wf, err := Parse(path, content)
	if err != nil {
		return nil, nil, err
	}

	def, raw, err := encodeProjection(content)
	if err != nil {
		return nil, nil, roundTripErr(path, err)
	}
	if where, found := findNUL(raw, ""); found {
		return nil, nil, roundTripErr(path, fmt.Errorf("U+0000 in %s (not storable in the projection)", where))
	}

	got, err := UnmarshalProjectionDefinition(def)
	if err != nil {
		return nil, nil, roundTripErr(path, fmt.Errorf("decode projected definition: %v", err))
	}
	// Parse refuses top-level path / commit_sha keys, so the decoded JSON
	// can never carry them. Assert it anyway: the normalisation below
	// overwrites both fields and must never mask a smuggled value.
	if got.Path != "" || got.CommitSHA != "" {
		return nil, nil, roundTripErr(path, errors.New("projected definition carries path or commit_sha"))
	}
	got.Path = wf.Path
	normaliseMode(got)
	if !reflect.DeepEqual(got, wf) {
		return nil, nil, roundTripErr(path, fmt.Errorf("field %q differs between the parsed and the projected definition", firstDifferingField(got, wf)))
	}
	return wf, def, nil
}

// VerifyProjectionDefinition checks a definition that is already stored,
// without git. The projection keeps a previously stored row on a
// rejection (INIT-023 TASK-011) only when it passes this check, so a kept
// row can never be one a folding reader and a strict reader disagree on
// — a row smuggled in before the fix (`ſteps`, `Steps`, `next_ſtep`,
// `path`, `commit_sha`, a number in a string field) fails here.
//
//  1. It must decode through UnmarshalProjectionDefinition (the loaders'
//     decoder: case-insensitive, unknown fields refused).
//  2. The same JSON is decoded into `any`, re-emitted with yaml.Marshal
//     and decoded with a yaml.v3 Decoder with KnownFields(true): exact,
//     case-sensitive keys, the yaml:"-" fields refused.
//  3. Both results must be DeepEqual after the Mode normalisation.
//
// The input is our own JSONB, so the yaml.v3 decoder is used directly:
// JSON has no aliases, and the stored definition was bounded when it was
// written. Every failure wraps ErrProjectionRoundTrip.
func VerifyProjectionDefinition(def []byte) error {
	got, err := UnmarshalProjectionDefinition(def)
	if err != nil {
		return fmt.Errorf("%w: decode stored definition: %v", ErrProjectionRoundTrip, err)
	}
	var raw any
	if err := json.Unmarshal(def, &raw); err != nil {
		return fmt.Errorf("%w: decode stored definition: %v", ErrProjectionRoundTrip, err)
	}
	if where, found := findNUL(raw, ""); found {
		return fmt.Errorf("%w: U+0000 in %s", ErrProjectionRoundTrip, where)
	}
	y, err := yaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("%w: re-encode stored definition: %v", ErrProjectionRoundTrip, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(y))
	dec.KnownFields(true)
	var strict domain.WorkflowDefinition
	if err := dec.Decode(&strict); err != nil {
		return fmt.Errorf("%w: strict read of stored definition: %v", ErrProjectionRoundTrip, err)
	}
	normaliseMode(got)
	normaliseMode(&strict)
	if !reflect.DeepEqual(got, &strict) {
		return fmt.Errorf("%w: field %q differs between the folding and the strict reading", ErrProjectionRoundTrip, firstDifferingField(got, &strict))
	}
	return nil
}

// encodeProjection is the projection's historical encoder: bound the
// input with yamlsafe, decode the document into `any` and json.Marshal
// it. It returns the decoded value too, so callers can inspect it
// without a second decode. Kept byte-for-byte identical to the encoder
// projectWorkflow used before INIT-023 TASK-011 — SMP reads the stored
// JSONB raw, so the shape of an accepted file must not change.
func encodeProjection(content []byte) ([]byte, any, error) {
	node, err := yamlsafe.Decode(content)
	if err != nil {
		return nil, nil, err
	}
	var raw any
	if err := node.Decode(&raw); err != nil {
		return nil, nil, fmt.Errorf("decode workflow document: %v", err)
	}
	def, err := json.Marshal(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("encode workflow definition: %v", err)
	}
	return def, raw, nil
}

func roundTripErr(path string, cause error) error {
	return fmt.Errorf("workflow %s: %w: %v", path, ErrProjectionRoundTrip, cause)
}

// normaliseMode mirrors Parse's default: an absent mode is "execution".
// The projected JSON carries the authored value, so a definition with no
// `mode:` key decodes with Mode "" there and "execution" from Parse.
func normaliseMode(wf *domain.WorkflowDefinition) {
	if wf.Mode == "" {
		wf.Mode = "execution"
	}
}

// findNUL walks a decoded YAML/JSON value and reports the first map key
// or string value containing U+0000, with a short location for the
// error. It recurses through both map shapes yaml.v3 produces
// (map[string]any, and map[any]any when a key is not a string) and
// through slices; every other scalar cannot hold a NUL.
func findNUL(v any, at string) (string, bool) {
	switch t := v.(type) {
	case string:
		if strings.IndexByte(t, 0) >= 0 {
			return locationOr(at), true
		}
	case map[string]any:
		for k, child := range t {
			if strings.IndexByte(k, 0) >= 0 {
				return locationOr(at) + " (a key)", true
			}
			if where, found := findNUL(child, joinLocation(at, k)); found {
				return where, true
			}
		}
	case map[any]any:
		for k, child := range t {
			ks := fmt.Sprint(k)
			if strings.IndexByte(ks, 0) >= 0 {
				return locationOr(at) + " (a key)", true
			}
			if where, found := findNUL(child, joinLocation(at, ks)); found {
				return where, true
			}
		}
	case []any:
		for i, child := range t {
			if where, found := findNUL(child, fmt.Sprintf("%s[%d]", locationOr(at), i)); found {
				return where, true
			}
		}
	}
	return "", false
}

func joinLocation(at, key string) string {
	// The key itself may hold control bytes; it is only used in an error
	// message the projection sanitises before logging, but keep the
	// location readable by quoting it.
	if at == "" {
		return fmt.Sprintf("%q", key)
	}
	return fmt.Sprintf("%s.%q", at, key)
}

func locationOr(at string) string {
	if at == "" {
		return "the document"
	}
	return at
}

// firstDifferingField names the first top-level field (by its json tag)
// on which two definitions differ, for an actionable error. It returns
// "?" when every top-level field compares equal (not reachable when the
// caller has just seen DeepEqual fail on the whole struct).
func firstDifferingField(a, b *domain.WorkflowDefinition) string {
	va, vb := reflect.ValueOf(a).Elem(), reflect.ValueOf(b).Elem()
	t := va.Type()
	for i := 0; i < t.NumField(); i++ {
		if reflect.DeepEqual(va.Field(i).Interface(), vb.Field(i).Interface()) {
			continue
		}
		name := t.Field(i).Name
		if tag := t.Field(i).Tag.Get("json"); tag != "" {
			if n, _, _ := strings.Cut(tag, ","); n != "" && n != "-" {
				name = n
			}
		}
		return name
	}
	return "?"
}
