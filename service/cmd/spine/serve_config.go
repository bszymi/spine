package main

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bszymi/spine/adapters/delivery"
	"github.com/bszymi/spine/adapters/secrets"
	"github.com/bszymi/spine/adapters/store"
	spinecrypto "github.com/bszymi/spine/core/crypto"
	"github.com/bszymi/spine/service/internal/workspace"
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
	// ProcessStart is when this spine serve process booted. The admin
	// bootstrap passes it as BootstrapAdminConfig.SupersededBefore, so
	// an SMP_ADMIN_TOKEN rotation revokes only bootstrap rows created
	// before this process started: a replica still carrying the old
	// bearer in a rolling deploy can never revoke the bearer a newer
	// replica seeded (INIT-023 TASK-013, audit P3-31).
	ProcessStart time.Time
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
		// Truncated to Postgres' microsecond precision so a row this
		// process inserts in the same microsecond it booted can never
		// compare as "before" its own start.
		ProcessStart: time.Now().UTC().Truncate(time.Microsecond),
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

// parseEventLogNotifyBridge reads SPINE_EVENT_LOG_NOTIFY_BRIDGE
// (INIT-011/EPIC-003/TASK-006 sub-PR 10a). Default false so the
// listener doesn't hold a dedicated pgxpool connection on
// deployments that haven't enabled the SMP-side notify emitter yet
// (sub-PR 10b). Unrecognised values fall back to false so a typo
// never silently enables the bridge.
func parseEventLogNotifyBridge(raw string) bool {
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

// resolveRuntimeEnv returns the normalized SPINE_ENV value: production,
// staging or development (case and surrounding whitespace ignored), or ""
// for an unset or unknown value. Only requireRuntimeEnv decides whether ""
// may run; callers past that gate always see a known value.
func resolveRuntimeEnv() string {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("SPINE_ENV")))
	switch v {
	case "production", "staging", "development":
		return v
	default:
		return ""
	}
}

// runtimeEnvMaxEcho bounds how much of an unknown SPINE_ENV value the
// refusal quotes, so a stray multi-kilobyte value cannot flood the log.
const runtimeEnvMaxEcho = 64

// requireRuntimeEnv is the SPINE_ENV startup gate of `spine serve` and
// `spine secrets reencrypt` (INIT-024 TASK-007, review finding N-16). It
// returns the normalized value, or an error naming the three accepted
// values when SPINE_ENV is unset, blank or anything else.
//
// Every production gate keys on the exact value "production": the
// SPINE_SECRET_ENCRYPTION_KEY and SPINE_CODE_REPO_BASE requirements, the
// SMP_ADMIN_TOKEN strength refusal, the SPINE_DEV_MODE refusal and the
// pooled bootstrap's strict collision handling. resolveRuntimeEnv maps an
// unset or mistyped value ("prod", "prodution") to "", which took the
// non-production branch of every one of them, so a deployment that
// forgot or misspelled the variable silently ran with plaintext webhook
// secrets, a weak platform bearer and dev-mode auth allowed. Refusing to
// start makes the choice explicit: the only ways to leave the production
// gates off are SPINE_ENV=staging and SPINE_ENV=development, written
// down. Owner question 2 default (refuse rather than treat unset as
// production), pending the owner.
//
// An accepted spelling is any ASCII case of the three words with
// surrounding whitespace, which resolveRuntimeEnv maps to exactly that
// word, so no accepted value can reach a gate as anything else. The
// refusal quotes an unknown value (bounded, %q-escaped): SPINE_ENV is not
// a secret.
func requireRuntimeEnv() (string, error) {
	if env := resolveRuntimeEnv(); env != "" {
		return env, nil
	}
	raw := os.Getenv("SPINE_ENV")
	const accepted = "set it to production, staging or development (the production startup gates apply only to SPINE_ENV=production)"
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("SPINE_ENV is not set; %s", accepted)
	}
	shown := raw
	if len(shown) > runtimeEnvMaxEcho {
		shown = shown[:runtimeEnvMaxEcho] + "..."
	}
	return "", fmt.Errorf("SPINE_ENV=%q is not a known runtime environment; %s", shown, accepted)
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

