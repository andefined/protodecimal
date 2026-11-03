//! Writes protobuf wire bytes directly from Python values.
//!
//! An `Encoder` is compiled once from a plan that `protodecimal.fast` derives
//! from a message's descriptor, so new schemas need no Rust changes. Output
//! must stay byte-identical to `SerializeToString`; py/tests/test_fast_encode.py
//! checks that against protobuf itself.

use pyo3::exceptions::{PyOverflowError, PyTypeError, PyValueError};
use pyo3::prelude::*;
use pyo3::types::{PyBytes, PyList, PyString, PyTuple};

const VARINT: u32 = 0;
const FIXED64: u32 = 1;
const LENGTH_DELIMITED: u32 = 2;
const FIXED32: u32 = 5;

// Matches the recursion limit of protobuf's own parsers, and stops a Python
// list that contains itself from overflowing the stack.
const MAX_DEPTH: usize = 100;

// Plan kinds. All but DECIMAL are FieldDescriptor.TYPE_* numbers.
const DECIMAL: u32 = 0;
const DOUBLE: u32 = 1;
const FLOAT: u32 = 2;
const INT64: u32 = 3;
const UINT64: u32 = 4;
const INT32: u32 = 5;
const FIXED64_KIND: u32 = 6;
const FIXED32_KIND: u32 = 7;
const BOOL: u32 = 8;
const STRING: u32 = 9;
const MESSAGE: u32 = 11;
const BYTES: u32 = 12;
const UINT32: u32 = 13;
const ENUM: u32 = 14;
const SFIXED32: u32 = 15;
const SFIXED64: u32 = 16;
const SINT32: u32 = 17;
const SINT64: u32 = 18;

struct Field {
    position: usize,
    kind: u32,
    tag: Vec<u8>,
    packed_tag: Vec<u8>,
    repeated: bool,
    packed: bool,
    presence: bool,
    oneof: Option<u32>,
    message: usize,
}

fn wire_type(kind: u32) -> PyResult<u32> {
    match kind {
        INT32 | INT64 | UINT32 | UINT64 | SINT32 | SINT64 | BOOL | ENUM => Ok(VARINT),
        FIXED64_KIND | SFIXED64 | DOUBLE => Ok(FIXED64),
        FIXED32_KIND | SFIXED32 | FLOAT => Ok(FIXED32),
        STRING | BYTES | MESSAGE | DECIMAL => Ok(LENGTH_DELIMITED),
        _ => Err(PyValueError::new_err(format!(
            "unsupported field kind {kind}"
        ))),
    }
}

fn put_varint(out: &mut Vec<u8>, mut value: u64) {
    while value >= 0x80 {
        out.push((value as u8) | 0x80);
        value >>= 7;
    }
    out.push(value as u8);
}

fn encoded_tag(number: u32, wire: u32) -> Vec<u8> {
    let mut tag = Vec::with_capacity(5);
    put_varint(&mut tag, u64::from(number) << 3 | u64::from(wire));
    tag
}

/// Reserves a length byte and returns where the payload starts.
fn begin_length(out: &mut Vec<u8>) -> usize {
    out.push(0);
    out.len()
}

/// Writes the payload length, moving the payload if it needs more than one byte.
fn end_length(out: &mut Vec<u8>, start: usize) {
    let length = out.len() - start;
    if length < 0x80 {
        out[start - 1] = length as u8;
        return;
    }
    let mut prefix = Vec::with_capacity(5);
    put_varint(&mut prefix, length as u64);
    out.resize(out.len() + prefix.len() - 1, 0);
    out.copy_within(start..start + length, start - 1 + prefix.len());
    out[start - 1..start - 1 + prefix.len()].copy_from_slice(&prefix);
}

enum Coefficient {
    Small(u128),
    Large(Vec<u8>),
}

/// Parses digits into big-endian bytes, for coefficients beyond 128 bits.
fn parse_large(digits: &[u8], limit: usize) -> PyResult<Vec<u8>> {
    let too_large = || PyValueError::new_err(format!("coefficient exceeds {limit} bytes"));
    // Every three digits need at least a byte, so this bounds the work below.
    if digits.len() > limit * 3 {
        return Err(too_large());
    }
    let mut limbs: Vec<u32> = vec![0];
    for chunk in digits.chunks(9) {
        let (mut scale, mut carry) = (1u64, 0u64);
        for digit in chunk {
            scale *= 10;
            carry = carry * 10 + u64::from(digit - b'0');
        }
        for limb in &mut limbs {
            let product = u64::from(*limb) * scale + carry;
            *limb = product as u32;
            carry = product >> 32;
        }
        if carry != 0 {
            limbs.push(carry as u32);
        }
    }
    let mut bytes: Vec<u8> = limbs
        .iter()
        .rev()
        .flat_map(|limb| limb.to_be_bytes())
        .collect();
    let first = bytes
        .iter()
        .position(|byte| *byte != 0)
        .unwrap_or(bytes.len());
    bytes.drain(..first);
    if bytes.len() > limit {
        return Err(too_large());
    }
    Ok(bytes)
}

