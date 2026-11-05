package main

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
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

// gitTrustedCIDRs is the parsed SPINE_GIT_HTTP_TRUSTED_CIDRS: the runner
// addresses whose direct peers may clone and fetch over /git without a
// bearer token. It has one of two forms (INIT-024 TASK-006, review
// finding N-2):
//
//   - Unbound, the bare list `172.28.1.10/32,172.28.1.11/32`. It names no
//     workspace, so it is accepted only in single mode, where bind gives
//     it to the one workspace. In shared mode it would let every listed
//     address read every workspace, and spine serve refuses to start.
//   - ByWorkspace, the per-workspace form
//     `ws-a=172.28.1.10/32;ws-b=172.28.1.11/32,172.28.1.12/32`: entries
//     separated by ';', each `<workspace-id>=<cidr>[,<cidr>...]`. An
//     address is trusted only for the workspaces it is listed under.
//     Accepted in every mode.
//
// Both empty means every caller presents a token (the default).
type gitTrustedCIDRs struct {
	Unbound     []string
	ByWorkspace map[string][]string
}

// all returns every CIDR of either form, for the startup overlap check
// against SPINE_TRUSTED_PROXY_CIDRS.
func (g gitTrustedCIDRs) all() []string {
	out := append([]string(nil), g.Unbound...)
	ids := make([]string, 0, len(g.ByWorkspace))
	for id := range g.ByWorkspace {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out = append(out, g.ByWorkspace[id]...)
	}
	return out
}

// gitTrustedCIDRsPerWorkspaceHint is the per-workspace form, named by
// every parse and mode error.
// The example is quoted: an unquoted ';' ends a shell assignment.
const gitTrustedCIDRsPerWorkspaceHint = "the per-workspace form <workspace-id>=<cidr>[,<cidr>...] with entries separated by ';', " +
	"e.g. SPINE_GIT_HTTP_TRUSTED_CIDRS='ws-a=172.28.1.10/32;ws-b=172.28.1.11/32'"

// parseGitHTTPTrustedCIDRs parses SPINE_GIT_HTTP_TRUSTED_CIDRS into one of
// the two forms gitTrustedCIDRs describes. A value with an '=' anywhere is
// the per-workspace form; any other value is the bare list. Whitespace
// around entries and empty entries are dropped, and an empty input yields
// no trust so deployments opt in explicitly (a prior default of all
// RFC1918 ranges made any container on any Docker bridge "trusted").
//
// Every entry is validated here, so a malformed value refuses startup
// (it used to reach githttp.NewHandler, whose error disabled the whole
// /git endpoint with only a log line): an unparsable CIDR, an entry with
// no '=' in the per-workspace form (which also catches a bare list mixed
// into it), a workspace ID that workspace.ValidateID refuses (no resolver
// can ever resolve it), a workspace named twice, or one bound to no CIDR.
func parseGitHTTPTrustedCIDRs(raw string) (gitTrustedCIDRs, error) {
	if !strings.Contains(raw, "=") {
		cidrs, err := parseTrustedCIDRList(raw)
		if err != nil {
			return gitTrustedCIDRs{}, err
		}
		return gitTrustedCIDRs{Unbound: cidrs}, nil
	}
	by := make(map[string][]string)
	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, list, ok := strings.Cut(entry, "=")
		if !ok {
			return gitTrustedCIDRs{}, fmt.Errorf("entry %q names no workspace; use %s", entry, gitTrustedCIDRsPerWorkspaceHint)
		}
		id = strings.TrimSpace(id)
		if err := workspace.ValidateID(id); err != nil {
			return gitTrustedCIDRs{}, fmt.Errorf("entry %q: %w; use %s", entry, err, gitTrustedCIDRsPerWorkspaceHint)
		}
		if _, dup := by[id]; dup {
			return gitTrustedCIDRs{}, fmt.Errorf("workspace %q is named twice; list all of its CIDRs in one entry", id)
		}
		cidrs, err := parseTrustedCIDRList(list)
		if err != nil {
			return gitTrustedCIDRs{}, fmt.Errorf("workspace %q: %w", id, err)
		}
		if len(cidrs) == 0 {
			return gitTrustedCIDRs{}, fmt.Errorf("workspace %q is bound to no CIDR", id)
		}
		by[id] = cidrs
	}
	return gitTrustedCIDRs{ByWorkspace: by}, nil
}

// parseTrustedCIDRList splits a comma-separated CIDR list, dropping
// whitespace and empty entries, and refuses an unparsable CIDR.
func parseTrustedCIDRList(raw string) ([]string, error) {
	var out []string
	for _, c := range strings.Split(raw, ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(c); err != nil {
			return nil, fmt.Errorf("invalid CIDR %q", c)
		}
		out = append(out, c)
	}
	return out, nil
}

