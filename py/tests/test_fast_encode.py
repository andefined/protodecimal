"""fast_encode must match SerializeToString byte for byte, on both backends.

Run through `make test-native` to cover the compiled encoder as well. There,
PROTODECIMAL_REQUIRE_NATIVE turns a missing extension into a failure, so the
native path can't be skipped without anyone noticing.
"""

import math
import os
import random
import unittest
from decimal import Decimal, localcontext

from google.protobuf.descriptor import FieldDescriptor as F

import example.v1.orderbook_pb2 as orderbook_pb2
import example.v1.orderbook_pb2_decimal  # noqa: F401  attaches the methods
from protodecimal import conv, fast

BACKENDS = {"python": fast._python_encoder}
if fast.NATIVE:
    BACKENDS["native"] = fast._native_encoder

RANDOM_MESSAGES = 150

MESSAGES = (
    orderbook_pb2.Level,
    orderbook_pb2.Snapshot,
    orderbook_pb2.DeltaBlock,
    orderbook_pb2.ScalarBlock,
    orderbook_pb2.OptionalBlock,
    orderbook_pb2.OutOfOrder,
    orderbook_pb2.PriceUpdate,
    orderbook_pb2.TreeNode,
)

INTEGER_RANGES = {
    F.TYPE_INT32: (-(2**31), 2**31 - 1),
    F.TYPE_SINT32: (-(2**31), 2**31 - 1),
    F.TYPE_SFIXED32: (-(2**31), 2**31 - 1),
    F.TYPE_INT64: (-(2**63), 2**63 - 1),
    F.TYPE_SINT64: (-(2**63), 2**63 - 1),
    F.TYPE_SFIXED64: (-(2**63), 2**63 - 1),
    F.TYPE_UINT32: (0, 2**32 - 1),
    F.TYPE_FIXED32: (0, 2**32 - 1),
    F.TYPE_UINT64: (0, 2**64 - 1),
    F.TYPE_FIXED64: (0, 2**64 - 1),
}
DOUBLES = (0.0, -0.0, 0.5, -2.25, 1e300, math.inf, -math.inf, math.nan)
FLOATS = (0.0, -0.0, 0.5, -2.25, math.inf, -math.inf, math.nan)
TEXTS = ("", "a", "héllo", "€" * 50, "x" * 200)
BLOBS = (b"", b"\x00", b"\xff" * 200)


def random_decimal(rng):
    sign = rng.choice(("", "-"))
    kind = rng.randrange(8)
    if kind == 0:
        return Decimal(f"{sign}{rng.randrange(10 ** rng.randint(1, 45))}")
    if kind == 1:
        digits = rng.randrange(10 ** rng.randint(1, 30))
        return Decimal(f"{sign}0.{'0' * rng.randint(0, 12)}{digits}")
    if kind == 2:
        digits = rng.randrange(10 ** rng.randint(1, 30))
        return Decimal(f"{sign}{rng.randrange(10**8)}.{digits}000")
    if kind == 3:
        return Decimal(f"{sign}{rng.randrange(10**6)}E{rng.randint(-60, 60)}")
    if kind == 4:
        return Decimal(f"{sign}0E{rng.randint(-60, 60)}")
    if kind == 5:
        return Decimal(f"{sign}0.{'0' * rng.randint(1, 30)}")
    if kind == 6:  # past the 128-bit fast path, and past one-byte lengths
        return Decimal(f"{sign}{rng.randrange(10 ** rng.randint(39, 400))}")
    digits = tuple(rng.randrange(10) for _ in range(rng.randint(1, 50)))
    return Decimal((rng.randrange(2), digits, rng.randint(-40, 40)))


def random_scalar(field, rng):
    kind = field.type
    if kind in INTEGER_RANGES:
        low, high = INTEGER_RANGES[kind]
        return rng.choice((0, 1, -1 if low else 2, low, high, rng.randint(low, high)))
    if kind == F.TYPE_BOOL:
        return rng.random() < 0.5
    if kind == F.TYPE_ENUM:
        return rng.choice((0, 1, 2, 99))
    if kind == F.TYPE_DOUBLE:
        return rng.choice(DOUBLES)
    if kind == F.TYPE_FLOAT:
        return rng.choice(FLOATS)
    if kind == F.TYPE_STRING:
        return rng.choice(TEXTS)
    return rng.choice(BLOBS)