/// Writes one Decimal message body from the text form of a decimal.Decimal.
fn put_decimal_body(text: &str, limit: usize, out: &mut Vec<u8>) -> PyResult<()> {
    let non_finite = || PyValueError::new_err(format!("cannot encode non-finite decimal: {text}"));
    let bytes = text.as_bytes();
    let (negative, unsigned) = match bytes.first() {
        Some(b'-') => (true, &bytes[1..]),
        _ => (false, bytes),
    };
    let split = unsigned
        .iter()
        .position(|byte| *byte == b'E' || *byte == b'e');
    let (mantissa, exponent_text) = match split {
        Some(index) => (&unsigned[..index], &unsigned[index + 1..]),
        None => (unsigned, &b""[..]),
    };

    let mut small: Option<u128> = Some(0);
    let mut digits = 0usize;
    let mut fraction = 0i64;
    let mut seen_dot = false;
    for byte in mantissa {
        match byte {
            b'0'..=b'9' => {
                small = small
                    .and_then(|value| value.checked_mul(10))
                    .and_then(|value| value.checked_add(u128::from(byte - b'0')));
                digits += 1;
                fraction += i64::from(seen_dot);
            }
            b'.' if !seen_dot => seen_dot = true,
            _ => return Err(non_finite()),
        }
    }
    if digits == 0 {
        return Err(non_finite());
    }
    let written_exponent: i64 = if exponent_text.is_empty() {
        0
    } else {
        std::str::from_utf8(exponent_text)
            .ok()
            .and_then(|value| value.parse().ok())
            .ok_or_else(non_finite)?
    };
    let exponent = written_exponent - fraction;
    let exponent = i32::try_from(exponent).map_err(|_| {
        PyValueError::new_err(format!("exponent {exponent} does not fit in sint32"))
    })?;

    let coefficient = match small {
        Some(value) => Coefficient::Small(value),
        None => {
            let digits: Vec<u8> = mantissa
                .iter()
                .copied()
                .filter(u8::is_ascii_digit)
                .collect();
            Coefficient::Large(parse_large(&digits, limit)?)
        }
    };
    if negative {
        out.extend_from_slice(&[0x08, 0x01]);
    }
    match coefficient {
        Coefficient::Small(0) => {}
        Coefficient::Small(value) => {
            let length = (128 - value.leading_zeros() as usize).div_ceil(8);
            out.extend_from_slice(&[0x12, length as u8]);
            out.extend_from_slice(&value.to_be_bytes()[16 - length..]);
        }
        Coefficient::Large(value) => {
            out.push(0x12);
            put_varint(out, value.len() as u64);
            out.extend_from_slice(&value);
        }
    }
    if exponent != 0 {
        out.push(0x18);
        put_varint(out, u64::from(((exponent << 1) ^ (exponent >> 31)) as u32));
    }
    Ok(())
}

// protobuf reports out-of-range integers as ValueError, so match it.
fn range_error(value: &Bound<'_, PyAny>, err: PyErr) -> PyErr {
    if err.is_instance_of::<PyOverflowError>(value.py()) {
        PyValueError::new_err(format!("value out of range: {value}"))
    } else {
        err
    }
}

macro_rules! integer {
    ($value:expr, $kind:ty) => {
        $value
            .extract::<$kind>()
            .map_err(|err| range_error($value, err))?
    };
}

