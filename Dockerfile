FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/api ./cmd/api
RUN CGO_ENABLED=0 go build -o /bin/worker ./cmd/worker

# runtime is the production image: contains both binaries so Fly.io [processes]
# can select /bin/api or /bin/worker without two separate builds.
FROM alpine:3.21 AS runtime
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /bin/api    /bin/api
COPY --from=builder /bin/worker /bin/worker

# api and worker stages exist for docker-compose (each needs its own ENTRYPOINT).
FROM runtime AS api
ENTRYPOINT ["/bin/api"]

# worker also needs a local Python environment: it shells out to
# scripts/extract_entities.py (internal/entity/gliner) to run spaCy+GLiNER
# entity extraction locally, the entire point being to avoid a per-document
# LLM call. This is the only place the Python ML stack is installed.
#
# Built from python:3.11-slim (Debian/glibc), not the Alpine runtime image:
# PyTorch (a GLiNER dependency) does not publish musl/Alpine wheels, so
# installing it there forces pip to compile from source or fail outright.
FROM python:3.11-slim AS worker
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /bin/worker /bin/worker
COPY scripts/requirements.txt /app/scripts/requirements.txt
RUN pip install --no-cache-dir -r /app/scripts/requirements.txt
COPY scripts/extract_entities.py /app/scripts/extract_entities.py
ENV ENTITY_EXTRACTOR_PYTHON=python3
ENV ENTITY_EXTRACTOR_SCRIPT=/app/scripts/extract_entities.py
ENTRYPOINT ["/bin/worker"]
