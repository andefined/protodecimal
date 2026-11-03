package example

import (
	"encoding/hex"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/andefined/protodecimal/go/decimalconv"
	"github.com/andefined/protodecimal/go/decimalpb"
	"github.com/andefined/protodecimal/go/example/orderbookpb"
)

// generatedDecoder is implemented by every generated Decimals struct.
type generatedDecoder interface {
	Unmarshal(b []byte) error
}

var decimalType = reflect.TypeOf(decimal.Decimal{})

// equalDecoded compares a generated struct against the message proto.Unmarshal
// produced. The two share Go field names, so one walk covers every message.
func equalDecoded(t *testing.T, path string, got, want reflect.Value) {
	t.Helper()
	switch {
	case got.Type() == decimalType:
		var p *decimalpb.Decimal
		if !want.IsNil() {
			p = want.Interface().(*decimalpb.Decimal)
		}
		g, w := got.Interface().(decimal.Decimal), decimalconv.ToShopspring(p)
		if p == nil {
			if !g.IsZero() {
				t.Errorf("%s: got %s, want unset", path, g)
			}
			return
		}
		if g.Coefficient().Cmp(w.Coefficient()) != 0 || g.Exponent() != w.Exponent() {
			t.Errorf("%s: got (%s, %d), want (%s, %d)", path, g.Coefficient(), g.Exponent(), w.Coefficient(), w.Exponent())
		}
	case got.Kind() == reflect.Struct:
		present := !want.IsNil()
		if !present {
			want = reflect.New(want.Type().Elem())
		}
		want = want.Elem()
		for i := 0; i < got.NumField(); i++ {
			name := got.Type().Field(i).Name
			field := want.FieldByName(name)
			if !field.IsValid() { // Has<Field> flag
				other := want.FieldByName(strings.TrimPrefix(name, "Has"))
				if got.Field(i).Bool() != !other.IsNil() {
					t.Errorf("%s.%s: got %v", path, name, got.Field(i).Bool())
				}
				continue
			}
			equalDecoded(t, path+"."+name, got.Field(i), field)
		}
	case got.Kind() == reflect.Slice && got.Type().Elem().Kind() != reflect.Uint8:
		if got.Len() != want.Len() {
			t.Fatalf("%s: got %d elements, want %d", path, got.Len(), want.Len())
		}
		for i := 0; i < got.Len(); i++ {
			equalDecoded(t, path, got.Index(i), want.Index(i))
		}
	case got.Kind() == reflect.Slice:
		if string(got.Bytes()) != string(want.Bytes()) {
			t.Errorf("%s: got %x, want %x", path, got.Bytes(), want.Bytes())
		}
	case got.Kind() == reflect.Float32 || got.Kind() == reflect.Float64:
		if math.Float64bits(got.Float()) != math.Float64bits(want.Float()) {
			t.Errorf("%s: got %v, want %v", path, got.Float(), want.Float())
		}
	default:
		if got.Interface() != want.Interface() {
			t.Errorf("%s: got %v, want %v", path, got.Interface(), want.Interface())
		}
	}
}

// compareDecoders requires the generated decoder to agree with proto.Unmarshal
// on b, apart from its two documented rejections.
func compareDecoders(t *testing.T, b []byte, decoder generatedDecoder, message proto.Message) {
	t.Helper()
	decodeErr := decoder.Unmarshal(b)
	protoErr := proto.Unmarshal(b, message)
	switch {
	case decodeErr == nil && protoErr != nil:
		t.Fatalf("accepted bytes proto.Unmarshal rejects (%v): %x", protoErr, b)
	case decodeErr != nil && protoErr == nil:
		if errors.Is(decodeErr, decimalconv.ErrDuplicateField) || decimalconv.ValidateMessage(message) != nil {
			return
		}
		t.Fatalf("rejected bytes proto.Unmarshal accepts (%v): %x", decodeErr, b)
	case decodeErr == nil:
		equalDecoded(t, "message", reflect.ValueOf(decoder).Elem(), reflect.ValueOf(message))
	}
}

