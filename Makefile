.PHONY: build test lint run migrate

COVERAGE_THRESHOLD := 80

build:
	go build ./...

test:
	go test ./... -coverprofile=coverage.out -covermode=atomic
	go test ./internal/... -coverprofile=coverage.out -covermode=atomic
	@go tool cover -func=coverage.out | grep total | awk '{gsub(/%/,""); if ($$3+0 < $(COVERAGE_THRESHOLD)) {print "coverage " $$3 "% is below threshold $(COVERAGE_THRESHOLD)%"; exit 1}}'

lint:
	golangci-lint run ./...

run:
	docker compose up --build

migrate:
	@echo "no migrations yet — placeholder for goose/atlas"
