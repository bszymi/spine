package branchprotect

// Governance-path guard facts and decision (ADR-016; INIT-023 TASK-006,
// audit F3 "run-branch slippage").
//
// ADR-009 allowed every governed merge unconditionally: a run branch could
// carry `.spine/branch-protection.yaml: rules: []` or a review-less
// workflow into `main`, and the projection installed it as live policy.
// The guard decides, for one governed merge, whether it changes a
// governance path and, if so, whether an operator approved exactly the
// commit the merge will merge. The facts are computed by the engine
// (core/engine governance_guard.go) and carried on Request.Governance; the
// decision is the single pure function EvaluateGovernance below, which
// both Policy.Evaluate and the engine call (a custom or permissive Policy
// can therefore not turn the guard off — ADR-016 §4, TASK-006 D4).
//
// Nothing here reads rules: the guard is a merge-authorization rule the
// deployment configures, not a branch-protection rule kind (ADR-009's
// ruleset stays flat).

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bszymi/spine/core/domain"
)

// DefaultGovernancePaths is the guarded set used when a deployment
// configures none (TASK-006 owner question 2). Callers add the
// projection's workflow directories (<artifacts_dir>/workflows/) on top.
var DefaultGovernancePaths = []string{".spine/", ".spine.yaml", "workflows/"}

// GovernancePaths is a validated, case-folded set of repository-relative
// path entries. The zero value guards DefaultGovernancePaths (fail toward
// guarding: a caller that forgot to build the set still holds governance
// changes).
type GovernancePaths struct {
	// entries are folded (foldPath) and carry no trailing "/".
	entries []string
}

// ErrInvalidGovernancePath is wrapped by every NewGovernancePaths error.
var ErrInvalidGovernancePath = errors.New("branchprotect: invalid governance path entry")

// NewGovernancePaths validates entries and returns the guarded set. For
// each entry it trims surrounding spaces, strips one leading "/", maps a
// trailing "/**" to "/", and rejects:
//
//   - an empty entry (also "/" alone);
//   - an empty, "." or ".." path segment;
//   - glob or quoting metacharacters (* ? [ \ ") — entries are prefixes,
//     never patterns (path.Match does not cross "/" and cannot express
//     "**"; F1 showed how pattern matching surprises a security gate);
//   - control bytes (< 0x20, 0x7f) and invalid UTF-8.
//
// An empty or nil list gives the default set. Duplicates (after folding)
// collapse.
func NewGovernancePaths(entries []string) (GovernancePaths, error) {
	if len(entries) == 0 {
		return defaultGovernancePaths(), nil
	}
	seen := make(map[string]struct{}, len(entries))
	out := make([]string, 0, len(entries))
	for _, raw := range entries {
		e, err := normalizeGovernanceEntry(raw)
		if err != nil {
			return GovernancePaths{}, err
		}
		if _, dup := seen[e]; dup {
			continue
		}
		seen[e] = struct{}{}
		out = append(out, e)
	}
	sort.Strings(out)
	return GovernancePaths{entries: out}, nil
}

func defaultGovernancePaths() GovernancePaths {
	g, err := NewGovernancePaths(DefaultGovernancePaths)
	if err != nil {
		panic("branchprotect: DefaultGovernancePaths is invalid: " + err.Error())
	}
	return g
}

func normalizeGovernanceEntry(raw string) (string, error) {
	bad := func(why string) error {
		return fmt.Errorf("%w %q: %s", ErrInvalidGovernancePath, raw, why)
	}
	if !utf8.ValidString(raw) {
		return "", bad("invalid UTF-8")
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < 0x20 || raw[i] == 0x7f {
			return "", bad("control byte")
		}
	}
	e := strings.TrimSpace(raw)
	e = strings.TrimPrefix(e, "/")
	if strings.HasSuffix(e, "/**") {
		e = strings.TrimSuffix(e, "**")
	}
	e = strings.TrimSuffix(e, "/")
	if e == "" {
		return "", bad("empty entry")
	}
	if strings.ContainsAny(e, "*?[\\\"") {
		return "", bad("glob or quoting metacharacter (entries are path prefixes, not patterns)")
	}
	for _, seg := range strings.Split(e, "/") {
		switch seg {
		case "":
			return "", bad("empty path segment")
		case ".", "..":
			return "", bad("dot segment")
		}
	}
	return foldPath(e), nil
}

