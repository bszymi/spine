#!/usr/bin/env bash
# INIT-011 / EPIC-001 / TASK-005: ADR-017 dependency-direction guard.
#
# ADR-017 (Spine Core Boundary and Dependency Direction) declares the
# tiered import rules that keep the Spine library importable from any
# embedder without dragging in the standalone service. This script is
# the CI enforcement of those rules. A PR that violates the dependency
# direction fails the lint, not the code review.
#
# Tiers:
#   core/     — pure domain + ports. No imports from adapters/ or service/.
#   adapters/ — concrete ports + DTOs. May import core/. Must not import service/.
#   service/  — HTTP/CLI hosts + wiring. May import any tier.
#
# Cross-repo rule: Spine is a library. It must never import the
# embedding host (Spine Management Platform). The hard-reject below
# is the structural guard. Today this is also enforced by go.mod
# (Spine does not list SMP as a dep); the script defends against a
# future drive-by `replace` directive or accidental import.
#
# Known-debt allowlist: TASK-004 left a documented follow-up to
# relocate adapter DTOs out of port signatures (PR #594 follow-up
# item 5). Until that lands, several core/ packages reach into
# adapters/ for type definitions. Those crossings are allowlisted by
# adapter prefix below — every new entry is technical debt that
# EPIC-001 follow-up work tracks out. Adding a new line here in
# review SHOULD trigger pushback ("can we plumb the type through
# core/ports instead?").
#
# Run from repo root. Exits non-zero on any violation. The failure
# message points at ADR-017 so the fix path is obvious.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

MODULE="github.com/bszymi/spine"
SMP_MODULE="github.com/bszymi/spine-management-platform"

# Tiers we lint. Each entry is `<name>:<rule-set>` where the rule set
# is a comma-separated list of which checks apply:
#   smp        — refuse any import of ${SMP_MODULE}/...
#   no-service — refuse any import of ${MODULE}/service/...
#   no-adapter — refuse any import of ${MODULE}/adapters/... (except allowlist)
#
# The SMP cross-repo rule applies to every Spine tier, including
# service/, because service/ shipping a drive-by SMP import would
# break the "Spine is a library" invariant (ADR-017) just as surely
# as core/ doing it. cmd/, testutil/, and scenariotest/ are leaf
# consumers that may cross internal tiers; cmd/embed-smoketest is
# separately exercised by the standalone-build CI step, which would
# fail to link if SMP appeared in its dep graph.
tier_rules=(
    "core:smp,no-service,no-adapter"
    "adapters:smp,no-service"
    "service:smp"
    "cmd:smp"
    "testutil:smp"
    "workflows:smp"
)

# Build tags used by the repo's test suites that we want included in
# the dep scan. `integration` and `scenario` files are excluded by
# default; without them, an SMP import inside an integration test or
# scenario harness would slip past the lint (codex pass 4 P2 #1).
# Add new tags here if a future test suite ships under a new tag.
BUILD_TAGS="integration,scenario"

# Adapter-package allowlist for core->adapters crossings. Every entry
# is a port-DTO leakage documented as a follow-up under EPIC-001.
# Format: full module path. Subpackages are implicitly allowed.
core_to_adapters_allowlist=(
    "${MODULE}/adapters/store"
    "${MODULE}/adapters/git"
    "${MODULE}/adapters/git/refname"
    "${MODULE}/adapters/repository"
    "${MODULE}/adapters/scheduler"
)

fail=0

emit() {
    echo "lint-boundaries: $*" >&2
}

is_allowed_core_to_adapters() {
    local dep="$1"
    for allowed in "${core_to_adapters_allowlist[@]}"; do
        if [[ "$dep" == "$allowed" || "$dep" == "$allowed/"* ]]; then
            return 0
        fi
    done
    return 1
}

# `go list -deps` resolves transitive deps. We then filter to deps
# owned by either this module OR the SMP module — those are the only
# prefixes the rule set cares about. Stdlib and third-party are not
# gated. SMP must be in the filter (not dropped as "out of scope") so
# the cross-repo guard can see a drive-by import the moment it lands.
#
# `go list` stderr is forwarded to our stderr so a build error or
# Go-side internal-package violation surfaces directly instead of
# being silently swallowed. A non-zero exit from `go list` is treated
# as a lint failure — we cannot vouch for the boundary if we cannot
# enumerate the deps.
has_rule() {
    local ruleset="$1" rule="$2"
    [[ ",${ruleset}," == *",${rule},"* ]]
}

