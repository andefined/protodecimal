"""Conversions between protodecimal.v1.Decimal and decimal.Decimal.

Lossless within the limits below, including scale: Decimal("1.50")
round-trips as coefficient=150 / exponent=-2 and comes back as "1.50".
Negative zero survives a Python<->Python round trip; the Go side
normalizes it to plain zero (big.Int has no -0).

NaN and infinities are rejected: they have no representation in the wire
type and should never appear in transferred financial data. So are
exponents outside sint32 and coefficients over MAX_COEFFICIENT_BYTES.
"""

from __future__ import annotations

from decimal import Decimal

from protodecimal.v1 import decimal_pb2

__all__ = ["to_decimal", "from_decimal", "fill", "MAX_COEFFICIENT_BYTES"]

# About 39,000 digits; matches Go's decimalconv.MaxCoefficientBytes. Decoding
# cost grows quadratically with size (1 MB takes over a minute), so this is
# what keeps a large message from stalling the decoder.
MAX_COEFFICIENT_BYTES = 16384

_MIN_EXPONENT = -(2**31)
_MAX_EXPONENT = 2**31 - 1

# Not str(): that would accept floats and strings, and a subclass could
# override what it returns.
_decimal_str = Decimal.__str__


def to_decimal(msg: decimal_pb2.Decimal) -> Decimal:
    """Decode a wire Decimal (or the default view of an unset field)."""
    raw = msg.coefficient
    if len(raw) > MAX_COEFFICIENT_BYTES:
        raise ValueError(
            f"coefficient of {len(raw)} bytes exceeds {MAX_COEFFICIENT_BYTES}"
        )
    coefficient = int.from_bytes(raw, "big")  # b"" -> 0
    try:
        return Decimal(f"{'-' if msg.negative else ''}{coefficient}E{msg.exponent}")
    except ValueError:  # past sys.get_int_max_str_digits(); Decimal(int) is exempt
        digits = Decimal(coefficient).as_tuple().digits
        return Decimal((1 if msg.negative else 0, digits, msg.exponent))


def from_decimal(value: Decimal) -> decimal_pb2.Decimal:
    """Encode a finite decimal into a new wire Decimal message."""
    msg = decimal_pb2.Decimal()
    fill(msg, value)
    return msg


def fill(msg: decimal_pb2.Decimal, value: Decimal) -> None:
    """Encode ``value`` into an existing message (e.g. a submessage view).

    Writing through a parent's field view (``fill(level.price, d)``) also
    marks the submessage as present. A rejected value leaves ``msg`` as it
    was, so every check runs before the first field is written.
    """
    # Reading plain notation is about 5x cheaper than as_tuple() and a digit
    # loop. Anything else fails int() and takes the slower path below.
    whole, _, fraction = _decimal_str(value).partition(".")
    try:
        coefficient = abs(int(whole + fraction))
        negative = whole[0] == "-"
        exponent = -len(fraction)
    except ValueError:
        negative, coefficient, exponent = _parts(value)
    raw = coefficient.to_bytes((coefficient.bit_length() + 7) // 8, "big")
    if len(raw) > MAX_COEFFICIENT_BYTES:
        raise ValueError(
            f"coefficient of {len(raw)} bytes exceeds {MAX_COEFFICIENT_BYTES}"
        )
    msg.negative = negative
    msg.coefficient = raw
    msg.exponent = exponent


def _parts(value: Decimal) -> tuple[bool, int, int]:
    """Handle scientific notation, non-finite values, and huge coefficients."""
    sign, digits, exponent = value.as_tuple()
    if not isinstance(exponent, int):  # 'n'/'N'/'F' for NaN/sNaN/Infinity
        raise ValueError(f"cannot encode non-finite decimal: {value!r}")
    if not _MIN_EXPONENT <= exponent <= _MAX_EXPONENT:
        raise ValueError(f"exponent {exponent} does not fit in sint32")
    coefficient = 0
    for d in digits:
        coefficient = coefficient * 10 + d
    return bool(sign), coefficient, exponent
