package yamlsafe

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestDecode_HappyPath(t *testing.T) {
	data := []byte("title: Hello\nitems:\n  - one\n  - two\nnested:\n  key: value\n")
	node, err := Decode(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if node == nil {
		t.Fatal("expected non-nil node")
	}
}

func TestDecode_InvalidYAML(t *testing.T) {
	_, err := Decode([]byte("{{not valid"))
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	if !strings.Contains(err.Error(), "invalid YAML") {
		t.Errorf("expected 'invalid YAML' wrapper, got %v", err)
	}
}

func TestDecode_MaxBytes(t *testing.T) {
	// At the limit: a single key-value mapping whose total byte length
	// is exactly MaxBytes — must succeed.
	prefix := "k: "
	atLimit := []byte(prefix + strings.Repeat("a", MaxBytes-len(prefix)))
	if len(atLimit) != MaxBytes {
		t.Fatalf("test setup: want %d bytes, got %d", MaxBytes, len(atLimit))
	}
	if _, err := Decode(atLimit); err != nil {
		t.Errorf("at-limit input should decode, got %v", err)
	}

	overLimit := append(append([]byte{}, atLimit...), 'a')
	_, err := Decode(overLimit)
	if err == nil {
		t.Fatal("expected error for over-limit input")
	}
	if !strings.Contains(err.Error(), "byte cap") {
		t.Errorf("expected byte-cap error, got %v", err)
	}
}

func TestDecode_MaxDepth(t *testing.T) {
	// MaxDepth nested empty sequences: deepest walk depth == MaxDepth,
	// which is NOT > MaxDepth, so the input is accepted.
	atLimit := strings.Repeat("[", MaxDepth) + strings.Repeat("]", MaxDepth)
	if _, err := Decode([]byte(atLimit)); err != nil {
		t.Errorf("at-limit nesting should decode, got %v", err)
	}

	// MaxDepth+1 nested sequences: deepest walk depth > MaxDepth, rejected.
	overLimit := strings.Repeat("[", MaxDepth+1) + strings.Repeat("]", MaxDepth+1)
	_, err := Decode([]byte(overLimit))
	if err == nil {
		t.Fatal("expected error for over-depth input")
	}
	if !strings.Contains(err.Error(), "depth") {
		t.Errorf("expected depth error, got %v", err)
	}
}

func TestDecode_MaxNodes(t *testing.T) {
	// A flow-style sequence of N scalars produces 2+N nodes
	// (DocumentNode + SequenceNode + N ScalarNodes). At limit:
	// 2+N == MaxNodes accepts (NOT > MaxNodes); 2+N > MaxNodes rejects.
	build := func(n int) []byte {
		var sb strings.Builder
		sb.Grow(2 * n)
		sb.WriteByte('[')
		for i := 0; i < n; i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteByte('a')
		}
		sb.WriteByte(']')
		return []byte(sb.String())
	}

	atLimit := build(MaxNodes - 2)
	if _, err := Decode(atLimit); err != nil {
		t.Errorf("at-limit node count should decode, got %v", err)
	}

	overLimit := build(MaxNodes - 1)
	_, err := Decode(overLimit)
	if err == nil {
		t.Fatal("expected error for over-node-count input")
	}
	if !strings.Contains(err.Error(), "node count") {
		t.Errorf("expected node-count error, got %v", err)
	}
}

func TestDecode_MaxAliases(t *testing.T) {
	// One anchor + N alias references in a flow-style sequence.
	build := func(n int) []byte {
		var sb strings.Builder
		sb.Grow(8 + 3*n)
		sb.WriteString("[&a 0")
		for i := 0; i < n; i++ {
			sb.WriteString(",*a")
		}
		sb.WriteByte(']')
		return []byte(sb.String())
	}

	atLimit := build(MaxAliases)
	if _, err := Decode(atLimit); err != nil {
		t.Errorf("at-limit alias count should decode, got %v", err)
	}

	overLimit := build(MaxAliases + 1)
	_, err := Decode(overLimit)
	if err == nil {
		t.Fatal("expected error for over-alias-count input")
	}
	if !strings.Contains(err.Error(), "alias") {
		t.Errorf("expected alias-count error, got %v", err)
	}
}

func TestDecode_CyclicAliasDoesNotLoop(t *testing.T) {
	// A sequence anchored as `a` whose only element is an alias to
	// itself produces a cyclic node graph. The walker counts the alias
	// without recursing into its target, so this must terminate
	// quickly rather than infinite-looping. Wrap in a goroutine + timeout
	// so a regression hangs the test rather than the suite.
	data := []byte("&a [*a]")
	done := make(chan error, 1)
	go func() {
		_, err := Decode(data)
		done <- err
	}()
	select {
	case err := <-done:
		// The single alias is well under MaxAliases, so success or a
		// non-hang error are both acceptable — what matters is that
		// the call returned.
		_ = err
	case <-time.After(2 * time.Second):
		t.Fatal("Decode hung on cyclic alias input")
	}
}

func TestDecodeInto_HappyPath(t *testing.T) {
	type cfg struct {
		Name  string `yaml:"name"`
		Count int    `yaml:"count"`
	}
	var got cfg
	if err := DecodeInto([]byte("name: foo\ncount: 7\n"), &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "foo" || got.Count != 7 {
		t.Errorf("unexpected decoded value: %+v", got)
	}
}

func TestDecodeInto_BoundRejectionPropagates(t *testing.T) {
	overLimit := append([]byte("k: "), bytes.Repeat([]byte("a"), MaxBytes)...)
	var dst struct {
		K string `yaml:"k"`
	}
	err := DecodeInto(overLimit, &dst)
	if err == nil {
		t.Fatal("expected DecodeInto to reject over-limit input")
	}
	if !strings.Contains(err.Error(), "byte cap") {
		t.Errorf("expected byte-cap error, got %v", err)
	}
}

func TestDecodeIntoStrict_HappyPath(t *testing.T) {
	type cfg struct {
		Name string `yaml:"name"`
	}
	var got cfg
	if err := DecodeIntoStrict([]byte("name: foo\n"), &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "foo" {
		t.Errorf("unexpected decoded value: %+v", got)
	}
}

func TestDecodeIntoStrict_RejectsUnknownFields(t *testing.T) {
	type cfg struct {
		Name string `yaml:"name"`
	}
	var got cfg
	err := DecodeIntoStrict([]byte("name: foo\nextra: bar\n"), &got)
	if err == nil {
		t.Fatal("expected strict decode to reject unknown field")
	}
	if !strings.Contains(err.Error(), "invalid YAML") {
		t.Errorf("expected 'invalid YAML' wrapper, got %v", err)
	}
}

func TestDecodeIntoStrict_BoundRejectionPropagates(t *testing.T) {
	overLimit := append([]byte("k: "), bytes.Repeat([]byte("a"), MaxBytes)...)
	var dst struct {
		K string `yaml:"k"`
	}
	err := DecodeIntoStrict(overLimit, &dst)
	if err == nil {
		t.Fatal("expected DecodeIntoStrict to reject over-limit input")
	}
	if !strings.Contains(err.Error(), "byte cap") {
		t.Errorf("expected byte-cap error, got %v", err)
	}
}