for entry in "${tier_rules[@]}"; do
    tier="${entry%%:*}"
    rules="${entry#*:}"

    if [ ! -d "$tier" ]; then
        # Tier directory missing — treat as fail-safe: if a future
        # restructure removes a tier, the operator should consciously
        # update this list rather than have the lint silently skip it.
        emit "FAIL: tier directory ./${tier}/ missing — update tier_rules in this script"
        fail=1
        continue
    fi

    # `go list -deps` resolves deps for files selected by the current
    # GOOS. Platform-specific files (`*_windows.go` etc.) are invisible
    # on a Linux runner and would be a silent gap (codex pass 2 P2).
    # We walk every supported GOOS and union the deps so a forbidden
    # import inside a `_windows.go` source still surfaces.
    #
    # Per-GOOS failure is treated as a hard fail, not a warning: if
    # the lint cannot enumerate the dep graph for a platform, it
    # cannot vouch for the boundary on that platform. The most common
    # cause of per-GOOS failure today is exactly the case this lint
    # is meant to catch — a forbidden import inside a build-tagged
    # file that Go refuses to load (e.g., reaching into another
    # package's internal/). Surface it cleanly.
    : > /tmp/lint-boundaries.deps
    list_failed=0
    for goos in linux darwin windows; do
        # `-test` includes imports from `*_test.go` files for the
        # listed packages so a test-only boundary violation (e.g.,
        # `core/foo_test.go` importing `service/...`) is caught
        # alongside production-code violations (codex pass 3 P2).
        #
        # `-tags=...` makes integration/scenario-tagged files visible
        # to the dep scan (codex pass 4 P2 #1); without them, a
        # tagged test could import SMP and slip past.
        if ! deps_raw=$(GOOS="$goos" go list -deps -test -tags="$BUILD_TAGS" ./"$tier"/... 2>&1 >>/tmp/lint-boundaries.deps); then
            emit "FAIL (${tier}/..., GOOS=${goos}): go list failed; cannot enumerate deps"
            printf '%s\n' "$deps_raw" >&2
            list_failed=1
            continue
        fi
        if [ -n "$deps_raw" ]; then
            printf '[GOOS=%s] %s\n' "$goos" "$deps_raw" >&2
        fi
    done
    if [ "$list_failed" -ne 0 ]; then
        fail=1
        # Don't `continue` — we still want to scan the deps we did get
        # so any non-platform-specific violation is also surfaced in
        # the same run.
    fi
    deps=$(grep -E "^(${MODULE}|${SMP_MODULE})(/|\$)" /tmp/lint-boundaries.deps | sort -u || true)

    while IFS= read -r dep; do
        [ -z "$dep" ] && continue

        # Cross-repo: Spine code must never reach into SMP. This rule
        # applies to every Spine tier; today go.mod also enforces.
        if has_rule "$rules" "smp" \
            && [[ "$dep" == "${SMP_MODULE}/"* || "$dep" == "${SMP_MODULE}" ]]; then
            emit "FAIL (${tier}/...): reaches into embedder ${dep}"
            emit "  Spine is a library; the host (SMP) must not appear in the dep graph."
            emit "  Fix: invert via a port in core/, not a direct import. See ADR-017."
            fail=1
            continue
        fi

        # Tier rule: core/ and adapters/ may not import service/.
        if has_rule "$rules" "no-service" \
            && [[ "$dep" == "${MODULE}/service/"* ]]; then
            emit "FAIL (${tier}/...): reaches into service tier via ${dep}"
            emit "  service/ is the wiring host. core/ and adapters/ are tier-below."
            emit "  Fix: invert the dependency, or relocate the type to its proper tier."
            emit "  See ADR-017 §Dependency Direction."
            fail=1
            continue
        fi

        # Tier rule: core/ may not import adapters/ EXCEPT for the
        # documented debt allowlist (TASK-004 follow-up #5).
        if has_rule "$rules" "no-adapter" \
            && [[ "$dep" == "${MODULE}/adapters/"* ]]; then
            if is_allowed_core_to_adapters "$dep"; then
                continue
            fi
            emit "FAIL (${tier}/...): new core->adapters crossing into ${dep}"
            emit "  Adapter packages are concrete ports. core/ must define the port interface."
            emit "  Fix: move the type to core/ports/ (or its owning core/ subpackage)."
            emit "  See ADR-017 §Dependency Direction + TASK-004 follow-up #5."
            fail=1
            continue
        fi
    done <<< "$deps"
done

if [ "$fail" -ne 0 ]; then
    emit ""
    emit "ADR-017 boundary violations detected. The rules:"
    emit "  core/     — no imports from adapters/ or service/"
    emit "  adapters/ — no imports from service/"
    emit "  any tier  — no imports from ${SMP_MODULE}/..."
    emit "Read /architecture/adrs/ADR-017-spine-core-boundary-and-dependency-direction.md for the full contract."
    exit 1
fi

echo "lint-boundaries: OK"
