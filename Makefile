PROVIDER     := pulumi-resource-anthropic
VERSION      ?= 0.1.0-dev
LDFLAGS      := -X github.com/pulumi/pulumi-anthropic/provider.Version=$(VERSION)
SCHEMA       := provider/schema.json
LANGUAGES    := nodejs python go dotnet java

.PHONY: build
build: ## Build the provider plugin into bin/.
	go build -ldflags "$(LDFLAGS)" -o bin/$(PROVIDER) ./cmd/$(PROVIDER)

.PHONY: install
install: build ## Install the locally built plugin for use by Pulumi programs.
	pulumi plugin install resource anthropic $(VERSION) --file bin/$(PROVIDER) --reinstall

.PHONY: test
test: ## Run the unit and lifecycle tests against the fake API.
	go test -race ./provider/...

.PHONY: schema
schema: ## Regenerate the golden schema after an intentional change to the surface.
	go test ./provider -run TestSchema -update

.PHONY: lint
lint: ## Lint the Go code.
	gofmt -l provider cmd examples | tee /dev/stderr | (! read)
	go vet ./...
	golangci-lint run --timeout 5m

.PHONY: sdks
sdks: $(foreach lang,$(LANGUAGES),sdk_$(lang)) ## Generate every language SDK from the schema.

sdk_%: $(SCHEMA)
	rm -rf sdk/$*
	pulumi package gen-sdk $(SCHEMA) --language $* --out sdk --version $(VERSION)

# The Go SDK is its own module, so programs do not depend on the provider's dependencies.
sdk_go: $(SCHEMA)
	rm -rf sdk/go/anthropic
	pulumi package gen-sdk $(SCHEMA) --language go --out sdk --version $(VERSION)
	cd sdk && go mod tidy

.PHONY: build_nodejs
build_nodejs: sdk_nodejs ## Build the Node.js SDK into sdk/nodejs/bin.
	cd sdk/nodejs && npm install && npx tsc && cp package.json ../../README.md bin/

.PHONY: test_acceptance
test_acceptance: build build_nodejs sdk_python ## Run the examples against the live Anthropic API (needs credentials).
	cd examples && go test -v -timeout 60m -tags acceptance ./...

.PHONY: help
help:
	@grep -E '^[a-z_%]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "%-18s %s\n", $$1, $$2}'
