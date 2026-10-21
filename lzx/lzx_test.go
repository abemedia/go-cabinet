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

	for _, test := range tests {
		for _, window := range []int{lzx.MinWindow, lzx.MaxWindow} {
			t.Run(fmt.Sprintf("%s/window=%d", test.name, window), func(t *testing.T) {
				frames := encode(t, test.data, window)
				got := decode(t, frames, len(test.data), window)
				if !bytes.Equal(got, test.data) {
					t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(got), len(test.data))
				}
			})
		}
	}
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
	// A target just below the translation size of 16 MiB wraps around when
	// translated.
	copy(b, []byte{0, 0, 0, 0, 0xE8})
	binary.LittleEndian.PutUint32(b[5:], 1<<24-1)
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
