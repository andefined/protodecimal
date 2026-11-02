# protodecimal

Compact, lossless arbitrary-precision decimals for protobuf, including protoc plugins that generate accessors converting directly to and from `github.com/shopspring/decimal` (Go) and `decimal.Decimal` (Python) — no hand-written conversion at call sites. For hot paths, `fast_encode` (Python) and generated `Decimals` decoders (Go) work directly on wire bytes; see [Fast paths](#fast-paths).

Built for order-book style payloads where prices and quantities routinely exceed 30 significant digits. For 25 levels of ~36-digit values, the decimal encoding measures **810 bytes vs 2,160 bytes** for the equivalent string fields — a 62.5% reduction.

## Wire format

```proto
message Decimal {
  bool   negative    = 1;  // omitted on the wire when positive
  bytes  coefficient = 2;  // big-endian magnitude; empty = 0
  sint32 exponent    = 3;  // zigzag; typical small negatives cost 1 byte
}
// value = (negative ? -1 : 1) * coefficient * 10^exponent
```

`negative` is a boolean, and is omitted from the wire entirely when false, so the common case of a positive value costs 0 bytes for the sign.

`coefficient` is the raw output of Go's `big.Int.Bytes()` / Python's `int.to_bytes(..., "big")`. Go converts directly between `big.Int` and those bytes; Python has to go through the string form of `decimal.Decimal`, so its conversions are slower. An empty `coefficient` means 0; leading zero bytes are permitted but encoders SHOULD emit the minimal representation. The representation is deliberately not normalized: `1.50` encodes as coefficient `150` / exponent `-2` and round-trips with its scale intact. Coefficients are limited to 16 KiB, about 39,000 significant digits (see [Limits](#limits)).

`exponent` is `sint32` (zigzag) because exponents are typically small negative numbers (-2, -8, -18, ...), which zigzag-encode in 1 byte instead of the 5 bytes plain varint encoding would cost for a negative value. 

*Size intuition: an n-digit coefficient costs about `n/2.4` bytes plus 4–6 bytes of framing. Below roughly 10 significant digits the encoding is about the same size as a string, so this format pays off exactly in the >30-digit cases it was built for.*

### Limits

Decimals are small on the wire but can be expensive to use, so treat them as untrusted input when they come from another service.

| Limit | Value | Enforced by |
| --- | --- | --- |
| Coefficient size | 16 KiB | Python `conv` and `fast_encode`; Go `Validate` and `Decimals` decoders |
| Exponent, decoding safety | ±6,144 | Go `Validate` and `Decimals` decoders |
| Exponent, wire range | `sint32` | Python `conv` on encode |

Python decoding cost grows quadratically with coefficient size (71 seconds for 1 MB), and Go rendering cost grows with the exponent (33 seconds and 50 MB for an exponent of 50,000,000).

Generated Go getters can't return errors and don't apply these limits, so unless you decode with a `Decimals` decoder, validate each message once when it arrives:

```go
if err := proto.Unmarshal(raw, &snapshot); err != nil {
	return err
}
if err := decimalconv.ValidateMessage(&snapshot); err != nil {
	return err
}
```

`ValidateMessage` checks every decimal in the message, including repeated fields, map values and oneof members, but not decimals inside `google.protobuf.Any`. Python raises `ValueError` from `to_decimal` and the generated getters, and `fill` and the generated setters reject a bad value before changing the message.

### Installation

The Go module is `github.com/andefined/protodecimal/go`:

```sh
go get github.com/andefined/protodecimal/go/decimalconv
go install github.com/andefined/protodecimal/go/cmd/protoc-gen-go-decimal@latest
```

The repo root `pyproject.toml` packages the `py/protodecimal` package for pip (source lives under `py/` so it sits next to the Go module instead of inside it). Its only runtime dependency is `protobuf>=6.30,<7`, because the shipped generated code is stamped 6.30.0 and won't import on an older major version — the full test suite runs on both ends of that range. Install it into your services with `pip install .` (or reference this repo from your requirements as a path or VCS dependency); this also puts the codegen plugin on PATH as a console script named `protoc-gen-python-decimal`, which is how CI boxes should get it. Inside this repo, buf uses the wrapper script at `py/protoc-gen-python-decimal`, which needs no installation.

Local development uses [`uv`](https://docs.astral.sh/uv/): `uv sync` creates a `.venv` and installs the pinned dependency graph from `uv.lock`, and `make test-python` / `make generate` run through `uv run` so they always use that pinned `protobuf` version rather than whatever `python3` happens to resolve to on PATH — the source of the gencode/runtime version mismatch described below.

## Fast paths

Both are optional, use the same wire format as everything else, and can be adopted on one side at a time.

### Python: `fast_encode`

Building a message field by field is the slow part of encoding from Python. With the default plugin options, each message that holds a decimal gets a `fast_encode` static method that takes field values in declaration order and returns what `SerializeToString` would, byte for byte:

```python
wire = orderbook_pb2.Snapshot.fast_encode(
    "BTC-USDT",                                # symbol
    [(Decimal("65122.99"), Decimal("0.50"))],  # bids
)
```

Nested messages are tuples of their own fields, repeated fields are lists or tuples, and `None` leaves a field unset. Trailing fields can be left out. Messages using maps, groups or required fields don't get the method.

The speed comes from `protodecimal-native`, a compiled extension in `py/native`. It reads a table built from the message descriptor, so new schemas need no Rust changes. It is optional and needs Python 3.10+: without it, `fast_encode` builds a normal message, which gives the same bytes much more slowly. Check `protodecimal.fast.NATIVE` to see which one you have. It isn't on PyPI yet, so install it from a checkout with `pip install ./py/native`, which needs a Rust toolchain.

### Go: `Decimals` decoders

With the default plugin options, `protoc-gen-go-decimal` also generates a `<Message>Decimals` struct that decodes wire bytes directly into `shopspring` decimals, without allocating protobuf messages:

```go
var snapshot orderbookpb.SnapshotDecimals // reuse it to keep its slices
if err := snapshot.Unmarshal(raw); err != nil {
	return err
}
price := snapshot.Bids[0].Price
```

Singular message fields also get a `Has<Field>` flag. `Unmarshal` accepts what `proto.Unmarshal` accepts, with two exceptions: it applies the limits above, and it rejects a singular message field that appears twice (`decimalconv.ErrDuplicateField`), which `proto.Unmarshal` would merge. That only comes up with concatenated messages. Unknown fields are skipped, so older readers still work with newer schemas.

Only proto3 messages get a decoder, and messages with maps, oneofs, `optional` fields, cyclic types or message types from other files are left out. Use `proto.Unmarshal` for those. `FromShopspring` and `proto.Marshal` remain the way to encode in Go.

## Code generation (buf)

The repo is a [buf](https://buf.build) workspace: `buf.yaml` defines the module rooted at `proto/` with `STANDARD` lint rules and `FILE`-level breaking-change detection (the schema is lint-clean); `buf.gen.yaml` wires all four generators. Day to day:

```sh
make generate            # uv run buf generate, all four plugins
buf lint                 # schema style checks
buf breaking --against '.git#branch=main'   # wire-compat gate for CI
```

Prerequisites on PATH: `buf`, `protoc-gen-go` (`go install google.golang.org/protobuf/cmd/protoc-gen-go@latest`), and [`uv`](https://docs.astral.sh/uv/). `make generate` depends on `plugins` (builds the Go decimal plugin into `bin/`), then runs `uv run buf generate` — the `uv run` wrapper puts the uv-managed `protobuf` install on PATH so the Python decimal plugin (`py/protoc-gen-python-decimal`, invoked by buf) can import `google.protobuf`.

Python message code comes from the BSR remote plugin `buf.build/protocolbuffers/python`, pinned to `v30.0` in `buf.gen.yaml` rather than left on `latest` or routed through a local `protoc` (buf's `protoc_builtin: python`, the offline alternative, always shells out to whatever `protoc` is on PATH and stamps generated code with that binary's own bundled protobuf version — which can run ahead of this repo's pinned `protobuf<7` runtime and then fail to import with `Detected incompatible Protobuf Gencode/Runtime versions`). `v30.0` stamps gencode as protobuf 6.30.0, which is why the pin starts there. This needs network access to Buf's registry at generate time; swap back to `protoc_builtin: python` (see the comment in `buf.gen.yaml`) if that's not available.

`fast_encode` and the `Decimals` decoders are only generated with the default `decimal_type` and `conv`, because they depend on the default wire layout.

Plain protoc still works if needed — invoke the same plugins with `--go-decimal_out`/`--python-decimal_out` and the options below.

Plugin options (comma-separated in `--go-decimal_opt` / `--python-decimal_opt`): `decimal_type=` overrides the proto message treated as the decimal wire type (default `protodecimal.v1.Decimal`), and
`conv=` overrides the conversion package — a Go import path for the Go plugin (default `github.com/andefined/protodecimal/go/decimalconv`), a Python module for the Python plugin (default `protodecimal.conv`). The Go plugin also honors the standard `module=`/`paths=` output-path options.

If you fork this under a different module path, rename it in `go/go.mod` (and the Go plugin's `conv` default, the `module=` options in `buf.gen.yaml`, and the `go_package` options, currently `.../go/decimalpb` and `.../go/example/orderbookpb`) from `github.com/andefined/protodecimal/go` to your real repository, then run `go mod tidy` (from `go/`) to refresh `go.sum`.

## Benchmarks

Both languages benchmark the same two hot paths — `Decimal`<->native conversion and raw proto wire marshal/unmarshal — over a 25-level `Snapshot` of ~36-digit values, and compare each one against the same data stored as decimal strings (`StringSnapshot`). Marshal and unmarshal alone flatter strings, because nothing has been parsed yet; the `SnapshotEncode` and `SnapshotDecode` benchmarks include the conversion to and from native decimals, so they are the fair comparison.

The [fast paths](#fast-paths) appear as `fast` variants wherever they apply: Python encoding (`fast_encode`) and Go decoding (`decimalconv.Unmarshal` and the `Decimals` decoders). `make bench-python` fails if the native encoder doesn't load, so those numbers are never the pure-Python fallback. The benchmark price needs more than 128 bits, which takes the encoder's slower route; `make compare` reports more realistic values.

```sh
make bench            # both languages
make bench-go         # go test -bench, via testing.B
make bench-python     # pytest-benchmark (dev dependency)
```

### Comparing with decimal strings

To measure what moving from string fields to protodecimal gains, use `make compare`. It reports payload size, raw and gzipped, and the CPU cost of encoding and decoding in Go and Python, including each producer -> consumer route. The string side parses with `decimal.NewFromString` (Go) and `decimal.Decimal` (Python).

```sh
make compare                     # synthetic values at 2-4, 12-28 and 36 decimal places
make compare SAMPLE=levels.csv   # your own values
```

`SAMPLE` is a CSV of `price,quantity` rows with no header, grouped into 25-level snapshots. Results depend heavily on the values, so a sample of real traffic is the number to trust.

## References

* [General Decimal Arithmetic Specification](https://speleotrove.com/decimal/decarith.html)
* [shopspring/decimal](https://github.com/shopspring/decimal)
* [buf](https://buf.build)