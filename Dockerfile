# ── Build stage ──
# Pinned by digest (INIT-024 TASK-009, SC-09): golang:1.26.10-bookworm, the
# image index of 2026-10-21. The image sets GOTOOLCHAIN=local, so its own
# GOLANG_VERSION is the compiler — it must equal go.mod's `toolchain` line,
# and `go run ./dev/pincheck` (CI job toolchain-parity) fails when they
# drift. Move the tag, the digest, go.mod, the ci.yml git-floor container and
# the Makefile's DOCKER_GO together.
FROM golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY core/ core/
COPY adapters/ adapters/
COPY service/ service/
# testutil/ is a build dependency because adapters/git/testutil.go
# (mis-named — predates the extraction) imports it. Tracked as a
# TASK-004 follow-up to rename to _test.go and drop testutil from
# production build context.
COPY testutil/ testutil/
RUN CGO_ENABLED=0 go build -o spine ./service/cmd/spine

# ── Runtime stage ──
# Pinned by digest: debian:bookworm-slim, the image index of 2026-10-20.
# Dependabot (docker ecosystem) proposes the next digest; take it for the
# Debian security updates it carries (git, ca-certificates).
FROM debian:bookworm-slim@sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3

# git is required at runtime: the artifact service shells out to
# `git worktree add`, `git push`, etc. ca-certificates is needed for
# outbound HTTPS (webhook deliveries, registry DB). wget was previously
# installed but unused — dropped to narrow the runtime attack surface.
RUN apt-get update && \
    apt-get install -y --no-install-recommends git ca-certificates && \
    rm -rf /var/lib/apt/lists/*

RUN useradd --create-home --shell /bin/bash spine
USER spine

COPY --from=builder /app/spine /usr/local/bin/spine

EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=3 \
    CMD ["spine", "health"]

ENTRYPOINT ["spine"]
CMD ["serve"]
