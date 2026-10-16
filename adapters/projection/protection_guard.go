package projection

// Protection-removal guard (ADR-016 §6; INIT-023 TASK-006, audit F3,
// projection half).
//
// The defect. projectBranchProtection installed whatever config reached
// the authoritative branch: a run branch that slipped `rules: []` into
// main (a bypassed review, a missing merge guard, a direct push to the
// bare repository) left zero rows, which the rule source reads as
// "explicitly unprotected", and any contributor could then direct-write or
// delete main. The projection never asked how the config arrived.
//
// The guard, opt-in through WithProtectionRemovalGuard (zero value:
// today's behaviour; `spine serve` turns it on): a candidate config that
// removes a protection kind the installed rules apply to the
// authoritative branch is installed only when a DATABASE record of an
// operator act that CHANGED the config matches it —
//
//   - an honoured branch_protection.override push (runtime.event_log)
//     whose old→new range changed .spine/branch-protection.yaml, or
//   - an approved governed merge whose recorded BaseSHA→merge range
//     changed it (the consumption marker the engine writes before the
//     push, runtime.runs.commit_meta).
//
// A record matches when its commit is a full id, reachable from the sync
// head, newer than what is installed (the installed rows' source commit;
// with bootstrap rows installed, the commit the sync started from), and
// its config bytes equal the candidate's. Commit trailers are never used:
// any run-branch author can forge them.
//
// Why "changed the config" (owner question 8): otherwise any later
// operator act — a hotfix override push, an approved workflow edit — would
// produce a commit whose tree still carries an unauthorised relaxation
// already sitting on main, and vouch for it (laundering). An approved
// merge must moreover have removed the protection itself — its base
// config applied every kind being removed (review round 1) — because the
// operator approved the run's diff, which may only add a rule on top of
// that relaxation. Why newness: re-pushing an old authorised blob after
// it was superseded must not replay its authorisation.
//
// While an approved governed merge is in flight — its run still
// committing, its approved commit on the head, its consumption marker not
// yet written — an unmatched removal holds the sync for a retry instead
// (approvedMergeInFlight; review round 1).
//
// A refusal keeps the previous rules, emits
// branch_protection.removal_refused and logs a Warn, and is NOT a sync
// error (owner question 7, INIT-023 TASK-011 D2): counting it would freeze
// last_synced_commit, so the diff window would grow every tick, and SMP's
// governed document index, which reconciles only at idle, would stop for
// the whole workspace. It is re-evaluated on the next change to the file
// and on every FullRebuild.
//
// While the installed rules carry no no-direct-write on the authoritative
// branch the guard is inactive (ADR-009 §1 bullet 3: self-protection is
// already off, and direct pushes then produce no override record).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/branchprotect"
	bpconfig "github.com/bszymi/spine/core/branchprotect/config"
	bpproj "github.com/bszymi/spine/core/branchprotect/projection"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
)

// GovernanceProvenanceReader lists the operator records the guard checks.
// *store.PostgresStore implements it; it is not part of store.Store, so
// no embedder's store has to change.
type GovernanceProvenanceReader interface {
	ListGovernanceAuthorizations(ctx context.Context, branch string, limit int) ([]store.GovernanceAuthorization, error)
}

// ProtectionRemovalGuard configures WithProtectionRemovalGuard.
type ProtectionRemovalGuard struct {
	Enabled bool
	// AuthoritativeBranch is the short branch name whose protection the
	// guard keeps. "" means the projection's WithAuthoritativeBranch
	// value, or "main" without one.
	AuthoritativeBranch string
	// Provenance reads the operator records. nil while Enabled refuses
	// every removal (fail closed).
	Provenance GovernanceProvenanceReader
	// Events receives branch_protection.removal_refused; nil uses the
	// service's event router (neither: the Warn log only).
	Events event.Emitter
	// Lookback is how many records per source are read; 0 means 200.
	Lookback int
}

// ErrProtectionRemovalUnauthorized is what projectBranchProtection returns
// for a refused removal. FullRebuild and IncrementalSync log it at Warn
// and do not count it as a sync error.
var ErrProtectionRemovalUnauthorized = errors.New(
	"projection: branch-protection config removes protection from the authoritative branch without an operator record")

const defaultProvenanceLookback = 200

// WithProtectionRemovalGuard configures the guard (Enabled false turns it
// off). Call it during wiring, before the service is shared.
func (s *Service) WithProtectionRemovalGuard(g ProtectionRemovalGuard) {
	if !g.Enabled {
		s.removalGuard = nil
		return
	}
	cp := g
	s.removalGuard = &cp
}

// ProtectionRemovalGuardEnabled reports whether the guard is on.
func (s *Service) ProtectionRemovalGuardEnabled() bool { return s.removalGuard != nil }

