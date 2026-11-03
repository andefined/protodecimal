"""Lossless protobuf decimals for ``decimal.Decimal``.

- ``protodecimal.conv`` converts between ``protodecimal.v1.Decimal`` and
  ``decimal.Decimal``.
- ``protodecimal.fast`` adds ``fast_encode`` to generated messages.
- ``protodecimal.plugin`` is the ``protoc-gen-python-decimal`` code generator.

See https://github.com/andefined/protodecimal for the wire format and usage.
"""

__version__ = "0.1.2"
