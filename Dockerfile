FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/api ./cmd/api
RUN CGO_ENABLED=0 go build -o /bin/worker ./cmd/worker

# web-builder compiles the React SPA so the api binary can serve it.
# Uses node:22-slim (Debian/glibc) rather than Alpine: esbuild (used by Vite)
# downloads glibc-linked binaries that don't run on Alpine's musl libc.
FROM node:22-slim AS web-builder
WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
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
COPY scripts/ /app/scripts/
RUN pip install --no-cache-dir -r /app/scripts/requirements.txt

# api and worker stages exist for docker-compose (each needs its own ENTRYPOINT).
FROM runtime AS api
ENTRYPOINT ["/bin/api"]

# The worker stage reuses the runtime image (Python + ML deps already installed).
FROM runtime AS worker
ENTRYPOINT ["/bin/worker"]
