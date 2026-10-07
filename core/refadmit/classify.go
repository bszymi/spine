// Package refadmit decides, per pushed ref, whether a principal may
// create, update or delete it at a git gateway (INIT-023 TASK-016,
// audit F2 push half, F10 gateway half and the F8 divergence-ref
// vector).
//
// It is the ref-class admission that runs BEFORE branchprotect.Policy
// on every receive-pack ref update, whether or not a policy is wired:
//
//   - Classify sorts a ref into a Class from its bytes alone (no store).
//   - Evaluator.Decide applies the per-class rule: ordinary branches go
//     on to branch protection unchanged; tags, notes and every other
//     non-branch ref, `spine/init` and the rest of the reserved `spine`
//     namespace are operator-only; a run, planning or divergence branch
//     may be written only by the actor bound to its current step (the
//     latest attempt, assigned or in progress, on an active run) or by
//     an operator, and deleted only by an operator.
//
// The package is pure and transport-agnostic: it imports only
// core/domain and the standard library, never logs, and reads the
// store only through the RunReader and StepDefLookup ports. That is
// what lets SMP's own gateway (INIT-027 EPIC-004 TASK-007) adopt the
// same rule later instead of a drifting copy (TASK-016 I5).
package refadmit

import (
	"strings"
)

// Class is the admission class of a ref. Classify assigns it from the
// ref's bytes alone.
type Class string

const (
	// ClassBranch is an ordinary branch: refs/heads/<x> with <x> outside
	// the reserved spine namespace. Admission defers to
	// branchprotect.Policy for it, unchanged (TASK-016 I3).
	ClassBranch Class = "branch"
	// ClassRunBranch is a run or planning branch:
	// refs/heads/spine/run/<name> or refs/heads/spine/plan/<name>, where
	// <name> carries the run ID's hex suffix after its last '-'
	// (core/engine/branchname.go).
	ClassRunBranch Class = "run_branch"
	// ClassDivergenceBranch is a divergence branch:
	// refs/heads/spine/<run-id>/<divergence-id>/<branch-id>
	// (core/divergence/service.go createBranchRecord).
	ClassDivergenceBranch Class = "divergence_branch"
	// ClassInitBranch is refs/heads/spine/init, the bootstrap branch
	// `spine init-repo` writes. Operator-only.
	ClassInitBranch Class = "init_branch"
	// ClassReserved is every other ref in the reserved spine namespace:
	// refs/heads/spine itself, any case-fold spelling of the namespace,
	// malformed run/plan names, and over-long names. Operator-only.
	ClassReserved Class = "reserved"
	// ClassNonBranch is any ref outside refs/heads/: tags, notes,
	// refs/<x>, refs/remotes/*. Operator-only.
	ClassNonBranch Class = "non_branch"
	// ClassInvalid is a ref carrying an ASCII control byte (0x00-0x1f,
	// 0x7f). Refused for every principal, operators included (TASK-016
	// DC-2).
	ClassInvalid Class = "invalid"
)

// Ref is the syntactic classification of one ref name.
type Ref struct {
	// Full is the ref exactly as given.
	Full string
	// Short is the branch name without "refs/heads/" for a ref under
	// refs/heads/, Full for any other ref, and empty for ClassInvalid
	// (whose bytes must never be echoed raw).
	Short string
	// Class is the admission class.
	Class Class
	// RunID is the run the ref belongs to, for ClassRunBranch
	// ("run-" + the name's suffix after its last '-') and
	// ClassDivergenceBranch (the first component after "spine/").
	// It is only a lookup key: Decide refuses unless the run it names
	// really owns this exact branch.
	RunID string
	// DivergencePath is "<divergence-id>/<branch-id…>" for
	// ClassDivergenceBranch.
	DivergencePath string
	// Shadowing reports that the ref has a "refs" path component other
	// than the first, or any "HEAD" path component (exact case), such as
	// refs/heads/refs/heads/main or refs/tags/HEAD. git's ref
	// abbreviation rules make such a name stand in for, or collide with,
	// another ref: next to an existing branch it makes every client push
	// and delete of that branch fail with "matches more than one", and
	// it substitutes for a missing one (INIT-023 TASK-002 DC-3 / D12).
	// Decide refuses creating or updating such a ref for every
	// principal; deleting one stays possible so it can be cleaned up.
	Shadowing bool
}

const (
	headsPrefix = "refs/heads/"
	// namespace is the reserved branch namespace, matched against the
	// first path component of a branch name (TASK-016 §1.1).
	namespace = "spine"
	// maxReservedShort bounds a branch name in the reserved namespace.
	// Longer names are ClassReserved before any shape match, which keeps
	// the store lookups and the echoed wire text of a run or divergence
	// ref bounded (TASK-016 DC-4). Engine-made names are about 80 bytes
	// at most (core/engine/branchname.go maxSlugLength = 60).
	maxReservedShort = 1024
)

