package lzx_test

import (
	"testing"

	"github.com/abemedia/go-cabinet/lzx"
)

func FuzzDecoder(f *testing.F) {
	for _, window := range []int{lzx.MinWindow, lzx.MaxWindow} {
		for _, frame := range encode(f, words(frameSize+100, 5), window) {
			f.Add(uint8(window-lzx.MinWindow), frame)
		}
	}

	f.Fuzz(func(t *testing.T, w uint8, data []byte) {
		d, err := lzx.NewDecoder(lzx.MinWindow + int(w)%(lzx.MaxWindow-lzx.MinWindow+1))
		if err != nil {
			t.Fatal(err)
		}
		dst := make([]byte, frameSize)
		_, _ = d.Decode(dst, data)
		_, _ = d.Decode(dst, data)
	})
}
