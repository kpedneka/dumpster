.PHONY: build test lint run migrate

COVERAGE_THRESHOLD := 80

# Packages that require external services (Postgres, S3, LLM APIs) are
# excluded from the unit-test coverage gate. They are exercised separately
# by integration tests that run against live infrastructure.
# This list must stay in sync with .github/workflows/ci.yml.
UNIT_COVERPKG := ./internal/auth,./internal/session,./internal/session/mock,./internal/account,./internal/account/mock,./internal/config,./internal/server,./internal/graphedge,./internal/graphedge/memory,./internal/manifest,./internal/manifest/memory,./internal/manifest/layout,./internal/llm,./internal/llm/mock,./internal/llm/inference,./internal/llm/anthropic,./internal/kb,./internal/kb/memory,./internal/document,./internal/document/memory,./internal/chunk,./internal/chunk/memory,./internal/entity,./internal/entity/memory,./internal/entity/mock,./internal/entity/inference,./internal/reembed,./internal/objectstore,./internal/objectstore/mock,./internal/queue,./internal/queue/memory,./internal/rls,./internal/retrieval,./internal/retrieval/memory,./internal/search,./internal/search/mock,./internal/worker,./internal/router,./internal/router/mock,./internal/graphrag,./internal/graphrag/memory,./internal/ratelimit,./internal/ratelimit/memory,./internal/ratelimit/mock

# UNIT_TESTPKG: test packages that exercise UNIT_COVERPKG (excludes pgstore and other infra).
# manifest/layout, entity/inference, and llm/inference are HTTP-client
# adapters tested with httptest — no external process needed, so unlike
# their retired subprocess predecessors (gliner, the old layout) they
# belong in the unit gate. llm/anthropic is tested against a fake
# http.RoundTripper (option.WithHTTPClient) serving canned SSE responses —
# same idea, no real Anthropic API call. reembed only touches memory/mock
# repositories in its tests, so it belongs here too despite existing to
# drive a real Postgres backfill in production.
UNIT_TESTPKG := ./internal/auth ./internal/account ./internal/config ./internal/server ./internal/graphedge ./internal/manifest ./internal/manifest/layout ./internal/llm ./internal/llm/inference ./internal/llm/anthropic \
                ./internal/kb ./internal/document ./internal/chunk ./internal/entity ./internal/entity/inference ./internal/reembed \
                ./internal/objectstore ./internal/worker \
                ./internal/rls ./internal/retrieval ./internal/retrieval/memory \
                ./internal/search ./internal/router ./internal/graphrag/memory \
                ./internal/ratelimit ./internal/ratelimit/memory

build:
	go build ./cmd/... ./internal/...

test:
	go test ./...
	go test -coverpkg=$(UNIT_COVERPKG) -coverprofile=coverage.out -covermode=atomic $(UNIT_TESTPKG)
	@go tool cover -func=coverage.out | grep total | awk '{gsub(/%/,""); if ($$3+0 < $(COVERAGE_THRESHOLD)) {print "coverage " $$3 "% is below threshold $(COVERAGE_THRESHOLD)%"; exit 1}}'
	cd web && npm test

lint:
	golangci-lint run ./...
	cd web && npm run typecheck
	cd web && npm run lint

run:
	docker compose up --build

migrate:
	goose -dir migrations postgres "host=$(DB_HOST) port=$(DB_PORT) dbname=$(DB_NAME) user=$(DB_USER) password=$(DB_PASSWORD) sslmode=$(DB_SSLMODE)" up
