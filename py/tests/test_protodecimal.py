"""Run from the py/ directory: python3 -m unittest discover tests -v"""

import sys
import unittest
from decimal import Decimal, localcontext

import example.v1.orderbook_pb2 as orderbook_pb2
import example.v1.orderbook_pb2_decimal  # noqa: F401  attaches the methods
from protodecimal import conv
from protodecimal.v1 import decimal_pb2

ROUND_TRIP = [
    "0",
    "0.00",  # zero with scale
    "-0.00",  # negative zero: preserved python<->python
    "1",
    "-1",
    "1.5",
    "1.50",  # trailing zero must survive
    "-1.50",
    "0.00000001",
    "123456.789012345678901234567890123456789",
    "-123456.789012345678901234567890123456789",
    "1234567890123456789012345678901234567890",
    "0.0000000000000000000000000000000000000001",
    "99999999999999999999999999999999999999999999999999.5",
    "123456789012345678901234567890.123456789012345678901234567890",
    "1E+30",  # positive exponent
    "-4.2E-45",
]


class ConvTest(unittest.TestCase):
    def test_round_trip_preserves_tuple(self):
        for s in ROUND_TRIP:
            with self.subTest(s=s):
                want = Decimal(s)
                got = conv.to_decimal(conv.from_decimal(want))
                # as_tuple equality implies identical str(), scale included.
                self.assertEqual(got.as_tuple(), want.as_tuple())
                self.assertEqual(str(got), str(want))

    def test_wire_round_trip(self):
        for s in ROUND_TRIP:
            with self.subTest(s=s):
                raw = conv.from_decimal(Decimal(s)).SerializeToString()
                back = decimal_pb2.Decimal.FromString(raw)
                self.assertEqual(str(conv.to_decimal(back)), str(Decimal(s)))

    def test_unset_field_reads_as_zero(self):
        lvl = orderbook_pb2.Level()
        self.assertEqual(conv.to_decimal(lvl.price), Decimal(0))

    def test_zero_encodes_minimally(self):
        msg = conv.from_decimal(Decimal("0"))
        self.assertEqual(msg.coefficient, b"")
        self.assertFalse(msg.negative)
        self.assertEqual(msg.exponent, 0)

    def test_non_finite_rejected(self):
        for bad in ("NaN", "sNaN", "Infinity", "-Infinity"):
            with self.subTest(bad=bad):
                with self.assertRaises(ValueError):
                    conv.from_decimal(Decimal(bad))

    def test_non_decimal_rejected(self):
        for bad in (1.5, "1.50", 3, None):
            with self.subTest(bad=bad):
                with self.assertRaises(TypeError):
                    conv.from_decimal(bad)

    def test_encoding_matches_as_tuple(self):
        for s in ROUND_TRIP + ["1.5E+3", "1E-7", "-1.5E-9", "-0E+3", "0E-9"]:
            with self.subTest(s=s):
                value = Decimal(s)
                sign, digits, exponent = value.as_tuple()
                msg = conv.from_decimal(value)
                self.assertEqual(msg.negative, bool(sign))
                self.assertEqual(
                    int.from_bytes(msg.coefficient, "big"),
                    int("".join(map(str, digits))),
                )
                self.assertEqual(msg.exponent, exponent)

    def test_encoding_ignores_context_capitals(self):
        want = conv.from_decimal(Decimal("1.5E+30")).SerializeToString()
        with localcontext() as ctx:
            ctx.capitals = 0
            got = conv.from_decimal(Decimal("1.5E+30")).SerializeToString()
        self.assertEqual(got, want)

    def test_encoding_ignores_subclass_str(self):
        class Display(Decimal):
            def __str__(self):
                return "1.50 USD"

        msg = conv.from_decimal(Display("-2.5"))
        self.assertEqual(str(conv.to_decimal(msg)), "-2.5")

    def test_zero_marks_field_present(self):
        lvl = orderbook_pb2.Level()
        lvl.set_price_decimal(Decimal("0"))
        self.assertTrue(lvl.HasField("price"))

    def test_rejected_fill_leaves_message_unchanged(self):
        oversized = Decimal("1" * 40000)
        for bad in (
            Decimal("9E+3000000000"),
            Decimal("9E-3000000000"),
            Decimal("NaN"),
            oversized,
        ):
            with self.subTest(bad=str(bad)[:20]):
                lvl = orderbook_pb2.Level()
                lvl.set_price_decimal(Decimal("-1.50"))
                before = lvl.SerializeToString()
                with self.assertRaises(ValueError):
                    lvl.set_price_decimal(bad)
                self.assertEqual(lvl.SerializeToString(), before)
                self.assertEqual(str(lvl.get_price_decimal()), "-1.50")

    def test_rejected_fill_does_not_mark_presence(self):
        lvl = orderbook_pb2.Level()
        with self.assertRaises(ValueError):
            lvl.set_price_decimal(Decimal("9E+3000000000"))
        self.assertFalse(lvl.HasField("price"))

    def test_sint32_exponent_limits_round_trip(self):
        for exponent in (-(2**31), 2**31 - 1):
            with self.subTest(exponent=exponent):
                value = Decimal((0, (1, 5), exponent))
                self.assertEqual(
                    conv.to_decimal(conv.from_decimal(value)).as_tuple(),
                    value.as_tuple(),
                )

    def test_coefficient_past_python_int_string_limit(self):
        limit = sys.get_int_max_str_digits()
        self.assertGreater(limit, 0)
        for digits in (limit + 1, 5000, 39000):
            with self.subTest(digits=digits):
                for sign in ("", "-"):
                    value = Decimal(sign + "1" * digits + "E-7")
                    got = conv.to_decimal(conv.from_decimal(value))
                    self.assertEqual(got.as_tuple(), value.as_tuple())

    def test_coefficient_size_limit(self):
        at_limit = decimal_pb2.Decimal(coefficient=b"\xff" * conv.MAX_COEFFICIENT_BYTES)
        self.assertGreater(len(conv.to_decimal(at_limit).as_tuple().digits), 39000)
        with self.assertRaises(ValueError):
            conv.to_decimal(
                decimal_pb2.Decimal(
                    coefficient=b"\xff" * (conv.MAX_COEFFICIENT_BYTES + 1)
                )
            )


