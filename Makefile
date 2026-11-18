PROVIDER     := pulumi-resource-anthropic
VERSION      ?= 0.1.0-dev
PROVIDER_DIR := provider
LDFLAGS      := -X github.com/pulumi-labs/pulumi-anthropic/provider/pkg/version.Version=$(VERSION)
SCHEMA       := provider/schema.json
LANGUAGES    := nodejs python go dotnet java
ACCEPTANCE_CREDENTIAL_ENV ?= ANTHROPIC_API_KEY

.PHONY: build
build: ## Build the provider plugin into bin/.
	cd $(PROVIDER_DIR) && go build -ldflags "$(LDFLAGS)" -o ../bin/$(PROVIDER) ./cmd/$(PROVIDER)

.PHONY: install
install: build ## Install the locally built plugin for use by Pulumi programs.
	pulumi plugin install resource anthropic $(VERSION) --file bin/$(PROVIDER) --reinstall

.PHONY: test_engine
test_engine: ## Run one approved live YAML engine scenario: make test_engine RESOURCE=Agent LIVE=1.
	@test "$(LIVE)" = "1" || { echo 'live scope must be approved; pass LIVE=1 explicitly'; exit 1; }
	@printf '%s\n' "$(RESOURCE)" | grep -Eq '^[A-Z][A-Za-z0-9]*$$' || { echo 'set RESOURCE to an exact resource member or Models'; exit 1; }
	cd test && go test -mod=readonly -tags acceptance -count=1 -timeout 30m -run '^TestLive$(RESOURCE)$$' . -live -provider-version "$(VERSION)" $(ARGS)

.PHONY: test
test: ## Run the unit and lifecycle tests against the fake API.
	go test -race ./provider/...

.PHONY: schema
schema: ## Regenerate the golden schema after an intentional change to the surface.
	go test ./provider -run TestSchema -update

# golangci-lint refuses to run when it was built against an older Go than go.mod
# targets, which the version most package managers ship usually is. Prefer the version
# mise.toml pins, and fall back to whatever is on PATH.
GOLANGCI := $(shell command -v mise >/dev/null 2>&1 && echo "mise exec golangci-lint -- golangci-lint" || echo golangci-lint)

.PHONY: lint
lint: ## Lint the Go code.
	gofmt -l provider test | tee /dev/stderr | (! read)
	go vet ./...
	$(GOLANGCI) run --timeout 5m

.PHONY: sdks
sdks: $(foreach lang,$(LANGUAGES),sdk_$(lang)) ## Generate every language SDK from the schema.

sdk_%: $(SCHEMA)
	rm -rf sdk/$*
	pulumi package gen-sdk $(SCHEMA) --language $* --out sdk --version $(VERSION)

# The Go SDK is its own module, so programs do not depend on the provider's
# dependencies. The module lives at the package directory, not at sdk/, so the
# Go proxy can serve it from a sdk/go/anthropic/vX.Y.Z tag.
sdk_go: $(SCHEMA)
	rm -rf sdk/go/anthropic
	pulumi package gen-sdk $(SCHEMA) --language go --out sdk --version $(VERSION)
	cd sdk/go/anthropic && go mod init github.com/pulumi-labs/pulumi-anthropic/sdk/go/anthropic && go mod tidy

.PHONY: build_nodejs
build_nodejs: sdk_nodejs ## Build the Node.js SDK into sdk/nodejs/bin.
	cd sdk/nodejs && npm install && npx tsc && cp package.json ../../README.md bin/

# Compile each generated SDK to validate its language-specific build.
.PHONY: build_go
build_go: sdk_go ## Compile the Go SDK.
	cd sdk/go/anthropic && go build ./...

.PHONY: build_python
build_python: sdk_python ## Build the Python SDK wheel into sdk/python/dist.
	python3 -m venv bin/python-sdk-venv
	bin/python-sdk-venv/bin/python -m pip install --quiet --upgrade pip build
	cd sdk/python && $(CURDIR)/bin/python-sdk-venv/bin/python -m build --wheel --outdir dist/

.PHONY: build_dotnet
build_dotnet: sdk_dotnet ## Build the .NET SDK.
	cd sdk/dotnet && dotnet build

.PHONY: build_java
build_java: sdk_java ## Build the Java SDK. Gradle 9 needs JDK 17 or newer to run.
	cd sdk/java && gradle build

.PHONY: build_sdks
build_sdks: $(foreach lang,$(LANGUAGES),build_$(lang)) ## Generate and compile every language SDK.

# Substitute VERSION into the language-SDK manifests. In the normal flow
# `make sdks VERSION=...` already bakes the right version in, because the
# target passes it to `pulumi package gen-sdk --version`. This target is a
# fallback for SDKs generated with the placeholder version and then published.
.PHONY: set_version
set_version: ## Stamp VERSION into the generated SDK manifests.
	@if [ -f sdk/nodejs/package.json ]; then \
		sed -i.bak -E 's/"version": "[^"]+"/"version": "$(VERSION)"/' sdk/nodejs/package.json && \
		rm -f sdk/nodejs/package.json.bak; \
	fi
	@if [ -f sdk/python/pyproject.toml ]; then \
		sed -i.bak -E 's/^( *version *= *)"[^"]+"/\1"$(VERSION)"/' sdk/python/pyproject.toml && \
		rm -f sdk/python/pyproject.toml.bak; \
	fi
	@if [ -f sdk/dotnet/Pulumi.Labs.Anthropic.csproj ]; then \
		sed -i.bak -E 's|<Version>[^<]+</Version>|<Version>$(VERSION)</Version>|' sdk/dotnet/Pulumi.Labs.Anthropic.csproj && \
		rm -f sdk/dotnet/Pulumi.Labs.Anthropic.csproj.bak; \
	fi
# sdk/java carries no version literal to stamp. Its build.gradle resolves the
# version from the PACKAGE_VERSION environment variable, which the release
# workflow sets on the publish step.

# Explicit non-admin allowlist: new scenarios do not enter live CI automatically.
# Tunnel scenarios require separately verified Tunnels API access and remain manual.
.PHONY: test_acceptance
test_acceptance: build build_nodejs ## Run the approved workspace live scenarios and AgentDelivery.
	@cd test && go test -mod=readonly -count=1 -v -timeout 60m -tags acceptance -run '^Test(AgentDelivery|Live(Agent|Deployment|Environment|MemoryStore|Models|Skill|UserProfile|Vault|VaultCredential))$$' . -live -live-examples -credential-env "$(ACCEPTANCE_CREDENTIAL_ENV)" -provider-version "$(VERSION)" $(ARGS)

.PHONY: test_examples
test_examples: build build_nodejs sdk_python ## Run all three live sample programs: make test_examples LIVE=1.
	@test "$(LIVE)" = "1" || { echo 'live scope must be approved; pass LIVE=1 explicitly'; exit 1; }
	cd test && go test -mod=readonly -count=1 -v -timeout 60m -tags acceptance -run '^Test(AgentDelivery|OrgBootstrap|WorkloadIdentity)$$' . -live-examples

.PHONY: help
help:
	@grep -E '^[a-z_%]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "%-18s %s\n", $$1, $$2}'
