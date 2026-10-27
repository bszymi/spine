package main

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// Workflow, composite-action and compose files are parsed, not line-scanned:
// flow style (`- {uses: x@v4}`), quoted or spaced keys (`"uses": …`,
// `uses : …`), flow mappings (`container: {image: …}`) and YAML anchors,
// aliases and merge keys all reach the same checks a block-style literal
// does. A file that does not parse is refused: it cannot be shown pinned.

// maxMergeDepth bounds merge-key expansion; YAML forbids cycles, but a
// pathological document must not recurse without end.
const maxMergeDepth = 32

// yamlDocs parses every document of a YAML stream.
func yamlDocs(content []byte) ([]*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(content))
	var docs []*yaml.Node
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, err
		}
		if len(n.Content) > 0 {
			docs = append(docs, n.Content[0])
		}
	}
}

// deref follows aliases to the anchored node.
func deref(n *yaml.Node) *yaml.Node {
	for i := 0; n != nil && n.Kind == yaml.AliasNode && i < maxMergeDepth; i++ {
		n = n.Alias
	}
	return n
}

// entry is one key/value pair of a mapping; owner is the mapping that
// literally holds it (a flow mapping carries a trailing comment there).
type entry struct {
	key, val, owner *yaml.Node
}

// entries returns a mapping's pairs with merge keys (`<<: *x`,
// `<<: [*x, *y]`) expanded, so a merged-in value is checked like a literal
// one. Both a merged value and the key that overrides it are returned;
// checking the shadowed one too is only ever stricter.
func entries(n *yaml.Node) []entry {
	return entriesDepth(n, 0)
}

func entriesDepth(n *yaml.Node, depth int) []entry {
	n = deref(n)
	if n == nil || n.Kind != yaml.MappingNode || depth > maxMergeDepth {
		return nil
	}
	var out []entry
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Tag == "!!merge" {
			switch mv := deref(v); {
			case mv == nil:
			case mv.Kind == yaml.MappingNode:
				out = append(out, entriesDepth(mv, depth+1)...)
			case mv.Kind == yaml.SequenceNode:
				for _, item := range mv.Content {
					out = append(out, entriesDepth(item, depth+1)...)
				}
			}
			continue
		}
		out = append(out, entry{key: k, val: v, owner: n})
	}
	return out
}

// lookup returns every pair of mapping n whose key is name.
func lookup(n *yaml.Node, name string) []entry {
	var out []entry
	for _, e := range entries(n) {
		if k := deref(e.key); k != nil && k.Kind == yaml.ScalarNode && k.Value == name {
			out = append(out, e)
		}
	}
	return out
}

// scalar returns the string value of a scalar node (through an alias).
func scalar(n *yaml.Node) (string, bool) {
	n = deref(n)
	if n == nil || n.Kind != yaml.ScalarNode {
		return "", false
	}
	return n.Value, true
}

// lineOf is the line a node starts on (through an alias, the anchor's).
func lineOf(n *yaml.Node) int {
	if d := deref(n); d != nil {
		return d.Line
	}
	return 0
}

// comment is the first trailing comment on the value, its key or (for a
// flow mapping such as `- {uses: x@sha} # v1.2.3`) the mapping holding it.
func (e entry) comment() string {
	for _, n := range []*yaml.Node{deref(e.val), e.val, e.key, e.owner} {
		if n != nil && n.LineComment != "" {
			return n.LineComment
		}
	}
	return ""
}

// parseYAML parses file, refusing it whole when it does not parse.
func (s *scanned) parseYAML(file string, content []byte) []*yaml.Node {
	docs, err := yamlDocs(content)
	if err != nil {
		s.refuse(file, 0, "does not parse as YAML (%v): a file that cannot be read cannot be shown pinned", err)
		return nil
	}
	return docs
}

// imageValue records an `image:` value; anything but a literal string is
// refused.
func (s *scanned) imageValue(file string, n *yaml.Node, origin string) {
	v, ok := scalar(n)
	switch {
	case !ok:
		s.refuse(file, lineOf(n), "image is not a literal string: only a literal name:tag@sha256:<digest> can be verified")
	case strings.TrimSpace(v) == "":
		s.refuse(file, lineOf(n), "image is empty")
	default:
		s.addImage(file, lineOf(n), strings.TrimSpace(v), origin)
	}
}

// scanWorkflow walks jobs.*: runs-on, a reusable-workflow `uses`, the job
// container (string or mapping), every service image and every step.
func (s *scanned) scanWorkflow(file string, content []byte) {
	for _, doc := range s.parseYAML(file, content) {
		for _, jobs := range lookup(doc, "jobs") {
			for _, job := range entries(jobs.val) {
				// One shell scope per job: each job runs on its own runner
				// with its own image store.
				sh := newShellScan(file, originWorkflow)
				for _, e := range lookup(job.val, "runs-on") {
					s.checkRunsOn(file, e.val)
				}
				for _, e := range lookup(job.val, "uses") {
					s.usesEntry(file, e, originWorkflow)
				}
				for _, e := range lookup(job.val, "container") {
					switch c := deref(e.val); {
					case c != nil && c.Kind == yaml.MappingNode:
						for _, img := range lookup(c, "image") {
							s.imageValue(file, img.val, originWorkflow)
						}
					default:
						s.imageValue(file, e.val, originWorkflow)
					}
				}
				for _, e := range lookup(job.val, "services") {
					for _, svc := range entries(e.val) {
						for _, img := range lookup(svc.val, "image") {
							s.imageValue(file, img.val, originWorkflow)
						}
					}
				}
				for _, e := range lookup(job.val, "steps") {
					s.scanSteps(file, e.val, originWorkflow, sh)
				}
				s.images = append(s.images, sh.images()...)
			}
		}
	}
}

