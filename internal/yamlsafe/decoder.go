// Package yamlsafe decodes YAML with hard bounds on size, depth, node count,
// and alias count. It exists so artifact front-matter and workflow bodies
// share one hardened entry point — both arrive via the same HTTP surface and
// the same class of billion-laughs input applies.
package yamlsafe

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Bounds caps. These match the pre-extraction internal/artifact values.
// Keeping them as exported package-level constants means any future tuning
// is a single edit here.
const (
	MaxBytes   = 64 * 1024 // hard cap on input size
	MaxNodes   = 10_000    // total YAML nodes (scalars, mappings, sequences)
	MaxDepth   = 64        // nesting depth
	MaxAliases = 100       // aliases — NOT followed, only counted, so
	// billion-laughs explosions are refused before decoding into a typed
	// target would materialise them.
)

// Decode parses data into a yaml.Node and rejects inputs that exceed the
// bounds. Callers can then .Decode the returned node into a typed target
// knowing alias expansion is safe.
func Decode(data []byte) (*yaml.Node, error) {
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("YAML exceeds %d byte cap (got %d)", MaxBytes, len(data))
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("invalid YAML: %v", err)
	}
	nodes, aliases := 0, 0
	if err := walk(&root, 0, &nodes, &aliases); err != nil {
		return nil, err
	}
	return &root, nil
}

// DecodeInto decodes data into the given typed target after the bounds pass.
// Provided for callers that only need the typed value and not the raw node.
func DecodeInto(data []byte, target any) error {
	node, err := Decode(data)
	if err != nil {
		return err
	}
	return node.Decode(target)
}

// DecodeIntoStrict decodes data into the given typed target with strict
// field-name checking — yaml.Decoder.KnownFields(true). Unknown keys at any
// nesting level fail with an error rather than being silently dropped. The
// bounds check runs first, on the same input, so the strict pass cannot be
// abused to amplify a bounds violation.
//
// Callers should prefer this over DecodeInto whenever the target is a
// closed-shape struct (e.g., workflow definitions): silent drop of an
// unknown step field is a documented misrouting risk per ADR-015.
func DecodeIntoStrict(data []byte, target any) error {
	if _, err := Decode(data); err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}
	return nil
}

func walk(n *yaml.Node, depth int, nodes, aliases *int) error {
	if n == nil {
		return nil
	}
	if depth > MaxDepth {
		return fmt.Errorf("YAML nesting depth exceeds %d", MaxDepth)
	}
	*nodes++
	if *nodes > MaxNodes {
		return fmt.Errorf("YAML node count exceeds %d", MaxNodes)
	}
	if n.Kind == yaml.AliasNode {
		*aliases++
		if *aliases > MaxAliases {
			return fmt.Errorf("YAML alias count exceeds %d", MaxAliases)
		}
		// Do not follow the alias — counting the reference is enough and
		// recursing could loop on cyclic anchors.
		return nil
	}
	for _, c := range n.Content {
		if err := walk(c, depth+1, nodes, aliases); err != nil {
			return err
		}
	}
	return nil
}
