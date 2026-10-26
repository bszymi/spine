package git

// Checkout-time expansion by .gitattributes (INIT-024 TASK-003; the
// first version's substring heuristic was replaced in review round 1).
//
// A tree's own .gitattributes can make a checkout write more than the
// blobs git stores: `ident` turns each 4-byte "$Id$" into "$Id: <hex> $",
// `text` / `eol=crlf` / the legacy `crlf` turn LF into CRLF, and
// `working-tree-encoding` re-encodes UTF-8. A filter=<name> runs whatever
// smudge driver the host's git config defines for <name> (git-lfs fetches
// arbitrary content), which no factor bounds.
//
// The measure reads every .gitattributes blob of the tree (all of them,
// in one `git cat-file --batch`, up to maxAttributeScanBytes) and parses
// them the way git reads attribute lines, conservatively:
//
//   - blank lines and lines whose first non-blank byte is '#' are
//     comments; a pattern is the first field, or a C-quoted string;
//   - each attribute after the pattern is "name" (set), "-name" (unset),
//     "!name" (unspecified) or "name=value"; only a SET attribute can
//     expand content, so unset and unspecified ones are ignored;
//   - "[attr]name attrs..." defines a macro; a set macro name on a pattern
//     line expands to its attributes (recursively, bounded), with the
//     built-in macro `binary` (-diff -merge -text) predefined; a macro
//     defined in more than one file expands to every definition;
//   - which paths a pattern matches is NOT evaluated: an expanding
//     attribute set by any line counts for the whole tree. That is the
//     conservative direction — the factor can only be too high — and it
//     keeps the scan linear in the attribute text.
//
// The classes a scan finds multiply: text/eol/crlf x2, ident x(7+hex)/4
// (11.75 with SHA-1 ids, 17.75 with SHA-256), working-tree-encoding x8
// (UTF-32 is 4x, UTF-7 up to 5x per isolated character; 8 bounds every
// encoding iconv offers per input byte). A configured filter driver makes
// the factor +Inf: the tree is refused whatever the limit, and the error
// says why.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// attrClass is a set of attribute classes that rewrite content on
// checkout.
type attrClass uint8

const (
	// attrCRLF: text / text=auto / eol=crlf / crlf — LF becomes CRLF, at
	// most 2x. (`text` alone writes CRLF only under a host core.eol=crlf
	// or core.autocrlf; it is counted anyway.)
	attrCRLF attrClass = 1 << iota
	// attrIdent: ident — "$Id$" (4 bytes) becomes "$Id: <hex> $".
	attrIdent
	// attrEncoding: working-tree-encoding=<anything but UTF-8>.
	attrEncoding

	attrAll = attrCRLF | attrIdent | attrEncoding

	encodingFactor = 8

	// maxMacroDepth bounds macro expansion (a macro naming a macro ...).
	maxMacroDepth = 8
)

// identFactor is ident's expansion for object ids of hexLen hex digits.
func identFactor(hexLen int) float64 {
	if hexLen <= 0 {
		hexLen = 40
	}
	// "$Id: " + hex + " $" for every 4-byte "$Id$".
	return float64(5+hexLen+2) / 4
}

// factor is the worst-case expansion of the classes in a.
func (a attrClass) factor(hexLen int) float64 {
	f := 1.0
	if a&attrCRLF != 0 {
		f *= 2
	}
	if a&attrIdent != 0 {
		f *= identFactor(hexLen)
	}
	if a&attrEncoding != 0 {
		f *= encodingFactor
	}
	return f
}

// describe names the classes in a with their factors, for an error.
func (a attrClass) describe(hexLen int) string {
	var parts []string
	if a&attrCRLF != 0 {
		parts = append(parts, "text/eol=crlf x2")
	}
	if a&attrIdent != 0 {
		parts = append(parts, "ident x"+strconv.FormatFloat(identFactor(hexLen), 'g', 4, 64))
	}
	if a&attrEncoding != 0 {
		parts = append(parts, "working-tree-encoding x"+strconv.Itoa(encodingFactor))
	}
	return strings.Join(parts, ", ")
}

// attrSet accumulates the .gitattributes files of one tree.
type attrSet struct {
	macros map[string][][]string // name -> every definition's attributes
	lines  [][]string            // the attributes of every pattern line
}

func newAttrSet() *attrSet {
	return &attrSet{macros: map[string][][]string{
		// git's built-in macro.
		"binary": {{"-diff", "-merge", "-text"}},
	}}
}

// add parses one .gitattributes text.
func (s *attrSet) add(text []byte) {
	for len(text) > 0 {
		var line []byte
		if i := bytes.IndexByte(text, '\n'); i >= 0 {
			line, text = text[:i], text[i+1:]
		} else {
			line, text = text, nil
		}
		line = bytes.TrimRight(line, "\r")
		line = bytes.TrimLeft(line, " \t")
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		var pattern, rest []byte
		if line[0] == '"' {
			end := quotedEnd(line)
			if end < 0 {
				continue // an unterminated quoted pattern: git drops the line
			}
			pattern, rest = line[:end+1], line[end+1:]
		} else if i := bytes.IndexAny(line, " \t"); i >= 0 {
			pattern, rest = line[:i], line[i:]
		} else {
			pattern = line
		}
		attrs := strings.Fields(string(rest))
		if name, ok := strings.CutPrefix(string(pattern), "[attr]"); ok {
			if name != "" {
				s.macros[name] = append(s.macros[name], attrs)
			}
			continue
		}
		if len(attrs) > 0 {
			s.lines = append(s.lines, attrs)
		}
	}
}

