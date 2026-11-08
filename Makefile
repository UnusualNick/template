GO ?= go
GOOSE := $(GO) tool goose
DATABASE_URL ?=

.PHONY: build run test lint generate migrate migrate-down clean

build:
	$(GO) build ./...

run:
	$(GO) run ./cmd/trip-service

test:
	$(GO) test -race ./...

lint:
	golangci-lint run ./...

generate:
	$(GO) tool oapi-codegen --config oapi-codegen.yaml contracts/openapi/trip-service.openapi.yaml

migrate:
	@test -n "$(DATABASE_URL)" || (echo "DATABASE_URL is required" >&2; exit 1)
	$(GOOSE) -dir migrations postgres "$(DATABASE_URL)" up

migrate-down:
	@test -n "$(DATABASE_URL)" || (echo "DATABASE_URL is required" >&2; exit 1)
	$(GOOSE) -dir migrations postgres "$(DATABASE_URL)" down-to 0

clean:
	rm -rf bin coverage.out