// gitHTTPDevNoAuthEnabled reports whether SPINE_GIT_HTTP_DEV_NO_AUTH is
// set to an affirmative value (1, true, yes, on; case-insensitive).
// INIT-023 TASK-015, audit P3-30: this is the only switch that lets /git
// clone and fetch skip the bearer outside the direct-peer trusted-CIDR
// rule. SPINE_DEV_MODE no longer touches /git, and no switch lets a push
// skip authentication.
func gitHTTPDevNoAuthEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SPINE_GIT_HTTP_DEV_NO_AUTH"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// guardGitHTTPDevNoAuth refuses SPINE_GIT_HTTP_DEV_NO_AUTH unless the
// runtime environment is explicitly development (owner question 1
// default, pending the owner). Unlike SPINE_DEV_MODE, which is refused
// only under production, an unset or staging SPINE_ENV is not enough: an
// unauthenticated repository read endpoint must be a deliberate local
// choice, and "SPINE_ENV unset" was exactly the gap the audit flagged.
func guardGitHTTPDevNoAuth(env string, on bool) error {
	if !on {
		return nil
	}
	if env != "development" {
		shown := env
		if shown == "" {
			shown = "unset"
		}
		return fmt.Errorf("SPINE_GIT_HTTP_DEV_NO_AUTH is set but SPINE_ENV is %s; the unauthenticated git read endpoint requires SPINE_ENV=development", shown)
	}
	return nil
}

