// Command compare times a snapshot's encode and decode as decimal strings and
// as protodecimal.v1.Decimal, on the levels in -input, and prints the result
// as JSON. It is driven by py/example/compare.py, which times Python on the
// same data and reports both; see `make compare`.
//
// -input is a CSV of price,quantity rows, grouped into snapshots of -levels.
package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"testing"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"

	"github.com/andefined/protodecimal/go/example/orderbookpb"
)

const runs = 3

type level struct {
	price, quantity decimal.Decimal
}

// measurement is per snapshot.
type measurement struct {
	EncodeNs float64 `json:"encode_ns"`
	DecodeNs float64 `json:"decode_ns"`
	Bytes    float64 `json:"bytes"`
}

func main() {
	input := flag.String("input", "", "CSV of price,quantity rows")
	levels := flag.Int("levels", 25, "levels per snapshot")
	flag.Parse()

	snapshots, err := readSnapshots(*input, *levels)
	if err != nil {
		log.Fatal(err)
	}
	decimalWire := make([][]byte, len(snapshots))
	stringWire := make([][]byte, len(snapshots))
	for i, s := range snapshots {
		if decimalWire[i], err = encodeDecimal(s); err != nil {
			log.Fatal(err)
		}
		if stringWire[i], err = encodeString(s); err != nil {
			log.Fatal(err)
		}
	}
	if err := verify(snapshots, decimalWire, stringWire); err != nil {
		log.Fatal(err)
	}

	out := make([]level, *levels)
	var generated orderbookpb.SnapshotDecimals
	report := map[string]measurement{
		"decimal": measure(snapshots, decimalWire, encodeDecimal, func(raw []byte) error { return decodeDecimal(raw, out) }),
		"string":  measure(snapshots, stringWire, encodeString, func(raw []byte) error { return decodeString(raw, out) }),
		"fast":    measure(snapshots, decimalWire, encodeDecimal, generated.Unmarshal),
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		log.Fatal(err)
	}
}

func readSnapshots(path string, levels int) ([][]level, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		return nil, err
	}
	var snapshots [][]level
	for start := 0; start+levels <= len(rows); start += levels {
		snapshot := make([]level, levels)
		for i, row := range rows[start : start+levels] {
			if snapshot[i].price, err = decimal.NewFromString(row[0]); err != nil {
				return nil, err
			}
			if snapshot[i].quantity, err = decimal.NewFromString(row[1]); err != nil {
				return nil, err
			}
		}
		snapshots = append(snapshots, snapshot)
	}
	if len(snapshots) == 0 {
		return nil, fmt.Errorf("%s: fewer than %d rows", path, levels)
	}
	return snapshots, nil
}

func encodeDecimal(levels []level) ([]byte, error) {
	snap := &orderbookpb.Snapshot{
		Symbol: "BTC-USDT",
		Bids:   make([]*orderbookpb.Level, len(levels)),
	}
	for i, l := range levels {
		lvl := &orderbookpb.Level{}
		lvl.SetPriceDecimal(l.price)
		lvl.SetQuantityDecimal(l.quantity)
		snap.Bids[i] = lvl
	}
	return proto.Marshal(snap)
}

func encodeString(levels []level) ([]byte, error) {
	snap := &orderbookpb.StringSnapshot{
		Symbol: "BTC-USDT",
		Bids:   make([]*orderbookpb.StringLevel, len(levels)),
	}
	for i, l := range levels {
		snap.Bids[i] = &orderbookpb.StringLevel{
			Price:    l.price.String(),
			Quantity: l.quantity.String(),
		}
	}
	return proto.Marshal(snap)
}

func decodeDecimal(raw []byte, out []level) error {
	var snap orderbookpb.Snapshot
	if err := proto.Unmarshal(raw, &snap); err != nil {
		return err
	}
	for i, lvl := range snap.Bids {
		out[i] = level{lvl.GetPriceDecimal(), lvl.GetQuantityDecimal()}
	}
	return nil
}

func decodeString(raw []byte, out []level) error {
	var snap orderbookpb.StringSnapshot
	if err := proto.Unmarshal(raw, &snap); err != nil {
		return err
	}
	for i, lvl := range snap.Bids {
		price, err := decimal.NewFromString(lvl.GetPrice())
		if err != nil {
			return err
		}
		quantity, err := decimal.NewFromString(lvl.GetQuantity())
		if err != nil {
			return err
		}
		out[i] = level{price, quantity}
	}
	return nil
}

// verify confirms both encodings carry the same values, so that the timings
// compare equivalent work.
func verify(snapshots [][]level, decimalWire, stringWire [][]byte) error {
	for i, want := range snapshots {
		fromDecimal := make([]level, len(want))
		fromString := make([]level, len(want))
		if err := decodeDecimal(decimalWire[i], fromDecimal); err != nil {
			return err
		}
		if err := decodeString(stringWire[i], fromString); err != nil {
			return err
		}
		var generated orderbookpb.SnapshotDecimals
		if err := generated.Unmarshal(decimalWire[i]); err != nil {
			return err
		}
		for j, l := range want {
			fromGenerated := level{generated.Bids[j].Price, generated.Bids[j].Quantity}
			for _, got := range []level{fromDecimal[j], fromString[j], fromGenerated} {
				if !got.price.Equal(l.price) || !got.quantity.Equal(l.quantity) {
					return fmt.Errorf("snapshot %d level %d: decoded %v, want %v", i, j, got, l)
				}
			}
		}
	}
	return nil
}

func measure(
	snapshots [][]level,
	wire [][]byte,
	encode func([]level) ([]byte, error),
	decode func([]byte) error,
) measurement {
	var size int
	for _, raw := range wire {
		size += len(raw)
	}
	count := float64(len(snapshots))
	return measurement{
		EncodeNs: median(func() error {
			for _, s := range snapshots {
				if _, err := encode(s); err != nil {
					return err
				}
			}
			return nil
		}) / count,
		DecodeNs: median(func() error {
			for _, raw := range wire {
				if err := decode(raw); err != nil {
					return err
				}
			}
			return nil
		}) / count,
		Bytes: float64(size) / count,
	}
}

func median(op func() error) float64 {
	results := make([]int64, runs)
	for i := range results {
		results[i] = testing.Benchmark(func(b *testing.B) {
			for n := 0; n < b.N; n++ {
				if err := op(); err != nil {
					log.Fatal(err)
				}
			}
		}).NsPerOp()
	}
	sort.Slice(results, func(i, j int) bool { return results[i] < results[j] })
	return float64(results[runs/2])
}
