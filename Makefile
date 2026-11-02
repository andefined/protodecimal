MODULE := github.com/andefined/protodecimal/go

.PHONY: plugins generate lint lint-proto lint-go lint-python lint-rust breaking \
	fmt fmt-go fmt-python fmt-rust test test-go test-python test-native \
	bench bench-go bench-python compare help

plugins: ## Build the protoc plugin for Go.
	cd go && go build -o ../bin/protoc-gen-go-decimal ./cmd/protoc-gen-go-decimal

# Requires buf and protoc-gen-go on PATH. See buf.gen.yaml. Runs under
# `uv run` because py/protoc-gen-python-decimal (invoked by buf) needs
# the uv-managed protobuf install on its PATH to import google.protobuf.
generate: plugins ## Generate Go and Python code from the .proto files via buf.
	uv run buf generate

lint: lint-proto lint-go lint-python lint-rust ## Run all linters (proto, Go, Python, Rust).

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
	cd py && PROTODECIMAL_REQUIRE_NATIVE=1 PYTHONPATH=. uv run --group native --project .. python3 -m unittest discover tests -v

bench: bench-go bench-python ## Run both Go and Python benchmarks.

bench-go: ## Run Go benchmarks.
	cd go && go test -run '^$$' -bench . -benchmem ./...

bench-python: ## Run Python benchmarks.
	cd py && PROTODECIMAL_REQUIRE_NATIVE=1 PYTHONPATH=. uv run --group native --project .. pytest tests/test_benchmarks.py --benchmark-only

# SAMPLE is a CSV of price,quantity rows; without it, synthetic values are used.
compare: ## Compare decimal strings with protodecimal: size and CPU, Go and Python.
	cd py && PYTHONPATH=. uv run --group native --project .. python3 -m example.compare $(if $(SAMPLE),--sample $(abspath $(SAMPLE)))

help: ## Show this help message.
	@echo "Usage: make [target]"
	@echo ""
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
