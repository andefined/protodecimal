"""Encode a whole message straight to wire bytes.

Filling in a protobuf message field by field is the slow part of encoding from
Python, so generated ``_pb2_decimal`` modules also attach a ``fast_encode``
static method to each message that holds a decimal. It takes the field values
in declaration order and returns what ``SerializeToString`` would:

    wire = orderbook_pb2.Snapshot.fast_encode(
        "BTC-USDT",
        [(Decimal("65122.99"), Decimal("0.50"))],  # bids
    )

Nested messages are tuples of their own fields, repeated fields are lists or
tuples, and None leaves a field unset. Trailing fields can be left out.

Encoding runs in the protodecimal_native extension when it is installed. If it
isn't, the same call builds a normal message: the bytes are identical, just
much slower. Messages using maps, groups, or required fields aren't attached.
"""

from __future__ import annotations

from decimal import Decimal

from google.protobuf import message_factory
from google.protobuf.descriptor import FieldDescriptor

from protodecimal import conv

try:
    import protodecimal_native as _native
except ImportError:
    _native = None

__all__ = ["attach_file", "NATIVE"]

NATIVE = _native is not None

_DECIMAL_FULL_NAME = "protodecimal.v1.Decimal"
_DECIMAL_KIND = 0  # the Rust plan's kind; the rest are FieldDescriptor.TYPE_*
_MAX_DEPTH = 100
_MAX_ONEOFS = 64


def _is_repeated(field) -> bool:
    try:
        return field.is_repeated
    except AttributeError:  # protobuf < 6.31; label is deprecated from 6.32
        return field.label == field.LABEL_REPEATED


def _is_required(field) -> bool:
    try:
        return field.is_required
    except AttributeError:  # protobuf < 6.31; label is deprecated from 6.32
        return field.label == field.LABEL_REQUIRED


def _is_decimal(field) -> bool:
    message = field.message_type
    return message is not None and message.full_name == _DECIMAL_FULL_NAME


def _is_map(field) -> bool:
    message = field.message_type
    return message is not None and message.GetOptions().map_entry


def _reachable(root):
    """List root and the messages below it, or None if one is unsupported."""
    found = {root.full_name: 0}
    messages = [root]
    for message in messages:  # grows as nested types are found
        if len(message.oneofs) > _MAX_ONEOFS:
            return None
        for field in message.fields:
            if (
                _is_map(field)
                or _is_required(field)
                or field.type == FieldDescriptor.TYPE_GROUP
            ):
                return None
            nested = field.message_type
            if nested is None or _is_decimal(field) or nested.full_name in found:
                continue
            found[nested.full_name] = len(messages)
            messages.append(nested)
    return messages


def _plan(messages):
    index = {message.full_name: i for i, message in enumerate(messages)}
    plan = []
    for message in messages:
        specs = []
        for position, field in enumerate(message.fields):
            nested = field.message_type
            decimal = _is_decimal(field)
            oneof = field.containing_oneof
            specs.append(
                (
                    position,
                    field.number,
                    _DECIMAL_KIND if decimal else field.type,
                    _is_repeated(field),
                    field.is_packed,
                    field.has_presence,
                    -1 if oneof is None else oneof.index,
                    index[nested.full_name] if nested and not decimal else 0,
                )
            )
        plan.append(sorted(specs, key=lambda spec: spec[1]))
    return plan


def _native_encoder(message):
    """Compile a native encoder for ``message``, if the extension is present."""
    if _native is None:
        return None
    plan = _plan(_reachable(message))
    return _native.Encoder(plan, 0, Decimal, conv.MAX_COEFFICIENT_BYTES).encode


def _python_encoder(message):
    """Build the slower encoder used when the extension isn't installed."""
    cls = message_factory.GetMessageClass(message)

    def encode(*fields):
        msg = cls()
        _fill(msg, message, fields, 0)
        return msg.SerializeToString()

    return encode


def _fill(msg, message, values, depth: int) -> None:
    if depth > _MAX_DEPTH:
        raise ValueError("message nested too deeply")
    if len(values) > len(message.fields):
        raise TypeError(
            f"expected at most {len(message.fields)} fields, got {len(values)}"
        )
    oneofs = set()
    for field, value in zip(message.fields, values):
        if value is None:
            continue
        oneof = field.containing_oneof
        if oneof is not None:
            if oneof.index in oneofs:
                raise ValueError("more than one member of a oneof is set")
            oneofs.add(oneof.index)
        if _is_repeated(field):
            _fill_repeated(getattr(msg, field.name), field, value, depth)
        elif _is_decimal(field):
            conv.fill(getattr(msg, field.name), value)
        elif field.message_type is not None:
            _fill_message(getattr(msg, field.name), field, value, depth)
        else:
            setattr(msg, field.name, value)


def _fill_message(msg, field, value, depth: int) -> None:
    if not isinstance(value, tuple):
        raise TypeError("a nested message must be a tuple of its fields")
    msg.SetInParent()  # an empty tuple still marks the field as present
    _fill(msg, field.message_type, value, depth + 1)


def _fill_repeated(target, field, values, depth: int) -> None:
    if not isinstance(values, (list, tuple)):
        raise TypeError("a repeated field must be a list or tuple")
    for value in values:
        if _is_decimal(field):
            conv.fill(target.add(), value)
        elif field.message_type is not None:
            _fill_message(target.add(), field, value, depth)
        else:
            target.append(value)


def _holds_decimal(messages) -> bool:
    return any(_is_decimal(f) for message in messages for f in message.fields)


def _declared(messages):
    for message in messages:
        if not message.GetOptions().map_entry:
            yield message
            yield from _declared(message.nested_types)


def attach_file(pb2) -> None:
    """Attach ``fast_encode`` to the messages in a generated ``_pb2`` module."""
    for message in _declared(pb2.DESCRIPTOR.message_types_by_name.values()):
        reachable = _reachable(message)
        if reachable is None or not _holds_decimal(reachable):
            continue
        encode = _native_encoder(message) or _python_encoder(message)
        cls = message_factory.GetMessageClass(message)
        cls.fast_encode = staticmethod(encode)