def populate(msg, rng, depth=0):
    """Fill msg randomly through protobuf, returning the matching arguments."""
    message = msg.DESCRIPTOR
    chosen_oneofs = {o.index: rng.choice(o.fields).name for o in message.oneofs}
    args = []
    for field in message.fields:
        oneof = field.containing_oneof
        skipped = rng.random() < 0.25
        if oneof is not None and chosen_oneofs[oneof.index] != field.name:
            skipped = True
        if skipped:
            args.append(None)
        elif fast._is_repeated(field):
            args.append(populate_repeated(msg, field, rng, depth))
        elif fast._is_decimal(field):
            value = random_decimal(rng)
            conv.fill(getattr(msg, field.name), value)
            args.append(value)
        elif field.message_type is not None:
            nested = getattr(msg, field.name)
            nested.SetInParent()
            args.append(populate(nested, rng, depth + 1))
        else:
            value = random_scalar(field, rng)
            setattr(msg, field.name, value)
            args.append(value)
    while args and args[-1] is None and rng.random() < 0.5:
        args.pop()  # trailing fields may be left out
    return tuple(args)


def populate_repeated(msg, field, rng, depth):
    target = getattr(msg, field.name)
    count = 0 if depth > 3 else rng.choice((0, 1, 2, 3, 40))
    values = []
    for _ in range(count):
        if fast._is_decimal(field):
            value = random_decimal(rng)
            conv.fill(target.add(), value)
        elif field.message_type is not None:
            value = populate(target.add(), rng, depth + 1)
        else:
            value = random_scalar(field, rng)
            target.append(value)
        values.append(value)
    return values if rng.random() < 0.5 else tuple(values)


class BackendTest(unittest.TestCase):
    def each_backend(self):
        for name, build in BACKENDS.items():
            with self.subTest(backend=name):
                yield build


class ByteIdentityTest(BackendTest):
    def test_native_is_available_when_required(self):
        if os.environ.get("PROTODECIMAL_REQUIRE_NATIVE"):
            self.assertTrue(fast.NATIVE, "protodecimal_native did not load")

    def test_random_messages_match_protobuf(self):
        for cls in MESSAGES:
            for build in self.each_backend():
                encode = build(cls.DESCRIPTOR)
                rng = random.Random(cls.DESCRIPTOR.name)
                for _ in range(RANDOM_MESSAGES):
                    msg = cls()
                    args = populate(msg, rng)
                    self.assertEqual(encode(*args), msg.SerializeToString(), args)

    def test_golden_wire(self):
        golden = bytes.fromhex(
            "0a084254432d55534454121e0a151211bf61315a8f139c552848fca4a172dcbac018"
            "4712051201321803121e0a071203635eab18031213121103a0c92075c0dbf3b8acbc"
            "5f96ce3f0ad21a0e0a0708011201011847120312012a"
        )
        price = Decimal("65123.123456789012345678901234567890123456")
        bids = [
            (price, Decimal("0.50")),
            (Decimal("65122.99"), Decimal("1234567890123456789012345678901234567890")),
        ]
        asks = [(Decimal("-0.000000000000000000000000000000000001"), Decimal("42"))]
        for build in self.each_backend():
            encode = build(orderbook_pb2.Snapshot.DESCRIPTOR)
            self.assertEqual(encode("BTC-USDT", bids, asks), golden)

    def test_attached_method_matches_protobuf(self):
        wire = orderbook_pb2.Snapshot.fast_encode(
            "BTC-USDT", [(Decimal("1.50"), Decimal("-2"))]
        )
        snap = orderbook_pb2.Snapshot.FromString(wire)
        self.assertEqual(snap.symbol, "BTC-USDT")
        self.assertEqual(str(snap.bids[0].get_price_decimal()), "1.50")
        self.assertEqual(str(snap.bids[0].get_quantity_decimal()), "-2")

    def test_no_fields_is_empty(self):
        for build in self.each_backend():
            self.assertEqual(build(orderbook_pb2.Snapshot.DESCRIPTOR)(), b"")

    def test_implicit_defaults_are_not_sent(self):
        for build in self.each_backend():
            encode = build(orderbook_pb2.Snapshot.DESCRIPTOR)
            self.assertEqual(encode("", [], ()), b"")

    def test_explicit_presence_sends_defaults(self):
        msg = orderbook_pb2.OptionalBlock(count=0, name="")
        for build in self.each_backend():
            encode = build(orderbook_pb2.OptionalBlock.DESCRIPTOR)
            self.assertEqual(encode(0, ""), msg.SerializeToString())

    def test_empty_nested_message_is_present(self):
        msg = orderbook_pb2.ScalarBlock()
        msg.level.SetInParent()
        for build in self.each_backend():
            encode = build(orderbook_pb2.ScalarBlock.DESCRIPTOR)
            args = [None] * 16 + [()]
            self.assertEqual(encode(*args), msg.SerializeToString())

    def test_negative_zero_keeps_its_sign(self):
        for build in self.each_backend():
            wire = build(orderbook_pb2.Level.DESCRIPTOR)(Decimal("-0.00"))
            level = orderbook_pb2.Level.FromString(wire)
            self.assertEqual(
                level.get_price_decimal().as_tuple(), Decimal("-0.00").as_tuple()
            )

    def test_coefficient_at_the_limit(self):
        value = Decimal(int.from_bytes(b"\xff" * conv.MAX_COEFFICIENT_BYTES, "big"))
        msg = orderbook_pb2.Level()
        conv.fill(msg.price, value)
        for build in self.each_backend():
            encode = build(orderbook_pb2.Level.DESCRIPTOR)
            self.assertEqual(encode(value), msg.SerializeToString())

    def test_independent_of_context(self):
        value = Decimal("1.2345678901234567890123456789012345E+30")
        msg = orderbook_pb2.Level()
        conv.fill(msg.price, value)
        for build in self.each_backend():
            encode = build(orderbook_pb2.Level.DESCRIPTOR)
            with localcontext() as ctx:
                ctx.capitals = 0
                ctx.prec = 3
                self.assertEqual(encode(value), msg.SerializeToString())

    def test_subclass_str_is_ignored(self):
        class Display(Decimal):
            def __str__(self):
                return "1.50 USD"

        msg = orderbook_pb2.Level()
        conv.fill(msg.price, Decimal("-2.5"))
        for build in self.each_backend():
            encode = build(orderbook_pb2.Level.DESCRIPTOR)
            self.assertEqual(encode(Display("-2.5")), msg.SerializeToString())