func goldenBytes(t testing.TB) []byte {
	raw, err := hex.DecodeString(goldenHex)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func fullScalarBlock(t testing.TB) *orderbookpb.ScalarBlock {
	amount, err := decimalconv.FromString("-65123.1234567890123456789012")
	if err != nil {
		t.Fatal(err)
	}
	return &orderbookpb.ScalarBlock{
		Flag: true, Int32Value: -7, Int64Value: math.MinInt64, Uint32Value: math.MaxUint32,
		Uint64Value: math.MaxUint64, Sint32Value: math.MinInt32, Sint64Value: math.MinInt64,
		Fixed32Value: math.MaxUint32, Fixed64Value: math.MaxUint64, Sfixed32Value: -5, Sfixed64Value: -6,
		FloatValue: 1.5, DoubleValue: -2.25, Text: "héllo", Data: []byte{0, 1, 2},
		Side:   orderbookpb.Side_SIDE_ASK,
		Level:  &orderbookpb.Level{Price: amount},
		Amount: amount,
		Flags:  []bool{true, false}, Int32Values: []int32{-1, 2}, Uint64Values: []uint64{math.MaxUint64, 0},
		Sint64Values: []int64{-1, math.MaxInt64}, Fixed32Values: []uint32{1, 2},
		Sfixed64Values: []int64{-9}, FloatValues: []float32{0.5}, DoubleValues: []float64{math.Inf(-1), 0.25},
		Texts: []string{"a", ""}, Blobs: [][]byte{{9}, {}}, Sides: []orderbookpb.Side{orderbookpb.Side_SIDE_BID, 99},
		Levels: []*orderbookpb.Level{{Quantity: amount}, {}}, Amounts: []*decimalpb.Decimal{amount, {}},
	}
}

func TestDecodersMatchProto(t *testing.T) {
	compareDecoders(t, goldenBytes(t), &orderbookpb.SnapshotDecimals{}, &orderbookpb.Snapshot{})
	compareDecoders(t, benchSnapshotWire, &orderbookpb.SnapshotDecimals{}, &orderbookpb.Snapshot{})
	compareDecoders(t, nil, &orderbookpb.SnapshotDecimals{}, &orderbookpb.Snapshot{})

	trade := &orderbookpb.Trade{Symbol: "BTC-USDT"}
	trade.SetPriceDecimal(benchPrice)
	trade.SetQuantityDecimal(benchQty)
	compareDecoders(t, mustMarshalWire(trade), &orderbookpb.TradeDecimals{}, &orderbookpb.Trade{})

	block := mustMarshalWire(fullScalarBlock(t))
	compareDecoders(t, block, &orderbookpb.ScalarBlockDecimals{}, &orderbookpb.ScalarBlock{})
	var decoded orderbookpb.ScalarBlockDecimals
	if err := decoded.Unmarshal(block); err != nil {
		t.Fatal(err)
	}
	if !decoded.HasLevel || !decoded.Level.HasPrice || decoded.Level.HasQuantity || !decoded.HasAmount {
		t.Errorf("presence flags wrong: %+v", decoded)
	}
}

func TestDecoderAcceptsUnpackedRepeatedScalars(t *testing.T) {
	var b []byte
	for _, v := range []uint64{1, 2} {
		b = protowire.AppendTag(b, 20, protowire.VarintType) // int32_values, packed by default
		b = protowire.AppendVarint(b, v)
	}
	compareDecoders(t, b, &orderbookpb.ScalarBlockDecimals{}, &orderbookpb.ScalarBlock{})
	var decoded orderbookpb.ScalarBlockDecimals
	if err := decoded.Unmarshal(b); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Int32Values) != 2 {
		t.Errorf("got %v", decoded.Int32Values)
	}
}

func TestDecoderSkipsUnknownFields(t *testing.T) {
	raw := protowire.AppendTag(goldenBytes(t), 9000, protowire.BytesType)
	raw = protowire.AppendString(raw, "from a newer schema")
	var snap orderbookpb.SnapshotDecimals
	if err := snap.Unmarshal(raw); err != nil {
		t.Fatal(err)
	}
	if snap.Symbol != "BTC-USDT" || len(snap.Bids) != 2 {
		t.Errorf("got %+v", snap)
	}
}

func TestDecoderRejections(t *testing.T) {
	level := func(d *decimalpb.Decimal) []byte {
		return mustMarshalWire(&orderbookpb.Level{Price: d})
	}
	var decoded orderbookpb.LevelDecimals

	price := level(&decimalpb.Decimal{Coefficient: []byte{1}})
	if err := decoded.Unmarshal(append(price, price...)); !errors.Is(err, decimalconv.ErrDuplicateField) {
		t.Errorf("duplicate field: got %v", err)
	}
	if err := decoded.Unmarshal(level(&decimalpb.Decimal{Exponent: math.MaxInt32})); err == nil {
		t.Error("exponent bomb accepted")
	}
	if err := decoded.Unmarshal(level(&decimalpb.Decimal{Coefficient: make([]byte, decimalconv.MaxCoefficientBytes+1)})); err == nil {
		t.Error("oversized coefficient accepted")
	}
	if err := decoded.Unmarshal(price[:len(price)-1]); err == nil {
		t.Error("truncated message accepted")
	}

	var block orderbookpb.ScalarBlockDecimals
	invalid := protowire.AppendString(protowire.AppendTag(nil, 14, protowire.BytesType), "\xff")
	if err := block.Unmarshal(invalid); !errors.Is(err, decimalconv.ErrInvalidUTF8) {
		t.Errorf("invalid UTF-8: got %v", err)
	}
	invalid = protowire.AppendString(protowire.AppendTag(nil, 27, protowire.BytesType), "\xff")
	if err := block.Unmarshal(invalid); !errors.Is(err, decimalconv.ErrInvalidUTF8) {
		t.Errorf("invalid UTF-8 in repeated: got %v", err)
	}
}

func TestDecoderReuseLeavesNoStaleValues(t *testing.T) {
	var snap orderbookpb.SnapshotDecimals
	if err := snap.Unmarshal(benchSnapshotWire); err != nil {
		t.Fatal(err)
	}
	capacity := cap(snap.Bids)
	short := mustMarshalWire(&orderbookpb.Snapshot{Bids: []*orderbookpb.Level{{}}})
	if err := snap.Unmarshal(short); err != nil {
		t.Fatal(err)
	}
	if snap.Symbol != "" || len(snap.Bids) != 1 || snap.Bids[0].HasPrice || !snap.Bids[0].Price.IsZero() {
		t.Errorf("stale values after reuse: %+v", snap)
	}
	if cap(snap.Bids) != capacity {
		t.Errorf("capacity changed from %d to %d", capacity, cap(snap.Bids))
	}
}

func FuzzSnapshotDecimals(f *testing.F) {
	f.Add(goldenBytes(f))
	f.Add(benchSnapshotWire)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		compareDecoders(t, b, &orderbookpb.SnapshotDecimals{}, &orderbookpb.Snapshot{})
	})
}

func FuzzScalarBlockDecimals(f *testing.F) {
	f.Add(mustMarshalWire(fullScalarBlock(f)))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		compareDecoders(t, b, &orderbookpb.ScalarBlockDecimals{}, &orderbookpb.ScalarBlock{})
	})
}
