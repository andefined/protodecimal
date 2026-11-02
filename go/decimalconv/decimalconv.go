// Package decimalconv converts between the wire type
// protodecimal.v1.Decimal and github.com/shopspring/decimal.Decimal.
//
// The conversion is lossless in both directions for all finite decimals,
// including scale (trailing zeros): "1.50" round-trips as "1.50", not "1.5".
//
// The only asymmetry is negative zero: big.Int cannot represent -0, so a
// wire value with negative=true and an empty coefficient decodes to plain
// zero. (Python's decimal.Decimal can produce -0; Go normalizes it away.)
//
// Decoding itself is cheap, but rendering or rescaling a decoded value
// costs time and memory proportional to its exponent, so a few bytes of
// wire data can ask for gigabytes. Call ValidateMessage on messages from
// untrusted peers before using their decimals.
package decimalconv

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"sync"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	pb "github.com/andefined/protodecimal/go/decimalpb"
)

const (
	// MaxExponent bounds the exponent magnitude accepted by Validate. It
	// matches the decimal128 exponent range, far beyond real financial data
	// and small enough that rendering a value stays cheap.
	MaxExponent = 6144

	// MaxCoefficientBytes bounds the coefficient size accepted by Validate
	// (about 39,000 digits). Python's conv module enforces the same limit,
	// because its decoding cost grows quadratically with coefficient size.
	MaxCoefficientBytes = 16384
)

var decimalName = (&pb.Decimal{}).ProtoReflect().Descriptor().FullName()

// Errors returned by generated Decimals decoders.
var (
	// ErrDuplicateField reports a singular message field that appears twice,
	// as in concatenated messages. proto.Unmarshal merges such fields; the
	// decoders reject them instead of guessing at a partial value.
	ErrDuplicateField = errors.New("decimalconv: singular message field repeated on the wire")

	// ErrInvalidUTF8 reports a string field that is not valid UTF-8.
	ErrInvalidUTF8 = errors.New("decimalconv: string field is not valid UTF-8")

	// ErrInvalidFieldNumber reports a field number above protobuf's maximum.
	ErrInvalidFieldNumber = errors.New("decimalconv: field number out of range")
)

var magnitudes = sync.Pool{New: func() any { return new(big.Int) }}

// FromShopspring encodes d into a wire Decimal. It never fails: every
// shopspring decimal is representable.
func FromShopspring(d decimal.Decimal) *pb.Decimal {
	coef := d.Coefficient() // returns a copy; sign lives on the big.Int
	return &pb.Decimal{
		Negative:    coef.Sign() < 0,
		Coefficient: coef.Bytes(), // big-endian magnitude; empty for zero
		Exponent:    d.Exponent(),
	}
}

// ToShopspring decodes a wire Decimal. A nil message decodes to zero,
// so it is safe to call on the result of a proto getter for an unset field.
// It does not bound the result: see Validate for untrusted input.
func ToShopspring(p *pb.Decimal) decimal.Decimal {
	mag := new(big.Int).SetBytes(p.GetCoefficient()) // nil/empty -> 0
	if p.GetNegative() {
		mag.Neg(mag)
	}
	return decimal.NewFromBigInt(mag, p.GetExponent())
}

