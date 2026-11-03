# protodecimal

[![CI](https://github.com/andefined/protodecimal/actions/workflows/ci.yml/badge.svg)](https://github.com/andefined/protodecimal/actions/workflows/ci.yml)

Lossless decimals for protobuf, with code generators that convert fields to and from [`shopspring/decimal`](https://github.com/shopspring/decimal) in Go and `decimal.Decimal` in Python.

It is built for order books, where prices and quantities carry many decimal places. A 25-level snapshot with 12 to 28 decimal places takes 876 bytes, compared with 1,407 bytes as strings.

## Wire format

```proto
message Decimal {
  bool   negative    = 1;
  bytes  coefficient = 2;
  sint32 exponent    = 3;
}
// value = (negative ? -1 : 1) * coefficient * 10^exponent
```

- `negative` is left off the wire when false.
- `coefficient` is the magnitude as big-endian bytes. Empty means zero.
- `exponent` uses zigzag encoding, so small negative exponents take one byte.
- Values keep their scale: `1.50` is stored as `150` and `-2`.

Below about 10 significant digits, a decimal is about the same size as a string.

## Installation

Go:

```sh
go get github.com/andefined/protodecimal/go/decimalconv
go install github.com/andefined/protodecimal/go/cmd/protoc-gen-go-decimal@latest
```

Python:

```sh
pip install protodecimal              # includes protoc-gen-python-decimal
pip install "protodecimal[native]"    # adds the fast encoder, Python 3.10+
```

The Python package needs `protobuf>=6.30,<7`. The fast encoder has prebuilt wheels for Linux and macOS (x86-64 and ARM) and Windows (x86-64). On other platforms, pip builds it from source, which needs Rust.

The schema is also on the Buf Schema Registry as `buf.build/andefined/protodecimal`.

Documentation: [Go](https://pkg.go.dev/github.com/andefined/protodecimal/go), [Python](https://andefined.github.io/protodecimal/).

## Usage

Add the plugins to your `buf.gen.yaml` (see the one in this repo). For each field of type `protodecimal.v1.Decimal`, they generate accessors:

```go
level.SetPriceDecimal(decimal.RequireFromString("65122.99"))
price := level.GetPriceDecimal()
```

```python
import orderbook_pb2_decimal  # adds the accessors to orderbook_pb2

level.set_price_decimal(Decimal("65122.99"))
price = level.get_price_decimal()
```

Repeated fields get the same accessors, using slices and lists.

### Fast paths

For busy services there are two faster options. Both use the same wire format, so each side can switch on its own.

**Python `fast_encode`** writes a whole message to bytes in one call. Pass field values in declaration order: nested messages as tuples, repeated fields as lists, and `None` for unset fields.

```python
wire = orderbook_pb2.Snapshot.fast_encode(
    "BTC-USDT",
    [(Decimal("65122.99"), Decimal("0.50"))],
)
```

It needs the native encoder. Without it, `fast_encode` still returns the same bytes but is slower than the normal path, so check `protodecimal.fast.NATIVE` at startup. Messages with maps, groups or required fields don't get `fast_encode`.

**Go `Decimals` decoders** read wire bytes straight into decimals:

```go
var snapshot orderbookpb.SnapshotDecimals // reuse it between calls
if err := snapshot.Unmarshal(raw); err != nil {
	return err
}
price := snapshot.Bids[0].Price
```

They are generated for proto3 messages without maps, oneofs, `optional` fields, recursive types or message types from other files. Unlike `proto.Unmarshal`, they reject a singular message field that appears twice. Encoding in Go still uses the accessors and `proto.Marshal`.

Both fast paths are generated only with the default plugin options.

## Limits

Treat decimals from other services as untrusted input. Large ones are cheap to send but slow to convert.

| Limit | Value |
| --- | --- |
| Coefficient size | 16 KiB (about 39,000 digits) |
| Exponent when decoding in Go | ±6,144 |

Python checks the limits on every conversion, and Go's `Decimals` decoders check them while decoding. Go's generated getters can't return errors, so if you use `proto.Unmarshal`, validate each message once:

```go
if err := decimalconv.ValidateMessage(&snapshot); err != nil {
	return err
}
```

## Plugin options

- `decimal_type`: the message used as the decimal type. Default `protodecimal.v1.Decimal`.
- `conv`: the conversion package. Defaults `github.com/andefined/protodecimal/go/decimalconv` (Go) and `protodecimal.conv` (Python).

The Go plugin also accepts the usual `module=` and `paths=` options.

## Development

You need `buf`, `protoc-gen-go`, [`uv`](https://docs.astral.sh/uv/) and a Rust toolchain.

```sh
make generate   # regenerate code from proto/
make test       # Go, Python, and Python with the native encoder
make lint
make bench      # Go and Python benchmarks
make help       # all targets
```

Python code is generated with buf's remote plugin, pinned to `v30.0` to match the `protobuf` runtime, so `make generate` needs network access.

## Versioning

The Go module and both Python packages share one version number and follow [Semantic Versioning](https://semver.org). Before 1.0, a minor release may include breaking changes. Changes are listed in [CHANGELOG.md](CHANGELOG.md).

- Go versions come from `go/vX.Y.Z` tags, because the module lives in `go/`. Python releases are tagged `vX.Y.Z`.
- `protodecimal` and `protodecimal-native` must be the same version. If they aren't, `fast_encode` ignores the native encoder and warns.
- Both plugins print their version with `--version`.
- The wire format is versioned separately, by its package name. `protodecimal.v1` will not change in ways that break existing data, and `make breaking` checks this. An incompatible format would be a new `protodecimal.v2` package.

To release:

```sh
make set-version V=0.2.0    # updates both Python packages and the lock files
# move the Unreleased notes in CHANGELOG.md under "## 0.2.0", then commit
make tag                    # creates v0.2.0 and go/v0.2.0
git push origin v0.2.0 go/v0.2.0
```

Pushing the tags starts the Release workflow. It checks that the tags, versions and changelog agree, runs CI, then builds and tests the wheels on each platform. After that it publishes to PyPI and the Buf Schema Registry, makes the Go version available through the Go proxy, creates the GitHub release from the changelog notes, and updates the Python docs.

## Continuous integration

Every push to `main` and every pull request runs:

- version and lock file checks
- `buf lint`, plus `buf breaking` against the base branch on pull requests
- a check that generated code matches `make generate`
- Go tests on 1.22 and the latest Go, golangci-lint, and 30 seconds of each fuzz test
- Python tests on 3.9 to 3.14, with and without the native encoder, and on the oldest supported `protobuf`
- ruff, rustfmt and clippy

Pull requests that change packaging also build and test the wheels, and changes to the Python package build the docs.

## Comparing with strings

`make compare` measures size and encode and decode time against string fields in both languages. It uses generated values with 2 to 4, 12 to 28 and 36 decimal places. Results depend a lot on the values, so try it with your own data:

```sh
make compare SAMPLE=levels.csv   # price,quantity rows, no header
```
