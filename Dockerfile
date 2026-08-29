# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /bin/api ./cmd/api
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /bin/worker ./cmd/worker

# web-builder compiles the React SPA so the api binary can serve it.
# Uses node:22-slim (Debian/glibc) rather than Alpine: esbuild (used by Vite)
# downloads glibc-linked binaries that don't run on Alpine's musl libc.
FROM node:22-slim AS web-builder
WORKDIR /app/web
COPY web/package*.json web/.npmrc ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
RUN npm run build

# runtime is the production image: contains both Go binaries, the built SPA,
# and the Python ML stack required by the worker for entity extraction.
#
# Built from python:3.11-slim (Debian/glibc), not Alpine: PyTorch (a GLiNER
# dependency) does not publish musl/Alpine wheels, so installing it there
# forces pip to compile from source or fail outright.
#
# WORKDIR /app ensures the Go API resolves "web/dist" relative to /app.
# Fly.io [processes] commands (/bin/api, /bin/worker) inherit this workdir.
FROM python:3.11-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=builder /bin/api    /bin/api
COPY --from=builder /bin/worker /bin/worker
COPY --from=web-builder /app/web/dist /app/web/dist
# requirements.txt copied and installed before the rest of scripts/ so an
# unrelated change to extract_regions.py/extract_entities.py doesn't bust the
# cache for this layer — installing torch et al. from scratch is the single
# most expensive step in this build.
COPY scripts/requirements.txt /app/scripts/requirements.txt
RUN --mount=type=cache,target=/root/.cache/pip pip install -r /app/scripts/requirements.txt
COPY scripts/ /app/scripts/

# No ENTRYPOINT or CMD here: Fly.io selects the binary via [processes] in
# fly.toml; docker-compose selects it via the `command:` key in compose.yml.
# This must remain the last stage so builds without --target use runtime.
