GO ?= go
LINTER ?= $(if $(wildcard .bin/golangci-lint),.bin/golangci-lint,golangci-lint)
GOOSE := $(GO) tool goose
DATABASE_URL ?=

.PHONY: build run test test-integration lint generate migrate migrate-down clean

build:
	$(GO) build ./...

run:
	$(GO) run ./cmd/trip-service

test:
	$(GO) test -race -cover ./...

# Run only against an explicitly configured course/test PostgreSQL database.
# Migrations are additive; the integration test cleans up its own test rows.
test-integration:
	@test -n "$(DATABASE_URL)" || (echo "DATABASE_URL is required" >&2; exit 1)
	@$(GOOSE) -dir migrations postgres "$(DATABASE_URL)" up
	$(GO) test -race -tags=integration -count=2 ./...

lint:
	$(LINTER) run ./...

generate:
	$(GO) tool oapi-codegen --config oapi-codegen.yaml contracts/openapi/trip-service.openapi.yaml
	$(GO) generate ./internal/usecase/... ./internal/transport/httpapi/...

migrate:
	@test -n "$(DATABASE_URL)" || (echo "DATABASE_URL is required" >&2; exit 1)
	@$(GOOSE) -dir migrations postgres "$(DATABASE_URL)" up

migrate-down:
	@test -n "$(DATABASE_URL)" || (echo "DATABASE_URL is required" >&2; exit 1)
	@$(GOOSE) -dir migrations postgres "$(DATABASE_URL)" down-to 0

clean:
	rm -rf bin coverage.out