class GeneratedAccessorsTest(unittest.TestCase):
    def test_singular(self):
        lvl = orderbook_pb2.Level()
        lvl.set_price_decimal(Decimal("123456.789012345678901234567890123456789"))
        lvl.set_quantity_decimal(Decimal("0.50"))
        self.assertEqual(
            lvl.get_price_decimal(),
            Decimal("123456.789012345678901234567890123456789"),
        )
        self.assertEqual(str(lvl.get_quantity_decimal()), "0.50")  # scale kept
        self.assertTrue(lvl.HasField("price"))

    def test_singular_survives_serialization(self):
        lvl = orderbook_pb2.Level()
        value = Decimal("-0.000000000000000000000000000000000001")
        lvl.set_price_decimal(value)
        back = orderbook_pb2.Level.FromString(lvl.SerializeToString())
        self.assertEqual(back.get_price_decimal().as_tuple(), value.as_tuple())

    def test_oneof(self):
        u = orderbook_pb2.PriceUpdate()
        u.set_delta_decimal(Decimal("-0.0001"))
        self.assertEqual(u.WhichOneof("kind"), "delta")
        self.assertEqual(u.get_delta_decimal(), Decimal("-0.0001"))
        u.set_absolute_decimal(Decimal("99.5"))
        self.assertEqual(u.WhichOneof("kind"), "absolute")
        self.assertEqual(u.get_delta_decimal(), Decimal(0))  # cleared member

    def test_rejected_oneof_set_keeps_current_case(self):
        u = orderbook_pb2.PriceUpdate()
        u.set_delta_decimal(Decimal("-0.0001"))
        with self.assertRaises(ValueError):
            u.set_absolute_decimal(Decimal("NaN"))
        self.assertEqual(u.WhichOneof("kind"), "delta")
        self.assertEqual(u.get_delta_decimal(), Decimal("-0.0001"))

    def test_repeated_set_accepts_a_generator(self):
        b = orderbook_pb2.DeltaBlock()
        b.set_offsets_decimal(Decimal(n) for n in ("1", "2"))
        self.assertEqual(b.get_offsets_decimal(), [Decimal(1), Decimal(2)])

    def test_repeated_set_from_own_values(self):
        b = orderbook_pb2.DeltaBlock()
        b.set_offsets_decimal([Decimal("1.50"), Decimal("2")])
        b.set_offsets_decimal(b.get_offsets_decimal())
        self.assertEqual([str(d) for d in b.get_offsets_decimal()], ["1.50", "2"])

    def test_rejected_repeated_set_leaves_field_intact(self):
        b = orderbook_pb2.DeltaBlock()
        b.set_offsets_decimal([Decimal("7")])
        before = b.SerializeToString()
        with self.assertRaises(ValueError):
            b.set_offsets_decimal([Decimal("1"), Decimal("NaN")])
        self.assertEqual(b.get_offsets_decimal(), [Decimal("7")])
        self.assertEqual(b.SerializeToString(), before)

    def test_repeated(self):
        b = orderbook_pb2.DeltaBlock()
        values = [Decimal("1.50"), Decimal("-2"), Decimal("0.001")]
        b.set_offsets_decimal(values)
        self.assertEqual(b.get_offsets_decimal(), values)
        b.set_offsets_decimal([])
        self.assertEqual(b.get_offsets_decimal(), [])

    def test_nested_in_snapshot(self):
        snap = orderbook_pb2.Snapshot(symbol="BTC-USDT")
        lvl = snap.bids.add()
        lvl.set_price_decimal(Decimal("65123.45"))
        back = orderbook_pb2.Snapshot.FromString(snap.SerializeToString())
        self.assertEqual(back.bids[0].get_price_decimal(), Decimal("65123.45"))