// WorkflowDirs returns the directories this service installs workflow
// definitions from (WorkflowDirs of its artifacts_dir), for wiring the
// engine's governance-path guard over exactly the same set.
func (s *Service) WorkflowDirs() []string { return WorkflowDirs(s.scope.dir) }

// staticRows adapts already-read rows to the rule source's reader, so the
// guard decodes them exactly as the policy's RuleSource does, from one
// read.
type staticRows []store.BranchProtectionRuleProjection

func (r staticRows) ListBranchProtectionRules(context.Context) ([]store.BranchProtectionRuleProjection, error) {
	return r, nil
}

// guardProtectionRemoval decides whether the candidate config may replace
// the installed rules at head. candidate is the config to install
// (BootstrapDefaults when the file is absent: present false, content nil);
// anchor is the commit this sync started from (newness when the installed
// rows are the bootstrap ones). It returns nil to install, an error
// wrapping ErrProtectionRemovalUnauthorized for a refusal (event emitted),
// or another error for an infrastructure failure (the caller holds the
// sync and retries; the rules are untouched either way).
func (s *Service) guardProtectionRemoval(ctx context.Context, head, anchor string, candidate []bpconfig.Rule, content []byte, present bool) error {
	g := s.removalGuard
	if g == nil {
		return nil
	}
	log := observe.Logger(ctx)
	rows, err := s.store.ListBranchProtectionRules(ctx)
	if err != nil {
		return fmt.Errorf("protection-removal guard: read installed rules: %w", err)
	}
	src, err := bpproj.New(staticRows(rows))
	if err != nil {
		return fmt.Errorf("protection-removal guard: %w", err)
	}
	installed, err := src.Rules(ctx)
	if err != nil {
		return fmt.Errorf("protection-removal guard: decode installed rules: %w", err)
	}
	installedSource := ""
	if len(rows) > 0 {
		installedSource = rows[0].SourceCommit
	}
	branch := g.AuthoritativeBranch
	if branch == "" {
		branch = s.authoritativeBranch
	}
	if branch == "" {
		branch = "main"
	}
	oldKinds := kindsFor(installed, branch)
	if !oldKinds[bpconfig.KindNoDirectWrite] {
		return nil // self-protection already off (ADR-009 §1 bullet 3)
	}
	newKinds := kindsFor(candidate, branch)
	removed := map[bpconfig.RuleKind]bool{}
	for k := range oldKinds {
		if !newKinds[k] {
			removed[k] = true
		}
	}
	if len(removed) == 0 {
		return nil // tightening, reordering, other branches: no provenance needed
	}

	newest := installedSource
	if !isHexSHA(newest) {
		newest = anchor // bootstrap rows: the commit this sync started from
	}
	if !isHexSHA(newest) {
		newest = "" // a fresh database: no anchor (accepted gap, ADR-016 §8)
	}

	var records []store.GovernanceAuthorization
	if g.Provenance != nil {
		lookback := g.Lookback
		if lookback <= 0 {
			lookback = defaultProvenanceLookback
		}
		records, err = g.Provenance.ListGovernanceAuthorizations(ctx, branch, lookback)
		if err != nil {
			return fmt.Errorf("protection-removal guard: read operator records: %w", err)
		}
	}
	var verifyErr error
	for _, rec := range records {
		ok, err := s.provenanceMatches(ctx, rec, head, newest, content, present, branch, removed)
		if err != nil && verifyErr == nil {
			verifyErr = err
		}
		if ok {
			log.Info("projection: protection removal authorised by an operator record",
				"branch", branch, "source", rec.Source, "record_commit", rec.CommitSHA,
				"actor_id", rec.ActorID, "run_id", rec.RunID, "event_id", rec.EventID, "head", shortSHA(head))
			return nil
		}
	}
	if verifyErr != nil {
		// A record could not be checked because git failed transiently
		// (codex review round 1): an infrastructure error, not a refusal —
		// a refusal advances the sync and would not look at this config
		// again until the file changes, losing an authorised relaxation.
		return fmt.Errorf("protection-removal guard: verify an operator record: %w", verifyErr)
	}
	// An approved governed merge in flight (review round 1, finding F4 and
	// codex): the engine moves the local main to the merge commit before
	// it records the consumption marker, and a projection following the
	// local main can sync in between. Refusing there advanced the sync
	// past the approved merge for good — and with bootstrap rows installed
	// even a full rebuild then took the late record for a replay. The sync
	// is held instead (an infrastructure retry, the rules untouched) and
	// the next tick decides with the marker in place.
	if rec, err := s.approvedMergeInFlight(ctx, records, head); err != nil {
		return fmt.Errorf("protection-removal guard: check for an approved merge in flight: %w", err)
	} else if rec != nil {
		log.Info("projection: an approved governed merge is on the head without its consumption marker yet; protection change deferred to the next sync",
			"branch", branch, "run_id", rec.RunID, "approved_commit", rec.CommitSHA, "head", shortSHA(head))
		return fmt.Errorf("protection-removal guard: an approved governed merge (run %s, commit %s) is on the head without its consumption marker yet; retrying",
			rec.RunID, shortSHA(rec.CommitSHA))
	}

	installedNames, candidateNames := kindNames(oldKinds), kindNames(newKinds)
	log.Warn("projection: refused to install a branch-protection config that removes protection from the authoritative branch without an operator record; the previous rules stay installed",
		"branch", branch, "installed_kinds", installedNames, "candidate_kinds", candidateNames,
		"head", shortSHA(head), "installed_source_commit", installedSource, "records_checked", len(records),
		"provenance_configured", g.Provenance != nil)
	emitter := g.Events
	if emitter == nil && s.events != nil {
		emitter = s.events
	}
	if emitter != nil {
		payload, _ := json.Marshal(map[string]any{
			"branch":                  branch,
			"installed_kinds":         installedNames,
			"candidate_kinds":         candidateNames,
			"head_commit":             head,
			"installed_source_commit": installedSource,
			"records_checked":         len(records),
		})
		event.EmitLogged(ctx, emitter, domain.Event{
			EventID: "bp-removal-refused-" + shortSHA12(head),
			Type:    domain.EventBranchProtectionRemovalRefused,
			Payload: payload,
		})
	}
	return fmt.Errorf("%w (branch %s, head %s, %d record(s) checked)", ErrProtectionRemovalUnauthorized, branch, shortSHA(head), len(records))
}

