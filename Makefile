GO ?= go
GOFMT ?= gofmt
RELEASE_VERSION ?= v0.1.0
RELEASE_REPOSITORY ?= w4llisz/small-chain-platform
RELEASE_SHA ?= $(shell git rev-parse HEAD)
RELEASE_ARCHIVE ?= artifacts/release-bundle/small-chain-platform-$(RELEASE_VERSION)-linux-amd64.tar.gz
.DEFAULT_GOAL := run

.PHONY: web-verify
web-verify:
	cd web && npm ci && npm run verify

.PHONY: run build load fmt check-fmt vet test test-scripts verify smoke fuzz migrate test-integration test-e2e compose-up compose-down compose-clean compose-smoke compose-trace-up compose-trace-smoke incident-compose measure-compose release-bundle
run:
	$(GO) run ./cmd/small-chain
build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/small-chain ./cmd/small-chain
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/small-chain-worker ./cmd/small-chain-worker
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/small-chain-load ./cmd/small-chain-load
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/small-chain-evidence ./cmd/small-chain-evidence
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/migrate ./cmd/migrate
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/healthcheck ./cmd/healthcheck
load:
	$(GO) run ./cmd/small-chain-load
fmt:
	$(GOFMT) -w cmd internal
check-fmt:
	@command -v $(GOFMT) >/dev/null || (echo "$(GOFMT) is required" >&2; exit 1)
	test -z "$$($(GOFMT) -l cmd internal)"
vet:
	$(GO) vet ./...
test:
	$(GO) test -race -count=1 -coverprofile=coverage.out ./...
test-scripts:
	python3 -m unittest discover -s scripts -p 'test_*.py'
verify: check-fmt vet test test-scripts build smoke
smoke: build
	python3 scripts/smoke.py
fuzz:
	$(GO) test ./internal/jobs -run='^$$' -fuzz=FuzzNormalize -fuzztime=10s -parallel=4

migrate:
	$(GO) run ./cmd/migrate
test-integration:
	@test -n "$$TEST_DATABASE_URL" || (echo "TEST_DATABASE_URL is required" >&2; exit 1)
	mkdir -p bin
	CGO_ENABLED=1 $(GO) build -race -trimpath -o bin/small-chain-worker-integration ./cmd/small-chain-worker
	CGO_ENABLED=1 $(GO) build -race -trimpath -o bin/small-chain-api-integration ./cmd/small-chain
	SMALL_CHAIN_WORKER_BINARY="$(CURDIR)/bin/small-chain-worker-integration" \
	SMALL_CHAIN_API_BINARY="$(CURDIR)/bin/small-chain-api-integration" \
		$(GO) test -tags=integration -race -count=1 -timeout=120s -v ./internal/postgres ./internal/dbworker ./internal/httpapi

test-e2e:
	@test -n "$$TEST_DATABASE_URL" || (echo "TEST_DATABASE_URL is required" >&2; exit 1)
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/migrate-e2e ./cmd/migrate
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/small-chain-api-e2e ./cmd/small-chain
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/small-chain-worker-e2e ./cmd/small-chain-worker
	./scripts/e2e.sh

compose-up:
	docker compose up --build --wait --wait-timeout 60

compose-down:
	docker compose down --remove-orphans

compose-clean:
	docker compose down --volumes --remove-orphans

compose-smoke:
	bash scripts/compose-smoke.sh

compose-trace-up:
	mkdir -p artifacts/traces
	chmod 0777 artifacts/traces
	SMALL_CHAIN_TRACE_DIR="$(CURDIR)/artifacts/traces" \
	SMALL_CHAIN_TRACE_ENDPOINT="http://otel-collector:4318/v1/traces" \
	SMALL_CHAIN_TRACE_SAMPLE_RATIO="1" \
		docker compose --profile tracing up --build --wait --wait-timeout 60

compose-trace-smoke:
	bash scripts/trace-compose-smoke.sh

incident-compose:
	bash scripts/incident-compose.sh

measure-compose:
	bash scripts/measure-compose.sh

release-bundle:
	python3 scripts/release_bundle.py build \
		--archive "$(RELEASE_ARCHIVE)" \
		--repository "$(RELEASE_REPOSITORY)" \
		--sha "$(RELEASE_SHA)" \
		--version "$(RELEASE_VERSION)"
