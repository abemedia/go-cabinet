package lzx_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/abemedia/go-cabinet/lzx"
)

const frameSize = 32768

func TestRoundTrip(t *testing.T) {
	text := words(3*frameSize, 1)
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"one byte", []byte{'a'}},
		{"short", []byte("hello")},
		{"frame minus one", text[:frameSize-1]},
		{"frame", text[:frameSize]},
		{"frame plus one", text[:frameSize+1]},
		{"zeros", make([]byte, 3*frameSize)},
		{"random", random(3 * frameSize)},
		{"x86 calls", x86Calls(3 * frameSize)},
		{"words", words(1<<20, 2)},
	}

	for _, tt := range tests {
		for _, window := range []int{lzx.MinWindow, lzx.MaxWindow} {
			t.Run(fmt.Sprintf("%s/window=%d", tt.name, window), func(t *testing.T) {
				frames := encode(t, tt.data, window)
				got := decode(t, frames, len(tt.data), window)
				if !bytes.Equal(got, tt.data) {
					t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(got), len(tt.data))
				}
			})
		}
	}
}

// TestReset verifies that an Encoder and a Decoder can be reused for a new stream.
func TestReset(t *testing.T) {
	data := words(2*frameSize+100, 3)
	want := encode(t, data, lzx.MaxWindow)

	e, err := lzx.NewEncoder(lzx.MaxWindow)
	if err != nil {
		t.Fatal(err)
	}
	d, err := lzx.NewDecoder(lzx.MaxWindow)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		e.Reset()
		d.Reset()
		for i, off := 0, 0; off < len(data); i, off = i+1, off+frameSize {
			src := data[off:min(off+frameSize, len(data))]
			frame, err := e.Encode(nil, src)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if !bytes.Equal(frame, want[i]) {
				t.Fatalf("frame %d differs from a new encoder's", i)
			}
			got := make([]byte, len(src))
			if _, err := d.Decode(got, frame); err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !bytes.Equal(got, src) {
				t.Fatalf("frame %d: round-trip mismatch", i)
			}
		}
	}
}