// WithDirs returns a copy of g with each dir added as a literal prefix
// entry (INIT-024 TASK-002, review finding N-5). It exists for entries the
// engine derives at decision time — the orchestrator's
// <artifacts_dir>/workflows directory and the WorkflowDefinitionDirs
// backstop — not for configuration, which goes through NewGovernancePaths.
//
// A dir is case-folded like every entry, with its leading and trailing
// "/" removed; nothing else is changed, and nothing is refused. That
// differs from NewGovernancePaths on purpose: its refusal of glob and
// quoting characters (* ? [ \ ") and of empty or dot segments keeps an
// operator from writing a pattern the guard would read as a literal, but
// these dirs come from git paths and from the artifacts directory the
// projection matches literally, and a directory named "[id]" is a legal
// git path. Refusing it would leave the backstop nothing to add — a merge
// the projection may install from, let through. Matching stays Match's:
// a literal, folded prefix compare. A dir that is empty once trimmed is
// skipped (it would guard every path). A zero-value g starts from
// DefaultGovernancePaths, as Match does.
func (g GovernancePaths) WithDirs(dirs ...string) GovernancePaths {
	base := g.entries
	if base == nil {
		base = defaultGovernancePaths().entries
	}
	seen := make(map[string]struct{}, len(base)+len(dirs))
	out := make([]string, 0, len(base)+len(dirs))
	for _, e := range base {
		seen[e] = struct{}{}
		out = append(out, e)
	}
	for _, d := range dirs {
		e := foldPath(strings.TrimRight(strings.TrimLeft(d, "/"), "/"))
		if e == "" {
			continue
		}
		if _, dup := seen[e]; dup {
			continue
		}
		seen[e] = struct{}{}
		out = append(out, e)
	}
	sort.Strings(out)
	return GovernancePaths{entries: out}
}

// workflowsDirName is the folded last segment of a directory the backstop
// treats as a workflow directory.
const workflowsDirName = "workflows"

// WorkflowDefinitionDirs is the guard's structural backstop (INIT-024
// TASK-002, review finding N-5). It returns the parent directory of every
// path that looks like a workflow definition: a leaf ending in ".yaml" or
// ".yml" directly inside a directory whose last segment is "workflows",
// at any depth, both compared case-folded like Match ("Docs/WORKFLOWS/x.YML"
// counts, and so does "worKflows" with U+212A KELVIN SIGN). Each parent is
// returned once, spelled as in the first path that named it.
//
// Why: the projection installs the direct *.yaml / *.yml children of
// workflows/ and of <artifacts_dir>/workflows/ as live workflow policy,
// and artifacts_dir is the projection's configuration, which the guard
// may never have been told (no GovernanceGuard.WorkflowDirs, no
// Orchestrator.WithArtifactsDir). Whatever artifacts_dir is, every file
// the projection could install from it has this shape, so adding these
// parents to the guarded set (GovernancePaths.WithDirs) holds the merge
// anyway. It over-approximates: a YAML file in any directory named
// "workflows" (".github/workflows/ci.yml") needs an operator approval
// too. Nested files ("workflows/steps/x.yaml") are not definitions and
// are not matched here; the artifacts-directory entry covers those.
//
// Paths Match already governs whatever the entries — "", a path
// domain.UnsafeRepositoryPath refuses, a C-quoted path that does not
// decode — are skipped; a C-quoted path that decodes is judged by its
// decoded form, as Match judges it.
func WorkflowDefinitionDirs(paths []string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, p := range paths {
		if p == "" || domain.UnsafeRepositoryPath(p) {
			continue
		}
		if strings.HasPrefix(p, `"`) {
			dec, ok := unquoteGitPath(p)
			if !ok || dec == "" || domain.UnsafeRepositoryPath(dec) {
				continue
			}
			p = dec
		}
		p = strings.TrimLeft(p, "/")
		i := strings.LastIndexByte(p, '/')
		if i <= 0 {
			continue // a root-level file has no parent directory
		}
		parent, leaf := p[:i], foldPath(p[i+1:])
		if !strings.HasSuffix(leaf, ".yaml") && !strings.HasSuffix(leaf, ".yml") {
			continue
		}
		if foldPath(parent[strings.LastIndexByte(parent, '/')+1:]) != workflowsDirName {
			continue
		}
		key := foldPath(parent)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, parent)
	}
	return out
}

// Entries returns the folded entries, each without a trailing "/", for
// logs and wiring tests.
func (g GovernancePaths) Entries() []string {
	if g.entries == nil {
		return defaultGovernancePaths().Entries()
	}
	return append([]string(nil), g.entries...)
}

