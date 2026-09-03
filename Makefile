.PHONY: build test lint run migrate hooks licenses

COVERAGE_THRESHOLD := 80

# Packages that require external services (Postgres, S3, LLM APIs) are
# excluded from the unit-test coverage gate. They are exercised separately
# by integration tests that run against live infrastructure.
# This list must stay in sync with .github/workflows/ci.yml.
UNIT_COVERPKG := ./internal/auth,./internal/session,./internal/session/mock,./internal/account,./internal/account/mock,./internal/config,./internal/server,./internal/graphedge,./internal/graphedge/memory,./internal/manifest,./internal/manifest/memory,./internal/manifest/layout,./internal/llm,./internal/llm/mock,./internal/llm/inference,./internal/llm/anthropic,./internal/kb,./internal/kb/memory,./internal/document,./internal/document/memory,./internal/chunk,./internal/chunk/memory,./internal/entity,./internal/entity/memory,./internal/entity/mock,./internal/entity/inference,./internal/entity/awsbatch,./internal/awsbatch/mock,./internal/canonical,./internal/canonical/memory,./internal/canonicalize,./internal/inquiry,./internal/inquiry/memory,./internal/reembed,./internal/objectstore,./internal/objectstore/mock,./internal/queue,./internal/queue/memory,./internal/rls,./internal/retrieval,./internal/retrieval/memory,./internal/search,./internal/search/mock,./internal/worker,./internal/router,./internal/router/mock,./internal/graphrag,./internal/graphrag/memory,./internal/ratelimit,./internal/ratelimit/memory,./internal/ratelimit/mock,./internal/stats,./internal/stats/memory,./internal/community,./internal/community/memory

# UNIT_TESTPKG: test packages that exercise UNIT_COVERPKG (excludes pgstore and other infra).
# manifest/layout, entity/inference, and llm/inference are HTTP-client
# adapters tested with httptest — no external process needed, so unlike
# their retired subprocess predecessors (gliner, the old layout) they
# belong in the unit gate. llm/anthropic is tested against a fake
# http.RoundTripper (option.WithHTTPClient) serving canned SSE responses —
# same idea, no real Anthropic API call. reembed and canonicalize only touch
# memory/mock repositories in their tests, so they belong here too despite
# existing to drive a real Postgres backfill in production. entity/awsbatch
# (the AWS Batch entity.Extractor) is tested entirely against
# internal/awsbatch/mock and internal/objectstore/mock, same idea — real
# AWS access only lives in internal/awsbatch/client.go, which (like
# s3store) is excluded below since it can't be exercised without live AWS.
UNIT_TESTPKG := ./internal/auth ./internal/account ./internal/config ./internal/server ./internal/graphedge ./internal/manifest ./internal/manifest/layout ./internal/llm ./internal/llm/inference ./internal/llm/anthropic \
                ./internal/kb ./internal/document ./internal/chunk ./internal/entity ./internal/entity/inference ./internal/entity/awsbatch ./internal/canonical ./internal/canonicalize ./internal/inquiry ./internal/reembed \
                ./internal/objectstore ./internal/worker \
                ./internal/rls ./internal/retrieval ./internal/retrieval/memory \
                ./internal/search ./internal/router ./internal/graphrag/memory \
                ./internal/ratelimit ./internal/ratelimit/memory \
                ./internal/stats ./internal/stats/memory \
                ./internal/community ./internal/community/memory

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

# Installs the pre-commit hooks defined in lefthook.yml (gitleaks, gofmt,
# go vet) into this clone's .git/hooks. One-time, per clone — lefthook.yml
# itself is checked in, but git hooks live outside version control, so
# every clone needs this run once. Requires lefthook (`brew install
# lefthook`).
hooks:
	lefthook install

# Regenerates THIRD_PARTY_LICENSES.csv: every Go dependency reachable from
# any cmd/ entrypoint, plus the license type go-licenses detected for it.
# Filters out the project's own module — go-licenses reports it too (now
# that a real LICENSE file exists to detect), but this repo's own license
# isn't a third-party notice. Requires go-licenses
# (`go install github.com/google/go-licenses/v2@latest`). Not run in CI —
# dependency licenses don't change often enough to justify a check on
# every push; rerun by hand after adding a new dependency.
licenses:
	go-licenses report ./cmd/... 2>/dev/null | grep -v '^github.com/kunalpednekar/dumpster,' > THIRD_PARTY_LICENSES.csv