// scanAction walks a composite action's runs.steps and a Docker action's
// `docker://` image (an image built from a Dockerfile in the action
// directory is checked as a Dockerfile).
func (s *scanned) scanAction(file string, content []byte) {
	sh := newShellScan(file, originAction)
	for _, doc := range s.parseYAML(file, content) {
		for _, runs := range lookup(doc, "runs") {
			for _, e := range lookup(runs.val, "steps") {
				s.scanSteps(file, e.val, originAction, sh)
			}
			for _, e := range lookup(runs.val, "image") {
				switch v, ok := scalar(e.val); {
				case !ok:
					s.refuse(file, lineOf(e.val), "image is not a literal string")
				case strings.HasPrefix(v, "docker://"):
					s.addImage(file, lineOf(e.val), strings.TrimPrefix(v, "docker://"), originAction)
				}
				// Otherwise it is a Dockerfile path, checked as a Dockerfile.
			}
		}
	}
	s.images = append(s.images, sh.images()...)
}

// scanCompose records every services.*.image.
func (s *scanned) scanCompose(file string, content []byte) {
	for _, doc := range s.parseYAML(file, content) {
		for _, services := range lookup(doc, "services") {
			for _, svc := range entries(services.val) {
				for _, img := range lookup(svc.val, "image") {
					s.imageValue(file, img.val, originCompose)
				}
			}
		}
	}
}

func (s *scanned) scanSteps(file string, steps *yaml.Node, origin string, sh *shellScan) {
	seq := deref(steps)
	if seq == nil || seq.Kind != yaml.SequenceNode {
		s.refuse(file, lineOf(steps), "steps is not a list")
		return
	}
	for _, step := range seq.Content {
		for _, e := range lookup(step, "uses") {
			uses := s.usesEntry(file, e, origin)
			if strings.HasPrefix(uses, "actions/setup-go@") {
				s.checkSetupGo(file, lineOf(e.val), step)
			}
		}
		for _, e := range lookup(step, "run") {
			if v, ok := scalar(e.val); ok {
				// A block scalar's text starts on the line after its key.
				first := lineOf(e.val)
				if d := deref(e.val); d.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
					first++
				}
				sh.scan(first, v)
			}
		}
	}
}

// usesEntry applies the action pin rule to a `uses:` pair and returns its
// value.
func (s *scanned) usesEntry(file string, e entry, origin string) string {
	v, ok := scalar(e.val)
	if !ok {
		s.refuse(file, lineOf(e.val), "uses is not a literal string")
		return ""
	}
	s.checkUses(file, lineOf(e.val), strings.TrimSpace(v), e.comment(), origin)
	return strings.TrimSpace(v)
}

// checkSetupGo requires an actions/setup-go step to install exactly go.mod's
// toolchain with `with.go-version`. setup-go v5 reads only go.mod's `go`
// line from go-version-file (installing go1.26.0 here), and the go command
// then downloads the toolchain before setup-go restores its module cache,
// whose restore then collides with the files already there.
func (s *scanned) checkSetupGo(file string, line int, step *yaml.Node) {
	with := lookup(step, "with")
	var versions []entry
	for _, w := range with {
		for _, e := range lookup(w.val, "go-version-file") {
			s.refuse(file, lineOf(e.val), "actions/setup-go uses go-version-file: set `go-version: '%s'` (go.mod's toolchain) instead", s.toolchain)
		}
		versions = append(versions, lookup(w.val, "go-version")...)
	}
	if len(versions) == 0 {
		s.refuse(file, line, "actions/setup-go has no `with.go-version`: set it to go.mod's toolchain (%s)", s.toolchain)
		return
	}
	for _, e := range versions {
		v, ok := scalar(e.val)
		switch {
		case !ok:
			s.refuse(file, lineOf(e.val), "actions/setup-go go-version is not a literal string")
		case s.toolchain != "" && strings.TrimSpace(v) != s.toolchain:
			s.refuse(file, lineOf(e.val), "actions/setup-go go-version %q is not go.mod's toolchain %s: CI would test another compiler than the one that ships", v, s.toolchain)
		}
	}
}

// checkRunsOn refuses a floating `*-latest` runner label (the image behind
// it moves to a new OS release without a change here) and a computed one.
func (s *scanned) checkRunsOn(file string, n *yaml.Node) {
	var labels []*yaml.Node
	switch d := deref(n); {
	case d == nil:
		return
	case d.Kind == yaml.ScalarNode:
		labels = append(labels, d)
	case d.Kind == yaml.SequenceNode:
		labels = append(labels, d.Content...)
	case d.Kind == yaml.MappingNode:
		for _, e := range lookup(d, "labels") {
			if l := deref(e.val); l != nil && l.Kind == yaml.SequenceNode {
				labels = append(labels, l.Content...)
			} else {
				labels = append(labels, e.val)
			}
		}
	}
	for _, l := range labels {
		v, ok := scalar(l)
		switch {
		case !ok:
			s.refuse(file, lineOf(l), "runs-on label is not a literal string")
		case strings.Contains(v, "${{"):
			s.refuse(file, lineOf(l), "runs-on %q is computed: only a literal, versioned runner label can be verified", v)
		case strings.HasSuffix(strings.TrimSpace(v), "-latest"):
			s.refuse(file, lineOf(l), "runs-on %q floats to a new OS image without a change here: name the version (for example ubuntu-24.04)", v)
		}
	}
}
