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

FROM runtime AS worker
ENTRYPOINT ["/bin/worker"]
