"""Measures what moving from decimal strings to protodecimal gains.

Times a snapshot's encode and decode both ways in Go and Python on the same
levels, then reports payload size (raw and gzip) and the CPU cost of each
producer -> consumer route. The string side is the usual setup: string proto
fields parsed with decimal.NewFromString (Go) and decimal.Decimal (Python). The
decimal side uses normal protobuf messages, and the fast side uses fast_encode
(Python) and the generated SnapshotDecimals decoder (Go) on the same wire bytes.

    make compare                     # synthetic profiles
    make compare SAMPLE=levels.csv   # your own values, one price,quantity row each

Results depend heavily on the values, so prefer a sample of real traffic.
"""

from __future__ import annotations

import argparse
import csv
import gzip
import json
import random
import statistics
import subprocess
import tempfile
import timeit
from decimal import Decimal
from pathlib import Path

import example.v1.orderbook_pb2 as orderbook_pb2
import example.v1.orderbook_pb2_decimal  # noqa: F401  attaches the methods
from protodecimal import fast

SYMBOL = "BTC-USDT"
GO_DIR = Path(__file__).resolve().parents[2] / "go"
SYNTHETIC_LEVELS = 1000


def fraction(rng: random.Random, places: int) -> str:
    """Random decimal places with a non-zero last digit.

    shopspring's String trims trailing zeros, so a zero here would make the
    Go and Python string payloads differ.
    """
    digits = "".join(rng.choice("0123456789") for _ in range(places - 1))
    return digits + rng.choice("123456789")


def price_quantity(rng, places, whole_quantity):
    price = f"{65000 + rng.randrange(200)}.{fraction(rng, rng.randint(*places))}"
    quantity = f"{rng.randrange(whole_quantity)}.{fraction(rng, rng.randint(*places))}"
    return price, quantity


def synthetic_profiles():
    rng = random.Random(42)
    shapes = (
        ("synthetic: 2-4 decimal places", (2, 4), 10),
        ("synthetic: 12-28 decimal places", (12, 28), 1000),
        ("synthetic: 36 decimal places", (36, 36), 1),
    )
    for name, places, whole_quantity in shapes:
        rows = [
            price_quantity(rng, places, whole_quantity) for _ in range(SYNTHETIC_LEVELS)
        ]
        yield name, rows


def read_sample(path: Path):
    with path.open(newline="") as f:
        rows = [(row[0].strip(), row[1].strip()) for row in csv.reader(f) if row]
    return rows


def group_levels(rows, levels_per_snapshot):
    decimals = [(Decimal(price), Decimal(quantity)) for price, quantity in rows]
    usable = len(decimals) - len(decimals) % levels_per_snapshot
    if usable == 0:
        raise SystemExit(f"need at least {levels_per_snapshot} rows")
    return [
        decimals[start : start + levels_per_snapshot]
        for start in range(0, usable, levels_per_snapshot)
    ]


def encode_decimal(levels) -> bytes:
    snap = orderbook_pb2.Snapshot(symbol=SYMBOL)
    for price, quantity in levels:
        level = snap.bids.add()
        level.set_price_decimal(price)
        level.set_quantity_decimal(quantity)
    return snap.SerializeToString()


def encode_fast(levels) -> bytes:
    return orderbook_pb2.Snapshot.fast_encode(SYMBOL, levels)


def encode_string(levels) -> bytes:
    snap = orderbook_pb2.StringSnapshot(symbol=SYMBOL)
    for price, quantity in levels:
        level = snap.bids.add()
        # Plain notation like Go: str() can switch to scientific notation.
        level.price = format(price, "f")
        level.quantity = format(quantity, "f")
    return snap.SerializeToString()


def decode_decimal(raw: bytes):
    snap = orderbook_pb2.Snapshot.FromString(raw)
    return [(x.get_price_decimal(), x.get_quantity_decimal()) for x in snap.bids]


def decode_string(raw: bytes):
    snap = orderbook_pb2.StringSnapshot.FromString(raw)
    return [(Decimal(x.price), Decimal(x.quantity)) for x in snap.bids]


def median_ns(op, count: int) -> float:
    """Median time per snapshot, with GC on to match Go."""
    timer = timeit.Timer(op, setup="import gc; gc.enable()")
    loops, _ = timer.autorange()
    return statistics.median(timer.repeat(repeat=3, number=loops)) / loops / count * 1e9


