.PHONY: build test lint run migrate

COVERAGE_THRESHOLD := 80

# Packages that require external services (Postgres, S3, LLM APIs) are
# excluded from the unit-test coverage gate. They are exercised separately
# by integration tests that run against live infrastructure.
UNIT_COVERPKG := $(shell go list ./internal/... | grep -Ev '/pgstore|/s3store|llm/anthropic|llm/openai|dumpster/internal/db$$' | tr '\n' ',' | sed 's/,$$//')

build:
	go build ./...

test:
	go test ./... -coverprofile=coverage.out -covermode=atomic
	go test ./internal/... -coverprofile=coverage.out -covermode=atomic -coverpkg=$(UNIT_COVERPKG)
	@go tool cover -func=coverage.out | grep total | awk '{gsub(/%/,""); if ($$3+0 < $(COVERAGE_THRESHOLD)) {print "coverage " $$3 "% is below threshold $(COVERAGE_THRESHOLD)%"; exit 1}}'

lint:
	golangci-lint run ./...

run:
	docker compose up --build

migrate:
	goose -dir migrations postgres "host=$(DB_HOST) port=$(DB_PORT) dbname=$(DB_NAME) user=$(DB_USER) password=$(DB_PASSWORD) sslmode=$(DB_SSLMODE)" up
