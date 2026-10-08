package workspace

// Standalone wiring of bound subscription signing secrets (INIT-023
// TASK-009, audit P3-10 and P3-1). Every place `spine serve` opens a
// workspace's runtime store — the single-workspace file resolver
// (cmd/spine buildStore), every shared-mode ServiceSet (buildServiceSet)
// and the `spine secrets reencrypt` operator command — goes through
// PrepareSubscriptionSecrets, so the binding (workspace id + mode) cannot
// diverge between them. A divergent id would make the other's rows fail
// closed.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/bszymi/spine/adapters/store"
	spinecrypto "github.com/bszymi/spine/core/crypto"
	"github.com/bszymi/spine/core/observe"
)

// subscriptionSweepTimeout bounds one workspace's re-encryption sweep.
// The sweep runs while a ServiceSet is being built — every request for
// the workspace waits on it — and its compare-and-swap UPDATE waits on
// the row lock of any open transaction holding the row (an operator's
// psql session, a stuck writer). Without a limit that would stall the
// workspace's first request, or file-mode startup, indefinitely. On
// timeout the store stays bound and usable and the next build sweeps
// again. It is a variable only so a test can shorten it.
var subscriptionSweepTimeout = 10 * time.Second

// ErrSubscriptionSecretBinding marks a PrepareSubscriptionSecrets error
// that came from binding the store (an empty workspace id or an unknown
// mode): a programming or configuration error the caller must not start
// with. Any other error it returns is a sweep error, already logged,
// which callers do not fail on.
var ErrSubscriptionSecretBinding = errors.New("bind subscription secrets")

// strictModeHint is logged after a clean migrate-mode sweep.
const strictModeHint = "all signing secrets are enc:v2 under the primary key; set SPINE_SECRET_ENVELOPE_MODE=strict to refuse legacy envelopes"

// PrepareSubscriptionSecrets binds st's subscription signing secrets to
// workspaceID and mode, then runs the re-encryption sweep under
// subscriptionSweepTimeout. cipher must be the cipher installed on st
// (SetSecretCipher).
//
//   - cipher == nil: no-op. No binding is set, nothing is written, and the
//     store keeps today's plaintext development posture.
//   - A binding error is returned wrapped in ErrSubscriptionSecretBinding,
//     before anything is read.
//   - A sweep error (database, timeout) is logged and returned with the
//     partial report. The store stays bound; callers do not fail startup
//     on it, and the next build sweeps again.
//
// workspaceID must be the trusted id of the workspace whose runtime
// database st was opened for (the resolver's Config.ID), never a value
// read from that database.
//
// Logging never includes a secret or a stored value: one Info record per
// converted row (subscription_id, from, from_key_id, repaired), one Error
// per refused row (subscription_id, envelope, key_id, reason), and one
// summary record with every counter. In migrate mode a clean sweep adds a
// hint to switch to strict, at Warn when SPINE_ENV=production.
func PrepareSubscriptionSecrets(ctx context.Context, st *store.PostgresStore, cipher *spinecrypto.SecretCipher,
	workspaceID string, mode store.SecretEnvelopeMode, log *slog.Logger) (store.SecretSweepReport, error) {
	if st == nil || cipher == nil {
		return store.SecretSweepReport{}, nil
	}
	if log == nil {
		log = observe.Logger(ctx)
	}
	if err := st.SetSubscriptionSecretBinding(store.SubscriptionSecretBinding{WorkspaceID: workspaceID, Mode: mode}); err != nil {
		return store.SecretSweepReport{}, fmt.Errorf("%w: workspace %q: %w", ErrSubscriptionSecretBinding, workspaceID, err)
	}
	binding, _ := st.SubscriptionSecretBinding()

	sweepCtx, cancel := context.WithTimeout(ctx, subscriptionSweepTimeout)
	defer cancel()
	rep, err := st.ReencryptSubscriptionSecrets(sweepCtx)

	for _, c := range rep.Converted {
		log.Info("subscription secret converted",
			"workspace_id", workspaceID,
			"subscription_id", c.SubscriptionID,
			"from", c.From.String(),
			"from_key_id", c.FromKeyID,
			"repaired", c.Repaired,
		)
	}
	for _, r := range rep.Refused {
		log.Error("subscription secret refused; the row was left untouched and fails every read until it is fixed (rotate-secret, restore the key, or correct the workspace id)",
			"workspace_id", workspaceID,
			"subscription_id", r.SubscriptionID,
			"envelope", r.Envelope.String(),
			"key_id", r.KeyID,
			"reason", r.Reason,
		)
	}
	if err != nil {
		log.Error("subscription secret sweep failed; the store stays bound and the next build sweeps again",
			"workspace_id", workspaceID,
			"mode", string(binding.Mode),
			"primary_key_id", cipher.PrimaryKeyID(),
			"scanned", rep.Scanned,
			"rewritten", rep.Rewritten,
			"error", err,
		)
		return rep, fmt.Errorf("sweep subscription secrets for workspace %q: %w", workspaceID, err)
	}

	level := slog.LevelInfo
	if !rep.Clean() {
		level = slog.LevelWarn
	}
	log.Log(ctx, level, "subscription secrets swept",
		"workspace_id", workspaceID,
		"mode", string(binding.Mode),
		"primary_key_id", cipher.PrimaryKeyID(),
		"scanned", rep.Scanned,
		"rewritten", rep.Rewritten,
		"current", rep.Current,
		"empty", rep.Empty,
		"conflicts", rep.Conflicts,
		"refused", len(rep.Refused),
		"from_plaintext", rep.FromPlaintext,
		"from_v1", rep.FromV1,
		"to_v1", rep.ToV1,
		"rekeyed", rep.Rekeyed,
		"repaired", rep.Repaired,
	)
	if binding.Mode == store.SecretEnvelopeMigrate && rep.Clean() {
		hintLevel := slog.LevelInfo
		if strings.EqualFold(strings.TrimSpace(os.Getenv("SPINE_ENV")), "production") {
			hintLevel = slog.LevelWarn
		}
		log.Log(ctx, hintLevel, strictModeHint, "workspace_id", workspaceID)
	}
	return rep, nil
}
