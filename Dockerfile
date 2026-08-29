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
# libgl1/libglib2.0-0/libsm6/libxext6/libxrender1/libxcb1: opencv-python (a
# transitive dependency of unstructured[pdf], via its PDF/image pipeline)
# dlopen()s these X11/GL libraries at import time. python:3.11-slim doesn't
# ship them, so extract_regions.py crashes with "ImportError: libxcb.so.1:
# cannot open shared object file" the moment it imports unstructured.partition.pdf
# — every PDF upload dead-letters. This never surfaced locally because the
# worker was run natively on the host during development, not through this
# image; docker-compose's worker service builds this same Dockerfile/target,
# so it hits the identical failure.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates tzdata \
    libgl1 libglib2.0-0 libsm6 libxext6 libxrender1 libxcb1 \
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
# torch (a transitive dependency of gliner) defaults to the CUDA-enabled
# PyPI wheel on Linux, which bundles the full NVIDIA/CUDA runtime (~2.9GB
# of nvidia-* packages) and triton (~650MB, a GPU kernel compiler) — dead
# weight on Fly's shared-cpu-1x, which has no GPU at all (confirmed:
# torch.cuda.is_available() is False on this exact image). Installing the
# CPU-only build first satisfies gliner's torch dependency before pip ever
# reaches for the GPU-enabled default.
#
# torchvision must be pinned to the same index: it ships its own compiled
# extension that has to exactly match the torch build it's paired with.
# Installing only torch from the CPU index and letting torchvision resolve
# normally afterward pairs a CPU torch with a mismatched torchvision,
# which fails at import with "operator torchvision::nms does not exist" —
# caught by smoke-testing this image before shipping it, not by the size
# measurement alone.
RUN --mount=type=cache,target=/root/.cache/pip pip install torch torchvision --index-url https://download.pytorch.org/whl/cpu
RUN --mount=type=cache,target=/root/.cache/pip pip install -r /app/scripts/requirements.txt
# `pip install spacy` installs only the library, not a language model —
# en_core_web_sm is a separate download this image never ran. Without it,
# extract_entities.py silently falls back to a naive punctuation-based
# sentencizer (see scripts/extract_entities.py's _load_nlp), which badly
# fragments citation/URL/abbreviation-heavy text and inflates entity counts
# (and downstream co-occurrence-edge counts) with no error anywhere. This
# had been running in every deployed image until caught.
RUN --mount=type=cache,target=/root/.cache/pip python -m spacy download en_core_web_sm
# Baking the GLiNER model into the image, take three — full history, because
# the right call kept changing as the surrounding facts changed:
#   1. Originally baked in + forced offline, to remove a network round trip
#      happening on every entity-extraction call.
#   2. Reverted after the warm sidecar (v4.7) shipped: that network check
#      dropped to once per worker lifetime, so paying 1.5GB of image size to
#      avoid it stopped being worth it — and at the time, this image's GPU
#      torch build had it sitting at 8GB+, with zero room to spare anyway.
#   3. Re-added here: switching to CPU-only torch dropped this image to
#      ~2.76GB, so the same 1.5GB now lands at ~4.26GB — comfortable
#      headroom under Fly's 8GB cap, not the razor's edge it was before.
#      More importantly, testing the "download on first use" behavior for
#      real (not just against an already-warm local cache) showed the
#      actual one-time cost is a ~2.5 minute fresh download of the model
#      over the network, not the ~9s figure measured with a warm cache —
#      worse than assumed when it was removed. Once per worker lifetime is
#      infrequent, but deploys/restarts happen often enough that a 2.5
#      minute tax each time is worth spending 1.5GB of now-available image
#      budget to avoid.
RUN python -c "from gliner import GLiNER; GLiNER.from_pretrained('urchade/gliner_mediumv2.1')"
ENV HF_HUB_OFFLINE=1
ENV TRANSFORMERS_OFFLINE=1
COPY scripts/ /app/scripts/

# No ENTRYPOINT or CMD here: Fly.io selects the binary via [processes] in
# fly.toml; docker-compose selects it via the `command:` key in compose.yml.
# This must remain the last stage so builds without --target use runtime.
