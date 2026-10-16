package lzx

import (
	"cmp"
	"slices"
)

// codeBuilder holds scratch space so that building a code does not allocate.
type codeBuilder struct {
	syms   [maxMainSyms]uint16
	weight [2 * maxMainSyms]uint32
	parent [2 * maxMainSyms]uint16
	depth  [2 * maxMainSyms]uint16
}

// build builds a canonical Huffman code with codewords of at most maxLen bits.
// A code with a single used symbol is given a second codeword, as decoders
// reject incomplete codes.
func (b *codeBuilder) build(freqs []uint32, maxLen int, lens []uint8, codewords []uint32) {
	n := 0
	for sym, f := range freqs {
		lens[sym] = 0
		if f > 0 {
			b.syms[n] = uint16(sym)
			n++
		}
	}
	syms := b.syms[:n]
	slices.SortFunc(syms, func(x, y uint16) int {
		return cmp.Or(cmp.Compare(freqs[x], freqs[y]), cmp.Compare(x, y))
	})

	switch n {
	case 0:
		return
	case 1:
		lens[syms[0]] = 1
		if syms[0] == 0 {
			lens[1] = 1
		} else {
			lens[0] = 1
		}
	default:
		b.assignLengths(freqs, syms, maxLen, lens)
	}
	canonicalCodewords(lens, codewords)
}

// assignLengths derives codeword lengths for syms, which are sorted by
// increasing frequency, from the depths of the internal nodes of their Huffman
// tree. To limit lengths to maxLen while keeping the code complete, an internal
// node that would sit at or below maxLen splits one of the deepest codewords
// shorter than maxLen instead.
func (b *codeBuilder) assignLengths(freqs []uint32, syms []uint16, maxLen int, lens []uint8) {
	n := len(syms)
	root := 2*n - 2
	for i, sym := range syms {
		b.weight[i] = freqs[sym]
	}

	leaf, node := 0, n
	for next := n; next <= root; next++ {
		var sum uint32
		for range 2 {
			if leaf < n && (node == next || b.weight[leaf] <= b.weight[node]) {
				sum += b.weight[leaf]
				leaf++
			} else {
				sum += b.weight[node]
				b.parent[node] = uint16(next)
				node++
			}
		}
		b.weight[next] = sum
	}

	var counts [maxCodeLen + 1]int
	counts[1] = 2
	b.depth[root] = 0
	for node := root - 1; node >= n; node-- {
		d := b.depth[b.parent[node]] + 1
		b.depth[node] = d
		l := int(d)
		if l >= maxLen {
			l = maxLen - 1
			for counts[l] == 0 {
				l--
			}
		}
		counts[l]--
		counts[l+1] += 2
	}

	i := 0
	for l := maxLen; l > 0; l-- {
		for range counts[l] {
			lens[syms[i]] = uint8(l)
			i++
		}
	}
}

func canonicalCodewords(lens []uint8, codewords []uint32) {
	var count [maxCodeLen + 1]uint32
	for _, l := range lens {
		count[l]++
	}
	var next [maxCodeLen + 1]uint32
	for l := 2; l <= maxCodeLen; l++ {
		next[l] = (next[l-1] + count[l-1]) << 1
	}
	for sym, l := range lens {
		if l > 0 {
			codewords[sym] = next[l]
			next[l]++
		}
	}
}