// Classify sorts ref into its admission class. It is syntactic only:
// it never consults a store, and it is total (every string gets a
// class).
//
// Order matters:
//
//  1. Any ASCII control byte makes the ref ClassInvalid. git ends a
//     receive-pack command's ref at its first NUL (audit F1), so
//     "refs/heads/spine\x00" would otherwise classify as an ordinary
//     branch while git creates refs/heads/spine (the run-creation D/F
//     denial of service). The pre-receive parser already refuses such
//     refs (INIT-023 TASK-001); this is defence in depth, so this
//     package's guarantees do not depend on that ordering (DC-2).
//  2. A ref outside refs/heads/ is ClassNonBranch (this covers HEAD,
//     refs/HEADS/main and every non-branch name the parser admits).
//  3. A branch whose first path component is not a case-fold spelling
//     of "spine" is ClassBranch.
//  4. Everything else is in the reserved namespace: ClassReserved
//     unless it is exactly one of the engine's own shapes.
//
// The namespace test folds case with Unicode simple folding as well as
// strings.ToLower (DC-1). On a case-insensitive server filesystem
// (macOS APFS, Windows, a Docker Desktop bind mount of either) "SPINE",
// "Spine" and "ſpine" (U+017F LATIN SMALL LETTER LONG S) name the same
// directory as "spine", and git refuses only a colliding *create*: an
// update that carries the ref's current object id lands on the real
// refs/heads/spine/... ref. strings.ToLower alone leaves U+017F alone;
// strings.EqualFold catches it, and ToLower additionally catches
// U+0130, which lower-cases to "i" but is outside the fold orbit of "i".
// A case-variant spelling is never one of the engine's shapes, so it
// is ClassReserved (operator-only), never a run branch an actor could
// be bound to. The shape matches below are exact and case-sensitive.
func Classify(ref string) Ref {
	r := Ref{Full: ref}
	if hasControlByte(ref) {
		r.Class = ClassInvalid
		return r
	}
	r.Shadowing = isShadowing(ref)

	if !strings.HasPrefix(ref, headsPrefix) {
		r.Short = ref
		r.Class = ClassNonBranch
		return r
	}
	short := ref[len(headsPrefix):]
	r.Short = short
	first, rest, hasRest := strings.Cut(short, "/")
	if !foldsToNamespace(first) {
		r.Class = ClassBranch
		return r
	}

	// Reserved namespace. Only an exact, case-sensitive engine shape of
	// bounded length escapes ClassReserved.
	r.Class = ClassReserved
	if first != namespace || !hasRest || len(short) > maxReservedShort {
		return r
	}
	if rest == "init" {
		r.Class = ClassInitBranch
		return r
	}
	kind, name, ok := strings.Cut(rest, "/")
	if !ok || kind == "" {
		// spine/<one-component>: spine/run, spine/plan, spine/other,
		// spine/run-0a1b2c3d — each a D/F blocker for a whole subtree.
		return r
	}
	switch kind {
	case "run", "plan":
		// spine/run/<name>: <name> is one component ending in
		// "-<run-id hex>" (generateBranchNameWithSuffix). The engine
		// always appends the suffix, so a name with no '-' or a
		// trailing '-' is not one of its branches.
		if name == "" || strings.Contains(name, "/") {
			return r
		}
		i := strings.LastIndexByte(name, '-')
		if i < 0 || i == len(name)-1 {
			return r
		}
		r.Class = ClassRunBranch
		r.RunID = "run-" + name[i+1:]
	case "init":
		// spine/init/... is not a divergence ref (DC-3); it only
		// collides with the init branch.
		return r
	default:
		// spine/<run-id>/<divergence-id>/<branch-id…>.
		div, branch, ok := strings.Cut(name, "/")
		if !ok || div == "" || branch == "" {
			return r
		}
		r.Class = ClassDivergenceBranch
		r.RunID = kind
		r.DivergencePath = name
	}
	return r
}

// foldsToNamespace reports whether c is a spelling of "spine" that a
// case-insensitive filesystem could alias onto it (see Classify, DC-1).
func foldsToNamespace(c string) bool {
	return strings.EqualFold(c, namespace) || strings.ToLower(c) == namespace
}

// hasControlByte reports whether s holds an ASCII control byte
// (0x00-0x1f or 0x7f). It is a byte loop, so invalid UTF-8 cannot hide
// one. core/ must not import adapters/git/refname, hence the local
// copy of the check refname.ValidateQualified also makes.
func hasControlByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if b := s[i]; b < 0x20 || b == 0x7f {
			return true
		}
	}
	return false
}

// isShadowing reports whether ref has a "refs" path component other
// than the first, or any "HEAD" component (INIT-023 TASK-002 D12; the
// same rule SMP's gateway applies, E4-TASK-005 §7). Exact case, as
// git's refspec and ref-abbreviation matching compare names byte-wise.
func isShadowing(ref string) bool {
	for i, c := range strings.Split(ref, "/") {
		if c == "HEAD" || (i > 0 && c == "refs") {
			return true
		}
	}
	return false
}