// checkMode refuses the bare list in shared mode (INIT-024 TASK-006,
// owner question 1 default "refuse to start", pending the owner). shared
// is true for a resolver that serves many workspaces (db,
// platform-binding). The bare list names no workspace, so it would trust
// every listed address for every tenant's repository; failing at startup
// makes the change visible instead of silently dropping the operator's
// list.
func (g gitTrustedCIDRs) checkMode(shared bool) error {
	if shared && len(g.Unbound) > 0 {
		return fmt.Errorf("SPINE_GIT_HTTP_TRUSTED_CIDRS is a bare CIDR list, which names no workspace, but this server is in shared mode "+
			"(WORKSPACE_RESOLVER=db or platform-binding): it would let every listed address clone every workspace's repository without a token. "+
			"Bind each runner address to the workspaces it serves with %s (docs/integration-guide.md §7)", gitTrustedCIDRsPerWorkspaceHint)
	}
	return nil
}

// bind returns the workspace-bound trust the git handler enforces:
// ByWorkspace as parsed, or — in single mode only — the bare list bound
// to singleWorkspaceID, the one workspace the file resolver serves. It
// repeats checkMode, so a shared server is refused here even if the
// early, environment-based check ever disagreed with the resolver that
// was actually built.
func (g gitTrustedCIDRs) bind(shared bool, singleWorkspaceID string) (map[string][]string, error) {
	if err := g.checkMode(shared); err != nil {
		return nil, err
	}
	if len(g.Unbound) > 0 {
		return map[string][]string{singleWorkspaceID: g.Unbound}, nil
	}
	return g.ByWorkspace, nil
}

// crossWorkspaceCIDROverlaps returns, sorted, every pair of CIDRs bound
// to different workspaces that overlap (`ws-a=172.28.1.0/24 overlaps
// ws-b=172.28.1.11/32`). An address in the overlap is trusted for both
// workspaces, which a per-workspace binding is meant to prevent (a range
// typo, or two tenants sharing a runner address), so serve warns at
// startup. It is not refused: one runner may legitimately serve two
// workspaces. Entries are already validated by parseGitHTTPTrustedCIDRs.
func crossWorkspaceCIDROverlaps(byWorkspace map[string][]string) []string {
	type bound struct {
		ws, raw string
		n       *net.IPNet
	}
	var all []bound
	for ws, cidrs := range byWorkspace {
		for _, raw := range cidrs {
			if _, n, err := net.ParseCIDR(raw); err == nil {
				all = append(all, bound{ws, raw, n})
			}
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].ws != all[j].ws {
			return all[i].ws < all[j].ws
		}
		return all[i].raw < all[j].raw
	})
	var out []string
	for i := range all {
		for j := i + 1; j < len(all); j++ {
			a, b := all[i], all[j]
			if a.ws == b.ws {
				continue
			}
			if a.n.Contains(b.n.IP) || b.n.Contains(a.n.IP) {
				out = append(out, a.ws+"="+a.raw+" overlaps "+b.ws+"="+b.raw)
			}
		}
	}
	return out
}

