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
// merge must moreover account for every kind being removed (review
// rounds 1 and 2): its own diff removed it — its base config applied it —
// or its base config is, byte for byte, the result of an earlier valid
// record that accounts for it in turn (a chain of custody; see
// provenanceCheck). The operator approved the run's diff, which may only
// add a rule on top of an unauthorised relaxation; and two authorised
// relaxations may land before one projection tick. Why newness:
// re-pushing an old authorised blob after it was superseded must not
// replay its authorisation.
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
	pc := &provenanceCheck{s: s, records: records, head: head, newest: newest, branch: branch,
		facts: make([]*recordFacts, len(records)), failed: map[custodyKey]bool{}}
	for i := range records {
		if !pc.authorises(ctx, i, content, present, removed) {
			continue
		}
		rec := &records[i]
		log.Info("projection: protection removal authorised by an operator record",
			"branch", branch, "source", rec.Source, "record_commit", rec.CommitSHA,
			"actor_id", rec.ActorID, "run_id", rec.RunID, "event_id", rec.EventID,
			"custody_commits", pc.custodyCommits(), "head", shortSHA(head))
		return nil
	}
	if verifyErr := pc.verifyErr; verifyErr != nil {
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

// provenanceCheck applies the matching rule to the operator records for
// one guard decision. Each record's own checks (recordFacts) cost git
// reads, so they are established once and shared by the record's turn as
// the candidate's match and by every chain that passes through it.
//
// The rule. A record matches when it is valid (validWith) and its config is
// byte-for-byte the candidate. The removal it vouches for must then be
// accounted for kind by kind along a chain of custody (custody):
//
//   - an override push accounts for every kind its bytes lack: the
//     operator wrote those bytes (owner question 8's changed-the-file
//     rule; the documented re-affirmation, docs/git-push-guide.md §4);
//   - an approved governed merge accounts for the kinds its base config
//     applied to the branch — its own diff removed them (review round 1:
//     the operator approved that diff, not a relaxation it was laid over);
//   - the kinds already missing at an approved merge's base are accounted
//     for only when that base config is itself an authorised state: the
//     result, byte for byte, of another valid record whose commit the base
//     descends from, which is then judged the same way (review round 2:
//     two authorised relaxations landing before one projection tick, each
//     newer than the installed rows, were refused for good because the
//     newest record's base no longer applied the kind the earlier act had
//     removed).
//
// Bytes, not kinds, link the chain, and every link must descend in
// history: an unauthorised write anywhere between two authorised acts
// breaks it, even when it only re-drops a kind an earlier act had dropped
// and a later one restored. A looser rule — "some valid record in the
// window removed this kind" — would launder exactly that write.
//
// A retryable git failure on any record is kept in verifyErr: when nothing
// matched, the caller holds the sync rather than refuse (a refusal is not
// looked at again until the file changes), and any other failure means
// the record does not match.
type provenanceCheck struct {
	s       *Service
	records []store.GovernanceAuthorization
	head    string
	newest  string // the installed rows' source commit or the anchor; "" for none
	branch  string
	facts   []*recordFacts
	// failed memoises custody walks that failed, keyed by record and the
	// kinds still to account for. Along a chain every link's commit is an
	// ancestor of the previous approved link's base, itself a proper
	// ancestor of that link's merge commit, so a record already on the
	// path can never be a later link and excluding it changes nothing: the
	// memo is exact. With inconsistent records it can only refuse more,
	// the safe direction. The path set itself only stops cycles on such
	// records.
	failed    map[custodyKey]bool
	chain     []int // the record indexes of the last successful custody walk, oldest first
	verifyErr error
}

// recordFacts is what one record's own checks established.
type recordFacts struct {
	// eligible: an authorisation source, a full commit id, reachable from
	// the head and newer than what is installed.
	eligible bool
	content  []byte // the config at the record's commit
	present  bool
	// The base is read only for a record whose content is wanted (as the
	// candidate, or as a link of a chain), as before review round 2.
	baseRead bool
	// changed: the act changed the config (owner question 8).
	changed bool
	// created: the record has no base (BaseSHA empty or all zeros) — the
	// act created the branch, so the state before it is the bootstrap
	// defaults and nothing older can be chained to.
	created     bool
	baseContent []byte
	basePresent bool
}

type custodyKey struct {
	record int
	kinds  string
}

// noteErr keeps the first retryable failure.
func (p *provenanceCheck) noteErr(ctx context.Context, err error) {
	if err = transientOnly(ctx, err); err != nil && p.verifyErr == nil {
		p.verifyErr = err
	}
}

// validWith reports whether record i is valid and its config is exactly
// (content, present), establishing each check once. A record whose checks
// failed is invalid (a retryable failure is noted).
func (p *provenanceCheck) validWith(ctx context.Context, i int, content []byte, present bool) bool {
	f := p.facts[i]
	if f == nil {
		var err error
		if f, err = p.eligibility(ctx, &p.records[i]); err != nil {
			p.noteErr(ctx, err)
			f = &recordFacts{}
		}
		p.facts[i] = f
	}
	if !f.eligible || f.present != present || (present && !bytes.Equal(f.content, content)) {
		return false
	}
	if !f.baseRead {
		if err := p.readBase(ctx, &p.records[i], f); err != nil {
			p.noteErr(ctx, err)
			f.eligible = false
			return false
		}
	}
	return f.changed
}

func (p *provenanceCheck) eligibility(ctx context.Context, rec *store.GovernanceAuthorization) (*recordFacts, error) {
	invalid := &recordFacts{}
	switch rec.Source {
	case store.GovernanceSourceOverrideEvent, store.GovernanceSourceApprovedMerge:
	default:
		return invalid, nil // a pending approval, or a source this guard does not know, vouches for nothing
	}
	c := rec.CommitSHA
	if !isHexSHA(c) {
		return invalid, nil
	}
	// Reachable: the record's commit is in the history being installed.
	if ok, err := p.ancestor(ctx, c, p.head); err != nil || !ok {
		return invalid, err
	}
	// New: not already behind what is installed (or synced).
	if p.newest != "" {
		if old, err := p.ancestor(ctx, c, p.newest); err != nil || old {
			return invalid, err
		}
	}
	content, present, err := p.s.configAt(ctx, c)
	if err != nil {
		return invalid, err
	}
	return &recordFacts{eligible: true, content: content, present: present}, nil
}

// readBase reads the record's base config and decides "changed".
func (p *provenanceCheck) readBase(ctx context.Context, rec *store.GovernanceAuthorization, f *recordFacts) error {
	f.baseRead = true
	if base := rec.BaseSHA; base == "" || isZeroSHA(base) {
		// The config was created by the act (its base is the bootstrap
		// defaults).
		f.created = true
		f.changed = f.present
		return nil
	} else if !isHexSHA(base) {
		return nil // changed stays false: the record does not match
	}
	var err error
	f.baseContent, f.basePresent, err = p.s.configAt(ctx, rec.BaseSHA)
	if err != nil {
		return err
	}
	f.changed = f.basePresent != f.present || (f.present && !bytes.Equal(f.baseContent, f.content))
	return nil
}

// ancestor reports whether a is b or an ancestor of b.
func (p *provenanceCheck) ancestor(ctx context.Context, a, b string) (bool, error) {
	mb, err := p.s.git.MergeBase(ctx, a, b)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(mb) == a, nil
}

// authorises reports whether record i vouches for installing the candidate
// (content, present), which removes the kinds in removed from the branch.
func (p *provenanceCheck) authorises(ctx context.Context, i int, content []byte, present bool, removed map[bpconfig.RuleKind]bool) bool {
	if !p.validWith(ctx, i, content, present) {
		return false
	}
	p.chain = nil
	return p.custody(ctx, i, removed, map[int]bool{i: true})
}

// custody reports whether the kinds in missing — absent from record i's
// resulting config, present in the installed rules — are accounted for by
// record i and the authorised states before it (the rule on
// provenanceCheck). Record i is valid.
func (p *provenanceCheck) custody(ctx context.Context, i int, missing map[bpconfig.RuleKind]bool, onPath map[int]bool) bool {
	rec, f := &p.records[i], p.facts[i]
	if rec.Source == store.GovernanceSourceOverrideEvent {
		p.chain = append(p.chain, i)
		return true
	}
	if f.created {
		if !protectsAll(nil, false, p.branch, missing) {
			return false
		}
		p.chain = append(p.chain, i)
		return true
	}
	// The kinds this approved diff removed itself are accounted for.
	baseKinds := configKinds(f.baseContent, f.basePresent, p.branch)
	left := map[bpconfig.RuleKind]bool{}
	for k := range missing {
		if !baseKinds[k] {
			left[k] = true
		}
	}
	if len(left) == 0 {
		p.chain = append(p.chain, i)
		return true
	}
	key := custodyKey{record: i, kinds: strings.Join(kindNames(left), ",")}
	if p.failed[key] {
		return false
	}
	// The rest were already missing at the base: that state must be the
	// result of an earlier authorised act the base descends from.
	for j := range p.records {
		if onPath[j] {
			continue
		}
		if !p.validWith(ctx, j, f.baseContent, f.basePresent) {
			continue
		}
		earlier, err := p.ancestor(ctx, p.records[j].CommitSHA, rec.BaseSHA)
		if err != nil {
			p.noteErr(ctx, err)
			continue
		}
		if !earlier {
			continue
		}
		onPath[j] = true
		ok := p.custody(ctx, j, left, onPath)
		delete(onPath, j)
		if ok {
			p.chain = append(p.chain, i)
			return true
		}
	}
	p.failed[key] = true
	return false
}

// custodyCommits lists the commits of the last successful custody walk,
// newest first, for the log.
func (p *provenanceCheck) custodyCommits() []string {
	out := make([]string, 0, len(p.chain))
	for n := len(p.chain) - 1; n >= 0; n-- {
		out = append(out, shortSHA(p.records[p.chain[n]].CommitSHA))
	}
	return out
}

// configKinds is the union of the protection kinds a config (absent: the
// bootstrap defaults) applies to branch. A config that does not parse
// applies nothing here: a record whose base cannot be read as rules cannot
// be shown to have removed anything.
func configKinds(content []byte, present bool, branch string) map[bpconfig.RuleKind]bool {
	rules := bootstrapCandidate()
	if present {
		cfg, err := bpconfig.Parse(bytes.NewReader(content))
		if err != nil {
			return map[bpconfig.RuleKind]bool{}
		}
		rules = cfg.Rules
	}
	return kindsFor(rules, branch)
}

// protectsAll reports whether a config (absent: the bootstrap defaults)
// applies every kind in kinds to branch.
func protectsAll(content []byte, present bool, branch string, kinds map[bpconfig.RuleKind]bool) bool {
	have := configKinds(content, present, branch)
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
