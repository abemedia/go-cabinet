package lzx

import "slices"

// huffmanBuilder computes the path lengths of length-limited Huffman codes.
type huffmanBuilder struct {
	symbols [maxMainSymbols]uint64 // frequency<<16 | symbol
	weights [maxMainSymbols]uint32
}

// build sets lens to the path lengths of a Huffman code for the symbol
// frequencies freqs, with no path longer than maxLen. A code has at least two
// codewords, so if fewer than two symbols occur, unused ones make up the rest.
func (b *huffmanBuilder) build(lens []uint8, freqs []uint32, maxLen int) {
	syms := b.symbols[:0]
	for s, f := range freqs {
		if f != 0 {
			syms = append(syms, uint64(f)<<16|uint64(s))
		}
	}
	for s := 0; len(syms) < 2; s++ {
		if freqs[s] == 0 {
			syms = append(syms, uint64(s))
		}
	}
	slices.Sort(syms)

	w := b.weights[:len(syms)]
	for i, s := range syms {
		w[i] = uint32(s >> 16)
	}
	pathLengths(w)

	var count [64]int
	for _, l := range w {
		count[l]++
	}
	for l := int(w[0]); l > maxLen; l-- {
		// Replace two leaves at depth l by their parent, and the deepest leaf
		// above depth l-1 by a node with two leaves, one of them moved up.
		for count[l] > 0 {
			j := l - 2
			for count[j] == 0 {
				j--
			}
			count[l] -= 2
			count[l-1]++
			count[j+1] += 2
			count[j]--
		}
	}

	clear(lens)
	i := 0
	for l := min(int(w[0]), maxLen); l > 0; l-- {
		for range count[l] {
			lens[syms[i]&0xffff] = uint8(l)
			i++
		}
	}
}

// pathLengths replaces the weights w, sorted in increasing order, by the path
// lengths of a minimum-redundancy code for them. It uses the in-place
// algorithm of Moffat and Katajainen. The first phase builds the tree from the
// leaves in w and the internal nodes it puts in their place, keeping the index
// of each node's parent; the second turns those into depths, and the third
// counts the leaves at each depth.
func pathLengths(w []uint32) {
	n := len(w)
	w[0] += w[1]
	root, leaf := 0, 2
	for next := 1; next < n-1; next++ {
		if leaf >= n || w[root] < w[leaf] {
			w[next] = w[root]
			w[root] = uint32(next)
			root++
		} else {
			w[next] = w[leaf]
			leaf++
		}
		if leaf >= n || root < next && w[root] < w[leaf] {
			w[next] += w[root]
			w[root] = uint32(next)
			root++
		} else {
			w[next] += w[leaf]
			leaf++
		}
	}

	w[n-2] = 0
	for next := n - 3; next >= 0; next-- {
		w[next] = w[w[next]] + 1
	}

	avail, depth := 1, uint32(0)
	root, next := n-2, n-1
	for avail > 0 {
		used := 0
		for root >= 0 && w[root] == depth {
			used++
			root--
		}
		for ; avail > used; avail-- {
			w[next] = depth
			next--
		}
		avail, depth = 2*used, depth+1
	}
}

// canonicalCodes sets codes to the codewords of the canonical Huffman code
// with the path lengths lens.
func canonicalCodes(codes []uint16, lens []uint8) {
	var count, next [maxCodeLen + 1]uint32
	for _, l := range lens {
		count[l]++
	}
	count[0] = 0
	code := uint32(0)
	for l := 1; l <= maxCodeLen; l++ {
		code = (code + count[l-1]) << 1
		next[l] = code
	}
	for s, l := range lens {
		if l != 0 {
			codes[s] = uint16(next[l])
			next[l]++
		}
	}
}