// unservedTrustedWorkspaces returns, sorted, the workspaces a
// per-workspace SPINE_GIT_HTTP_TRUSTED_CIDRS binds other than served —
// in single mode such an entry can never match a request, and serve
// warns about it.
func unservedTrustedWorkspaces(byWorkspace map[string][]string, served string) []string {
	var out []string
	for id := range byWorkspace {
		if id != served {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// sharedWorkspaceResolverEnv reports whether WORKSPACE_RESOLVER selects
// a shared-mode resolver (db or platform-binding), read the way
// buildWorkspaceResolver reads it. It lets the bare-list refusal run
// before any registry, secret or database is opened; an unknown value
// is left to buildWorkspaceResolver's own refusal, and bind re-checks
// against the resolver actually built.
func sharedWorkspaceResolverEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WORKSPACE_RESOLVER"))) {
	case "db", "platform-binding":
		return true
	}
	return false
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
func requireSecureDBURL(dbURL string) error {
	if !sslModeDisabled(dbURL) {
		return nil
	}
	if os.Getenv("SPINE_INSECURE_LOCAL") == "1" {
		return nil
	}
	return fmt.Errorf("database URL uses sslmode=disable; set SPINE_INSECURE_LOCAL=1 to acknowledge (local development only) or use sslmode=require/verify-full")
}

// sslModeDisabled reports whether dsn sets sslmode to disable in any
// spelling pgx reads as disable (INIT-024 TASK-013).
//
// The gate used to look for the literal text "sslmode=disable". pgx
// v5.11.0 parses URIs as libpq does: it trims spaces around a query key
// and value, and the last of a repeated parameter wins, so
// `?sslmode= disable` and `?sslmode=require&sslmode= disable` now connect
// without TLS (v5.9.2 refused the first and kept TLS for the second), and
// the literal check passed both. Percent-encoding (`%64isable`,
// `ssl%6Dode`) and key/value spacing, quoting and escapes (`sslmode =
// disable`, `sslmode='disable'`) passed it with every pgx release.
//
// The settings are read with pgx v5.11.0's structure (uriQuerySettings,
// keywordValueSettings), so text inside a password or another quoted
// value is not taken for a setting. Every sslmode setting that reads as
// disable refuses, not only the one pgx applies: refusing is the safe
// side, and SPINE_INSECURE_LOCAL=1 is the same acknowledgement. The
// literal check stays first, so nothing the gate refused before passes
// now. Settings pgx merges from the environment (PGSSLMODE) are not read
// here, as before.
func sslModeDisabled(dsn string) bool {
	if strings.Contains(dsn, "sslmode=disable") {
		return true
	}
	var settings [][2]string
	if rest, ok := strings.CutPrefix(dsn, "postgres://"); ok {
		settings = uriQuerySettings(rest)
	} else if rest, ok := strings.CutPrefix(dsn, "postgresql://"); ok {
		settings = uriQuerySettings(rest)
	} else {
		settings = keywordValueSettings(dsn)
	}
	for _, kv := range settings {
		if kv[0] == "sslmode" && kv[1] == "disable" {
			return true
		}
	}
	return false
}

// uriQuerySettings returns the query settings of a connection URI (the
// text after its scheme) where pgx v5.11.0 finds them: the userinfo ends
// at the first '@' before any '/', the query begins at the first '?' after
// it, and its pairs are split at '&'. Each key and value is decoded as
// pgx decodes it (uriSetting).
func uriQuerySettings(rest string) [][2]string {
	if i := strings.IndexAny(rest, "@/"); i >= 0 && rest[i] == '@' {
		rest = rest[i+1:]
	}
	q := strings.IndexByte(rest, '?')
	if q < 0 {
		return nil
	}
	var out [][2]string
	for _, pair := range strings.Split(rest[q+1:], "&") {
		if k, v, ok := strings.Cut(pair, "="); ok {
			out = append(out, [2]string{uriSetting(k), uriSetting(v)})
		}
	}
	return out
}

// uriSetting decodes one URI query component the way pgx v5.11.0 does,
// leniently: an invalid escape leaves the component as written (pgx then
// refuses the string).
func uriSetting(s string) string {
	s = strings.Trim(s, " ")
	if dec, err := url.PathUnescape(s); err == nil {
		s = dec
	}
	return strings.Trim(s, " ")
}

// keywordValueSettings returns the settings of a key/value connection
// string as pgx v5.11.0 (like libpq) reads them: a keyword, optional
// whitespace, '=', optional whitespace, then a value that is quoted
// ('…', ending at the first unescaped quote) or runs to the next
// whitespace; in both, a backslash is dropped and the character after it
// kept. Where pgx refuses the string (whitespace inside a keyword, an
// unterminated quote, no '='), reading stops: pgx does not connect.
func keywordValueSettings(s string) [][2]string {
	const ws = " \t\n\r\v\f"
	var out [][2]string
	s = strings.TrimLeft(s, ws)
	for len(s) > 0 {
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		key := strings.Trim(s[:eq], ws)
		if strings.ContainsAny(key, ws) {
			break
		}
		s = strings.TrimLeft(s[eq+1:], ws)
		var val strings.Builder
		if strings.HasPrefix(s, "'") {
			i := 1
			for ; i < len(s) && s[i] != '\''; i++ {
				if s[i] == '\\' {
					if i++; i == len(s) {
						break
					}
				}
				val.WriteByte(s[i])
			}
			if i >= len(s) {
				break
			}
			s = s[i+1:]
		} else {
			i := 0
			for ; i < len(s) && !strings.ContainsRune(ws, rune(s[i])); i++ {
				if s[i] == '\\' {
					if i++; i == len(s) {
						break
					}
				}
				val.WriteByte(s[i])
			}
			s = s[i:]
		}
		out = append(out, [2]string{key, val.String()})
		s = strings.TrimLeft(s, ws)
	}
	return out
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
