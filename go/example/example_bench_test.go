package example

import (
	"testing"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"

	"github.com/andefined/protodecimal/go/decimalconv"
	"github.com/andefined/protodecimal/go/decimalpb"
	"github.com/andefined/protodecimal/go/example/orderbookpb"
)

var (
	benchPrice = decimal.RequireFromString("65123.123456789012345678901234567890123456")
	benchQty   = decimal.RequireFromString("0.000000000000000000000000000000000042")
	benchWire  = decimalconv.FromShopspring(benchPrice)
)

func makeBenchSnapshot(nLevels int) *orderbookpb.Snapshot {
	snap := &orderbookpb.Snapshot{Symbol: "BTC-USDT"}
	for range nLevels {
		lvl := &orderbookpb.Level{}
		lvl.SetPriceDecimal(benchPrice)
		lvl.SetQuantityDecimal(benchQty)
		snap.Bids = append(snap.Bids, lvl)
	}
	return snap
}

var (
	benchSnapshot     = makeBenchSnapshot(25)
	benchSnapshotWire = mustMarshalWire(benchSnapshot)
)

func mustMarshalWire(m proto.Message) []byte {
	b, err := proto.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

func BenchmarkFromShopspring(b *testing.B) {
	for i := 0; i < b.N; i++ {
		decimalconv.FromShopspring(benchPrice)
	}
}

func BenchmarkToShopspring(b *testing.B) {
	for i := 0; i < b.N; i++ {
		decimalconv.ToShopspring(benchWire)
	}
}

func BenchmarkWireMarshal(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := proto.Marshal(benchSnapshot); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWireUnmarshal(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var snap orderbookpb.Snapshot
		if err := proto.Unmarshal(benchSnapshotWire, &snap); err != nil {
			b.Fatal(err)
		}
	}
}

// The benchmarks below compare Decimal against the usual alternative, a
// decimal string, on the same 25 levels. Each has a Decimal and a String
// sub-benchmark. String fields unmarshal cheaply because nothing has been
// parsed yet, so SnapshotEncode and SnapshotDecode, which include the
// conversion to and from shopspring decimals, are the fair comparison.
//
// Fast sub-benchmarks use decimalconv.Unmarshal and the generated Decimals
// decoders, which read wire bytes directly. Encoding has no Go fast path.

const benchLevels = 25

var (
	benchPriceString    = benchPrice.String()
	benchWireBytes      = mustMarshalWire(benchWire)
	benchStringSnapshot = makeBenchStringSnapshot(benchLevels)
	benchStringWire     = mustMarshalWire(benchStringSnapshot)

	sinkWire    *decimalpb.Decimal
	sinkString  string
	sinkDecimal decimal.Decimal
)

func makeBenchStringSnapshot(nLevels int) *orderbookpb.StringSnapshot {
	snap := &orderbookpb.StringSnapshot{Symbol: "BTC-USDT"}
	for range nLevels {
		snap.Bids = append(snap.Bids, &orderbookpb.StringLevel{
			Price:    benchPrice.String(),
			Quantity: benchQty.String(),
		})
	}
	return snap
}

func BenchmarkValueEncode(b *testing.B) {
	b.Run("Decimal", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			sinkWire = decimalconv.FromShopspring(benchPrice)
		}
	})
	b.Run("String", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			sinkString = benchPrice.String()
		}
	})
}

// BenchmarkValueDecode's Decimal variant starts from a message protobuf has
// already decoded, while Fast and String start from their wire form. Fast does
// more work here; compare whole decodes with BenchmarkSnapshotDecode.
func BenchmarkValueDecode(b *testing.B) {
	b.Run("Decimal", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			sinkDecimal = decimalconv.ToShopspring(benchWire)
		}
	})
	b.Run("Fast", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			d, err := decimalconv.Unmarshal(benchWireBytes)
			if err != nil {
				b.Fatal(err)
			}
			sinkDecimal = d
		}
	})
	b.Run("String", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			d, err := decimal.NewFromString(benchPriceString)
			if err != nil {
				b.Fatal(err)
			}
			sinkDecimal = d
		}
	})
}

func BenchmarkSnapshotMarshal(b *testing.B) {
	benchmarkMarshal := func(b *testing.B, m proto.Message) {
		b.ReportAllocs()
		b.ReportMetric(float64(proto.Size(m)), "wire-bytes")
		for i := 0; i < b.N; i++ {
			if _, err := proto.Marshal(m); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.Run("Decimal", func(b *testing.B) { benchmarkMarshal(b, benchSnapshot) })
	b.Run("String", func(b *testing.B) { benchmarkMarshal(b, benchStringSnapshot) })
}

func BenchmarkSnapshotUnmarshal(b *testing.B) {
	b.Run("Decimal", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var snap orderbookpb.Snapshot
			if err := proto.Unmarshal(benchSnapshotWire, &snap); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("String", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var snap orderbookpb.StringSnapshot
			if err := proto.Unmarshal(benchStringWire, &snap); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkSnapshotEncode(b *testing.B) {
	b.Run("Decimal", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := proto.Marshal(makeBenchSnapshot(benchLevels)); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("String", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := proto.Marshal(makeBenchStringSnapshot(benchLevels)); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkSnapshotDecode(b *testing.B) {
	b.Run("Decimal", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var snap orderbookpb.Snapshot
			if err := proto.Unmarshal(benchSnapshotWire, &snap); err != nil {
				b.Fatal(err)
			}
			for _, lvl := range snap.Bids {
				sinkDecimal = lvl.GetPriceDecimal()
				sinkDecimal = lvl.GetQuantityDecimal()
			}
		}
	})
	b.Run("Fast", func(b *testing.B) {
		b.ReportAllocs()
		var snap orderbookpb.SnapshotDecimals
		for i := 0; i < b.N; i++ {
			if err := snap.Unmarshal(benchSnapshotWire); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("String", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var snap orderbookpb.StringSnapshot
			if err := proto.Unmarshal(benchStringWire, &snap); err != nil {
				b.Fatal(err)
			}
			for _, lvl := range snap.Bids {
				price, err := decimal.NewFromString(lvl.GetPrice())
				if err != nil {
					b.Fatal(err)
				}
				qty, err := decimal.NewFromString(lvl.GetQuantity())
				if err != nil {
					b.Fatal(err)
				}
				sinkDecimal, sinkDecimal = price, qty
			}
		}
	})
}