/// Writes a scalar, with its tag if `tagged`. A proto3 default isn't sent.
fn put_scalar(
    field: &Field,
    value: &Bound<'_, PyAny>,
    tagged: bool,
    out: &mut Vec<u8>,
) -> PyResult<()> {
    enum Number {
        Varint(u64),
        Fixed32(u32),
        Fixed64(u64),
    }
    let number = match field.kind {
        BOOL => match value.extract::<bool>() {
            Ok(flag) => Number::Varint(u64::from(flag)),
            Err(_) => Number::Varint(u64::from(integer!(value, i64) != 0)),
        },
        INT32 | ENUM => Number::Varint(integer!(value, i32) as i64 as u64),
        INT64 => Number::Varint(integer!(value, i64) as u64),
        UINT32 => Number::Varint(u64::from(integer!(value, u32))),
        UINT64 => Number::Varint(integer!(value, u64)),
        SINT32 => {
            let v = integer!(value, i32);
            Number::Varint(u64::from(((v << 1) ^ (v >> 31)) as u32))
        }
        SINT64 => {
            let v = integer!(value, i64);
            Number::Varint(((v << 1) ^ (v >> 63)) as u64)
        }
        FIXED32_KIND => Number::Fixed32(integer!(value, u32)),
        SFIXED32 => Number::Fixed32(integer!(value, i32) as u32),
        FLOAT => Number::Fixed32((value.extract::<f64>()? as f32).to_bits()),
        FIXED64_KIND => Number::Fixed64(integer!(value, u64)),
        SFIXED64 => Number::Fixed64(integer!(value, i64) as u64),
        _ => Number::Fixed64(value.extract::<f64>()?.to_bits()), // DOUBLE
    };
    let is_default = match number {
        Number::Varint(v) | Number::Fixed64(v) => v == 0,
        Number::Fixed32(v) => v == 0,
    };
    if tagged && is_default && !field.presence && !field.repeated {
        return Ok(());
    }
    if tagged {
        out.extend_from_slice(&field.tag);
    }
    match number {
        Number::Varint(v) => put_varint(out, v),
        Number::Fixed32(v) => out.extend_from_slice(&v.to_le_bytes()),
        Number::Fixed64(v) => out.extend_from_slice(&v.to_le_bytes()),
    }
    Ok(())
}

#[pyclass(frozen)]
struct Encoder {
    messages: Vec<Vec<Field>>,
    root: usize,
    decimal: Py<PyAny>,
    limit: usize,
}

impl Encoder {
    fn put_decimal(
        &self,
        value: &Bound<'_, PyAny>,
        field: &Field,
        out: &mut Vec<u8>,
    ) -> PyResult<()> {
        let py = value.py();
        let decimal = self.decimal.bind(py);
        let text = if value.get_type().is(decimal) {
            value.str()?
        } else if value.is_instance(decimal)? {
            // A subclass may override __str__, so ask Decimal itself.
            decimal
                .getattr("__str__")?
                .call1((value,))?
                .cast_into::<PyString>()?
        } else {
            let found = value.get_type().name()?;
            return Err(PyTypeError::new_err(format!(
                "expected decimal.Decimal, got {found}"
            )));
        };
        out.extend_from_slice(&field.tag);
        let start = begin_length(out);
        put_decimal_body(text.to_str()?, self.limit, out)?;
        end_length(out, start);
        Ok(())
    }

    fn put_message(
        &self,
        value: &Bound<'_, PyAny>,
        field: &Field,
        depth: usize,
        out: &mut Vec<u8>,
    ) -> PyResult<()> {
        let values = value
            .cast::<PyTuple>()
            .map_err(|_| PyTypeError::new_err("a nested message must be a tuple of its fields"))?;
        out.extend_from_slice(&field.tag);
        let start = begin_length(out);
        self.put_fields(field.message, values, depth + 1, out)?;
        end_length(out, start);
        Ok(())
    }

    fn put_bytes_like(
        &self,
        value: &Bound<'_, PyAny>,
        field: &Field,
        out: &mut Vec<u8>,
    ) -> PyResult<()> {
        let bytes: &[u8] = if field.kind == STRING {
            value
                .cast::<PyString>()
                .map_err(|_| PyTypeError::new_err("expected str"))?
                .to_str()?
                .as_bytes()
        } else {
            value
                .extract::<&[u8]>()
                .map_err(|_| PyTypeError::new_err("expected bytes"))?
        };
        if !bytes.is_empty() || field.presence || field.repeated {
            out.extend_from_slice(&field.tag);
            put_varint(out, bytes.len() as u64);
            out.extend_from_slice(bytes);
        }
        Ok(())
    }

    fn put_one(
        &self,
        value: &Bound<'_, PyAny>,
        field: &Field,
        depth: usize,
        out: &mut Vec<u8>,
    ) -> PyResult<()> {
        match field.kind {
            DECIMAL => self.put_decimal(value, field, out),
            MESSAGE => self.put_message(value, field, depth, out),
            STRING | BYTES => self.put_bytes_like(value, field, out),
            _ => put_scalar(field, value, true, out),
        }
    }