// quotedEnd returns the index of the quote that closes the C-quoted
// string line starts with, or -1.
func quotedEnd(line []byte) int {
	for i := 1; i < len(line); i++ {
		switch line[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// evaluate returns the classes some line sets and the filter drivers some
// line names.
func (s *attrSet) evaluate() (attrClass, []string) {
	var classes attrClass
	filters := map[string]bool{}
	var visit func(attrs []string, depth int)
	visit = func(attrs []string, depth int) {
		for _, a := range attrs {
			if a == "" || a[0] == '-' || a[0] == '!' {
				continue // unset or unspecified: expands nothing
			}
			name, value, hasValue := strings.Cut(a, "=")
			switch name {
			case "text":
				if !hasValue || value == "auto" {
					classes |= attrCRLF
				}
			case "crlf":
				if !hasValue || (value != "input" && value != "false") {
					classes |= attrCRLF
				}
			case "eol":
				if hasValue && strings.EqualFold(value, "crlf") {
					classes |= attrCRLF
				}
			case "ident":
				if !hasValue {
					classes |= attrIdent
				}
			case "working-tree-encoding":
				if hasValue && value != "" && !strings.EqualFold(value, "utf-8") && !strings.EqualFold(value, "utf8") {
					classes |= attrEncoding
				}
			case "filter":
				if hasValue && value != "" {
					filters[value] = true
				}
			default:
				if !hasValue && depth < maxMacroDepth {
					for _, def := range s.macros[name] {
						visit(def, depth+1)
					}
				}
			}
		}
	}
	for _, attrs := range s.lines {
		visit(attrs, 0)
	}
	names := make([]string, 0, len(filters))
	for n := range filters {
		names = append(names, n)
	}
	return classes, names
}

// errMalformedBatch is scanAttributes' refusal of `cat-file --batch`
// output it cannot parse.
var errMalformedBatch = errors.New("malformed `git cat-file --batch` output")

// scanAttributes sets st's expansion from the tree's .gitattributes blobs
// (oids), or the worst case for the reason in worst.
func (c *CLIClient) scanAttributes(ctx context.Context, st *TreeStats, oids []string, worst string) error {
	st.Expansion, st.ExpansionReason = 1, ""
	if worst != "" {
		st.Expansion = attrAll.factor(st.hexLen)
		st.ExpansionReason = worst + "; the worst case is assumed (" + attrAll.describe(st.hexLen) + ")"
		return nil
	}
	if len(oids) == 0 {
		return nil
	}
	// One process for every file. The blobs run under the read bounds
	// (GIT_ALLOC_LIMIT, pinned delta-base cache): an attributes file
	// stored as a delta on a huge base is refused, not inflated.
	env := []string{deltaBaseCacheEnv(c.inheritedConfigParameters(), c.blobLimit()),
		"GIT_ALLOC_LIMIT=" + strconv.FormatInt(c.gitAllocLimit(), 10)}
	out, _, err := c.runWith(ctx, measureOp, runOpts{env: env, stdin: strings.NewReader(strings.Join(oids, "\n") + "\n")},
		"cat-file", "--batch")
	if err != nil {
		return err
	}
	set := newAttrSet()
	rest := out
	for _, oid := range oids {
		header, tail, ok := strings.Cut(rest, "\n")
		f := strings.Fields(header)
		if !ok || len(f) != 3 || f[0] != oid || f[1] != TreeEntryBlob {
			return &GitError{Kind: ErrKindPermanent, Op: measureOp, Message: "cannot read the tree's .gitattributes", Err: errMalformedBatch}
		}
		size, err := strconv.Atoi(f[2])
		if err != nil || size < 0 || size+1 > len(tail) || tail[size] != '\n' {
			return &GitError{Kind: ErrKindPermanent, Op: measureOp, Message: "cannot read the tree's .gitattributes", Err: errMalformedBatch}
		}
		set.add([]byte(tail[:size]))
		rest = tail[size+1:]
	}
	classes, filters := set.evaluate()
	if len(filters) > 0 {
		drivers, err := c.filterDrivers(ctx)
		if err != nil {
			return err
		}
		for _, name := range filters {
			if drivers[name] {
				st.Expansion = math.Inf(1)
				st.ExpansionReason = fmt.Sprintf("filter=%q runs a smudge filter driver this host's git config defines", name)
				return nil
			}
		}
	}
	st.Expansion = classes.factor(st.hexLen)
	st.ExpansionReason = classes.describe(st.hexLen)
	return nil
}

// inheritedConfigParameters is the GIT_CONFIG_PARAMETERS a command of
// this client inherits.
func (c *CLIClient) inheritedConfigParameters() string {
	for i := len(c.measureEnv) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(c.measureEnv[i], "GIT_CONFIG_PARAMETERS="); ok {
			return v
		}
	}
	return os.Getenv("GIT_CONFIG_PARAMETERS")
}

// filterDrivers returns the filter names whose smudge (or process)
// driver the git config this client's checkouts read defines. A clone
// made without ambient credentials reads none of the host's global or
// system config, so its measure passes the clone's environment
// (measureEnv) and sees what its checkout will see.
func (c *CLIClient) filterDrivers(ctx context.Context) (map[string]bool, error) {
	out, _, err := c.runWith(ctx, measureOp, runOpts{env: c.measureEnv}, "config", "--get-regexp", `^filter\..*\.(smudge|process)$`)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil // no driver configured
		}
		return nil, err
	}
	drivers := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		key, value, _ := strings.Cut(line, " ")
		if strings.TrimSpace(value) == "" {
			continue // an empty driver runs nothing
		}
		key = strings.TrimPrefix(key, "filter.")
		if i := strings.LastIndexByte(key, '.'); i > 0 {
			drivers[key[:i]] = true
		}
	}
	return drivers, nil
}
