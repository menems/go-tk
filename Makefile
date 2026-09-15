GO ?= go

# One module per dependency set, so importing one does not drag the others
# into a consumer's module graph. See README, Dependencies.
MODULES ?= . storage/postgres telemetry/otel telemetry/prometheus

define for_each_module
	@set -e; for m in $(MODULES); do echo "== $$m"; (cd $$m && $(1)); done
endef

.PHONY: help
help: ## list the targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

.PHONY: test
test: ## run the tests
	$(call for_each_module,$(GO) test ./...)

.PHONY: race
race: ## run the tests under the race detector, uncached
	$(call for_each_module,$(GO) test -race -count=1 ./...)

.PHONY: cover
cover: ## report test coverage per package
	$(call for_each_module,$(GO) test -cover ./...)

.PHONY: vet
vet: ## run go vet
	$(call for_each_module,$(GO) vet ./...)

.PHONY: fmt
fmt: ## format the source
	@gofmt -w .

.PHONY: fmt-check
fmt-check: ## fail on unformatted source
	@out=$$(gofmt -l .); test -z "$$out" || { echo "unformatted:"; echo "$$out"; exit 1; }

.PHONY: tidy
tidy: ## sync every go.mod and go.sum
	$(call for_each_module,$(GO) mod tidy)

.PHONY: check
check: fmt-check vet race ## everything CI runs
