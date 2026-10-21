package lzx_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/abemedia/go-cabinet/lzx"
)

func TestNewDecoder(t *testing.T) {
	for window := lzx.MinWindow - 1; window <= lzx.MaxWindow+1; window++ {
		want := window < lzx.MinWindow || window > lzx.MaxWindow
		if _, err := lzx.NewDecoder(window); (err != nil) != want {
			t.Errorf("NewDecoder(%d): got %v, want error: %t", window, err, want)
		}
	}
}

func TestDecoder_Reset(t *testing.T) {
	data := words(2*frameSize+100, 3)
	frames := encode(t, data, lzx.MaxWindow)

	d, err := lzx.NewDecoder(lzx.MaxWindow)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		d.Reset()
		got := make([]byte, len(data))
		for i, frame := range frames {
			off := i * frameSize
			if _, err := d.Decode(got[off:min(off+frameSize, len(data))], frame); err != nil {
				t.Fatalf("Decode frame %d: %v", i, err)
			}
		}
		if !bytes.Equal(got, data) {
			t.Fatal("round-trip mismatch")
		}
	}
}

// TestDecoderUncompressedBlocks decodes streams made of uncompressed blocks,
// which the Encoder produces only as whole frames.
func TestDecoderUncompressedBlocks(t *testing.T) {
	data := random(frameSize + 100)

	t.Run("odd blocks in one frame", func(t *testing.T) {
		var b bitWriter
		b.bits(0, 1)
		b.block([]byte("HELLO"))
		b.raw(0)
		b.block([]byte("abc"))
		b.raw(0)

		got := decode(t, [][]byte{b.buf}, 8, lzx.MaxWindow)
		if want := "HELLOabc"; string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("block spanning frames", func(t *testing.T) {
		var first, second bitWriter
		first.bits(0, 1)
		first.header(frameSize + 1)
		first.raw(data[:frameSize]...)
		second.raw(data[frameSize], 0)
		second.block(data[frameSize+1:])
		second.raw(0)

		got := decode(t, [][]byte{first.buf, second.buf}, len(data), lzx.MaxWindow)
		if !bytes.Equal(got, data) {
			t.Fatal("content mismatch")
		}
	})

	// A block of odd length that ends a frame may be followed by its padding
	// byte in that frame or in the next.
	for _, name := range []string{"padding at end of frame", "padding at start of frame"} {
		t.Run(name, func(t *testing.T) {
			var first, second bitWriter
			first.bits(0, 1)
			first.block(data[:1])
			first.raw(0)
			first.block(data[1:frameSize])
			if name == "padding at end of frame" {
				first.raw(0)
			} else {
				second.raw(0)
			}
			second.block(data[frameSize:])

			got := decode(t, [][]byte{first.buf, second.buf}, len(data), lzx.MaxWindow)
			if !bytes.Equal(got, data) {
				t.Fatal("content mismatch")
			}
		})
	}
}

func TestDecoderErrors(t *testing.T) {
	data := words(2*frameSize, 4)
	frames := encode(t, data, lzx.MaxWindow)
	pretreeFrame := func(lens [20]uint32) []byte {
		var b bitWriter
		b.bits(0, 1)
		b.bits(1, 3)
		b.bits(1, 24)
		for _, l := range lens {
			b.bits(l, 4)
		}
		b.bits(0, 4)
		return b.buf
	}
	tests := []struct {
		name string
		size int
		src  []byte
		want error
	}{
		{"truncated frame", frameSize, frames[0][:len(frames[0])/2], io.ErrUnexpectedEOF},
		{"over-subscribed pretree", 1, pretreeFrame([20]uint32{1, 1, 2}), lzx.ErrCorrupt},
		{"incomplete pretree", 1, pretreeFrame([20]uint32{1, 2, 3}), lzx.ErrCorrupt},
		{"empty pretree", 1, pretreeFrame([20]uint32{}), lzx.ErrCorrupt},
		{"empty frame", 0, frames[0], lzx.ErrFrameSize},
		{"oversized frame", frameSize + 1, frames[0], lzx.ErrFrameSize},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d, err := lzx.NewDecoder(lzx.MaxWindow)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.Decode(make([]byte, test.size), test.src); !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
			dst := make([]byte, frameSize)
			if _, err := d.Decode(dst, frames[0]); !errors.Is(err, test.want) {
				t.Fatalf("valid frame after the error: got %v, want %v until Reset", err, test.want)
			}
			d.Reset()
			if _, err := d.Decode(dst, frames[0]); err != nil {
				t.Fatalf("valid frame after Reset: %v", err)
			}
		})
	}
}

func TestDecoderTruncated(t *testing.T) {
	data := words(frameSize+100, 6)
	frames := encode(t, data, lzx.MinWindow)
	d, err := lzx.NewDecoder(lzx.MinWindow)
	if err != nil {
		t.Fatal(err)
	}
	prior := make([]byte, frameSize)
	for k, frame := range frames {
		dst := make([]byte, min(frameSize, len(data)-k*frameSize))
		for n := range frame {
			d.Reset()
			for _, f := range frames[:k] {
				if _, err := d.Decode(prior, f); err != nil {
					t.Fatalf("Decode frame before %d: %v", k, err)
				}
			}
			if _, err := d.Decode(dst, frame[:n]); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("Decode of frame %d cut to %d bytes: got %v, want %v", k, n, err, io.ErrUnexpectedEOF)
			}
		}
	}
}

// bitWriter builds LZX frames by hand.
type bitWriter struct {
	buf []byte
	acc uint64
	n   uint
}

// bits writes the low n bits of v, most significant first, in 16-bit
// little-endian words.
func (w *bitWriter) bits(v uint32, n uint) {
	w.acc = w.acc<<n | uint64(v)
	w.n += n
	for w.n >= 16 {
		w.n -= 16
		w.buf = binary.LittleEndian.AppendUint16(w.buf, uint16(w.acc>>w.n))
	}
}

// header writes the header of an uncompressed block of the given size.
func (w *bitWriter) header(size int) {
	w.bits(3, 3)
	w.bits(uint32(size), 24)
	w.bits(0, 16-w.n)
	for range 3 {
		w.buf = binary.LittleEndian.AppendUint32(w.buf, 1)
	}
}

// raw writes bytes. It must not be called in the middle of a word.
func (w *bitWriter) raw(p ...byte) {
	w.buf = append(w.buf, p...)
}

// block writes an uncompressed block holding p, without the padding byte
// that follows a block of odd length.
func (w *bitWriter) block(p []byte) {
	w.header(len(p))
	w.raw(p...)
}