    fn put_items<'py>(
        &self,
        items: impl ExactSizeIterator<Item = Bound<'py, PyAny>>,
        field: &Field,
        depth: usize,
        out: &mut Vec<u8>,
    ) -> PyResult<()> {
        if !field.packed {
            for item in items {
                self.put_one(&item, field, depth, out)?;
            }
            return Ok(());
        }
        if items.len() == 0 {
            return Ok(());
        }
        out.extend_from_slice(&field.packed_tag);
        let start = begin_length(out);
        for item in items {
            put_scalar(field, &item, false, out)?;
        }
        end_length(out, start);
        Ok(())
    }

    fn put_repeated(
        &self,
        value: &Bound<'_, PyAny>,
        field: &Field,
        depth: usize,
        out: &mut Vec<u8>,
    ) -> PyResult<()> {
        if let Ok(list) = value.cast::<PyList>() {
            self.put_items(list.iter(), field, depth, out)
        } else if let Ok(tuple) = value.cast::<PyTuple>() {
            self.put_items(tuple.iter(), field, depth, out)
        } else {
            Err(PyTypeError::new_err(
                "a repeated field must be a list or tuple",
            ))
        }
    }

    fn put_fields(
        &self,
        index: usize,
        values: &Bound<'_, PyTuple>,
        depth: usize,
        out: &mut Vec<u8>,
    ) -> PyResult<()> {
        if depth > MAX_DEPTH {
            return Err(PyValueError::new_err("message nested too deeply"));
        }
        let fields = &self.messages[index];
        let count = values.len();
        if count > fields.len() {
            return Err(PyTypeError::new_err(format!(
                "expected at most {} fields, got {count}",
                fields.len()
            )));
        }
        let mut seen_oneofs = 0u64;
        for field in fields {
            if field.position >= count {
                continue;
            }
            let value = values.get_item(field.position)?;
            if value.is_none() {
                continue;
            }
            if let Some(oneof) = field.oneof {
                let bit = 1u64 << oneof;
                if seen_oneofs & bit != 0 {
                    return Err(PyValueError::new_err(
                        "more than one member of a oneof is set",
                    ));
                }
                seen_oneofs |= bit;
            }
            if field.repeated {
                self.put_repeated(&value, field, depth, out)?;
            } else {
                self.put_one(&value, field, depth, out)?;
            }
        }
        Ok(())
    }
}

#[pymethods]
impl Encoder {
    /// `plan` holds one list per message, of (position, number, kind, repeated,
    /// packed, presence, oneof, message index) tuples sorted by field number.
    #[new]
    fn new(
        plan: &Bound<'_, PyList>,
        root: usize,
        decimal: Py<PyAny>,
        limit: usize,
    ) -> PyResult<Self> {
        type Spec = (usize, u32, u32, bool, bool, bool, i64, usize);
        let mut messages = Vec::with_capacity(plan.len());
        for specs in plan.iter() {
            let mut fields = Vec::new();
            for spec in specs.cast::<PyList>()?.iter() {
                let (position, number, kind, repeated, packed, presence, oneof, message): Spec =
                    spec.extract()?;
                fields.push(Field {
                    position,
                    kind,
                    tag: encoded_tag(number, wire_type(kind)?),
                    packed_tag: encoded_tag(number, LENGTH_DELIMITED),
                    repeated,
                    packed,
                    presence,
                    oneof: u32::try_from(oneof).ok(),
                    message,
                });
            }
            messages.push(fields);
        }
        Ok(Encoder {
            messages,
            root,
            decimal,
            limit,
        })
    }

    /// Encodes a message from its field values, in declaration order. Trailing
    /// fields may be left out, and None leaves a field unset.
    #[pyo3(signature = (*fields))]
    fn encode<'py>(&self, fields: &Bound<'py, PyTuple>) -> PyResult<Bound<'py, PyBytes>> {
        let mut out = Vec::with_capacity(1024);
        self.put_fields(self.root, fields, 0, &mut out)?;
        Ok(PyBytes::new(fields.py(), &out))
    }
}

#[pymodule]
fn protodecimal_native(module: &Bound<'_, PyModule>) -> PyResult<()> {
    module.add("__version__", env!("CARGO_PKG_VERSION"))?;
    module.add_class::<Encoder>()
}