// provenanceMatches applies the matching rule to one record. A git
// failure that is retryable (a lock, the network, a cancelled context)
// is returned as an error — the caller holds the sync so the record is
// checked again — and any other failure (a commit that does not exist in
// this repository, an unreadable entry) means the record does not match.
//
// An approved governed merge must also have performed the removal itself
// (review round 1): the config at its BaseSHA — the target before the
// merge — must apply every kind being removed (removed) to branch.
// Otherwise an unauthorised relaxation already sitting on main (refused,
// the rules kept) was installed by the next approved merge that touched
// the file at all, even one whose own diff only adds a rule — the
// operator approved that diff, not the relaxation it was laid over. An
// override push keeps owner question 8's rule (the push changed the
// file): the operator wrote the bytes being installed, and that is the
// documented re-affirmation (docs/git-push-guide.md §4).
func (s *Service) provenanceMatches(ctx context.Context, rec store.GovernanceAuthorization, head, newest string, content []byte, present bool, branch string, removed map[bpconfig.RuleKind]bool) (bool, error) {
	switch rec.Source {
	case store.GovernanceSourceOverrideEvent, store.GovernanceSourceApprovedMerge:
	default:
		return false, nil // a pending approval, or a source this guard does not know, vouches for nothing
	}
	c := rec.CommitSHA
	if !isHexSHA(c) {
		return false, nil
	}
	// Reachable: the record's commit is in the history being installed.
	mb, err := s.git.MergeBase(ctx, c, head)
	if err != nil {
		return false, transientOnly(ctx, err)
	}
	if strings.TrimSpace(mb) != c {
		return false, nil
	}
	// New: not already behind what is installed (or synced).
	if newest != "" {
		mb, err := s.git.MergeBase(ctx, c, newest)
		if err != nil {
			return false, transientOnly(ctx, err)
		}
		if strings.TrimSpace(mb) == c {
			return false, nil
		}
	}
	// Content: the record's config is byte-for-byte the candidate (or,
	// for a deleted file, the record deleted it too).
	recContent, recPresent, err := s.configAt(ctx, c)
	if err != nil {
		return false, transientOnly(ctx, err)
	}
	if recPresent != present || (present && !bytes.Equal(recContent, content)) {
		return false, nil
	}
	// Changed: the act itself changed the config (owner question 8).
	base := rec.BaseSHA
	if base == "" || isZeroSHA(base) {
		// The config was created by the act (its base is the bootstrap
		// defaults).
		return recPresent && (rec.Source != store.GovernanceSourceApprovedMerge || protectsAll(nil, false, branch, removed)), nil
	}
	if !isHexSHA(base) {
		return false, nil
	}
	baseContent, basePresent, err := s.configAt(ctx, base)
	if err != nil {
		return false, transientOnly(ctx, err)
	}
	if basePresent == recPresent && (!recPresent || bytes.Equal(baseContent, recContent)) {
		return false, nil // the act did not change the config
	}
	if rec.Source == store.GovernanceSourceApprovedMerge && !protectsAll(baseContent, basePresent, branch, removed) {
		return false, nil // the approved diff did not remove the protection
	}
	return true, nil
}