// TestDecoder_UncompressedBlocks decodes streams made of uncompressed blocks,
// which the Encoder never produces.
func TestDecoder_UncompressedBlocks(t *testing.T) {
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

func TestDecoder_Errors(t *testing.T) {
	data := words(2*frameSize, 4)
	frames := encode(t, data, lzx.MaxWindow)
	dst := make([]byte, frameSize)

	t.Run("truncated frame", func(t *testing.T) {
		d, err := lzx.NewDecoder(lzx.MaxWindow)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Decode(dst, frames[0][:len(frames[0])/2]); err == nil {
			t.Fatal("expected error for truncated frame")
		}
		if _, err := d.Decode(dst, frames[0]); err == nil {
			t.Fatal("expected the error to persist until Reset")
		}
		d.Reset()
		if _, err := d.Decode(dst, frames[0]); err != nil {
			t.Fatalf("Decode after Reset: %v", err)
		}
	})

	t.Run("frame size", func(t *testing.T) {
		for _, n := range []int{0, frameSize + 1} {
			d, err := lzx.NewDecoder(lzx.MaxWindow)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.Decode(make([]byte, n), frames[0]); err == nil {
				t.Errorf("frame of %d bytes: expected error", n)
			}
		}
	})

	t.Run("window bits", func(t *testing.T) {
		for _, window := range []int{lzx.MinWindow - 1, lzx.MaxWindow + 1} {
			if _, err := lzx.NewDecoder(window); err == nil {
				t.Errorf("window bits %d: expected error", window)
			}
		}
	})
}

func TestEncoder_Errors(t *testing.T) {
	t.Run("frame size", func(t *testing.T) {
		for _, n := range []int{0, frameSize + 1} {
			e, err := lzx.NewEncoder(lzx.MaxWindow)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.Encode(nil, make([]byte, n)); err == nil {
				t.Errorf("frame of %d bytes: expected error", n)
			}
		}
	})

	t.Run("frame after final frame", func(t *testing.T) {
		e, err := lzx.NewEncoder(lzx.MaxWindow)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Encode(nil, []byte("a short frame ends the stream")); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Encode(nil, []byte("more")); err == nil {
			t.Fatal("expected error encoding after a short frame")
		}
	})

	t.Run("window bits", func(t *testing.T) {
		for _, window := range []int{lzx.MinWindow - 1, lzx.MaxWindow + 1} {
			if _, err := lzx.NewEncoder(window); err == nil {
				t.Errorf("window bits %d: expected error", window)
			}
		}
	})
}

func FuzzDecoder(f *testing.F) {
	for _, frame := range encode(f, words(frameSize+100, 5), lzx.MinWindow) {
		f.Add(frame)
	}
	f.Add([]byte{})

	f.Fuzz(func(_ *testing.T, data []byte) {
		d, err := lzx.NewDecoder(lzx.MinWindow)
		if err != nil {
			return
		}
		dst := make([]byte, frameSize)
		_, _ = d.Decode(dst, data)
		_, _ = d.Decode(dst, data)
	})
}

// encode compresses data and returns its frames.
func encode(tb testing.TB, data []byte, window int) [][]byte {
	tb.Helper()
	e, err := lzx.NewEncoder(window)
	if err != nil {
		tb.Fatal(err)
	}
	var frames [][]byte
	for off := 0; off < len(data); off += frameSize {
		frame, err := e.Encode(nil, data[off:min(off+frameSize, len(data))])
		if err != nil {
			tb.Fatalf("Encode: %v", err)
		}
		frames = append(frames, frame)
	}
	return frames
}

// decode decompresses frames that hold size bytes in total.
func decode(tb testing.TB, frames [][]byte, size, window int) []byte {
	tb.Helper()
	d, err := lzx.NewDecoder(window)
	if err != nil {
		tb.Fatal(err)
	}
	out := make([]byte, size)
	for i, frame := range frames {
		off := i * frameSize
		if _, err := d.Decode(out[off:min(off+frameSize, size)], frame); err != nil {
			tb.Fatalf("Decode frame %d: %v", i, err)
		}
	}
	return out
}

// bitWriter builds LZX frames by hand.
type bitWriter struct {
	buf []byte
	acc uint64
	n   uint
}

// bits writes the low n bits of v, most significant first, in 16-bit little-endian words.
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

func random(n int) []byte {
	rng := rand.New(rand.NewPCG(1, 2))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.Uint32())
	}
	return b
}

// x86Calls returns data with frequent CALL opcodes (0xE8) followed by targets
// near the call, which E8 translation turns into repeated absolute targets.
func x86Calls(n int) []byte {
	rng := rand.New(rand.NewPCG(3, 4))
	b := make([]byte, n)
	for i := 0; i < n; i++ {
		if rng.IntN(4) == 0 && i+5 <= n {
			binary.LittleEndian.PutUint32(b[i+1:], uint32(rng.IntN(64)*16-i))
			b[i] = 0xE8
			i += 4
		} else {
			b[i] = byte(rng.IntN(32))
		}
	}
	// A target just below the translation size of 12,000,000 bytes wraps around
	// when translated.
	copy(b, []byte{0, 0, 0, 0, 0xE8})
	binary.LittleEndian.PutUint32(b[5:], 12000000-1)
	return b
}

// words returns text made from a small vocabulary, which is full of repeats of
// varying lengths.
func words(n int, seed uint64) []byte {
	rng := rand.New(rand.NewPCG(seed, seed))
	vocab := make([]string, 300)
	for i := range vocab {
		w := make([]byte, 2+rng.IntN(9))
		for j := range w {
			w[j] = byte('a' + rng.IntN(26))
		}
		vocab[i] = string(w)
	}
	var b bytes.Buffer
	for b.Len() < n {
		b.WriteString(vocab[int(rng.ExpFloat64()*40)%len(vocab)])
		b.WriteByte(" \n,."[rng.IntN(4)])
	}
	return b.Bytes()[:n]
}
