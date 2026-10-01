GO ?= go

.PHONY: run build fmt check-fmt vet test verify smoke fuzz migrate test-integration
run:
	$(GO) run ./cmd/small-chain
build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/small-chain ./cmd/small-chain
fmt:
	gofmt -w cmd internal
check-fmt:
	test -z "$$(gofmt -l cmd internal)"
vet:
	$(GO) vet ./...
test:
	$(GO) test -race -count=1 -coverprofile=coverage.out ./...
verify: check-fmt vet test build smoke
smoke: build
	python3 scripts/smoke.py
fuzz:
	$(GO) test ./internal/jobs -run='^$$' -fuzz=FuzzNormalize -fuzztime=10s -parallel=4

migrate:
	$(GO) run ./cmd/migrate
test-integration:
	@test -n "$$TEST_DATABASE_URL" || (echo "TEST_DATABASE_URL is required" >&2; exit 1)
	$(GO) test -tags=integration -race -count=1 -timeout=90s -v ./internal/postgres
