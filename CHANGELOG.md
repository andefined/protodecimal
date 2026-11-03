# Changelog

Newest first. Versions follow [Semantic Versioning](https://semver.org).

## Unreleased

## 0.1.0 - 2026-11-03

First release.

- `protodecimal.v1.Decimal` wire format.
- Go: `decimalconv`, and `protoc-gen-go-decimal` with accessors and `Decimals` decoders. Needs Go 1.27 or newer.
- Python: `protodecimal.conv`, and `protoc-gen-python-decimal` with accessors and `fast_encode`. Needs Python 3.10 or newer and `protobuf` 6.30 or newer.
- `protodecimal-native`, the optional compiled encoder for `fast_encode`, installed with `pip install "protodecimal[native]"`.
