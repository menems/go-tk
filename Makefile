GO ?= go

.PHONY: help
help: ## list the targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

.PHONY: test
test: ## run the tests
	$(GO) test ./...

.PHONY: race
race: ## run the tests under the race detector, uncached
	$(GO) test -race -count=1 ./...

.PHONY: cover
cover: ## run the tests and open the coverage report
	$(GO) test -coverprofile=cover.out ./...
	$(GO) tool cover -html=cover.out

.PHONY: vet
vet: ## run go vet
	$(GO) vet ./...

.PHONY: fmt
fmt: ## format the source
	@gofmt -w .

.PHONY: fmt-check
fmt-check: ## fail on unformatted source
	@out=$$(gofmt -l .); test -z "$$out" || { echo "unformatted:"; echo "$$out"; exit 1; }

.PHONY: tidy
tidy: ## sync go.mod and go.sum
	$(GO) mod tidy

.PHONY: check
check: fmt-check vet race ## everything CI runs
