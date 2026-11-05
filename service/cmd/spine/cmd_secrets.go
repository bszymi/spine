package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/bszymi/spine/adapters/secrets"
	"github.com/bszymi/spine/adapters/store"
	spinecrypto "github.com/bszymi/spine/core/crypto"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/service/internal/workspace"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/spf13/cobra"
)

// secretsCmd groups operator commands for at-rest secrets.
func secretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage at-rest secrets (webhook signing secrets)",
	}
	cmd.AddCommand(secretsReencryptCmd())
	return cmd
}

// secretsReencryptCmd is `spine secrets reencrypt [--all-workspaces]`
// (INIT-023 TASK-009 AC23). It runs the same bind-and-sweep that
// `spine serve` runs whenever it opens a workspace store
// (workspace.PrepareSubscriptionSecrets), with the same key ring
// (SPINE_SECRET_ENCRYPTION_KEY + SPINE_SECRET_PREVIOUS_KEYS) and mode
// (SPINE_SECRET_ENVELOPE_MODE — which this command requires to be set
// explicitly; see loadExplicitSecretEnvelopeMode), but on demand and for
// every workspace at once. In shared mode `serve` sweeps a workspace only
// when it loads it, so this is how an operator proves every workspace is
// clean before switching to strict or dropping a previous key; flipping
// early would make an idle workspace's legacy rows unreadable and stop its
// webhook fan-out.
//
// It prints one line of counters per workspace and one line per refused
// row — never a secret or a stored value; database-sourced text
// (subscription ids, reasons, errors) is quoted so a crafted value cannot
// forge or erase output lines — and exits non-zero unless every workspace's
// sweep is clean.
func secretsReencryptCmd() *cobra.Command {
	var opts reencryptOptions
	cmd := &cobra.Command{
		Use:   "reencrypt",
		Short: "Re-encrypt webhook signing secrets to the configured envelope and primary key",
		// A refused row or a failed workspace is an outcome, not a usage
		// error: print the error without the usage text.
		SilenceUsage: true,
		Long: `Re-encrypt every runtime.event_subscriptions.signing_secret to the envelope of
SPINE_SECRET_ENVELOPE_MODE (migrate/strict: enc:v2, downgrade: enc:v1) under the
primary key of SPINE_SECRET_ENCRYPTION_KEY, bound to the workspace id.

SPINE_SECRET_ENVELOPE_MODE must be set explicitly, to the mode spine serve runs
with: the command writes every workspace database, and an unset value would
apply migrate, which converts legacy rows a strict deployment refuses.

Without --all-workspaces it sweeps the single file-mode workspace, bound to
SPINE_WORKSPACE_ID (database from SPINE_DATABASE_URL or the configured secret
backend). The workspace id must be stated explicitly, like the mode: set
SPINE_WORKSPACE_ID to the value spine serve runs with, or pass --workspace
default for a deployment that leaves it unset. The id is part of every enc:v2
value's associated data, so rows sealed under a guessed "default" would be
refused by a serve bound to another id. With --all-workspaces (WORKSPACE_RESOLVER=db only)
it sweeps every workspace in the registry (SPINE_REGISTRY_DATABASE_URL) —
active and inactive, because an inactive workspace's rows still need the key
that sealed them. WORKSPACE_RESOLVER=platform-binding has no registry to
enumerate: check each workspace's "subscription secrets swept" log instead
(after a rolling update, restart every process first: a request to a
workspace a process already holds does not sweep it).

Run it once the rollout of the change it verifies has completed: until then a
process still on the previous setting can sweep a workspace back.

Exits non-zero unless every workspace is clean (no refused row, no lost
compare-and-swap). An inactive workspace whose database can no longer be
resolved or reached (for example a decommissioned tenant) counts as not clean
unless it is named in --allow-unreachable-inactive; the summary lists every
workspace so acknowledged. Output contains counters, subscription ids, envelope
names and key ids only.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := observe.WithComponent(context.Background(), "secrets-reencrypt")
			return runSecretsReencrypt(ctx, cmd.OutOrStdout(), opts)
		},
	}
	cmd.Flags().BoolVar(&opts.allWorkspaces, "all-workspaces", false, "Sweep every workspace in the registry (WORKSPACE_RESOLVER=db)")
	cmd.Flags().StringSliceVar(&opts.allowUnreachableInactive, "allow-unreachable-inactive", nil,
		"Comma-separated INACTIVE workspace ids whose database cannot be resolved or reached; they are reported as acknowledged instead of not clean (needs --all-workspaces)")
	return cmd
}

// reencryptOptions are the flags of `spine secrets reencrypt`.
type reencryptOptions struct {
	allWorkspaces bool
	// allowUnreachableInactive names inactive workspaces the operator
	// acknowledges as unreachable (a dropped database, a secret ref
	// deleted at decommissioning). Their resolve or connect failure is
	// reported but does not make the run unclean. It never applies to an
	// active workspace, nor to a workspace that was reached: a reachable
	// workspace is always swept and judged on its rows.
	allowUnreachableInactive []string
}

// sweepTarget is one workspace to sweep.
type sweepTarget struct {
	id       string
	dbURL    string
	inactive bool
	// resolveErr is set when the registry row could not be resolved to a
	// database; the target is then reported unreachable and not swept.
	resolveErr error
}

// label is the workspace as printed: its id (quoted unless it is a valid
// workspace id — a registry row that failed to resolve is printed as
// stored), marked when inactive.
func (t sweepTarget) label() string {
	id := t.printableID()
	if t.inactive {
		return id + " (inactive)"
	}
	return id
}

// printableID is the workspace id, quoted unless it is a valid workspace
// id.
func (t sweepTarget) printableID() string {
	if workspace.ValidateID(t.id) != nil {
		return strconv.Quote(t.id)
	}
	return t.id
}

// sweepOutcome is how one workspace's sweep ended.
type sweepOutcome int

const (
	sweepClean sweepOutcome = iota
	sweepNotClean
	// sweepUnreachable: the workspace's database could not be resolved
	// or connected to; nothing was read or written.
	sweepUnreachable
)

// loadExplicitSecretEnvelopeMode is loadSecretEnvelopeMode for `spine
// secrets reencrypt`, which REQUIRES SPINE_SECRET_ENVELOPE_MODE to be set.
// `spine serve` treats an unset value as migrate, but this command writes
// to every workspace database and cannot see the mode serve runs with: run
// from an ops shell or job that carries the keys but not the mode, an
// implicit migrate would convert every planted legacy row of a strict
// deployment into a valid enc:v2 row (laundering it) and report the run
// clean, and a stray downgrade would rewrite every enc:v2 row to enc:v1,
// which strict serve then refuses. So the operator must state the mode.
func loadExplicitSecretEnvelopeMode() (store.SecretEnvelopeMode, error) {
	if strings.TrimSpace(os.Getenv("SPINE_SECRET_ENVELOPE_MODE")) == "" {
		return "", errors.New("SPINE_SECRET_ENVELOPE_MODE must be set for this command, to the mode spine serve runs with (migrate, strict or downgrade): the command writes every workspace database, and an unset value would apply migrate, which converts legacy rows a strict deployment refuses (nothing was changed)")
	}
	return loadSecretEnvelopeMode()
}

// requireRegistryResolver refuses --all-workspaces unless WORKSPACE_RESOLVER
// is db, mirroring the single-workspace path's refusal of a shared
// resolver. --all-workspaces binds each registry database under its
// registry workspace_id, which is the id `spine serve` binds only in db
// mode; in file mode serve binds SPINE_WORKSPACE_ID, so sweeping a
// (possibly stale) registry would seal rows under ids serve then refuses,
// stopping their fan-out. platform-binding has no registry to enumerate.
func requireRegistryResolver() error {
	switch r := strings.ToLower(strings.TrimSpace(os.Getenv("WORKSPACE_RESOLVER"))); r {
	case "db":
		return nil
	case "platform-binding":
		return errors.New("--all-workspaces needs a workspace registry, and WORKSPACE_RESOLVER=platform-binding has none to enumerate: a spine serve process sweeps a workspace only when it builds it, not on every request, so before switching to strict, dropping a previous key or rolling back, restart every process once the rollout has completed, then load every workspace and check that each one's latest \"subscription secrets swept\" log across all replicas comes from a restarted process and shows refused=0 and conflicts=0 (docs/integration-guide.md §2.1, Verifying every workspace; nothing was changed)")
	case "", "file":
		return errors.New("--all-workspaces sweeps the workspace registry, which spine serve uses only with WORKSPACE_RESOLVER=db; in file mode (WORKSPACE_RESOLVER unset or file) run the command without --all-workspaces, or set WORKSPACE_RESOLVER=db as spine serve does (nothing was changed)")
	default:
		return fmt.Errorf("--all-workspaces needs WORKSPACE_RESOLVER=db, got %q (nothing was changed)", r)
	}
}

func runSecretsReencrypt(ctx context.Context, out io.Writer, opts reencryptOptions) error {
	// The same SPINE_ENV gate as spine serve (INIT-024 TASK-007, N-16):
	// the key ring below is required only under production, so an unset
	// or mistyped value must not quietly pick the lenient branch.
	env, err := requireRuntimeEnv()
	if err != nil {
		return fmt.Errorf("%w (nothing was changed)", err)
	}
	cipher, err := loadSecretCipher(env)
	if err != nil {
		return err
	}
	if cipher == nil {
		return errors.New("SPINE_SECRET_ENCRYPTION_KEY is required: the sweep seals signing secrets with it (nothing was changed)")
	}
	mode, err := loadExplicitSecretEnvelopeMode()
	if err != nil {
		return err
	}
	if !opts.allWorkspaces && len(opts.allowUnreachableInactive) > 0 {
		return errors.New("--allow-unreachable-inactive applies only with --all-workspaces (nothing was changed)")
	}
	if opts.allWorkspaces {
		if err := requireRegistryResolver(); err != nil {
			return err
		}
	}
	header := fmt.Sprintf("secret envelope: mode=%s primary_key_id=%s previous_key_ids=%s",
		mode, cipher.PrimaryKeyID(), strings.Join(cipher.KeyIDs()[1:], ","))

	if !opts.allWorkspaces {
		target, err := singleWorkspaceSweepTarget(ctx)
		if err != nil {
			return err
		}
		// The bound id is part of every enc:v2 value's associated data:
		// print it with the key ids, before anything is written.
		outf(out, "%s workspace_id=%s\n", header, target.printableID())
		if outcome := sweepOneWorkspace(ctx, out, target, cipher, mode); outcome != sweepClean {
			if outcome == sweepUnreachable {
				outf(out, "workspace %s: NOT CLEAN (unreachable)\n", target.label())
			}
			return fmt.Errorf("workspace %s is not clean; see the output above", target.label())
		}
		return nil
	}

	outf(out, "%s\n", header)
	targets, failed, err := registrySweepTargets(ctx, out)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, id := range opts.allowUnreachableInactive {
		if id = strings.TrimSpace(id); id != "" {
			allowed[id] = false // set to true once it acknowledges a workspace
		}
	}
	total := len(targets) + len(failed)
	var acknowledged []string
	for _, target := range targets {
		switch sweepOneWorkspace(ctx, out, target, cipher, mode) {
		case sweepClean:
		case sweepUnreachable:
			if _, ok := allowed[target.id]; ok && target.inactive {
				allowed[target.id] = true
				acknowledged = append(acknowledged, target.printableID())
				outf(out, "workspace %s: unreachable, ACKNOWLEDGED by --allow-unreachable-inactive (not swept: its rows were neither read nor converted)\n", target.label())
				continue
			}
			outf(out, "workspace %s: NOT CLEAN (unreachable)\n", target.label())
			failed = append(failed, target.label())
		default:
			failed = append(failed, target.label())
		}
	}
	for _, id := range opts.allowUnreachableInactive {
		if id = strings.TrimSpace(id); id != "" && !allowed[id] {
			outf(out, "note: --allow-unreachable-inactive %q matched no unreachable inactive workspace; ignored\n", id)
			allowed[id] = true // report each id once
		}
	}
	outf(out, "summary: workspaces=%d clean=%d not_clean=%d acknowledged_unreachable=%d",
		total, total-len(failed)-len(acknowledged), len(failed), len(acknowledged))
	if len(acknowledged) > 0 {
		outf(out, " (%s)", strings.Join(acknowledged, ", "))
	}
	outf(out, "\n")
	if len(failed) > 0 {
		return fmt.Errorf("re-encryption is not clean for %d workspace(s): %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// singleWorkspaceSweepTarget resolves the file-mode workspace exactly as
// `spine serve` does (workspace.NewFileProvider over the same secret
// client), so the sweep binds the id serve binds.
//
// The id must be stated, never defaulted (INIT-023 wave 2 cross-task
// review): it is part of every enc:v2 value's associated data, exactly
// like the key, and an ops job whose environment was copied from `spine
// migrate` (which needs no workspace id) would otherwise bind the
// FileProvider's "default" fallback. In migrate mode that seals every
// legacy row under AAD("default", …) and reports the workspace clean,
// while serve, bound to its real SPINE_WORKSPACE_ID, refuses every such
// row and the workspace's webhook fan-out stops. So SPINE_WORKSPACE_ID
// must be set, or --workspace given (`--workspace default` for a
// deployment that really runs as "default"), as the mode must be.
func singleWorkspaceSweepTarget(ctx context.Context) (sweepTarget, error) {
	if r := strings.ToLower(strings.TrimSpace(os.Getenv("WORKSPACE_RESOLVER"))); r != "" && r != "file" {
		return sweepTarget{}, fmt.Errorf("WORKSPACE_RESOLVER=%s is a shared mode: use --all-workspaces", r)
	}
	if os.Getenv("SPINE_WORKSPACE_ID") == "" && globalWorkspaceID == "" {
		return sweepTarget{}, errors.New("the workspace id must be stated for this command: set SPINE_WORKSPACE_ID to the value spine serve runs with, or pass --workspace default if serve runs without it; the id is part of every enc:v2 value's associated data, and rows sealed under a guessed \"default\" are refused by a serve bound to another id (nothing was changed)")
	}
	secretClient, err := buildFileResolverSecretClient(ctx)
	if err != nil {
		return sweepTarget{}, err
	}
	cfg, err := workspace.NewFileProvider(secretClient).Resolve(ctx, "")
	if err != nil {
		return sweepTarget{}, fmt.Errorf("resolve workspace: %w", err)
	}
	// --workspace (or SPINE_WORKSPACE_ID) must not name a different id
	// than the one serve binds; sealing under another id would make
	// every row unreadable to serve.
	if globalWorkspaceID != "" && globalWorkspaceID != cfg.ID {
		return sweepTarget{}, fmt.Errorf("--workspace %q does not match the file-mode workspace %q that spine serve binds (set SPINE_WORKSPACE_ID instead)", globalWorkspaceID, cfg.ID)
	}
	dbURL := string(cfg.DatabaseURL.Reveal())
	if dbURL == "" {
		return sweepTarget{}, fmt.Errorf("workspace %s has no database configured (SPINE_DATABASE_URL or the secret backend)", cfg.ID)
	}
	if err := requireSecureDBURL(dbURL); err != nil {
		return sweepTarget{}, err
	}
	return sweepTarget{id: cfg.ID, dbURL: dbURL}, nil
}

// registrySweepTargets lists every registry workspace — active AND
// inactive — and resolves its database. Unlike `spine migrate
// --all-workspaces` (active only), inactive workspaces are included: their
// rows still need the key that sealed them, and a "clean" result is what
// licenses dropping a previous key or rolling back. A workspace that
// cannot be resolved is returned as a target with resolveErr set (it is
// reported unreachable); one whose database URL is refused is reported and
// returned in failed (it counts as not clean).
func registrySweepTargets(ctx context.Context, out io.Writer) (targets []sweepTarget, failed []string, err error) {
	registryURL := os.Getenv("SPINE_REGISTRY_DATABASE_URL")
	if registryURL == "" {
		return nil, nil, errors.New("SPINE_REGISTRY_DATABASE_URL is required for --all-workspaces")
	}
	if err := requireSecureDBURL(registryURL); err != nil {
		return nil, nil, fmt.Errorf("registry URL: %w", err)
	}
	var dbSecretClient secrets.SecretClient
	if secretClientConfigured() {
		c, err := buildSecretClient(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("build secret client: %w", err)
		}
		dbSecretClient = c
	}
	provider, err := workspace.NewDBProvider(ctx, registryURL, workspace.DBProviderConfig{SecretClient: dbSecretClient})
	if err != nil {
		return nil, nil, fmt.Errorf("connect to workspace registry: %s", describeDBError(err, registryURL))
	}
	defer provider.Close()
	list, err := provider.ListAllWorkspaces(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list workspaces: %w", err)
	}
	for _, ws := range list {
		resolved, err := provider.ResolveAnyStatus(ctx, ws.ID)
		if err != nil {
			targets = append(targets, sweepTarget{id: ws.ID, inactive: ws.Status != workspace.StatusActive, resolveErr: err})
			continue
		}
		target := sweepTarget{id: resolved.ID, inactive: resolved.Status != workspace.StatusActive}
		target.dbURL = string(resolved.DatabaseURL.Reveal())
		if target.dbURL == "" {
			outf(out, "workspace %s: skipped (no database configured)\n", target.label())
			continue
		}
		if err := requireSecureDBURL(target.dbURL); err != nil {
			outf(out, "workspace %s: NOT CLEAN %q\n", target.label(), err.Error())
			failed = append(failed, target.label())
			continue
		}
		targets = append(targets, target)
	}
	return targets, failed, nil
}

// sweepOneWorkspace opens target's runtime store, binds and sweeps it
// through the same helper serve uses, prints the result, and reports how
// it ended. Database-sourced text is printed quoted (%q): a subscription
// id is written by whoever writes the workspace database — the attacker of
// audit P3-10 — and must not be able to inject lines (a forged "clean"
// line, a terminal escape) into the output operators gate strict and key
// drops on.
func sweepOneWorkspace(ctx context.Context, out io.Writer, target sweepTarget, cipher *spinecrypto.SecretCipher, mode store.SecretEnvelopeMode) sweepOutcome {
	if target.resolveErr != nil {
		outf(out, "workspace %s: unreachable: resolve failed: %q\n", target.label(), target.resolveErr.Error())
		return sweepUnreachable
	}
	wsCtx := observe.WithWorkspaceID(ctx, target.id)
	st, err := store.NewPostgresStore(wsCtx, target.dbURL)
	if err != nil {
		outf(out, "workspace %s: unreachable: connect failed: %q\n", target.label(), describeDBError(err, target.dbURL))
		return sweepUnreachable
	}
	defer st.Close()
	st.SetSecretCipher(cipher)
	rep, err := workspace.PrepareSubscriptionSecrets(wsCtx, st, cipher, target.id, mode, observe.Logger(wsCtx))
	for _, r := range rep.Refused {
		printRefused(out, target, r)
	}
	status := "clean"
	if err != nil || !rep.Clean() {
		status = "NOT CLEAN"
	}
	outf(out, "workspace %s: %s scanned=%d rewritten=%d current=%d empty=%d conflicts=%d refused=%d from_plaintext=%d from_v1=%d to_v1=%d rekeyed=%d repaired=%d\n",
		target.label(), status, rep.Scanned, rep.Rewritten, rep.Current, rep.Empty, rep.Conflicts, len(rep.Refused),
		rep.FromPlaintext, rep.FromV1, rep.ToV1, rep.Rekeyed, rep.Repaired)
	if err != nil {
		outf(out, "workspace %s: sweep error: %q\n", target.label(), err.Error())
		return sweepNotClean
	}
	if !rep.Clean() {
		return sweepNotClean
	}
	return sweepClean
}

// describeDBError renders a database open/connect error for operator
// output without credentials (codex review, round 1). pgx masks a password
// in the URL's userinfo, but a pgconn.ParseConfigError echoes the rest of
// the connection string, including a password passed as a query parameter
// (postgres://u@h/db?password=…&connect_timeout=bad). So a configuration
// error is reduced to a fixed message, and in any other error every
// password-like value the DSN carries (userinfo password, password= and
// sslpassword= in URL or key=value form) is masked.
func describeDBError(err error, dsn string) string {
	var parseErr *pgconn.ParseConfigError
	if errors.As(err, &parseErr) {
		return "invalid database connection string (details withheld: it may carry credentials)"
	}
	msg := err.Error()
	// Longest first, so a piece of a password cannot break up the whole
	// and leave the rest of it in the text.
	secrets := dsnSecrets(dsn)
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		msg = strings.ReplaceAll(msg, secret, "xxxxx")
	}
	return msg
}

// dsnSecrets lists the credential values a connection string carries.
//
// pgx v5.11.0 ends a URI's userinfo at the first '@' (libpq's rule);
// net/url, which url.Parse below follows, at the last. An unencoded '@' in
// a password therefore splits it, and its tail becomes part of the host
// that a connect error names (INIT-024 TASK-013), so every '@'-separated
// piece of 3 bytes or more is listed too.
func dsnSecrets(dsn string) []string {
	var out []string
	add := func(v string) {
		if v = strings.Trim(v, "'"); v != "" {
			out = append(out, v)
		}
	}
	if pw, ok := workspace.URLUserinfoPassword(dsn); ok {
		for _, piece := range strings.Split(pw, "@") {
			if len(piece) >= 3 && piece != pw {
				add(piece)
				if dec, err := url.PathUnescape(piece); err == nil && dec != piece {
					add(dec)
				}
			}
		}
	}
	if u, err := url.Parse(dsn); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		if pw, ok := u.User.Password(); ok {
			add(pw)
		}
		q := u.Query()
		for _, k := range []string{"password", "sslpassword"} {
			for _, v := range q[k] {
				add(v)
			}
		}
		return out
	}
	for _, field := range strings.Fields(dsn) {
		if k, v, ok := strings.Cut(field, "="); ok && (k == "password" || k == "sslpassword") {
			add(v)
		}
	}
	return out
}

// printRefused prints one refused row. The subscription id, key id and
// reason are quoted: they come from (or are derived from) the workspace
// database.
func printRefused(out io.Writer, target sweepTarget, r store.SecretSweepIssue) {
	outf(out, "workspace %s: refused subscription %q envelope=%s key_id=%q reason=%q\n",
		target.label(), r.SubscriptionID, r.Envelope, r.KeyID, r.Reason)
}

// outf writes one line of command output. A failed write to the
// operator's terminal cannot be reported anywhere better, and the exit
// status still reflects the sweep, so the write error is dropped.
func outf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
