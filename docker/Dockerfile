FROM golang:1.27.0-alpine AS builder

RUN apk add --no-cache git ca-certificates

# Empty parent directory for the SQLite metadata DB; copied into the runner
# with nonroot ownership so the app can create pgadmin4.db at /data.
RUN mkdir -p /data && chown 65532:65532 /data

WORKDIR /app

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o main ./cmd/server

# The application creates its SQLite metadata DB (pgadmin4.db) in the working
# directory, so run it from a directory the nonroot user owns.
# Alpine (not distroless) so the PostgreSQL client tools used by Backup &
# Restore (pg_dump / pg_dumpall / pg_restore) can be installed.
FROM alpine:3.23 AS runner

RUN apk add --no-cache postgresql18-client

COPY --from=builder /app/main /main
COPY --from=builder --chown=65532:65532 /data /data

WORKDIR /data
USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/main"]