class RejectionTest(BackendTest):
    def assert_rejected(self, cls, error, *args):
        for build in self.each_backend():
            with self.assertRaises(error):
                build(cls.DESCRIPTOR)(*args)

    def test_non_decimal_values(self):
        for bad in (1.5, "1.50", 3, b"1"):
            with self.subTest(bad=bad):
                self.assert_rejected(orderbook_pb2.Level, TypeError, bad)

    def test_decimals_the_format_cannot_hold(self):
        for bad in ("NaN", "sNaN", "Infinity", "-Infinity", "9E+3000000000"):
            with self.subTest(bad=bad):
                self.assert_rejected(orderbook_pb2.Level, ValueError, Decimal(bad))
        self.assert_rejected(orderbook_pb2.Level, ValueError, Decimal("1" * 40000))

    def test_too_many_fields(self):
        self.assert_rejected(orderbook_pb2.Level, TypeError, None, None, None)

    def test_wrong_shapes(self):
        snapshot = orderbook_pb2.Snapshot
        self.assert_rejected(snapshot, TypeError, "X", "not a list")
        self.assert_rejected(snapshot, TypeError, "X", iter(()))
        self.assert_rejected(snapshot, TypeError, "X", [Decimal(1)])
        self.assert_rejected(snapshot, TypeError, "X", [None])
        self.assert_rejected(snapshot, TypeError, 5)

    def test_wrong_scalars(self):
        block = orderbook_pb2.ScalarBlock
        for position, bad, error in (
            (0, "x", TypeError),
            (1, 2**31, ValueError),
            (1, 1.5, TypeError),
            (2, 2**63, ValueError),
            (3, -1, ValueError),
            (4, 2**64, ValueError),
            (5, 2**31, ValueError),
            (7, 2**32, ValueError),
            (11, "x", TypeError),
            (13, 5, TypeError),
            (14, "text", TypeError),
        ):
            with self.subTest(position=position, bad=bad):
                args = [None] * position + [bad]
                self.assert_rejected(block, error, *args)
        args = [None] * 19 + [[1, None]]
        self.assert_rejected(block, TypeError, *args)

    def test_two_members_of_a_oneof(self):
        update = orderbook_pb2.PriceUpdate
        self.assert_rejected(update, ValueError, Decimal(1), Decimal(2))

    def test_self_containing_list(self):
        children = []
        children.append((None, children))
        self.assert_rejected(orderbook_pb2.TreeNode, ValueError, None, children)


class AttachmentTest(unittest.TestCase):
    def test_only_messages_holding_decimals(self):
        for cls in MESSAGES:
            self.assertTrue(hasattr(cls, "fast_encode"), cls)
        self.assertFalse(hasattr(orderbook_pb2.StringSnapshot, "fast_encode"))

    def test_map_messages_are_not_supported(self):
        self.assertFalse(hasattr(orderbook_pb2.MapBlock, "fast_encode"))


if __name__ == "__main__":
    unittest.main()
