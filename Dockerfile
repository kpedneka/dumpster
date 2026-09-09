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
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /bin/cleanup ./cmd/cleanup

# web-builder compiles the React SPA so the api binary can serve it.
# Uses node:22-slim (Debian/glibc) rather than Alpine: esbuild (used by Vite)
# downloads glibc-linked binaries that don't run on Alpine's musl libc.
FROM node:22-slim AS web-builder
WORKDIR /app/web
COPY web/package*.json web/.npmrc ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY web/ ./
RUN npm run build

# runtime is cmd/api + cmd/worker + cmd/cleanup's production image: just
# the Go binaries and the built SPA. No Python here — entity extraction,
# PDF region classification, and embeddings all go out over HTTP to the
# inference image below instead of running as a subprocess embedded in
# this process (the pattern this replaced; see the System Architecture
# page's Inference Service sub-page for the full history and why it
# changed).
#
# cmd/cleanup added to this same image (not its own) so ECS can run it as
# a scheduled RunTask against this one image with its command overridden
# to /bin/cleanup, rather than needing a separate build/push/ECR-repo
# pipeline for a small, occasionally-run utility -- the AWS infra plan's
# "reuse the existing image" call, just via a distinct binary in the same
# image rather than a --mode flag on an existing one, since cmd/cleanup
# was already its own main package by the time this got built.
#
# WORKDIR /app ensures the Go API resolves "web/dist" relative to /app.
# Fly.io [processes] commands (/bin/api, /bin/worker) inherit this
# workdir; ECS's cleanup RunTask overrides command to ["/bin/cleanup"].
FROM alpine:3.20 AS runtime
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /bin/api     /bin/api
COPY --from=builder /bin/worker  /bin/worker
COPY --from=builder /bin/cleanup /bin/cleanup
COPY --from=web-builder /app/web/dist /app/web/dist

# inference is the standalone, always-on ML inference service: entity
# extraction, PDF region classification, and local embeddings behind one
# HTTP API. Built from python:3.11-slim (Debian/glibc), not Alpine: PyTorch
# (a GLiNER dependency) does not publish musl/Alpine wheels, so installing
# it there forces pip to compile from source or fail outright.
FROM python:3.11-slim AS inference
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# requirements.txt copied and installed before the rest of scripts/ so an
# unrelated change to extract_regions.py/inference_service.py doesn't bust
# the cache for this layer — installing torch is the single most expensive
# step in this build.
COPY scripts/requirements.txt /app/scripts/requirements.txt
# torch (a transitive dependency of sentence-transformers, for local
# embeddings) defaults to the CUDA-enabled PyPI wheel on Linux, which
# bundles the full NVIDIA/CUDA runtime (~2.9GB of nvidia-* packages) and
# triton (~650MB, a GPU kernel compiler) — dead weight on Fly's
# shared-cpu-1x, which has no GPU at all (confirmed: torch.cuda.is_available()
# is False on this exact image). Installing the CPU-only build first
# satisfies sentence-transformers' torch dependency before pip ever reaches
# for the GPU-enabled default.
RUN --mount=type=cache,target=/root/.cache/pip pip install torch --index-url https://download.pytorch.org/whl/cpu
RUN --mount=type=cache,target=/root/.cache/pip pip install -r /app/scripts/requirements.txt
# Baking the local-embedding model weights into the image and forcing
# offline mode eliminates HuggingFace Hub network round trips at runtime
# entirely — see the Warm Entity-Extraction Sidecar topic page for the full
# history of why baking beats downloading on first use (short version: a
# cold network download of these weights is a multi-minute tax, worth
# spending image size to avoid on a process that's always on anyway).
# Entity extraction's own weights (GLiNER) are not baked here at all —
# entity extraction runs as a separate AWS Batch GPU job
# (Dockerfile.batch-entity-job), not in this always-on process.
RUN python -c "from sentence_transformers import SentenceTransformer; SentenceTransformer('BAAI/bge-small-en-v1.5')"
ENV HF_HUB_OFFLINE=1
ENV TRANSFORMERS_OFFLINE=1
COPY scripts/ /app/scripts/
# Run from scripts/ itself, not /app: inference_service.py imports its
# sibling modules (embeddings, extract_entities, extract_regions) as plain
# top-level imports, which only resolve when scripts/ is the working
# directory Python adds to sys.path — importing it as "scripts.inference_service"
# from /app would need scripts/ to be a real package instead.
WORKDIR /app/scripts
EXPOSE 8000
# UVICORN_HOST defaults to 0.0.0.0 (all IPv4 interfaces) for local
# docker-compose. On Fly, fly.inference.toml overrides this to
# "fly-local-6pn" — Fly's private network (6PN) is IPv6-only, and
# 0.0.0.0 never binds an IPv6 address, so another machine reaching this
# one over 6PN would get "connection refused" despite the process being
# up and DNS resolving correctly. Needs a shell (not plain exec form) so
# the env var actually expands; Fly's own docs are explicit that no
# [[services]] block substitutes for this — 6PN traffic bypasses Fly's
# proxy entirely, so the process has to be the thing actually listening
# on that address. Written as a JSON array invoking /bin/sh -c, not bare
# shell form, purely to satisfy Docker's JSONArgsRecommended lint check —
# the inner `exec` still does the actual work of replacing the shell
# with uvicorn (PID 1) so SIGTERM/SIGINT reach it directly.
CMD ["/bin/sh", "-c", "exec uvicorn inference_service:app --host ${UVICORN_HOST:-0.0.0.0} --port 8000"]

# No ENTRYPOINT/CMD on runtime: Fly.io selects the binary via [processes] in
# fly.toml; docker-compose selects it via the `command:` key in
# compose.yml. Both docker-compose.yml and fly.toml pass --target=runtime
# or --target=inference explicitly — `docker build` without --target
# defaults to the last stage (inference), so this is never relied upon.
