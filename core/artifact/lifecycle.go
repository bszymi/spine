package artifact

import (
	"reflect"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/yamlsafe"
)

// LifecycleFields reads the type and status the governed-merge status
// lifecycle judges (INIT-023 TASK-017, audit P3-21). It uses exactly the
// decoder Parse uses — splitFrontMatter, yamlsafe.Decode and the
// frontMatter struct decode — so the lifecycle reads what the projection
// reads; there is no second parser and no regex read. One decoder serves
// all three judgements of the merge: the plan, the re-parse of every
// status rewrite (R1) and the merge-result check (V1).
//
// ok is false when the content has no front matter, the front matter does
// not decode (yaml.v3 refuses a duplicate key, so a second `status:` line
// makes the file not-ok, never "the first value wins"), or the type is
// not one of domain.ValidMarkdownArtifactTypes. The status may be "" (the
// line was removed); callers treat that as a status like any other, so
// removing it is a status change.
func LifecycleFields(content []byte) (domain.ArtifactType, domain.ArtifactStatus, bool) {
	parsed, ok := decodeFrontMatter(content)
	if !ok {
		return "", "", false
	}
	t := domain.ArtifactType(parsed.Type)
	for _, valid := range domain.ValidMarkdownArtifactTypes() {
		if t == valid {
			return t, domain.ArtifactStatus(parsed.Status), true
		}
	}
	return "", "", false
}

// decodeFrontMatter is Parse's decoding prefix: no field validation.
func decodeFrontMatter(content []byte) (*frontMatter, bool) {
	fm, _, err := splitFrontMatter(content)
	if err != nil {
		return nil, false
	}
	root, err := yamlsafe.Decode(fm)
	if err != nil {
		return nil, false
	}
	var parsed frontMatter
	if err := root.Decode(&parsed); err != nil {
		return nil, false
	}
	return &parsed, true
}

// EqualExceptStatus reports whether a and b are the same artifact as the
// parser reads them, apart from the `status` key: the same front-matter
// mapping (every key, decoded generically) and the same body. It is the
// second half of the status-rewrite check (R1): the cascade's writer is a
// byte-preserving regex, and a status line whose value is empty can
// swallow the line after it ("status:\nid: X" becomes "status: Completed"),
// which a re-parse of type and status alone would not see. Either side
// failing to decode is "not equal".
func EqualExceptStatus(a, b []byte) bool {
	ma, bodyA, ok := frontMatterMapAndBody(a)
	if !ok {
		return false
	}
	mb, bodyB, ok := frontMatterMapAndBody(b)
	if !ok {
		return false
	}
	delete(ma, "status")
	delete(mb, "status")
	return bodyA == bodyB && reflect.DeepEqual(ma, mb)
}

func frontMatterMapAndBody(content []byte) (map[string]any, string, bool) {
	fm, body, err := splitFrontMatter(content)
	if err != nil {
		return nil, "", false
	}
	root, err := yamlsafe.Decode(fm)
	if err != nil {
		return nil, "", false
	}
	var m map[string]any
	if err := root.Decode(&m); err != nil {
		return nil, "", false
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, body, true
}