# Produced by this file originally; also embedded in go/example/example_test.go.
# Both languages must decode it to the same values and re-encode it to the
# same bytes, pinning cross-language wire compatibility.
GOLDEN_HEX = (
    "0a084254432d55534454121e0a151211bf61315a8f139c552848fca4a172dcbac018"
    "4712051201321803121e0a071203635eab18031213121103a0c92075c0dbf3b8acbc"
    "5f96ce3f0ad21a0e0a0708011201011847120312012a"
)


class GoldenWireTest(unittest.TestCase):
    def test_decode_and_reencode(self):
        snap = orderbook_pb2.Snapshot.FromString(bytes.fromhex(GOLDEN_HEX))
        self.assertEqual(snap.symbol, "BTC-USDT")
        self.assertEqual(
            str(snap.bids[0].get_price_decimal()),
            "65123.123456789012345678901234567890123456",
        )
        self.assertEqual(str(snap.bids[0].get_quantity_decimal()), "0.50")
        self.assertEqual(snap.bids[0].get_quantity_decimal().as_tuple().exponent, -2)
        self.assertEqual(str(snap.bids[1].get_price_decimal()), "65122.99")
        self.assertEqual(
            str(snap.bids[1].get_quantity_decimal()),
            "1234567890123456789012345678901234567890",
        )
        self.assertEqual(str(snap.asks[0].get_quantity_decimal()), "42")
        self.assertEqual(
            snap.asks[0].get_price_decimal(),
            Decimal("-0.000000000000000000000000000000000001"),
        )

        rebuilt = orderbook_pb2.Snapshot(symbol=snap.symbol)
        for src, dst in ((snap.bids, rebuilt.bids), (snap.asks, rebuilt.asks)):
            for lvl in src:
                out = dst.add()
                out.set_price_decimal(lvl.get_price_decimal())
                out.set_quantity_decimal(lvl.get_quantity_decimal())
        self.assertEqual(rebuilt.SerializeToString().hex(), GOLDEN_HEX)


if __name__ == "__main__":
    unittest.main()
