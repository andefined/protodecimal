"""Benchmarks for the conversion and wire hot paths.

Run from the py/ directory:
    PYTHONPATH=. uv run --project .. pytest tests/test_benchmarks.py --benchmark-only

Mirrors go/example/example_bench_test.go so the two languages can be
compared on equivalent workloads.

`make bench-python` sets PROTODECIMAL_REQUIRE_NATIVE, so that the fast
benchmarks can't quietly time the pure-Python fallback.
"""

import os
from decimal import Decimal

import example.v1.orderbook_pb2 as orderbook_pb2
import example.v1.orderbook_pb2_decimal  # noqa: F401  attaches the methods
from protodecimal import conv, fast

if os.environ.get("PROTODECIMAL_REQUIRE_NATIVE") and not fast.NATIVE:
    raise RuntimeError("protodecimal_native did not load")

PRICE = Decimal("65123.123456789012345678901234567890123456")
QTY = Decimal("0.000000000000000000000000000000000042")

WIRE_PRICE = conv.from_decimal(PRICE)


def make_snapshot(n_levels: int = 25) -> orderbook_pb2.Snapshot:
    snap = orderbook_pb2.Snapshot(symbol="BTC-USDT")
    for _ in range(n_levels):
        level = snap.bids.add()
        conv.fill(level.price, PRICE)
        conv.fill(level.quantity, QTY)
    return snap


SNAPSHOT = make_snapshot()
SNAPSHOT_WIRE = SNAPSHOT.SerializeToString()


def test_from_decimal(benchmark):
    benchmark(conv.from_decimal, PRICE)


def test_to_decimal(benchmark):
    benchmark(conv.to_decimal, WIRE_PRICE)


def test_wire_marshal(benchmark):
    benchmark(SNAPSHOT.SerializeToString)


def test_wire_unmarshal(benchmark):
    def run():
        return orderbook_pb2.Snapshot.FromString(SNAPSHOT_WIRE)

    benchmark(run)


# The benchmarks below compare Decimal against the usual alternative, a
# decimal string, on the same 25 levels, grouped so each pair prints as its
# own table. String fields parse cheaply because nothing has been converted
# yet, so "snapshot encode" and "snapshot decode", which include the
# conversion to and from decimal.Decimal, are the fair comparison.
#
# Strings use format(value, "f") rather than str(): str() renders QTY as
# "4.2E-35", which would make the payload differ from the Go benchmarks.


def make_string_snapshot(n_levels: int = 25) -> orderbook_pb2.StringSnapshot:
    snap = orderbook_pb2.StringSnapshot(symbol="BTC-USDT")
    for _ in range(n_levels):
        level = snap.bids.add()
        level.price = format(PRICE, "f")
        level.quantity = format(QTY, "f")
    return snap


PRICE_STRING = format(PRICE, "f")
STRING_SNAPSHOT = make_string_snapshot()
STRING_SNAPSHOT_WIRE = STRING_SNAPSHOT.SerializeToString()


def test_value_encode_decimal(benchmark):
    benchmark.group = "value encode"
    benchmark(conv.from_decimal, PRICE)


def test_value_encode_fast(benchmark):
    # Does more than the decimal variant: it serializes, and wraps the value
    # in a Level, because fast_encode isn't attached to Decimal itself.
    benchmark.group = "value encode"
    benchmark(orderbook_pb2.Level.fast_encode, PRICE)


def test_value_encode_string(benchmark):
    benchmark.group = "value encode"
    benchmark(format, PRICE, "f")


def test_value_decode_decimal(benchmark):
    benchmark.group = "value decode"
    benchmark(conv.to_decimal, WIRE_PRICE)


def test_value_decode_string(benchmark):
    benchmark.group = "value decode"
    benchmark(Decimal, PRICE_STRING)


def test_snapshot_marshal_decimal(benchmark):
    benchmark.group = "snapshot marshal"
    benchmark(SNAPSHOT.SerializeToString)


def test_snapshot_marshal_string(benchmark):
    benchmark.group = "snapshot marshal"
    benchmark(STRING_SNAPSHOT.SerializeToString)


def test_snapshot_unmarshal_decimal(benchmark):
    benchmark.group = "snapshot unmarshal"
    benchmark(orderbook_pb2.Snapshot.FromString, SNAPSHOT_WIRE)


def test_snapshot_unmarshal_string(benchmark):
    benchmark.group = "snapshot unmarshal"
    benchmark(orderbook_pb2.StringSnapshot.FromString, STRING_SNAPSHOT_WIRE)


def test_snapshot_encode_decimal(benchmark):
    benchmark.group = "snapshot encode"
    benchmark(lambda: make_snapshot().SerializeToString())


def test_snapshot_encode_fast(benchmark):
    levels = [(PRICE, QTY)] * 25
    benchmark.group = "snapshot encode"
    benchmark(orderbook_pb2.Snapshot.fast_encode, "BTC-USDT", levels)


def test_snapshot_encode_string(benchmark):
    benchmark.group = "snapshot encode"
    benchmark(lambda: make_string_snapshot().SerializeToString())


def test_snapshot_decode_decimal(benchmark):
    def run():
        snap = orderbook_pb2.Snapshot.FromString(SNAPSHOT_WIRE)
        return [
            (conv.to_decimal(x.price), conv.to_decimal(x.quantity)) for x in snap.bids
        ]

    benchmark.group = "snapshot decode"
    benchmark(run)


def test_snapshot_decode_string(benchmark):
    def run():
        snap = orderbook_pb2.StringSnapshot.FromString(STRING_SNAPSHOT_WIRE)
        return [(Decimal(x.price), Decimal(x.quantity)) for x in snap.bids]

    benchmark.group = "snapshot decode"
    benchmark(run)
