# ── Build stage ──
FROM golang:1.26-bookworm AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -o spine ./cmd/spine

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
