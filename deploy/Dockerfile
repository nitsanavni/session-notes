# Multi-stage build for session-notes. Build context is the repo root:
#   docker build -f deploy/Dockerfile -t session-notes .
#
# The SQLite driver (modernc.org/sqlite) is pure Go, so CGO_ENABLED=0 yields a
# fully static binary that runs in a scratch/distroless image with no libc.

# ---- build stage ----
FROM golang:1.26 AS build
WORKDIR /src

# Cache module downloads first.
COPY go.mod go.sum ./
RUN go mod download

# Then the sources.
COPY . .

# Static, stripped binary. -trimpath keeps the build reproducible.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags "-s -w" \
    -o /out/session-notes .

# ---- runtime stage ----
# Distroless static: no shell, no package manager, just CA certs + tzdata.
FROM gcr.io/distroless/static-debian12:nonroot

# The DB lives on a mounted volume; the server listens on 7099.
VOLUME ["/data"]
EXPOSE 7099

COPY --from=build /out/session-notes /usr/local/bin/session-notes

# Bind to all interfaces inside the container; Caddy terminates TLS in front.
ENTRYPOINT ["/usr/local/bin/session-notes", "server", \
            "--addr", "0.0.0.0:7099", \
            "--db", "/data/server.db"]
