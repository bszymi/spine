// Package yamlsafe decodes YAML with hard bounds on size, depth, node count,
// and alias count. It exists so artifact front-matter and workflow bodies
// share one hardened entry point — both arrive via the same HTTP surface and
// the same class of billion-laughs input applies.
package yamlsafe

import (
	"bytes"
	"errors"
	"fmt"
	"io"

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
//
// The input is one YAML document (INIT-024 TASK-002, review finding N-7).
// A decoder stops after the first document, so content after a `---`
// separator used to be dropped without a word: a viewer or another YAML
// tool could show, or act on, a document no Spine reader ever saw. Every
// document after the first must be empty — what a bare trailing `---`
// produces (see isEmptyDocument), which editors and generators append —
// or the input is refused with "unexpected second document". A syntax
// error in the trailing content is refused too ("trailing content"). An
// explicit document end (`...`) followed by nothing is not a document.
// The branch-protection parser refuses even an empty second document;
// the workflow corpus accepts it because it carries no content.
func DecodeIntoStrict(data []byte, target any) error {
	if _, err := Decode(data); err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}
	// Read every remaining document, not just the next one: an empty
	// document followed by one with content ("---\n---\nid: b") must not
	// pass because the first trailing document was empty. The loop is
	// bounded by the input, which Decode capped at MaxBytes, and the
	// trailing documents are decoded into yaml.Node only (aliases are
	// never expanded), so nothing here can amplify.
	for {
		var trailing yaml.Node
		err := dec.Decode(&trailing)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("invalid YAML: trailing content: %w", err)
		}
		if !isEmptyDocument(&trailing) {
			return errors.New("invalid YAML: unexpected second document (only one document per file)")
		}
	}
}

// isEmptyDocument reports whether n is the document a bare `---` line
// produces: a DocumentNode whose only child is an untagged, unanchored
// null scalar with an empty value and no children. Comments do not
// count as content (no reader sees them). Everything else is content and
// is refused, including an anchor alone (`--- &x`), an explicit tag
// (`--- !!null`) and an explicit null (`--- ~`, `--- null`): someone wrote
// them, and their meaning to another tool is not Spine's to guess.
func isEmptyDocument(n *yaml.Node) bool {
	if n.Kind != yaml.DocumentNode || n.Anchor != "" || len(n.Content) != 1 {
		return false
	}
	c := n.Content[0]
	return c.Kind == yaml.ScalarNode &&
		c.Tag == "!!null" &&
		c.Value == "" &&
		c.Anchor == "" &&
		c.Style&yaml.TaggedStyle == 0 &&
		c.Alias == nil &&
		len(c.Content) == 0
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