// protectsAll reports whether a config (absent: the bootstrap defaults)
// applies every kind in kinds to branch. A config that does not parse
// protects nothing here: a record whose base cannot be read as rules
// cannot be shown to have removed anything.
func protectsAll(content []byte, present bool, branch string, kinds map[bpconfig.RuleKind]bool) bool {
	rules := bootstrapCandidate()
	if present {
		cfg, err := bpconfig.Parse(bytes.NewReader(content))
		if err != nil {
			return false
		}
		rules = cfg.Rules
	}
	have := kindsFor(rules, branch)
	for k := range kinds {
		if !have[k] {
			return false
		}
	}
	return true
}

// approvedMergeInFlight returns the pending approval
// (store.GovernanceSourcePendingApproval: a run still committing with an
// approval recorded) whose approved commit is reachable from head while
// no consumption marker of it is — the approved merge has moved the
// branch but not yet recorded its marker — or nil. A retryable git
// failure is returned (the caller holds the sync); any other failure on a
// record skips it.
//
// Residual (KNOWN-LIMITATIONS "Governance-Path Guard" 13): while such a
// run stays committing in that state — its marker write failed and the
// take-back too (the engine then blocks governed merges), or its approved
// commit reached the head through another merge — a pending protection
// removal holds the sync instead of being decided.
func (s *Service) approvedMergeInFlight(ctx context.Context, records []store.GovernanceAuthorization, head string) (*store.GovernanceAuthorization, error) {
	reachable := func(c string) (bool, error) {
		mb, err := s.git.MergeBase(ctx, c, head)
		if err != nil {
			return false, transientOnly(ctx, err)
		}
		return strings.TrimSpace(mb) == c, nil
	}
	for i := range records {
		rec := &records[i]
		if rec.Source != store.GovernanceSourcePendingApproval || !isHexSHA(rec.CommitSHA) {
			continue
		}
		on, err := reachable(rec.CommitSHA)
		if err != nil {
			return nil, err
		}
		if !on {
			continue
		}
		if isHexSHA(rec.MergeSHA) {
			marked, err := reachable(rec.MergeSHA)
			if err != nil {
				return nil, err
			}
			if marked {
				continue // the marker is in place: the approved-merge record decides
			}
		}
		return rec, nil
	}
	return nil, nil
}

// errUnreadableConfig marks a config entry that is not a readable file.
var errUnreadableConfig = errors.New("branch-protection config entry is not a readable file")

// configAt reads the branch-protection config at commit: its bytes and
// whether it exists.
func (s *Service) configAt(ctx context.Context, commit string) ([]byte, bool, error) {
	files, err := s.git.ListFiles(ctx, commit, BranchProtectionConfigPath)
	if err != nil {
		return nil, false, err
	}
	if !containsPath(files, BranchProtectionConfigPath) {
		return nil, false, nil
	}
	content, unreadable, err := s.readFileEntry(ctx, commit, BranchProtectionConfigPath)
	if err != nil {
		return nil, false, err
	}
	if unreadable != nil {
		return nil, false, errUnreadableConfig
	}
	return content, true, nil
}

// transientOnly returns err when it is worth retrying (a retryable git
// error or a cancelled context) and nil otherwise.
func transientOnly(ctx context.Context, err error) error {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var gitErr *git.GitError
	if errors.As(err, &gitErr) && gitErr.IsRetryable() {
		return err
	}
	return nil
}

// kindsFor is the union of the protection kinds the rules apply to branch.
func kindsFor(rules []bpconfig.Rule, branch string) map[bpconfig.RuleKind]bool {
	cfg := &bpconfig.Config{Rules: rules}
	out := map[bpconfig.RuleKind]bool{}
	for _, r := range cfg.MatchRules(branch) {
		for _, k := range r.Protections {
			out[k] = true
		}
	}
	return out
}

func kindNames(kinds map[bpconfig.RuleKind]bool) []string {
	out := []string{}
	for _, k := range []bpconfig.RuleKind{bpconfig.KindNoDelete, bpconfig.KindNoDirectWrite} {
		if kinds[k] {
			out = append(out, string(k))
		}
	}
	for k := range kinds {
		if k != bpconfig.KindNoDelete && k != bpconfig.KindNoDirectWrite {
			out = append(out, string(k))
		}
	}
	return out
}

func isZeroSHA(s string) bool {
	return (len(s) == 40 || len(s) == 64) && strings.Trim(s, "0") == ""
}

func shortSHA12(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// bootstrapCandidate is the candidate a deleted config installs.
func bootstrapCandidate() []bpconfig.Rule { return branchprotect.BootstrapDefaults() }
