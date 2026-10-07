package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bszymi/spine/adapters/secrets"
	"github.com/bszymi/spine/adapters/store"
	spinecrypto "github.com/bszymi/spine/core/crypto"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/service/internal/workspace"
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
// (SPINE_SECRET_ENVELOPE_MODE), but on demand and for every workspace at
// once. In shared mode `serve` sweeps a workspace only when it loads it,
// so this is how an operator proves every workspace is clean before
// switching to strict or dropping a previous key; flipping early would
// make an idle workspace's legacy rows unreadable and stop its webhook
// fan-out.
//
// It prints one line of counters per workspace and one line per refused
// row — never a secret or a stored value — and exits non-zero unless
// every workspace's sweep is clean.
func secretsReencryptCmd() *cobra.Command {
	var allWorkspaces bool
	cmd := &cobra.Command{
		Use:   "reencrypt",
		Short: "Re-encrypt webhook signing secrets to the configured envelope and primary key",
		// A refused row or a failed workspace is an outcome, not a usage
		// error: print the error without the usage text.
		SilenceUsage: true,
		Long: `Re-encrypt every runtime.event_subscriptions.signing_secret to the envelope of
SPINE_SECRET_ENVELOPE_MODE (migrate/strict: enc:v2, downgrade: enc:v1) under the
primary key of SPINE_SECRET_ENCRYPTION_KEY, bound to the workspace id.

Without --all-workspaces it sweeps the single file-mode workspace
(SPINE_WORKSPACE_ID, default "default"; database from SPINE_DATABASE_URL or the
configured secret backend). With --all-workspaces it sweeps every active
workspace in the registry (SPINE_REGISTRY_DATABASE_URL), like
'spine migrate --all-workspaces'.

Exits non-zero unless every workspace is clean (no refused row, no lost
compare-and-swap). Output contains counters, subscription ids, envelope names
and key ids only.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := observe.WithComponent(context.Background(), "secrets-reencrypt")
			return runSecretsReencrypt(ctx, cmd.OutOrStdout(), allWorkspaces)
		},
	}
	cmd.Flags().BoolVar(&allWorkspaces, "all-workspaces", false, "Sweep every workspace in the registry (shared mode)")
	return cmd
}

// sweepTarget is one workspace to sweep.
type sweepTarget struct {
	id    string
	dbURL string
}

func runSecretsReencrypt(ctx context.Context, out io.Writer, allWorkspaces bool) error {
	cipher, err := loadSecretCipher(resolveRuntimeEnv())
	if err != nil {
		return err
	}
	if cipher == nil {
		return errors.New("SPINE_SECRET_ENCRYPTION_KEY is required: the sweep seals signing secrets with it (nothing was changed)")
	}
	mode, err := loadSecretEnvelopeMode()
	if err != nil {
		return err
	}
	outf(out, "secret envelope: mode=%s primary_key_id=%s previous_key_ids=%s\n",
		mode, cipher.PrimaryKeyID(), strings.Join(cipher.KeyIDs()[1:], ","))

	if !allWorkspaces {
		target, err := singleWorkspaceSweepTarget(ctx)
		if err != nil {
			return err
		}
		if !sweepOneWorkspace(ctx, out, target, cipher, mode) {
			return fmt.Errorf("workspace %s is not clean; see the refused rows above", target.id)
		}
		return nil
	}

	targets, skipped, err := registrySweepTargets(ctx, out)
	if err != nil {
		return err
	}
	var failed []string
	failed = append(failed, skipped...)
	for _, target := range targets {
		if !sweepOneWorkspace(ctx, out, target, cipher, mode) {
			failed = append(failed, target.id)
		}
	}
	outf(out, "summary: workspaces=%d clean=%d not_clean=%d\n",
		len(targets)+len(skipped), len(targets)+len(skipped)-len(failed), len(failed))
	if len(failed) > 0 {
		return fmt.Errorf("re-encryption is not clean for %d workspace(s): %s", len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// singleWorkspaceSweepTarget resolves the file-mode workspace exactly as
// `spine serve` does (workspace.NewFileProvider over the same secret
// client), so the sweep binds the id serve binds.
func singleWorkspaceSweepTarget(ctx context.Context) (sweepTarget, error) {
	if r := strings.ToLower(strings.TrimSpace(os.Getenv("WORKSPACE_RESOLVER"))); r != "" && r != "file" {
		return sweepTarget{}, fmt.Errorf("WORKSPACE_RESOLVER=%s is a shared mode: use --all-workspaces", r)
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

// registrySweepTargets lists every active registry workspace and
// resolves its database, as `spine migrate --all-workspaces` does.
// Workspaces that cannot be resolved are reported and returned in
// skipped (they count as not clean).
func registrySweepTargets(ctx context.Context, out io.Writer) (targets []sweepTarget, skipped []string, err error) {
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
		return nil, nil, fmt.Errorf("connect to workspace registry: %w", err)
	}
	defer provider.Close()
	list, err := provider.List(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list workspaces: %w", err)
	}
	for _, ws := range list {
		resolved, err := provider.Resolve(ctx, ws.ID)
		if err != nil {
			outf(out, "workspace %s: NOT CLEAN resolve failed: %v\n", ws.ID, err)
			skipped = append(skipped, ws.ID)
			continue
		}
		dbURL := string(resolved.DatabaseURL.Reveal())
		if dbURL == "" {
			outf(out, "workspace %s: skipped (no database configured)\n", resolved.ID)
			continue
		}
		if err := requireSecureDBURL(dbURL); err != nil {
			outf(out, "workspace %s: NOT CLEAN %v\n", resolved.ID, err)
			skipped = append(skipped, resolved.ID)
			continue
		}
		targets = append(targets, sweepTarget{id: resolved.ID, dbURL: dbURL})
	}
	return targets, skipped, nil
}

// sweepOneWorkspace opens target's runtime store, binds and sweeps it
// through the same helper serve uses, prints the result, and reports
// whether it is clean.
func sweepOneWorkspace(ctx context.Context, out io.Writer, target sweepTarget, cipher *spinecrypto.SecretCipher, mode store.SecretEnvelopeMode) bool {
	wsCtx := observe.WithWorkspaceID(ctx, target.id)
	st, err := store.NewPostgresStore(wsCtx, target.dbURL)
	if err != nil {
		// pgx connection errors name the host and database, never the
		// password; the URL itself is not printed.
		outf(out, "workspace %s: NOT CLEAN connect failed: %v\n", target.id, err)
		return false
	}
	defer st.Close()
	st.SetSecretCipher(cipher)
	rep, err := workspace.PrepareSubscriptionSecrets(wsCtx, st, cipher, target.id, mode, observe.Logger(wsCtx))
	for _, r := range rep.Refused {
		outf(out, "workspace %s: refused subscription %s envelope=%s key_id=%s reason=%s\n",
			target.id, r.SubscriptionID, r.Envelope, r.KeyID, r.Reason)
	}
	status := "clean"
	if err != nil || !rep.Clean() {
		status = "NOT CLEAN"
	}
	outf(out, "workspace %s: %s scanned=%d rewritten=%d current=%d empty=%d conflicts=%d refused=%d from_plaintext=%d from_v1=%d to_v1=%d rekeyed=%d repaired=%d\n",
		target.id, status, rep.Scanned, rep.Rewritten, rep.Current, rep.Empty, rep.Conflicts, len(rep.Refused),
		rep.FromPlaintext, rep.FromV1, rep.ToV1, rep.Rekeyed, rep.Repaired)
	if err != nil {
		outf(out, "workspace %s: sweep error: %v\n", target.id, err)
		return false
	}
	return rep.Clean()
}

// outf writes one line of command output. A failed write to the
// operator's terminal cannot be reported anywhere better, and the exit
// status still reflects the sweep, so the write error is dropped.
func outf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
