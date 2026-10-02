package mszip_test

import (
	"bytes"
	"math/rand/v2"
	"testing"

	"github.com/abemedia/go-cabinet/mszip"
)

const blockSize = 32768

// TestRoundTrip verifies basic compress/decompress with CK prefix.
func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"short", bytes.Repeat([]byte("hello world this is a test of ms-zip compression "), 50)},
		{"one byte", []byte{'a'}},
		{"block", bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz0123456789"), 2860)[:blockSize]},
		{"multiple blocks", bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz0123456789"), 2860)[:100*1024]},
		{"random", random(3*blockSize + 100)},
	}
	levels := []int{
		mszip.DefaultCompression,
		mszip.BestSpeed,
		mszip.BestCompression,
		mszip.NoCompression,
		mszip.HuffmanOnly,
	}

	for _, tt := range tests {
		for _, level := range levels {
			blocks := encode(t, tt.data, level)
			for i, block := range blocks {
				if !bytes.HasPrefix(block, []byte("CK")) {
					t.Fatalf("%s: block %d: expected CK prefix, got %v", tt.name, i, block[:2])
				}
			}
			got := decode(t, blocks, len(tt.data))
			if !bytes.Equal(got, tt.data) {
				t.Errorf("%s: round-trip mismatch at level %d: got %d bytes, want %d", tt.name, level, len(got), len(tt.data))
			}
		}
	}
}

// TestDictionary verifies that a block is compressed against the previous one.
func TestDictionary(t *testing.T) {
	block := random(blockSize)
	rotated := append(block[1:len(block):len(block)], block[0])
	blocks := encode(t, append(block, rotated...), mszip.DefaultCompression)
	if len(blocks[0]) < blockSize {
		t.Fatalf("random block compressed to %d bytes", len(blocks[0]))
	}
	if len(blocks[1]) > blockSize/10 {
		t.Errorf("repeated block compressed to %d bytes, expected it to match the previous block", len(blocks[1]))
	}
}

// TestReset verifies that an Encoder and a Decoder can be reused for a new stream.
func TestReset(t *testing.T) {
	data := bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz"), 2100)
	want := encode(t, data, mszip.DefaultCompression)

	e, err := mszip.NewEncoder(mszip.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	d := mszip.NewDecoder()
	for range 2 {
		e.Reset()
		d.Reset()
		for i, off := 0, 0; off < len(data); i, off = i+1, off+blockSize {
			src := data[off:min(off+blockSize, len(data))]
			block, err := e.Encode(nil, src)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if !bytes.Equal(block, want[i]) {
				t.Fatalf("block %d differs from a new encoder's", i)
			}
			got := make([]byte, len(src))
			if _, err := d.Decode(got, block); err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !bytes.Equal(got, src) {
				t.Fatalf("block %d: round-trip mismatch", i)
			}
		}
	}
}

func TestEncoder_Errors(t *testing.T) {
	if _, err := mszip.NewEncoder(10); err == nil {
		t.Error("level 10: expected error")
	}
	e, err := mszip.NewEncoder(mszip.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, blockSize + 1} {
		if _, err := e.Encode(nil, make([]byte, n)); err == nil {
			t.Errorf("block of %d bytes: expected error", n)
		}
	}
}

func TestDecoder_Errors(t *testing.T) {
	data := []byte("hello world this is a test of ms-zip compression")
	block := encode(t, data, mszip.DefaultCompression)[0]

	tests := []struct {
		name string
		src  []byte
		size int
	}{
		{"missing signature", block[2:], len(data)},
		{"empty", nil, len(data)},
		{"truncated", block[:len(block)/2], len(data)},
		{"size too small", block, len(data) - 1},
		{"size too large", block, len(data) + 1},
		{"size over a block", block, blockSize + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := mszip.NewDecoder().Decode(make([]byte, tt.size), tt.src); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// encode compresses data and returns its blocks.
func encode(tb testing.TB, data []byte, level int) [][]byte {
	tb.Helper()
	e, err := mszip.NewEncoder(level)
	if err != nil {
		tb.Fatal(err)
	}
	var blocks [][]byte
	for off := 0; off < len(data); off += blockSize {
		block, err := e.Encode(nil, data[off:min(off+blockSize, len(data))])
		if err != nil {
			tb.Fatalf("Encode: %v", err)
		}
		blocks = append(blocks, block)
	}
	return blocks
}

// decode decompresses blocks that hold size bytes in total.
func decode(tb testing.TB, blocks [][]byte, size int) []byte {
	tb.Helper()
	d := mszip.NewDecoder()
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
