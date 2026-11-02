package example

import (
	"encoding/hex"
	"testing"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"

	"github.com/andefined/protodecimal/go/decimalconv"
	"github.com/andefined/protodecimal/go/decimalpb"
	"github.com/andefined/protodecimal/go/example/orderbookpb"
)

func d(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	v, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

func TestGeneratedSingularAccessors(t *testing.T) {
	lvl := &orderbookpb.Level{}
	price := d(t, "123456.789012345678901234567890123456789")
	qty := d(t, "0.50")

	lvl.SetPriceDecimal(price)
	lvl.SetQuantityDecimal(qty)

	if got := lvl.GetPriceDecimal(); got.String() != price.String() {
		t.Errorf("price: got %s, want %s", got, price)
	}
	// Scale (trailing zeros) survives on the wire: exponent -2 with
	// coefficient 50. shopspring's String() trims it for display ("0.5"),
	// but the Python side will render the same bytes as "0.50".
	if got := lvl.GetQuantityDecimal(); got.Exponent() != -2 || got.Coefficient().Int64() != 50 {
		t.Errorf("quantity scale lost: got (%s, %d), want (50, -2)", got.Coefficient(), got.Exponent())
	}
	// Unset field decodes to zero via nil-safe getter chain.
	if got := (&orderbookpb.Level{}).GetPriceDecimal(); !got.IsZero() {
		t.Errorf("unset price: got %s, want 0", got)
	}
}

func TestGeneratedOneofAccessors(t *testing.T) {
	u := &orderbookpb.PriceUpdate{}
	u.SetDeltaDecimal(d(t, "-0.0001"))
	if _, ok := u.Kind.(*orderbookpb.PriceUpdate_Delta); !ok {
		t.Fatalf("oneof case not set to delta: %T", u.Kind)
	}
	if got := u.GetDeltaDecimal(); got.String() != "-0.0001" {
		t.Errorf("delta: got %s", got)
	}
	u.SetAbsoluteDecimal(d(t, "99.5"))
	if _, ok := u.Kind.(*orderbookpb.PriceUpdate_Absolute); !ok {
		t.Fatalf("oneof case did not switch to absolute: %T", u.Kind)
	}
	// The no-longer-set member reads as zero.
	if got := u.GetDeltaDecimal(); !got.IsZero() {
		t.Errorf("stale delta: got %s, want 0", got)
	}
}

func TestGeneratedRepeatedAccessors(t *testing.T) {
	b := &orderbookpb.DeltaBlock{}
	in := []decimal.Decimal{d(t, "1.50"), d(t, "-2"), d(t, "0.001")}
	b.SetOffsetsDecimal(in)
	out := b.GetOffsetsDecimal()
	if len(out) != len(in) {
		t.Fatalf("len: got %d, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i].String() != in[i].String() {
			t.Errorf("offsets[%d]: got %s, want %s", i, out[i], in[i])
		}
	}
	b.SetOffsetsDecimal(nil)
	if b.GetOffsetsDecimal() != nil {
		t.Error("nil did not clear the field")
	}
}

func TestSerializedRoundTrip(t *testing.T) {
	snap := &orderbookpb.Snapshot{Symbol: "BTC-USDT"}
	for i := 0; i < 25; i++ {
		lvl := &orderbookpb.Level{}
		lvl.SetPriceDecimal(d(t, "65123.123456789012345678901234567890123456"))
		lvl.SetQuantityDecimal(d(t, "0.000000000000000000000000000000000042"))
		snap.Bids = append(snap.Bids, lvl)
	}
	raw, err := proto.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var back orderbookpb.Snapshot
	if err := proto.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if got := back.Bids[24].GetPriceDecimal().String(); got != "65123.123456789012345678901234567890123456" {
		t.Errorf("price after wire round trip: %s", got)
	}
}

// TestWireSizeVsStrings demonstrates the size win for the intended data
// shape: 25 levels, ~36 significant digits per value.
func TestWireSizeVsStrings(t *testing.T) {
	price := "65123.123456789012345678901234567890123456"
	qty := "0.000000000000000000000000000000000042"

	snap := &orderbookpb.Snapshot{Symbol: "BTC-USDT"}
	for i := 0; i < 25; i++ {
		lvl := &orderbookpb.Level{}
		lvl.SetPriceDecimal(d(t, price))
		lvl.SetQuantityDecimal(d(t, qty))
		snap.Bids = append(snap.Bids, lvl)
	}
	decimalSize := proto.Size(snap)

	// Equivalent string-based encoding: per string field, 1 tag byte +
	// 1 length byte + payload; per Level submessage, 2 framing bytes;
	// same outer framing as the decimal version.
	perLevel := 2 + (2 + len(price)) + (2 + len(qty))
	stringSize := len("BTC-USDT") + 2 + 25*perLevel

	if decimalSize >= stringSize {
		t.Errorf("decimal encoding (%dB) not smaller than string encoding (%dB)", decimalSize, stringSize)
	}
	t.Logf("25 levels: decimal=%dB strings=%dB reduction=%.1f%%",
		decimalSize, stringSize, 100*(1-float64(decimalSize)/float64(stringSize)))
}

func TestValidateMessageReachesEveryDecimal(t *testing.T) {
	bad := &decimalpb.Decimal{Exponent: decimalconv.MaxExponent + 1}
	ok := &decimalpb.Decimal{Exponent: -2}
	cases := []struct {
		name string
		good proto.Message
		bomb proto.Message
	}{
		{
			"nested repeated",
			&orderbookpb.Snapshot{Asks: []*orderbookpb.Level{{Price: ok}}},
			&orderbookpb.Snapshot{Asks: []*orderbookpb.Level{{Price: ok}, {Quantity: bad}}},
		},
		{
			"oneof",
			&orderbookpb.PriceUpdate{Kind: &orderbookpb.PriceUpdate_Delta{Delta: ok}},
			&orderbookpb.PriceUpdate{Kind: &orderbookpb.PriceUpdate_Delta{Delta: bad}},
		},
		{
			"repeated decimal",
			&orderbookpb.DeltaBlock{Offsets: []*decimalpb.Decimal{ok}},
			&orderbookpb.DeltaBlock{Offsets: []*decimalpb.Decimal{ok, bad}},
		},
		{
			"map value",
			&orderbookpb.MapBlock{BySymbol: map[string]*decimalpb.Decimal{"BTC": ok}},
			&orderbookpb.MapBlock{BySymbol: map[string]*decimalpb.Decimal{"BTC": bad}},
		},
	}
	for _, c := range cases {
		if err := decimalconv.ValidateMessage(c.good); err != nil {
			t.Errorf("%s: valid message rejected: %v", c.name, err)
		}
		if err := decimalconv.ValidateMessage(c.bomb); err == nil {
			t.Errorf("%s: out-of-range decimal not found", c.name)
		}
	}
}

// goldenHex was produced by the Python implementation
// (py/tests/test_protodecimal.py, GoldenWireTest). Both languages must
// decode it to the same values and re-encode it to the same bytes; this
// pins cross-language wire compatibility.
const goldenHex = "0a084254432d55534454121e0a151211bf61315a8f139c552848fca4a172dcbac0184712051201321803121e0a071203635eab18031213121103a0c92075c0dbf3b8acbc5f96ce3f0ad21a0e0a0708011201011847120312012a"

func TestGoldenWireCrossLanguage(t *testing.T) {
	raw, err := hex.DecodeString(goldenHex)
	if err != nil {
		t.Fatal(err)
	}
	var snap orderbookpb.Snapshot
	if err := proto.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}

	if snap.Symbol != "BTC-USDT" || len(snap.Bids) != 2 || len(snap.Asks) != 1 {
		t.Fatalf("unexpected shape: %v", &snap)
	}
	checks := []struct {
		got, want string
	}{
		{snap.Bids[0].GetPriceDecimal().String(), "65123.123456789012345678901234567890123456"},
		{snap.Bids[0].GetQuantityDecimal().String(), "0.5"}, // shopspring String trims scale; bytes keep it
		{snap.Bids[1].GetPriceDecimal().String(), "65122.99"},
		{snap.Bids[1].GetQuantityDecimal().String(), "1234567890123456789012345678901234567890"},
		{snap.Asks[0].GetPriceDecimal().String(), "-0.000000000000000000000000000000000001"},
		{snap.Asks[0].GetQuantityDecimal().String(), "42"},
	}
	for i, c := range checks {
		if c.got != c.want {
			t.Errorf("value %d: got %s, want %s", i, c.got, c.want)
		}
	}
	// Scale must survive even though String() hides it.
	if q := snap.Bids[0].GetQuantityDecimal(); q.Exponent() != -2 {
		t.Errorf("bids[0].quantity exponent: got %d, want -2", q.Exponent())
	}

	// Re-encoding from the decoded decimals must reproduce Python's bytes.
	rebuilt := &orderbookpb.Snapshot{Symbol: snap.Symbol}
	for _, l := range snap.Bids {
		nl := &orderbookpb.Level{}
		nl.SetPriceDecimal(l.GetPriceDecimal())
		nl.SetQuantityDecimal(l.GetQuantityDecimal())
		rebuilt.Bids = append(rebuilt.Bids, nl)
	}
	for _, l := range snap.Asks {
		nl := &orderbookpb.Level{}
		nl.SetPriceDecimal(l.GetPriceDecimal())
		nl.SetQuantityDecimal(l.GetQuantityDecimal())
		rebuilt.Asks = append(rebuilt.Asks, nl)
	}
	out, err := proto.Marshal(rebuilt)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(out) != goldenHex {
		t.Errorf("re-encoded bytes differ from golden:\n got %s\nwant %s", hex.EncodeToString(out), goldenHex)
	}
}
