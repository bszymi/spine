package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	spinecrypto "github.com/bszymi/spine/internal/crypto"
	"github.com/bszymi/spine/internal/delivery"
	"github.com/bszymi/spine/internal/secrets"
	"github.com/bszymi/spine/internal/workspace"
)

// operatorTokenMinLength is the minimum acceptable length for
// SPINE_OPERATOR_TOKEN. A 32-byte random secret gives ~192 bits of
// entropy, well beyond brute-force reach.
const operatorTokenMinLength = 32

// workspaceDeliveryConfig holds the env-derived delivery settings the
// pooled workspace builder needs to start per-workspace event delivery.
// Identical fields to the top-level startEventDelivery path; built once
// in serveCmd so single-mode and pooled modes share the same source of
// truth and the same parser quirks.
type workspaceDeliveryConfig struct {
	Enabled          bool
	WebhookTargets   *delivery.TargetValidator
	EventRetention   time.Duration
	SMPEventURL      string
	SMPInternalToken string
	// SMPAdminToken seeds the per-workspace bootstrap admin row
	// (auth.actors / auth.tokens) so the platform's bearer
	// authenticates against the workspace runtime DB on the first
	// request. Empty disables the bootstrap (single-workspace and
	// pre-platform-binding deployments).
	SMPAdminToken string
	// ProductionStrict mirrors SPINE_ENV=production. The pooled
	// workspace builder uses it to escalate bootstrap-admin token-hash
	// collisions (auth.ErrAdminTokenHashCollision) from "log and
	// continue" to "fail workspace load", matching the strict-startup
	// philosophy that gates SPINE_DEV_MODE, SPINE_CODE_REPO_BASE, and
	// SPINE_SECRET_ENCRYPTION_KEY at boot.
	ProductionStrict bool
}

// loadWorkspaceDeliveryConfig reads the env vars that drive event
// delivery into a single struct. Called once at boot so the pool builder
// (db / platform-binding modes) and the top-level startEventDelivery
// (file mode) agree on configuration. SMP_WORKSPACE_ID is intentionally
// NOT here — in pooled modes it comes from the per-workspace binding
// (ss.Config.SMPWorkspaceID), not the process env.
func loadWorkspaceDeliveryConfig() workspaceDeliveryConfig {
	cfg := workspaceDeliveryConfig{
		Enabled:          os.Getenv("SPINE_EVENT_DELIVERY") == "true",
		WebhookTargets:   delivery.NewTargetValidator(parseWebhookAllowedHosts(os.Getenv("SPINE_WEBHOOK_ALLOWED_HOSTS"))),
		SMPEventURL:      os.Getenv("SMP_EVENT_URL"),
		SMPInternalToken: os.Getenv("SMP_INTERNAL_TOKEN"),
		SMPAdminToken:    os.Getenv("SMP_ADMIN_TOKEN"),
		ProductionStrict: resolveRuntimeEnv() == "production",
	}
	if v := os.Getenv("SPINE_EVENT_RETENTION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.EventRetention = d
		}
	}
	return cfg
}

// parsePositiveIntEnv returns the integer value of the named env var,
// or 0 if the var is unset or not a positive integer.
func parsePositiveIntEnv(name string) int {
	raw := os.Getenv(name)
	if raw == "" {
		return 0
	}
	n := 0
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// parseWebhookAllowedHosts parses the SPINE_WEBHOOK_ALLOWED_HOSTS env
// var into a slice of hostnames the webhook target validator should
// exempt from the "HTTPS + public IP only" default. Entries are
// comma-separated; empty or whitespace-only entries are dropped.
// Operators opt into private destinations explicitly — a bare env var
// never permits arbitrary private ranges.
func parseWebhookAllowedHosts(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseGitHTTPTrustedCIDRs parses the SPINE_GIT_HTTP_TRUSTED_CIDRS
// comma-separated list. An empty input yields nil so deployments must
// opt in explicitly — a prior default of all RFC1918 ranges made any
// container on any Docker bridge "trusted".
func parseGitHTTPTrustedCIDRs(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	for _, c := range strings.Split(raw, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// parseGitReceivePackEnabled reads the SPINE_GIT_RECEIVE_PACK_ENABLED
// env var. Default false — an explicit true/1/yes/on is required to
// turn push on. Unrecognised values fall back to false so a typo never
// silently enables the endpoint.
func parseGitReceivePackEnabled(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// requireSecureDBURL rejects connection strings that use sslmode=disable
// unless SPINE_INSECURE_LOCAL=1 is set.
func requireSecureDBURL(url string) error {
	if !strings.Contains(url, "sslmode=disable") {
		return nil
	}
	if os.Getenv("SPINE_INSECURE_LOCAL") == "1" {
		return nil
	}
	return fmt.Errorf("database URL uses sslmode=disable; set SPINE_INSECURE_LOCAL=1 to acknowledge (local development only) or use sslmode=require/verify-full")
}

// resolveRuntimeEnv returns the normalized SPINE_ENV value.
func resolveRuntimeEnv() string {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("SPINE_ENV")))
	switch v {
	case "production", "staging", "development":
		return v
	default:
		return ""
	}
}

// devModeEnabled reports whether SPINE_DEV_MODE is set to an
// affirmative value.
func devModeEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("SPINE_DEV_MODE")))
	return v == "1" || v == "true"
}

