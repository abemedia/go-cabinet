package mszip_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/abemedia/go-cabinet/mszip"
)

func TestDecoder_Reset(t *testing.T) {
	data := bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz"), 2100)
	blocks := encode(t, data, mszip.DefaultCompression)

	d := mszip.NewDecoder()
	for range 2 {
		d.Reset()
		got := decodeBlocks(t, d, blocks, len(data))
		if !bytes.Equal(got, data) {
			t.Fatal("round-trip mismatch")
		}
	}
}

func TestDecoderErrors(t *testing.T) {
	data := []byte("hello world this is a test of mszip compression")
	block := encode(t, data, mszip.DefaultCompression)[0]

	tests := []struct {
		name string
		src  []byte
		size int
		want string
	}{
		{"missing signature", block[2:], len(data), "mszip: invalid block signature"},
		{"empty", nil, len(data), "mszip: invalid block signature"},
		{"truncated", block[:len(block)/2], len(data), io.ErrUnexpectedEOF.Error()},
		{"size too small", block, len(data) - 1, "mszip: block does not match its uncompressed size"},
		{"size too large", block, len(data) + 1, "mszip: block does not match its uncompressed size"},
		{"size over a block", block, blockSize + 1, "mszip: invalid block size"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := mszip.NewDecoder().Decode(make([]byte, test.size), test.src); err == nil || err.Error() != test.want {
				t.Errorf("Decode = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDecoderTruncated(t *testing.T) {
	data := bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz"), 2100)
	blocks := encode(t, data, mszip.DefaultCompression)

	d := mszip.NewDecoder()
	for k, block := range blocks {
		dst := make([]byte, min(blockSize, len(data)-k*blockSize))
		for n := len("CK"); n < len(block); n++ {
			d.Reset()
			decodeBlocks(t, d, blocks[:k], k*blockSize)
			if _, err := d.Decode(dst, block[:n]); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("Decode of block %d cut to %d bytes: got %v, want %v", k, n, err, io.ErrUnexpectedEOF)
			}
		}
	}
}
