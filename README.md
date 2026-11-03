# protodecimal

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

Python, from a checkout:

```sh
pip install .              # protodecimal and protoc-gen-python-decimal
pip install ./py/native    # optional fast encoder; needs Rust and Python 3.10+
```

The Python package needs `protobuf>=6.30,<7`.

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

## Comparing with strings

`make compare` measures size and encode and decode time against string fields in both languages. It uses generated values with 2 to 4, 12 to 28 and 36 decimal places. Results depend a lot on the values, so try it with your own data:

```sh
make compare SAMPLE=levels.csv   # price,quantity rows, no header
```