// Match reports whether a git path is governed. It is fail-closed — a
// path the guard cannot read safely is governed:
//
//   - "" is governed;
//   - a path that domain.UnsafeRepositoryPath refuses (invalid UTF-8, a
//     control byte, U+2028/U+2029, a bidi control) is governed;
//   - a path beginning with `"` is git's C-quoted form (a newline-parsed
//     `git diff --name-status` quotes any path with a byte >= 0x80, `"`
//     or `\`). Since INIT-023 TASK-003 Spine's own Diff is NUL-delimited
//     and returns raw paths, but an embedder's GitOperator may not be:
//     the path is decoded (\a \b \t \n \v \f \r \" \\ and \ooo) and the
//     decoded path is matched; a path that does not decode is governed.
//     No guarded entry starts with `"` (NewGovernancePaths refuses it),
//     so decoding can only add governance, never remove it;
//   - otherwise q is the path without leading "/", case-folded (simple
//     Unicode case folding, so a case-insensitive checkout cannot dodge
//     the match: "Workflows/A.yaml", "worKflows" with U+212A KELVIN SIGN
//     and ".ſpine" with U+017F all fold onto the entries). For every
//     entry E the path matches when q == E or q has the prefix E + "/".
//     So every entry also covers what lies beneath it, whatever the entry
//     names: a file or symlink named like a guarded directory (".spine",
//     "workflows"), and anything under a guarded file name
//     (".spine.yaml/x") — the type-confusion class.
func (g GovernancePaths) Match(p string) bool {
	if p == "" || domain.UnsafeRepositoryPath(p) {
		return true
	}
	if strings.HasPrefix(p, `"`) {
		dec, ok := unquoteGitPath(p)
		if !ok || dec == "" || domain.UnsafeRepositoryPath(dec) {
			return true
		}
		p = dec
	}
	entries := g.entries
	if entries == nil {
		entries = defaultGovernancePaths().entries
	}
	q := foldPath(strings.TrimLeft(p, "/"))
	for _, e := range entries {
		if q == e || strings.HasPrefix(q, e+"/") {
			return true
		}
	}
	return false
}

// foldPath maps every rune to (the lower case of) the smallest rune of
// its simple case-fold orbit (unicode.SimpleFold), so two spellings that
// any simple case folding identifies get the same key — strings.ToLower
// alone misses U+017F LATIN SMALL LETTER LONG S, which is already lower
// case.
func foldPath(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < least {
				least = f
			}
		}
		// The orbit's smallest rune is the same for every member; its
		// lower case keeps the keys readable ("workflows", not
		// "WORKFLOWS").
		b.WriteRune(unicode.ToLower(least))
	}
	return b.String()
}

// unquoteGitPath decodes git's C-quoted path form ("a\303\244b"): the
// escapes git's quote_c_style emits. It returns false for anything else
// (no closing quote, an unknown escape, a stray quote, an octal escape
// above \377).
func unquoteGitPath(p string) (string, bool) {
	if len(p) < 2 || p[0] != '"' || p[len(p)-1] != '"' {
		return "", false
	}
	in := p[1 : len(p)-1]
	var b strings.Builder
	for i := 0; i < len(in); i++ {
		c := in[i]
		if c == '"' {
			return "", false
		}
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(in) {
			return "", false
		}
		switch in[i] {
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'v':
			b.WriteByte('\v')
		case 'f':
			b.WriteByte('\f')
		case 'r':
			b.WriteByte('\r')
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		case '0', '1', '2', '3':
			if i+2 >= len(in) {
				return "", false
			}
			v, err := strconv.ParseUint(in[i:i+3], 8, 8)
			if err != nil {
				return "", false
			}
			b.WriteByte(byte(v))
			i += 2
		default:
			return "", false
		}
	}
	return b.String(), true
}

// GovernanceApproval is an operator's approval of one bound commit, as
// the engine recorded it server-side (reserved runs.commit_meta keys).
// It is never read from commit text, branch content or workflow-authored
// data.
type GovernanceApproval struct {
	SHA        string       // the bound commit the approver approved
	Actor      domain.Actor // ActorID and Role at approval time
	ApprovedAt time.Time
}

// GovernanceCheck carries the facts of one governed merge. A nil check on
// a Request keeps the v1 behaviour (Allow, governed_merge).
type GovernanceCheck struct {
	Paths GovernancePaths
	// BoundSHA is the commit the run's merge is bound to (INIT-023
	// TASK-005): the approval binds to it.
	BoundSHA string
	// ChangedPaths are the Path and OldPath of every diff entry between
	// each merge base of (main, merged commit) and the merged commit — the
	// bound commit or the status-cascade commit built on it.
	ChangedPaths []string
	// ExemptPaths are exact paths excused by the engine (the ADR-008
	// lifecycle exemption, the own-run evidence carve-out). They are
	// compared byte-exactly, never case-folded.
	ExemptPaths []string
	Approval    *GovernanceApproval
}

