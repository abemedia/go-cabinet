package lzx_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/abemedia/go-cabinet/lzx"
)

func TestNewEncoder(t *testing.T) {
	for window := lzx.MinWindow - 1; window <= lzx.MaxWindow+1; window++ {
		want := window < lzx.MinWindow || window > lzx.MaxWindow
		if _, err := lzx.NewEncoder(window); (err != nil) != want {
			t.Errorf("NewEncoder(%d): got %v, want error: %t", window, err, want)
		}
	}
}

func TestEncoder_Reset(t *testing.T) {
	data := words(2*frameSize+100, 3)
	want := encode(t, data, lzx.MaxWindow)

	e, err := lzx.NewEncoder(lzx.MaxWindow)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		e.Reset()
		for i, off := 0, 0; off < len(data); i, off = i+1, off+frameSize {
			frame, err := e.Encode(nil, data[off:min(off+frameSize, len(data))])
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if !bytes.Equal(frame, want[i]) {
				t.Fatalf("frame %d differs from a new encoder's", i)
			}
		}
	}
}

// TestEncoderFrameLimit verifies that a frame of data that does not compress
// stays within the 32768+6144 bytes that the format allows.
func TestEncoderFrameLimit(t *testing.T) {
	const maxFrame = frameSize + 6144
	data := random(3*frameSize + 1)
	for _, window := range []int{lzx.MinWindow, lzx.MaxWindow} {
		for i, frame := range encode(t, data, window) {
			if len(frame) > maxFrame {
				t.Errorf("window=%d: frame %d holds %d bytes, want at most %d", window, i, len(frame), maxFrame)
			}
		}
	}
}

// TestEncoderSlide round-trips data that makes the Encoder's window slide at
// least twice, at every window size.
func TestEncoderSlide(t *testing.T) {
	data := words(3<<lzx.MaxWindow+frameSize, 6)
	for window := lzx.MinWindow; window <= lzx.MaxWindow; window++ {
		t.Run(fmt.Sprintf("window=%d", window), func(t *testing.T) {
			// The Encoder slides by at least 256 KiB, whatever the window.
			src := data[:3<<max(window, 18)+frameSize]
			got := decode(t, encode(t, src, window), len(src), window)
			if !bytes.Equal(got, src) {
				t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(got), len(src))
			}
		})
	}
}

func TestEncoderErrors(t *testing.T) {
	t.Run("frame size", func(t *testing.T) {
		for _, n := range []int{0, frameSize + 1} {
			e, err := lzx.NewEncoder(lzx.MaxWindow)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.Encode(nil, make([]byte, n)); !errors.Is(err, lzx.ErrFrameSize) {
				t.Errorf("frame of %d bytes: got %v, want %v", n, err, lzx.ErrFrameSize)
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
		if _, err := e.Encode(nil, []byte("more")); !errors.Is(err, lzx.ErrFrameSize) {
			t.Fatalf("after a short frame: got %v, want %v", err, lzx.ErrFrameSize)
		}
	})
}
