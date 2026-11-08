package lzx //nolint:testpackage

import (
	"fmt"
	"testing"
)

func TestHuffmanBuilder(t *testing.T) {
	trees := []struct {
		name   string
		n      int
		maxLen int
	}{
		{"pretree", numPretreeSymbols, maxPretreeLen},
		{"length", numSecondaryLens, maxCodeLen},
		{"main", maxMainSymbols, maxCodeLen},
	}
	tests := []struct {
		name  string
		freqs func(n int) []uint32
	}{
		{"none", func(n int) []uint32 { return make([]uint32, n) }},
		{"one", func(n int) []uint32 {
			freqs := make([]uint32, n)
			freqs[n/2] = 1
			return freqs
		}},
		{"fibonacci", fibonacci},
	}

	for _, tree := range trees {
		for _, test := range tests {
			t.Run(fmt.Sprintf("%s/%s", tree.name, test.name), func(t *testing.T) {
				freqs := test.freqs(tree.n)
				lens := make([]uint8, tree.n)
				var hb huffmanBuilder
				hb.build(lens, freqs, tree.maxLen)

				used, kraft := 0, 0
				for s, l := range lens {
					if int(l) > tree.maxLen {
						t.Errorf("symbol %d: length %d, want at most %d", s, l, tree.maxLen)
						continue
					}
					if freqs[s] != 0 && l == 0 {
						t.Errorf("symbol %d: frequency %d but no codeword", s, freqs[s])
					}
					if l != 0 {
						used++
						kraft += 1 << (tree.maxLen - int(l))
					}
				}
				if used < 2 {
					t.Errorf("got %d codewords, want at least 2", used)
				}
				if kraft != 1<<tree.maxLen {
					t.Errorf("Kraft sum %d, want %d", kraft, 1<<tree.maxLen)
				}
				var h huffmanDecoder
				if !h.init(lens) {
					t.Error("huffmanDecoder.init rejected the code")
				}
			})
		}
	}
}

// fibonacci returns n frequencies whose first min(21, n) are the Fibonacci
// numbers from 1, which give a code deeper than any tree allows.
func fibonacci(n int) []uint32 {
	freqs := make([]uint32, n)
	a, b := uint32(1), uint32(1)
	for i := range min(21, n) {
		freqs[i] = a
		a, b = b, a+b
	}
	return freqs
}