def measure_python(snapshots):
    decimal_wire = [encode_decimal(s) for s in snapshots]
    string_wire = [encode_string(s) for s in snapshots]
    for levels, from_decimal, from_string in zip(snapshots, decimal_wire, string_wire):
        if not levels == decode_decimal(from_decimal) == decode_string(from_string):
            raise SystemExit("encodings decoded to different values")
        if encode_fast(levels) != from_decimal:
            raise SystemExit("fast_encode differs from protobuf")

    def encode_ns(encode):
        def encode_all():
            for levels in snapshots:
                encode(levels)

        return median_ns(encode_all, len(snapshots))

    def side(encode, decode, wire):
        def decode_all():
            for raw in wire:
                decode(raw)

        count = len(snapshots)
        return {
            "encode_ns": encode_ns(encode),
            "decode_ns": median_ns(decode_all, count),
            "bytes": sum(map(len, wire)) / count,
            "gzip_bytes": sum(len(gzip.compress(raw, 6, mtime=0)) for raw in wire)
            / count,
        }

    decimal = side(encode_decimal, decode_decimal, decimal_wire)
    return {
        "decimal": decimal,
        "fast": {**decimal, "encode_ns": encode_ns(encode_fast)},
        "string": side(encode_string, decode_string, string_wire),
    }


def measure_go(rows, levels_per_snapshot: int):
    with tempfile.NamedTemporaryFile("w", suffix=".csv", newline="") as f:
        csv.writer(f).writerows(rows)
        f.flush()
        result = subprocess.run(
            [
                "go",
                "run",
                "./example/compare",
                f"-input={f.name}",
                f"-levels={levels_per_snapshot}",
            ],
            cwd=GO_DIR,
            check=True,
            capture_output=True,
            text=True,
        )
    return json.loads(result.stdout)


def change(string: float, decimal: float) -> str:
    return f"{(decimal - string) / string * 100:+.0f}%"


def row(label: str, string: float, decimal: float, fast: float, unit: str) -> str:
    return (
        f"  {label:<20}{string:>10,.1f} {unit:<2}{decimal:>10,.1f} {unit:<2}"
        f"{fast:>10,.1f} {unit:<2}{change(string, fast):>8}"
    )


def report(name, snapshots, go, py):
    if go["decimal"]["bytes"] != py["decimal"]["bytes"]:
        raise SystemExit("Go and Python disagree on the Decimal payload size")
    size = {k: py[k]["bytes"] for k in ("string", "decimal", "fast")}
    print(f"\n{name}: {len(snapshots)} snapshots of {len(snapshots[0])} levels")
    print(f"  {'':<20}{'string':>13}{'decimal':>13}{'fast':>13}{'change':>8}")
    print(row("raw size", *size.values(), "B"))
    print(
        row(
            "gzip size",
            py["string"]["gzip_bytes"],
            py["decimal"]["gzip_bytes"],
            py["fast"]["gzip_bytes"],
            "B",
        )
    )

    def us(side, op):
        return side[op + "_ns"] / 1000

    for label, side, op in (
        ("Go encode", go, "encode"),
        ("Go decode", go, "decode"),
        ("Python encode", py, "encode"),
        ("Python decode", py, "decode"),
    ):
        columns = [us(side[k], op) for k in ("string", "decimal", "fast")]
        print(row(label, *columns, "us"))
    print("  encode + decode by route:")
    for label, producer, consumer in (
        ("Go -> Go", go, go),
        ("Go -> Python", go, py),
        ("Python -> Go", py, go),
        ("Python -> Python", py, py),
    ):
        columns = [
            us(producer[k], "encode") + us(consumer[k], "decode")
            for k in ("string", "decimal", "fast")
        ]
        print(row(label, *columns, "us"))
    if go["string"]["bytes"] != size["string"]:
        print(
            f"  note: Go string payload is {go['string']['bytes']:,.1f} B: "
            "shopspring's String trims trailing zeros, so it drops scale"
        )


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--sample", type=Path, help="CSV of price,quantity rows")
    parser.add_argument("--levels", type=int, default=25, help="levels per snapshot")
    args = parser.parse_args()

    if args.sample:
        profiles = [(f"sample: {args.sample.name}", read_sample(args.sample))]
    else:
        profiles = synthetic_profiles()
    for name, rows in profiles:
        snapshots = group_levels(rows, args.levels)
        rows = rows[: len(snapshots) * args.levels]
        report(
            name, snapshots, measure_go(rows, args.levels), measure_python(snapshots)
        )
    backend = "native" if fast.NATIVE else "pure Python fallback"
    print(
        f"\nfast_encode backend: {backend}. Change compares fast with string, so\n"
        "negative means smaller or faster. Over a million snapshots, 1 us per\n"
        "snapshot is 1 CPU second and 1 B is 1 MB."
    )


if __name__ == "__main__":
    main()