// guardDevModeEnv enforces TASK-020. Running with dev-mode auth in a
// production environment is refused outright.
func guardDevModeEnv(env string, dev bool) error {
	if !dev {
		return nil
	}
	if env == "production" {
		return fmt.Errorf("SPINE_DEV_MODE is enabled but SPINE_ENV=production; dev-mode auth bypass MUST NOT run in production")
	}
	return nil
}

// validateOperatorToken enforces the startup gate described in TASK-010.
func validateOperatorToken(token string) error {
	if token == "" {
		return nil
	}
	if len(token) < operatorTokenMinLength {
		return fmt.Errorf("SPINE_OPERATOR_TOKEN is %d characters; minimum is %d. Generate a stronger secret (e.g. `openssl rand -hex 32`) before starting spine serve", len(token), operatorTokenMinLength)
	}
	return nil
}

// loadCodeRepoBase reads SPINE_CODE_REPO_BASE and returns the absolute
// directory under which every code-repo binding's LocalPath must
// resolve. Required when SPINE_ENV=production — a production deployment
// without an explicit base could let an Operator land
// `local_path=/etc` in a binding and serve arbitrary host directories
// through git-http-backend. Empty in non-production disables the
// containment check (dev / single-workspace setups).
func loadCodeRepoBase(env string) (string, error) {
	v := strings.TrimSpace(os.Getenv("SPINE_CODE_REPO_BASE"))
	if v == "" {
		if env == "production" {
			return "", fmt.Errorf("SPINE_CODE_REPO_BASE is required when SPINE_ENV=production; set it to the absolute directory under which every code-repo binding's local_path must resolve (e.g. /var/spine/code-repos)")
		}
		return "", nil
	}
	// Reject relative paths explicitly. filepath.Abs would silently
	// anchor "repos" to the process CWD, making the containment root
	// depend on how the service was launched — a deployment config
	// that "works" can validate a different set of local_path values
	// after a WorkingDirectory change. The error text and runbook
	// promise an explicit absolute root; enforce it here.
	if !filepath.IsAbs(v) {
		return "", fmt.Errorf("SPINE_CODE_REPO_BASE %q must be an absolute path; relative values would anchor the containment root to the process working directory", v)
	}
	return filepath.Clean(v), nil
}

// loadSecretCipher builds the at-rest secret cipher from
// SPINE_SECRET_ENCRYPTION_KEY. In production the key is required.
func loadSecretCipher(env string) (*spinecrypto.SecretCipher, error) {
	encoded := os.Getenv("SPINE_SECRET_ENCRYPTION_KEY")
	if encoded == "" {
		if env == "production" {
			return nil, fmt.Errorf("SPINE_SECRET_ENCRYPTION_KEY is required when SPINE_ENV=production; generate one with `openssl rand -base64 32`")
		}
		return nil, nil
	}
	key, err := spinecrypto.ParseEncryptionKey(encoded)
	if err != nil {
		return nil, fmt.Errorf("SPINE_SECRET_ENCRYPTION_KEY: %w", err)
	}
	return spinecrypto.NewSecretCipher(key)
}

// dbPolicyFromEnv reads ADR-012 pool overrides from environment
// variables. Unset fields fall back to PoolPolicyDefault().
//
//   - SPINE_WS_POOL_MIN_CONNS, SPINE_WS_POOL_MAX_CONNS
//   - SPINE_WS_POOL_ACQUIRE_TIMEOUT (Go duration; e.g. "5s")
//   - SPINE_WS_POOL_HEALTH_CHECK_PERIOD
//   - SPINE_WS_POOL_QUEUE_SIZE
//
// Per-workspace overrides via binding metadata are out of scope
// here; ADR-012 leaves that for a future binding field.
func dbPolicyFromEnv() workspace.PoolPolicy {
	var p workspace.PoolPolicy
	if v := parsePositiveIntEnv("SPINE_WS_POOL_MIN_CONNS"); v > 0 && v <= math.MaxInt32 {
		p.MinConns = int32(v)
	}
	if v := parsePositiveIntEnv("SPINE_WS_POOL_MAX_CONNS"); v > 0 && v <= math.MaxInt32 {
		p.MaxConns = int32(v)
	}
	if raw := os.Getenv("SPINE_WS_POOL_ACQUIRE_TIMEOUT"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			p.AcquireTimeout = d
		}
	}
	if raw := os.Getenv("SPINE_WS_POOL_HEALTH_CHECK_PERIOD"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			p.HealthCheckPeriod = d
		}
	}
	if v := parsePositiveIntEnv("SPINE_WS_POOL_QUEUE_SIZE"); v > 0 {
		p.QueueSize = v
	}
	return p
}