// Touched returns the sorted, de-duplicated changed paths that are
// governed and not exempt. A nil check touches nothing.
func (c *GovernanceCheck) Touched() []string {
	if c == nil {
		return nil
	}
	exempt := make(map[string]struct{}, len(c.ExemptPaths))
	for _, p := range c.ExemptPaths {
		exempt[p] = struct{}{}
	}
	seen := make(map[string]struct{}, len(c.ChangedPaths))
	var out []string
	for _, p := range c.ChangedPaths {
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		if _, ok := exempt[p]; ok {
			continue
		}
		if c.Paths.Match(p) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// approvalMatches reports whether a is an operator-or-above approval of
// exactly sha.
func approvalMatches(a *GovernanceApproval, sha string) bool {
	return a != nil &&
		a.Actor.ActorID != "" &&
		a.Actor.Role.HasAtLeast(domain.RoleOperator) &&
		a.SHA != "" &&
		strings.EqualFold(a.SHA, sha)
}

// governanceMessagePaths is how many paths a deny message names.
const governanceMessagePaths = 5

// EvaluateGovernance is the guard's single decision. In order:
//
//  1. c == nil → Allow (governed_merge), the v1 behaviour.
//  2. no valid bound commit → Deny (governance_unevaluable). The engine
//     maps this to "service unavailable" and records no hold: a hold must
//     always be decidable by approve or reject, and both need the bound
//     commit. It can only arise from a caller bug — the engine evaluates
//     after TASK-005 validated the binding.
//  3. no governed path touched → Allow (governed_merge).
//  4. an operator-or-above approval of exactly the bound commit → Allow
//     (governance_approved).
//  5. otherwise → Deny (governance_approval_required), naming the count,
//     the first paths and the bound commit, and an approval recorded for
//     another commit.
func EvaluateGovernance(c *GovernanceCheck) (Decision, []Reason) {
	if c == nil {
		return DecisionAllow, []Reason{{
			Code:    ReasonGovernedMerge,
			Message: "governed merge — branch protection does not gate this operation",
		}}
	}
	if !isFullHexObjectID(c.BoundSHA) {
		return DecisionDeny, []Reason{{
			Code:    ReasonGovernanceUnevaluable,
			Message: "governance guard: the merge has no bound commit to evaluate",
		}}
	}
	touched := c.Touched()
	if len(touched) == 0 {
		return DecisionAllow, []Reason{{
			Code:    ReasonGovernedMerge,
			Message: "governed merge changes no governance path",
		}}
	}
	if approvalMatches(c.Approval, c.BoundSHA) {
		return DecisionAllow, []Reason{{
			Code: ReasonGovernanceApproved,
			Message: fmt.Sprintf("governance change at %s approved by %s (%s)",
				c.BoundSHA, c.Approval.Actor.ActorID, c.Approval.Actor.Role),
		}}
	}
	return DecisionDeny, []Reason{{
		Code:    ReasonGovernanceApprovalRequired,
		Message: GovernanceHoldMessage(c, touched),
	}}
}

// GovernanceHoldMessage renders the deny message for a held merge.
func GovernanceHoldMessage(c *GovernanceCheck, touched []string) string {
	shown := touched
	more := ""
	if len(shown) > governanceMessagePaths {
		shown = shown[:governanceMessagePaths]
		more = ", …"
	}
	msg := fmt.Sprintf("governed merge changes %d governance path(s) (%s%s) at bound commit %s: an operator must approve this commit",
		len(touched), strings.Join(quoteAll(shown), ", "), more, c.BoundSHA)
	if a := c.Approval; a != nil && a.SHA != "" && !strings.EqualFold(a.SHA, c.BoundSHA) {
		msg += fmt.Sprintf(" (approval recorded for %s, merge is bound to %s)", a.SHA, c.BoundSHA)
	}
	return msg
}

// quoteAll Go-quotes paths for messages: they are committed,
// attacker-chosen text and may hold anything a filesystem allows.
func quoteAll(ps []string) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = strconv.Quote(p)
	}
	return out
}

// isFullHexObjectID reports whether s is a full lowercase hex object id
// (SHA-1 or SHA-256). Duplicated from adapters/git (a byte loop) so
// core/branchprotect gains no import.
func isFullHexObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
