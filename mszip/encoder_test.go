package mszip_test

import (
	"bytes"
	"testing"

	"github.com/abemedia/go-cabinet/mszip"
)

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

func TestEncoder_Reset(t *testing.T) {
	data := bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz"), 2100)
	want := encode(t, data, mszip.DefaultCompression)

	e, err := mszip.NewEncoder(mszip.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		e.Reset()
		got := encodeBlocks(t, e, data)
		for i := range want {
			if !bytes.Equal(got[i], want[i]) {
				t.Fatalf("block %d differs from a new encoder's", i)
			}
		}
	}
}

func TestEncoderErrors(t *testing.T) {
	if _, err := mszip.NewEncoder(10); err == nil || err.Error() != "mszip: invalid compression level: 10" {
		t.Errorf("NewEncoder(10) = %v", err)
	}
	e, err := mszip.NewEncoder(mszip.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, blockSize + 1} {
		if _, err := e.Encode(nil, make([]byte, n)); err == nil || err.Error() != "mszip: invalid block size" {
			t.Errorf("Encode of %d bytes = %v", n, err)
		}
	}
}