// secretClientConfigured reports whether the env signals any secret
// backend selection. It treats both the explicit
// SPINE_SECRET_PROVIDER selector and the file-mode SPINE_SECRET_FILE_ROOT
// (which buildSecretClient honours when SPINE_SECRET_PROVIDER is
// unset) as "configured". Used by db-mode and migrate paths to
// decide whether a SecretClient should be wired into DBProvider for
// dereferencing ref-shaped database_url rows.
func secretClientConfigured() bool {
	if strings.TrimSpace(os.Getenv("SPINE_SECRET_PROVIDER")) != "" {
		return true
	}
	if os.Getenv("SPINE_SECRET_FILE_ROOT") != "" {
		return true
	}
	return false
}

// buildFileResolverSecretClient assembles the SecretClient used by
// the single-workspace FileProvider. The shape depends on the
// configured backend:
//
//   - No backend / file backend: a real (or NotFound) client wrapped
//     by EnvFallbackSecretClient so SPINE_DATABASE_URL keeps working
//     for the canonical default/runtime_db ref. Dev-friendly bootstrap.
//   - AWS backend: the AWS client is returned directly, with no env
//     fallback. Production deployments that explicitly opted into AWS
//     must fail closed when a runtime_db secret is missing — falling
//     back to a stale SPINE_DATABASE_URL would mask a misprovisioned
//     secret.
func buildFileResolverSecretClient(ctx context.Context) (secrets.SecretClient, error) {
	id := os.Getenv("SPINE_WORKSPACE_ID")
	if id == "" {
		id = "default"
	}

	provider := strings.ToLower(strings.TrimSpace(os.Getenv("SPINE_SECRET_PROVIDER")))

	if provider == "aws" {
		// Production: no env-var bypass. A missing AWS secret must
		// surface as ErrSecretNotFound, not silently resolve to
		// whatever is in SPINE_DATABASE_URL.
		return buildSecretClient(ctx)
	}

	var inner secrets.SecretClient
	if !secretClientConfigured() {
		inner = workspace.NotFoundSecretClient{}
	} else {
		c, err := buildSecretClient(ctx)
		if err != nil {
			return nil, err
		}
		inner = c
	}

	return workspace.NewEnvFallbackSecretClient(inner, id, "SPINE_DATABASE_URL")
}

// poolIdleTimeoutFromEnv reads SPINE_WS_POOL_IDLE_TIMEOUT (Go duration,
// e.g. "10m"). Zero means "use the ServicePool default" (10m, ADR-012).
// Bad values are silently ignored so a typo doesn't fail startup; the
// default keeps the runtime correct.
func poolIdleTimeoutFromEnv() time.Duration {
	raw := os.Getenv("SPINE_WS_POOL_IDLE_TIMEOUT")
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// buildSecretClient constructs a SecretClient based on
// SPINE_SECRET_PROVIDER. Required for WORKSPACE_RESOLVER=platform-binding.
//
// Supported values:
//   - file: dev/test path, reads from SPINE_SECRET_FILE_ROOT.
//   - aws : production path, reads from SPINE_SECRET_AWS_REGION,
//     SPINE_SECRET_AWS_ACCOUNT, SPINE_SECRET_AWS_ENV.
func buildSecretClient(ctx context.Context) (secrets.SecretClient, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("SPINE_SECRET_PROVIDER")))
	switch provider {
	case "", "file":
		root := os.Getenv("SPINE_SECRET_FILE_ROOT")
		if root == "" {
			return nil, fmt.Errorf("SPINE_SECRET_FILE_ROOT is required for SPINE_SECRET_PROVIDER=file")
		}
		return secrets.NewFileClient(secrets.FileConfig{Root: root})

	case "aws":
		region := os.Getenv("SPINE_SECRET_AWS_REGION")
		account := os.Getenv("SPINE_SECRET_AWS_ACCOUNT")
		env := os.Getenv("SPINE_SECRET_AWS_ENV")
		if region == "" || account == "" || env == "" {
			return nil, fmt.Errorf("SPINE_SECRET_AWS_REGION, SPINE_SECRET_AWS_ACCOUNT, SPINE_SECRET_AWS_ENV are required for SPINE_SECRET_PROVIDER=aws")
		}
		return secrets.NewAWSClient(ctx, secrets.AWSConfig{Region: region, Account: account, Env: env})

	default:
		return nil, fmt.Errorf("unknown SPINE_SECRET_PROVIDER=%q (expected file|aws)", provider)
	}
}
