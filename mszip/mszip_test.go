package mszip_test

import (
	"bytes"
	"math/rand/v2"
	"testing"

	"github.com/abemedia/go-cabinet/mszip"
)

const blockSize = 1 << 15

// TestRoundTrip verifies basic compress/decompress with CK prefix.
func TestRoundTrip(t *testing.T) {
	pattern := []byte("abcdefghijklmnopqrstuvwxyz0123456789")
	tests := []struct {
		name string
		data []byte
	}{
		{"short", bytes.Repeat([]byte("hello world this is a test of mszip compression "), 50)},
		{"one byte", []byte{'a'}},
		{"block", bytes.Repeat(pattern, blockSize/len(pattern)+1)[:blockSize]},
		{"multiple blocks", bytes.Repeat(pattern, (3*blockSize+100)/len(pattern)+1)[:3*blockSize+100]},
		{"random", random(3*blockSize + 100)},
	}
	levels := []int{
		mszip.NoCompression,
		mszip.BestSpeed,
		mszip.BestCompression,
		mszip.DefaultCompression,
		mszip.HuffmanOnly,
	}

	for _, test := range tests {
		for _, level := range levels {
			blocks := encode(t, test.data, level)
			for i, block := range blocks {
				if !bytes.HasPrefix(block, []byte("CK")) {
					t.Fatalf("%s: block %d: expected CK prefix, got %v", test.name, i, block[:2])
				}
			}
			got := decode(t, blocks, len(test.data))
			if !bytes.Equal(got, test.data) {
				t.Errorf("%s: round-trip mismatch at level %d: got %d bytes, want %d", test.name, level, len(got), len(test.data))
			}
		}
	}
}

// encode compresses data and returns its blocks.
func encode(tb testing.TB, data []byte, level int) [][]byte {
	tb.Helper()
	e, err := mszip.NewEncoder(level)
	if err != nil {
		tb.Fatal(err)
	}
	return encodeBlocks(tb, e, data)
}

// encodeBlocks compresses data with e and returns its blocks.
func encodeBlocks(tb testing.TB, e *mszip.Encoder, data []byte) [][]byte {
	tb.Helper()
	var blocks [][]byte
	for off := 0; off < len(data); off += blockSize {
		block, err := e.Encode(nil, data[off:min(off+blockSize, len(data))])
		if err != nil {
			tb.Fatalf("Encode block %d: %v", len(blocks), err)
		}
		blocks = append(blocks, block)
	}
	return blocks
}

// decode decompresses blocks that hold size bytes in total.
func decode(tb testing.TB, blocks [][]byte, size int) []byte {
	tb.Helper()
	return decodeBlocks(tb, mszip.NewDecoder(), blocks, size)
}

// decodeBlocks decompresses blocks that hold size bytes in total with d.
func decodeBlocks(tb testing.TB, d *mszip.Decoder, blocks [][]byte, size int) []byte {
	tb.Helper()
	out := make([]byte, size)
	for i, block := range blocks {
		off := i * blockSize
		if _, err := d.Decode(out[off:min(off+blockSize, size)], block); err != nil {
			tb.Fatalf("Decode block %d: %v", i, err)
		}
	}
	return out
}

func random(n int) []byte {
	rng := rand.New(rand.NewPCG(1, 2))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.Uint32())
	}
	return b
}
