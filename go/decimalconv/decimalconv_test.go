package decimalconv

import (
	"math"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"

	pb "github.com/andefined/protodecimal/go/decimalpb"
)

// roundTripCases must survive parse -> encode -> decode with identical
// coefficient, exponent, and string rendering (i.e. scale is preserved).
var roundTripCases = []string{
	"0",
	"1",
	"-1",
	"1.5",
	"1.50", // trailing zero: distinct encoding from 1.5, must survive
	"-1.50",
	"0.00000001",
	"42",
	"123456.789012345678901234567890123456789", // 39 sig digits
	"-123456.789012345678901234567890123456789",
	"1234567890123456789012345678901234567890",   // 40-digit integer
	"0.0000000000000000000000000000000000000001", // tiny
	"-0.000000000000000000000000000000000001",
	"99999999999999999999999999999999999999999999999999.5",          // 51 sig digits
	"123456789012345678901234567890.123456789012345678901234567890", // 60 sig digits
}

func TestRoundTrip(t *testing.T) {
	for _, s := range roundTripCases {
		want, err := decimal.NewFromString(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		got := ToShopspring(FromShopspring(want))
		if got.Coefficient().Cmp(want.Coefficient()) != 0 || got.Exponent() != want.Exponent() {
			t.Errorf("%q: round trip changed representation: got (%s, %d), want (%s, %d)",
				s, got.Coefficient(), got.Exponent(), want.Coefficient(), want.Exponent())
		}
		if got.String() != want.String() {
			t.Errorf("%q: String() changed: got %q, want %q", s, got.String(), want.String())
		}
	}
}

func TestNilAndUnset(t *testing.T) {
	if got := ToShopspring(nil); !got.Equal(decimal.Zero) {
		t.Errorf("ToShopspring(nil) = %s, want 0", got)
	}
	if got := ToShopspring(&pb.Decimal{}); !got.Equal(decimal.Zero) {
		t.Errorf("ToShopspring(empty) = %s, want 0", got)
	}
}

func TestZeroEncodesEmptyCoefficient(t *testing.T) {
	// Note: decimal.Zero is New(0, 1) internally, so parse "0" instead to
	// get the (0, 0) representation.
	p := FromShopspring(mustParse(t, "0"))
	if len(p.Coefficient) != 0 || p.Negative || p.Exponent != 0 {
		t.Errorf("zero encoded as %+v, want all-default message", p)
	}
}

func TestFromStringToString(t *testing.T) {
	for _, s := range append([]string{"0.00", "-0.10", "100"}, roundTripCases...) {
		p, err := FromString(s)
		if err != nil {
			t.Fatalf("FromString(%q): %v", s, err)
		}
		// Compared with the input, not with shopspring's String(), which
		// trims the very trailing zeros this must preserve.
		got, err := ToString(p)
		if err != nil {
			t.Fatalf("ToString(%q): %v", s, err)
		}
		if got != s {
			t.Errorf("ToString(FromString(%q)) = %q", s, got)
		}
	}
	if _, err := FromString("not a number"); err == nil {
		t.Error("FromString on garbage: want error, got nil")
	}
}

func TestToStringPositiveExponent(t *testing.T) {
	got, err := ToString(&pb.Decimal{Coefficient: []byte{12}, Exponent: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got != "12000" {
		t.Errorf("got %q, want 12000", got)
	}
}

func TestValidateBounds(t *testing.T) {
	valid := []*pb.Decimal{
		nil,
		{},
		{Exponent: MaxExponent},
		{Exponent: -MaxExponent},
		{Coefficient: make([]byte, MaxCoefficientBytes)},
	}
	for _, p := range valid {
		if err := Validate(p); err != nil {
			t.Errorf("Validate(exp=%d, %d bytes): %v", p.GetExponent(), len(p.GetCoefficient()), err)
		}
	}
	invalid := []*pb.Decimal{
		{Exponent: MaxExponent + 1},
		{Exponent: -MaxExponent - 1},
		{Exponent: math.MaxInt32},
		{Exponent: math.MinInt32}, // has no positive counterpart
		{Coefficient: make([]byte, MaxCoefficientBytes+1)},
	}
	for _, p := range invalid {
		if err := Validate(p); err == nil {
			t.Errorf("Validate(exp=%d, %d bytes): want error", p.GetExponent(), len(p.GetCoefficient()))
		}
		if s, err := ToString(p); err == nil {
			t.Errorf("ToString(exp=%d): want error, got %d bytes", p.GetExponent(), len(s))
		}
	}
}

func TestToStringRejectsExponentBombQuickly(t *testing.T) {
	start := time.Now()
	if _, err := ToString(&pb.Decimal{Coefficient: []byte{1}, Exponent: math.MaxInt32}); err == nil {
		t.Fatal("want error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("rejection took %s", elapsed)
	}
}

func TestUnmarshalMatchesToShopspring(t *testing.T) {
	for _, s := range append([]string{"0.00", "-0.10", "1E+30", "-1E-30"}, roundTripCases...) {
		p, err := FromString(s)
		if err != nil {
			t.Fatalf("FromString(%q): %v", s, err)
		}
		raw, err := proto.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Unmarshal(raw)
		if err != nil {
			t.Fatalf("Unmarshal(%q): %v", s, err)
		}
		want := ToShopspring(p)
		if got.Coefficient().Cmp(want.Coefficient()) != 0 || got.Exponent() != want.Exponent() {
			t.Errorf("%q: got (%s, %d), want (%s, %d)", s, got.Coefficient(), got.Exponent(), want.Coefficient(), want.Exponent())
		}
	}
}

func TestUnmarshalLastScalarWins(t *testing.T) {
	first, err := proto.Marshal(&pb.Decimal{Coefficient: []byte{1}, Exponent: -1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := proto.Marshal(&pb.Decimal{Coefficient: []byte{2}, Exponent: -2})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Unmarshal(append(first, second...))
	if err != nil {
		t.Fatal(err)
	}
	if got.Coefficient().Int64() != 2 || got.Exponent() != -2 {
		t.Errorf("got (%s, %d)", got.Coefficient(), got.Exponent())
	}
}

func TestUnmarshalRejections(t *testing.T) {
	for name, p := range map[string]*pb.Decimal{
		"exponent":    {Coefficient: []byte{1}, Exponent: MaxExponent + 1},
		"coefficient": {Coefficient: make([]byte, MaxCoefficientBytes+1)},
	} {
		raw, err := proto.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Unmarshal(raw); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := Unmarshal([]byte{0x12, 5, 1}); err == nil {
		t.Error("truncated coefficient: want error")
	}
}

func TestUnmarshalAllocations(t *testing.T) {
	raw, err := proto.Marshal(FromShopspring(mustParse(t, "65123.1234567890123456789012")))
	if err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(100, func() {
		if _, err := Unmarshal(raw); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 2 {
		t.Errorf("got %.0f allocations, want at most 2", allocs)
	}
}

func FuzzUnmarshal(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x08, 0x01, 0x12, 0x02, 0x01, 0x02, 0x18, 0x03})
	f.Fuzz(func(t *testing.T, b []byte) {
		var p pb.Decimal
		protoErr := proto.Unmarshal(b, &p)
		got, err := Unmarshal(b)
		switch {
		case err == nil && protoErr != nil:
			t.Fatalf("accepted bytes proto.Unmarshal rejects: %x", b)
		case err != nil && protoErr == nil && Validate(&p) == nil:
			t.Fatalf("rejected valid bytes (%v): %x", err, b)
		case err == nil:
			want := ToShopspring(&p)
			if got.Coefficient().Cmp(want.Coefficient()) != 0 || got.Exponent() != want.Exponent() {
				t.Fatalf("got (%s, %d), want (%s, %d)", got.Coefficient(), got.Exponent(), want.Coefficient(), want.Exponent())
			}
		}
	})
}

func TestValidateMessageOnDecimal(t *testing.T) {
	if err := ValidateMessage(&pb.Decimal{Exponent: MaxExponent + 1}); err == nil {
		t.Error("want error")
	}
	if err := ValidateMessage(nil); err != nil {
		t.Errorf("ValidateMessage(nil): %v", err)
	}
	if err := ValidateMessage((*pb.Decimal)(nil)); err != nil {
		t.Errorf("ValidateMessage(typed nil): %v", err)
	}
}

func mustParse(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return d
}