// overlappingCIDRs returns the SPINE_GIT_HTTP_TRUSTED_CIDRS entries that
// overlap a SPINE_TRUSTED_PROXY_CIDRS network. A request whose peer is a
// declared proxy is never token-less (gateway gitPeerIP), so token-less
// clones from an address in the overlap stop working; the caller logs
// this at startup. Unparsable entries are skipped here (githttp refuses
// them when it builds the handler).
func overlappingCIDRs(gitTrusted []string, proxies []*net.IPNet) []string {
	var out []string
	for _, raw := range gitTrusted {
		_, n, err := net.ParseCIDR(raw)
		if err != nil {
			continue
		}
		for _, p := range proxies {
			if n.Contains(p.IP) || p.Contains(n.IP) {
				out = append(out, raw)
				break
			}
		}
	}
	return out
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

// smpAdminTokenMinDistinct is the minimum number of distinct bytes in
// SMP_ADMIN_TOKEN. The length floor alone accepts long-but-trivial
// values ("aaaa…", "0000…", "abab…").
const smpAdminTokenMinDistinct = 8

// validateSMPAdminToken is the SMP_ADMIN_TOKEN strength gate (INIT-023
// TASK-013, audit P3-35). spine serve seeds that bearer as the admin
// actor smp-admin into every workspace it serves, so a guessable value
// is a cross-tenant admin credential.
//
// A token is weak when it is shorter than operatorTokenMinLength (32,
// SPINE_OPERATOR_TOKEN's floor) or has fewer than 8 distinct bytes.
// This is a floor against human-chosen values, not an entropy
// estimator: a Shannon estimate would add false positives on hex tokens
// without stopping a determined weak choice. Empty means the bootstrap
// is off, which is fine.
//
// SPINE_ENV=production refuses to start (err); every other environment
// gets a warning for serveCmd to log, like the other startup gates that
// treat only production as strict. Neither message ever echoes the
// token. The gate lives here, not in core/auth: embedders call
// auth.BootstrapInternalAdmin with their own bearer under their own
// policy (SMP's dev default passes it anyway: 58 characters, 24
// distinct bytes).
func validateSMPAdminToken(token, env string) (warning string, err error) {
	if token == "" {
		return "", nil
	}
	var seen [256]bool
	distinct := 0
	for i := 0; i < len(token); i++ {
		if !seen[token[i]] {
			seen[token[i]] = true
			distinct++
		}
	}
	var problem string
	switch {
	case len(token) < operatorTokenMinLength:
		problem = fmt.Sprintf("SMP_ADMIN_TOKEN is %d characters; minimum is %d", len(token), operatorTokenMinLength)
	case distinct < smpAdminTokenMinDistinct:
		problem = fmt.Sprintf("SMP_ADMIN_TOKEN has %d distinct characters; minimum is %d", distinct, smpAdminTokenMinDistinct)
	default:
		return "", nil
	}
	const remedy = ". It is the platform bearer seeded into every workspace this process serves; generate one with `openssl rand -hex 32`"
	if env == "production" {
		return "", fmt.Errorf("%s%s before starting spine serve", problem, remedy)
	}
	return problem + remedy, nil
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

// loadSecretCipher builds the at-rest secret key ring from
// SPINE_SECRET_ENCRYPTION_KEY (the primary key: it seals) and
// SPINE_SECRET_PREVIOUS_KEYS (a comma-separated list of base64 keys that
// still open values sealed before a rotation; whitespace is trimmed and
// empty entries are ignored). In production the primary key is required.
// Previous keys without a primary key are a startup error: they would
// silently do nothing. Errors name a previous key by its 1-based entry
// index, never by its value.
func loadSecretCipher(env string) (*spinecrypto.SecretCipher, error) {
	encoded := os.Getenv("SPINE_SECRET_ENCRYPTION_KEY")
	previous, err := loadPreviousSecretKeys()
	if err != nil {
		return nil, err
	}
	if encoded == "" {
		if len(previous) > 0 {
			return nil, fmt.Errorf("SPINE_SECRET_PREVIOUS_KEYS is set but SPINE_SECRET_ENCRYPTION_KEY is not; previous keys only decrypt, so a primary key is required")
		}
		if env == "production" {
			return nil, fmt.Errorf("SPINE_SECRET_ENCRYPTION_KEY is required when SPINE_ENV=production; generate one with `openssl rand -base64 32`")
		}
		return nil, nil
	}
	key, err := spinecrypto.ParseEncryptionKey(encoded)
	if err != nil {
		return nil, fmt.Errorf("SPINE_SECRET_ENCRYPTION_KEY: %w", err)
	}
	c, err := spinecrypto.NewSecretCipherRing(key, previous...)
	if err != nil {
		return nil, fmt.Errorf("SPINE_SECRET_ENCRYPTION_KEY / SPINE_SECRET_PREVIOUS_KEYS: %w", err)
	}
	return c, nil
}

// loadPreviousSecretKeys parses SPINE_SECRET_PREVIOUS_KEYS. The error for
// a malformed entry names its 1-based index and wraps only the decoder's
// message (a byte offset or a length), never the entry itself.
func loadPreviousSecretKeys() ([][]byte, error) {
	raw := os.Getenv("SPINE_SECRET_PREVIOUS_KEYS")
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var keys [][]byte
	n := 0
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		n++
		key, err := spinecrypto.ParseEncryptionKey(entry)
		if err != nil {
			return nil, fmt.Errorf("SPINE_SECRET_PREVIOUS_KEYS entry %d: %w", n, err)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// loadSecretEnvelopeMode reads SPINE_SECRET_ENVELOPE_MODE: empty means
// migrate; migrate, strict and downgrade are accepted case-insensitively;
// anything else is a startup error (INIT-023 TASK-009).
func loadSecretEnvelopeMode() (store.SecretEnvelopeMode, error) {
	mode, err := store.ParseSecretEnvelopeMode(os.Getenv("SPINE_SECRET_ENVELOPE_MODE"))
	if err != nil {
		return "", fmt.Errorf("SPINE_SECRET_ENVELOPE_MODE: %w", err)
	}
	return mode, nil
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
