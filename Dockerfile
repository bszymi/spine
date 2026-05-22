# ── Build stage ──
FROM golang:1.26-bookworm AS builder

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
FROM debian:bookworm-slim

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
