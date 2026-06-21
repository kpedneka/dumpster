.PHONY: build test lint run migrate

COVERAGE_THRESHOLD := 80

# Packages that require external services (Postgres, S3, LLM APIs) are
# excluded from the unit-test coverage gate. They are exercised separately
# by integration tests that run against live infrastructure.
# This list must stay in sync with .github/workflows/ci.yml.
UNIT_COVERPKG := ./internal/auth,./internal/auth/mock,./internal/config,./internal/server,./internal/llm,./internal/llm/mock,./internal/kb,./internal/kb/memory,./internal/document,./internal/document/memory,./internal/chunk,./internal/chunk/memory,./internal/entity,./internal/entity/memory,./internal/entity/mock,./internal/objectstore,./internal/objectstore/mock,./internal/queue,./internal/queue/memory,./internal/rls,./internal/retrieval,./internal/retrieval/memory,./internal/search,./internal/search/mock

# UNIT_TESTPKG: test packages that exercise UNIT_COVERPKG (excludes pgstore and other infra).
UNIT_TESTPKG := ./internal/auth ./internal/config ./internal/server ./internal/llm \
                ./internal/kb ./internal/document ./internal/chunk ./internal/entity \
                ./internal/objectstore \
                ./internal/rls ./internal/retrieval ./internal/retrieval/memory \
                ./internal/search

build:
	go build ./...

test:
	go test ./...
	go test -coverpkg=$(UNIT_COVERPKG) -coverprofile=coverage.out -covermode=atomic $(UNIT_TESTPKG)
	@go tool cover -func=coverage.out | grep total | awk '{gsub(/%/,""); if ($$3+0 < $(COVERAGE_THRESHOLD)) {print "coverage " $$3 "% is below threshold $(COVERAGE_THRESHOLD)%"; exit 1}}'
	cd web && npm test

lint:
	golangci-lint run ./...
	cd web && npm run lint

run:
	docker compose up --build

migrate:
	goose -dir migrations postgres "host=$(DB_HOST) port=$(DB_PORT) dbname=$(DB_NAME) user=$(DB_USER) password=$(DB_PASSWORD) sslmode=$(DB_SSLMODE)" up