// Unmarshal decodes the wire bytes of one Decimal directly, without a
// protobuf message. Unlike ToShopspring it applies Validate's limits, so it
// is safe on untrusted input. Generated Decimals decoders are built on it.
func Unmarshal(b []byte) (decimal.Decimal, error) {
	var (
		negative    bool
		coefficient []byte
		exponent    int32
	)
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return decimal.Decimal{}, protowire.ParseError(n)
		}
		b = b[n:]
		switch {
		case num == 1 && typ == protowire.VarintType:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return decimal.Decimal{}, protowire.ParseError(n)
			}
			negative = protowire.DecodeBool(v)
			b = b[n:]
		case num == 2 && typ == protowire.BytesType:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return decimal.Decimal{}, protowire.ParseError(n)
			}
			coefficient = v
			b = b[n:]
		case num == 3 && typ == protowire.VarintType:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return decimal.Decimal{}, protowire.ParseError(n)
			}
			exponent = int32(protowire.DecodeZigZag(v & math.MaxUint32))
			b = b[n:]
		default:
			if num > protowire.MaxValidNumber {
				return decimal.Decimal{}, ErrInvalidFieldNumber
			}
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return decimal.Decimal{}, protowire.ParseError(n)
			}
			b = b[n:]
		}
	}
	if err := checkBounds(len(coefficient), exponent); err != nil {
		return decimal.Decimal{}, err
	}
	// NewFromBigInt copies its argument, so the magnitude can be reused.
	mag := magnitudes.Get().(*big.Int)
	defer magnitudes.Put(mag)
	mag.SetBytes(coefficient)
	if negative {
		mag.Neg(mag)
	}
	return decimal.NewFromBigInt(mag, exponent), nil
}

// FromString parses s with shopspring rules and encodes it.
func FromString(s string) (*pb.Decimal, error) {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return nil, err
	}
	return FromShopspring(d), nil
}

// ToString validates p, decodes it and formats it in plain notation with
// the encoded scale preserved ("1.50" stays "1.50"), which shopspring's
// String does not do: it trims trailing zeros.
func ToString(p *pb.Decimal) (string, error) {
	if err := Validate(p); err != nil {
		return "", err
	}
	d := ToShopspring(p)
	if exp := d.Exponent(); exp < 0 {
		return d.StringFixed(-exp), nil
	}
	return d.String(), nil
}

// Validate reports whether p is within MaxExponent and MaxCoefficientBytes.
// A nil message is valid: it decodes to zero.
func Validate(p *pb.Decimal) error {
	return checkBounds(len(p.GetCoefficient()), p.GetExponent())
}

// ValidateMessage applies Validate to every Decimal reachable from m,
// including repeated fields, map values, and oneof members. Decimals nested
// inside google.protobuf.Any are not inspected.
func ValidateMessage(m proto.Message) error {
	if m == nil {
		return nil
	}
	return validateMessage(m.ProtoReflect())
}

func validateMessage(m protoreflect.Message) error {
	if m.Descriptor().FullName() == decimalName {
		fields := m.Descriptor().Fields()
		coefficient := m.Get(fields.ByName("coefficient")).Bytes()
		exponent := int32(m.Get(fields.ByName("exponent")).Int())
		return checkBounds(len(coefficient), exponent)
	}
	var err error
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if err = validateField(fd, v); err != nil {
			err = fmt.Errorf("%s: %w", fd.Name(), err)
		}
		return err == nil
	})
	return err
}

func validateField(fd protoreflect.FieldDescriptor, v protoreflect.Value) error {
	switch {
	case fd.IsMap():
		if fd.MapValue().Message() == nil {
			return nil
		}
		var err error
		v.Map().Range(func(_ protoreflect.MapKey, value protoreflect.Value) bool {
			err = validateMessage(value.Message())
			return err == nil
		})
		return err
	case fd.Message() == nil:
		return nil
	case fd.IsList():
		list := v.List()
		for i := 0; i < list.Len(); i++ {
			if err := validateMessage(list.Get(i).Message()); err != nil {
				return err
			}
		}
		return nil
	default:
		return validateMessage(v.Message())
	}
}

func checkBounds(coefficientBytes int, exponent int32) error {
	if exponent < -MaxExponent || exponent > MaxExponent {
		return fmt.Errorf("decimalconv: exponent %d outside +/-%d", exponent, MaxExponent)
	}
	if coefficientBytes > MaxCoefficientBytes {
		return fmt.Errorf("decimalconv: coefficient of %d bytes exceeds %d", coefficientBytes, MaxCoefficientBytes)
	}
	return nil
}
