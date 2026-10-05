.PHONY: build test lint run migrate hooks licenses lambdas dev-plan dev-deploy dev-env

COVERAGE_THRESHOLD := 80

# Packages that require external services (Postgres, S3, LLM APIs) are
# excluded from the unit-test coverage gate. They are exercised separately
# by integration tests that run against live infrastructure.
# This list must stay in sync with .github/workflows/ci.yml.
UNIT_COVERPKG := ./internal/auth,./internal/session,./internal/session/mock,./internal/account,./internal/account/mock,./internal/config,./internal/server,./internal/graphedge,./internal/graphedge/memory,./internal/manifest,./internal/manifest/memory,./internal/llm,./internal/llm/mock,./internal/llm/inference,./internal/llm/anthropic,./internal/kb,./internal/kb/memory,./internal/document,./internal/document/memory,./internal/chunk,./internal/chunk/memory,./internal/entity,./internal/entity/memory,./internal/entity/mock,./internal/awsbatch/mock,./internal/canonical,./internal/canonical/memory,./internal/canonicalize,./internal/inquiry,./internal/inquiry/memory,./internal/reembed,./internal/objectstore,./internal/objectstore/mock,./internal/queue,./internal/queue/memory,./internal/queue/sqs,./internal/queue/sqs/mock,./internal/queue/stepfunctions,./internal/queue/stepfunctions/mock,./internal/queue/dispatch,./internal/queue/dispatch/dispatchaws,./internal/worker/lambda,./internal/rls,./internal/retrieval,./internal/retrieval/memory,./internal/search,./internal/search/mock,./internal/worker,./internal/router,./internal/router/mock,./internal/graphrag,./internal/graphrag/memory,./internal/ratelimit,./internal/ratelimit/memory,./internal/ratelimit/mock,./internal/stats,./internal/stats/memory,./internal/community,./internal/community/memory,./internal/theme,./internal/theme/memory,./internal/intrusion,./internal/intrusion/memory,./internal/relation,./internal/relation/memory,./internal/graphrecall,./internal/evalcorpus,./internal/multihopqa,./internal/crosslink,./internal/crosslink/memory,./internal/jobstatus,./internal/jobstatus/memory,./internal/pipeline,./internal/pipeline/docindex,./internal/pipeline/entityextract,./internal/pipeline/embedtail,./internal/pipeline/regionclassify,./internal/pipeline/router

# UNIT_TESTPKG: test packages that exercise UNIT_COVERPKG (excludes pgstore and other infra).
# llm/inference is an HTTP-client adapter tested with httptest — no
# external process needed, so it belongs in the unit gate.
# llm/anthropic is tested against a fake http.RoundTripper
# (option.WithHTTPClient) serving canned SSE responses — same idea, no real
# Anthropic API call. reembed and canonicalize only touch memory/mock
# repositories in their tests, so they belong here too despite existing to
# drive a real Postgres backfill in production. Real AWS access only lives
# in the adapters' client.go files (internal/awsbatch, queue/sqs,
# queue/stepfunctions), which can't be exercised without live AWS.
UNIT_TESTPKG := ./internal/auth ./internal/account ./internal/config ./internal/server ./internal/graphedge ./internal/manifest ./internal/llm ./internal/llm/inference ./internal/llm/anthropic \
                ./internal/kb ./internal/document ./internal/chunk ./internal/entity ./internal/canonical ./internal/canonicalize ./internal/inquiry ./internal/reembed \
                ./internal/objectstore ./internal/worker \
                ./internal/queue/sqs ./internal/queue/stepfunctions ./internal/queue/dispatch ./internal/queue/dispatch/dispatchaws \
                ./internal/worker/lambda \
                ./internal/rls ./internal/retrieval ./internal/retrieval/memory \
                ./internal/search ./internal/router ./internal/graphrag/memory \
                ./internal/ratelimit ./internal/ratelimit/memory \
                ./internal/stats ./internal/stats/memory \
                ./internal/community ./internal/community/memory ./internal/theme ./internal/theme/memory \
                ./internal/intrusion ./internal/intrusion/memory \
                ./internal/relation ./internal/relation/memory \
                ./internal/graphrecall ./internal/evalcorpus ./internal/multihopqa \
                ./internal/crosslink ./internal/crosslink/memory \
                ./internal/jobstatus ./internal/jobstatus/memory \
                ./internal/pipeline ./internal/pipeline/docindex ./internal/pipeline/entityextract ./internal/pipeline/regionclassify ./internal/pipeline/router

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

# Builds both Lambda binaries (linux/arm64, static) where Terraform's
# archive_file expects them: build/lambda-pipeline/ and
# build/lambda-stateless-jobs/. CI does the same before any tofu step.
lambdas:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o build/lambda-pipeline/bootstrap ./cmd/lambda-pipeline
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o build/lambda-stateless-jobs/bootstrap ./cmd/lambda-stateless-jobs

# Reads .env.local and hands its settings to terraform/dev as TF_VAR_*,
# so secrets live in one place. Any AWS keys .env.local sets are cleared,
# so your own AWS profile is what plans and deploys.
DEV_STACK_ENV = set -a && . ./.env.local && set +a && \
	unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN && \
	export TF_VAR_database_url_pooled="$$DATABASE_URL_POOLED" \
	       TF_VAR_anthropic_api_key="$$ANTHROPIC_API_KEY" \
	       TF_VAR_anthropic_model="$$ANTHROPIC_MODEL" \
	       TF_VAR_entity_types="$$ENTITY_TYPES" \
	       TF_VAR_uploads_endpoint="$$S3_ENDPOINT" \
	       TF_VAR_uploads_region="$${S3_REGION:-auto}" \
	       TF_VAR_uploads_bucket="$$S3_BUCKET" \
	       TF_VAR_uploads_access_key="$$S3_ACCESS_KEY" \
	       TF_VAR_uploads_secret_key="$$S3_SECRET_KEY" \
	       TF_VAR_uploads_use_path_style="$${S3_USE_PATH_STYLE:-true}" && \
	tofu -chdir=terraform/dev init -input=false >/dev/null &&

# Shows what dev-deploy would change.
dev-plan: lambdas
	@$(DEV_STACK_ENV) tofu -chdir=terraform/dev plan

# Deploys the pipeline-only dev stack (terraform/dev): the state machines
# and Lambdas local development publishes jobs to. Uses your own AWS
# profile (e.g. AWS_PROFILE=dumpster-admin make dev-deploy), never CI.
dev-deploy: lambdas
	@$(DEV_STACK_ENV) tofu -chdir=terraform/dev apply
	@echo "Add these to .env.local so the local API publishes to the dev stack:"
	@$(MAKE) --no-print-directory dev-env

# Prints the .env.local lines for the deployed dev stack again.
dev-env:
	@unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN && \
	tofu -chdir=terraform/dev output -raw dotenv

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
