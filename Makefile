MODULE := github.com/andefined/protodecimal/go

# The Python packages each record the version; the Go module takes it from tags.
VERSION := $(shell sed -n 's/^__version__ = "\(.*\)"/\1/p' py/protodecimal/__init__.py)
NATIVE_VERSION := $(shell sed -n 's/^version = "\(.*\)"/\1/p' py/native/Cargo.toml)
EXTRA_VERSION := $(shell sed -n 's/.*"protodecimal-native==\([^;"]*\).*/\1/p' pyproject.toml)

# protoc-gen-go formats its output with the formatter of the Go that built it,
# and newer Go versions lay out some comments differently. Pinning both keeps
# make generate identical everywhere. Go downloads the toolchain if needed.
PROTOC_GEN_GO_VERSION := v1.33.0
PROTOC_GEN_GO_TOOLCHAIN := go1.27.3

.PHONY: plugins generate lint lint-proto lint-go lint-python lint-rust breaking \
	fmt fmt-go fmt-python fmt-rust test test-go test-python test-native \
	bench bench-go bench-python compare version set-version tag help

plugins: ## Build the Go protoc plugins into bin/.
	cd go && go build -o ../bin/protoc-gen-go-decimal ./cmd/protoc-gen-go-decimal
	GOTOOLCHAIN=$(PROTOC_GEN_GO_TOOLCHAIN) GOBIN=$(CURDIR)/bin \
		go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)

# Requires buf on PATH. See buf.gen.yaml. Runs under
# `uv run` because py/protoc-gen-python-decimal (invoked by buf) needs
# the uv-managed protobuf install on its PATH to import google.protobuf.
generate: plugins ## Generate Go and Python code from the .proto files via buf.
	uv run buf generate

lint: version lint-proto lint-go lint-python lint-rust ## Run all linters (proto, Go, Python, Rust).

lint-proto: ## Run buf lint to check for linting issues in the .proto files.
	buf lint

lint-go: ## Run golangci-lint on Go code.
	cd go && golangci-lint run ./...

lint-python: ## Run ruff check + format --check on Python code.
	uv run ruff check py/
	uv run ruff format --check py/

lint-rust: ## Run rustfmt --check and clippy on the native encoder.
	cd py/native && cargo fmt --check
	cd py/native && cargo clippy --release -- -D warnings

breaking: ## Check for breaking changes in the .proto files compared to the main branch.
	buf breaking --against '.git#branch=main'

fmt: fmt-go fmt-python fmt-rust ## Auto-format Go, Python and Rust code.

fmt-go: ## Run gofmt -w and goimports -w on Go code.
	gofmt -w go/
	cd go && golangci-lint fmt ./...

fmt-python: ## Run ruff format on Python code.
	uv run ruff format py/
	uv run ruff check --fix py/

fmt-rust: ## Run cargo fmt on the native encoder.
	cd py/native && cargo fmt

test: test-go test-python test-native ## Run Go and Python tests, with the native encoder too.

test-go: ## Run Go tests.
	cd go && go test ./...

test-python: ## Run Python tests.
	cd py && PYTHONPATH=. uv run --project .. python3 -m unittest discover tests -v

# Fails rather than skips if the extension doesn't load.
test-native: ## Build the native encoder and run the Python tests against it.
	cd py && PROTODECIMAL_REQUIRE_NATIVE=1 PYTHONPATH=. uv run --extra native --project .. python3 -m unittest discover tests -v

bench: bench-go bench-python ## Run both Go and Python benchmarks.

bench-go: ## Run Go benchmarks.
	cd go && go test -run '^$$' -bench . -benchmem ./...

bench-python: ## Run Python benchmarks.
	cd py && PROTODECIMAL_REQUIRE_NATIVE=1 PYTHONPATH=. uv run --extra native --project .. pytest tests/test_benchmarks.py --benchmark-only

# SAMPLE is a CSV of price,quantity rows; without it, synthetic values are used.
compare: ## Compare decimal strings with protodecimal: size and CPU, Go and Python.
	cd py && PYTHONPATH=. uv run --extra native --project .. python3 -m example.compare $(if $(SAMPLE),--sample $(abspath $(SAMPLE)))

version: ## Print the version, and fail if the two Python packages disagree.
	@test "$(VERSION)" = "$(NATIVE_VERSION)" || \
		{ echo "protodecimal is $(VERSION) but protodecimal-native is $(NATIVE_VERSION)" >&2; exit 1; }
	@test "$(VERSION)" = "$(EXTRA_VERSION)" || \
		{ echo "protodecimal is $(VERSION) but its native extra pins $(EXTRA_VERSION)" >&2; exit 1; }
	@echo $(VERSION)

set-version: ## Set the version of both Python packages: make set-version V=0.2.0
	@echo "$(V)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$$' || \
		{ echo "usage: make set-version V=x.y.z" >&2; exit 1; }
	sed -i.bak 's/^__version__ = ".*"/__version__ = "$(V)"/' py/protodecimal/__init__.py
	sed -i.bak 's/^version = ".*"/version = "$(V)"/' py/native/Cargo.toml
	sed -i.bak 's/"protodecimal-native==[^;"]*/"protodecimal-native==$(V)/' pyproject.toml
	rm py/protodecimal/__init__.py.bak py/native/Cargo.toml.bak pyproject.toml.bak
	cd py/native && cargo update --workspace --offline
	uv lock

tag: version ## Tag HEAD as v$(VERSION) and go/v$(VERSION), after checks.
	@test -z "$$(git status --porcelain)" || { echo "commit your changes first" >&2; exit 1; }
	@grep -q '^## $(VERSION)' CHANGELOG.md || \
		{ echo "CHANGELOG.md has no '## $(VERSION)' section" >&2; exit 1; }
	git tag -a v$(VERSION) -m "protodecimal $(VERSION)"
	git tag -a go/v$(VERSION) -m "protodecimal Go module $(VERSION)"

help: ## Show this help message.
	@echo "Usage: make [target]"
	@echo ""
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
