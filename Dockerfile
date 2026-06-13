FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/api ./cmd/api
RUN CGO_ENABLED=0 go build -o /bin/worker ./cmd/worker

FROM alpine:3.20 AS api
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/api /bin/api
ENTRYPOINT ["/bin/api"]

FROM alpine:3.20 AS worker
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/worker /bin/worker
ENTRYPOINT ["/bin/worker"]
