package lzx

import (
	"encoding/binary"
	"math"
)

const (
	maxPretreeLen = 1<<pretreeLenBits - 1
	maxAlignedLen = 1<<alignedLenBits - 1

	pretreeValueShift = 5
	pretreeSymbolMask = 1<<pretreeValueShift - 1

	// pretreePasses is the number of times build chooses a tree's codes.
	pretreePasses = 2
)

// pretreeExtraBits[s] is the number of extra bits that follow pretree symbol s.
var pretreeExtraBits = [numPretreeSymbols]uint8{17: 4, 18: 5, 19: 1}

// blockWriter writes each frame as a block, given the tokens that code its
// data. It keeps the code of the previous verbatim or aligned offset block, as
// the path lengths of each block are coded as differences from it.
type blockWriter struct {
	numMainSymbols int
	counts         symbolCounts
	code           blockCode
	prev           blockCode
	coded          bool // a verbatim or aligned offset block has been written
	trees          [3]pretree
	hb             huffmanBuilder
}

// newBlockWriter returns a block writer for a main tree of numMainSymbols
// symbols.
func newBlockWriter(numMainSymbols int) blockWriter {
	b := blockWriter{numMainSymbols: numMainSymbols}
	for i := range b.trees {
		b.trees[i] = newPretree()
	}
	return b
}

// reset forgets the previous block, as at the start of a stream.
func (b *blockWriter) reset() {
	b.prev = blockCode{}
	b.coded = false
}

// previous returns the code of the previous verbatim or aligned offset block,
// or nil if there is none.
func (b *blockWriter) previous() *blockCode {
	if !b.coded {
		return nil
	}
	return &b.prev
}

// build sets the path lengths of b.code to those of Huffman codes for the
// symbols of tokens, and chooses whether they make an aligned offset block. It
// returns b.code.
func (b *blockWriter) build(tokens []token, data []byte) *blockCode {
	s, c := &b.counts, &b.code
	s.count(tokens, data)
	b.hb.build(c.mainLens[:b.numMainSymbols], s.main[:b.numMainSymbols], maxCodeLen)
	b.hb.build(c.lenLens[:], s.length[:], maxCodeLen)
	b.hb.build(c.alignedLens[:], s.aligned[:], maxAlignedLen)
	c.aligned = c.symbolBits(s, true) < c.symbolBits(s, false)
	return c
}

// write writes tokens as a block with the path lengths that build last set, or
// data as an uncompressed block if that is smaller. rep holds the repeated
// offsets before the block. It reports whether it wrote a verbatim or aligned
// offset block.
func (b *blockWriter) write(w *bitWriter, tokens []token, data []byte, rep repeatedOffsets) bool {
	c, prev := &b.code, &b.prev
	treeBits := b.trees[0].build(&b.hb, c.mainLens[:numChars], prev.mainLens[:numChars]) +
		b.trees[1].build(&b.hb, c.mainLens[numChars:b.numMainSymbols], prev.mainLens[numChars:b.numMainSymbols]) +
		b.trees[2].build(&b.hb, c.lenLens[:], prev.lenLens[:])
	start := w.n + blockTypeBits + blockSizeBits
	if storedSize(start, len(data)) < wordBytes(start+uint(treeBits+c.symbolBits(&b.counts, c.aligned))) {
		writeUncompressed(w, data, rep)
		return false
	}

	canonicalCodes(c.mainCodes[:b.numMainSymbols], c.mainLens[:b.numMainSymbols])
	canonicalCodes(c.lenCodes[:], c.lenLens[:])
	canonicalCodes(c.alignedCodes[:], c.alignedLens[:])
	typ := blockVerbatim
	if c.aligned {
		typ = blockAligned
	}
	w.writeBits(uint32(typ), blockTypeBits)
	w.writeBits(uint32(len(data)), blockSizeBits)
	if c.aligned {
		for _, l := range c.alignedLens {
			w.writeBits(uint32(l), alignedLenBits)
		}
	}
	for i := range b.trees {
		b.trees[i].write(w)
	}
	c.writeTokens(w, tokens, data)

	b.prev = b.code
	b.coded = true
	return true
}

// wordBytes returns the size in bytes of n bits padded to a 16-bit boundary.
func wordBytes(n uint) uint {
	return (n + 15) / 16 * 2
}

// storedSize returns the size in bytes of a frame that holds n bytes as an
// uncompressed block whose header ends after start bits.
func storedSize(start uint, n int) uint {
	// The data starts on the next 16-bit boundary, after at least 1 bit of
	// padding.
	return wordBytes(start+1) + 4*numRepeatedOffsets + uint(n+n%2)
}

// writeUncompressed writes data as an uncompressed block, which sets the
// repeated offsets to rep.
func writeUncompressed(w *bitWriter, data []byte, rep repeatedOffsets) {
	w.writeBits(blockUncompressed, blockTypeBits)
	w.writeBits(uint32(len(data)), blockSizeBits)
	w.writeBits(0, 16-w.n%16)
	w.flush()
	for _, r := range rep {
		w.buf = binary.LittleEndian.AppendUint32(w.buf, r)
	}
	w.buf = append(w.buf, data...)
	if len(data)%2 == 1 {
		w.buf = append(w.buf, 0)
	}
}

// symbolCounts counts the symbols that code a sequence of tokens.
type symbolCounts struct {
	main       [maxMainSymbols]uint32
	length     [numSecondaryLens]uint32
	aligned    [numAlignedSymbols]uint32
	footerBits int // position footer bits in a verbatim block
}

// count counts the symbols of tokens, taking literals from data.
func (s *symbolCounts) count(tokens []token, data []byte) {
	*s = symbolCounts{}
	pos := 0
	for _, t := range tokens {
		l := t.length()
		if l == 1 {
			s.main[data[pos]]++
			pos++
			continue
		}
		sym, slot := t.mainSymbol()
		s.main[sym]++
		if l >= minSecondaryLen {
			s.length[l-minSecondaryLen]++
		}
		n := int(extraBits[slot])
		if n >= numAlignedBits {
			s.aligned[t.formatted()%numAlignedSymbols]++
		}
		s.footerBits += n
		pos += l
	}
}

// blockCode holds the Huffman codes of a verbatim or aligned offset block.
type blockCode struct {
	mainLens     [maxMainSymbols]uint8
	mainCodes    [maxMainSymbols]uint16
	lenLens      [numSecondaryLens]uint8
	lenCodes     [numSecondaryLens]uint16
	alignedLens  [numAlignedSymbols]uint8
	alignedCodes [numAlignedSymbols]uint16
	aligned      bool // the block is an aligned offset block
}

// symbolBits returns the size in bits of the symbols that s counts, coded in
// an aligned offset block if aligned is set, including its aligned offset
// tree.
func (c *blockCode) symbolBits(s *symbolCounts, aligned bool) int {
	n := 0
	for i, f := range s.main {
		n += int(f) * int(c.mainLens[i])
	}
	for i, f := range s.length {
		n += int(f) * int(c.lenLens[i])
	}
	n += s.footerBits
	if aligned {
		n += numAlignedSymbols * alignedLenBits
		for i, f := range s.aligned {
			n += int(f) * (int(c.alignedLens[i]) - numAlignedBits)
		}
	}
	return n
}

// writeTokens writes the symbols of tokens, taking literals from data.
func (c *blockCode) writeTokens(w *bitWriter, tokens []token, data []byte) {
	pos := 0
	for _, t := range tokens {
		l := t.length()
		if l == 1 {
			b := data[pos]
			w.writeBits(uint32(c.mainCodes[b]), uint(c.mainLens[b]))
			pos++
			continue
		}
		sym, slot := t.mainSymbol()
		w.writeBits(uint32(c.mainCodes[sym]), uint(c.mainLens[sym]))
		if l >= minSecondaryLen {
			lf := l - minSecondaryLen
			w.writeBits(uint32(c.lenCodes[lf]), uint(c.lenLens[lf]))
		}
		n := uint(extraBits[slot])
		footer := t.formatted() - positionBase[slot]
		if c.aligned && n >= numAlignedBits {
			a := footer % numAlignedSymbols
			w.writeBits(footer>>numAlignedBits, n-numAlignedBits)
			w.writeBits(uint32(c.alignedCodes[a]), uint(c.alignedLens[a]))
		} else {
			w.writeBits(footer, n)
		}
		pos += l
	}
}

// pretree codes the path lengths of a tree as differences from the previous
// tree's path lengths.
type pretree struct {
	codes     []uint16 // pretree symbols, with their extra bits above bit 5
	best      []uint16
	lens      [numPretreeSymbols]uint8
	codewords [numPretreeSymbols]uint16

	// cost[i] is the least cost of coding a tree's path lengths from index i
	// to the end, and choice[i] is the pretree symbol that starts that
	// coding, with the number of path lengths it codes above bit 5.
	cost   []uint32
	choice []uint16
}

// newPretree returns a pretree for trees of up to maxMainSymbols symbols.
func newPretree() pretree {
	return pretree{
		codes:  make([]uint16, 0, maxMainSymbols),
		best:   make([]uint16, 0, maxMainSymbols),
		cost:   make([]uint32, maxMainSymbols+1),
		choice: make([]uint16, maxMainSymbols),
	}
}

// build sets t to code the change of path lengths from prev to lens, and
// returns its size in bits. It chooses the pretree codes that cost least
// under the code lengths of the pretree that its previous choice would give.
func (t *pretree) build(hb *huffmanBuilder, lens, prev []uint8) int {
	var costs [numPretreeSymbols]uint32
	for i := range costs {
		costs[i] = 4 + uint32(pretreeExtraBits[i])
	}
	bestBits := math.MaxInt
	var bestLens [numPretreeSymbols]uint8
	for range pretreePasses {
		t.choose(lens, prev, &costs)
		var freqs [numPretreeSymbols]uint32
		for _, c := range t.codes {
			freqs[c&pretreeSymbolMask]++
		}
		hb.build(t.lens[:], freqs[:], maxPretreeLen)
		n := numPretreeSymbols * pretreeLenBits
		for i, f := range freqs {
			n += int(f) * int(t.lens[i]+pretreeExtraBits[i])
		}
		if n < bestBits {
			bestBits, bestLens = n, t.lens
			t.best = append(t.best[:0], t.codes...)
		}
		for i, l := range t.lens {
			costs[i] = uint32(l + pretreeExtraBits[i])
			if l == 0 {
				costs[i] = maxPretreeLen
			}
		}
	}
	t.codes, t.best = t.best, t.codes
	t.lens = bestLens
	canonicalCodes(t.codewords[:], t.lens[:])
	return bestBits
}

// choose sets t.codes to the cheapest pretree codes that change the path
// lengths prev to lens, given the cost of each pretree symbol and its extra
// bits.
func (t *pretree) choose(lens, prev []uint8, costs *[numPretreeSymbols]uint32) {
	n := len(lens)
	cost := t.cost[:n+1]
	choice := t.choice[:n]
	cost[n] = 0
	same := 0 // the length of the run of equal path lengths that starts at i
	for i := n - 1; i >= 0; i-- {
		if i+1 < n && lens[i+1] == lens[i] {
			same++
		} else {
			same = 1
		}
		// The code that changes prev[i] to lens[i], as lengthDelta is its own
		// inverse.
		d := uint16(lengthDelta(prev[i], lens[i]))
		best, ch := cost[i+1]+costs[d], d|1<<pretreeValueShift
		best, ch = cheapestRun(cost[i:], 19, costs[19]+costs[d], 4, min(same, 5), best, ch)
		if lens[i] == 0 {
			best, ch = cheapestRun(cost[i:], 17, costs[17], 4, min(same, 19), best, ch)
			best, ch = cheapestRun(cost[i:], 18, costs[18], 20, min(same, 51), best, ch)
		}
		cost[i], choice[i] = best, ch
	}

	codes := t.codes[:0]
	for i := 0; i < n; {
		sym, r := choice[i]&pretreeSymbolMask, int(choice[i]>>pretreeValueShift)
		switch sym {
		case 17:
			codes = append(codes, 17|uint16(r-4)<<pretreeValueShift)
		case 18:
			codes = append(codes, 18|uint16(r-20)<<pretreeValueShift)
		case 19:
			codes = append(codes, 19|uint16(r-4)<<pretreeValueShift, uint16(lengthDelta(prev[i], lens[i])))
		default:
			codes = append(codes, sym)
		}
		i += r
	}
	t.codes = codes
}

// cheapestRun compares the coding that costs best and starts with choice
// against coding a run of lo to hi path lengths with the pretree symbol sym,
// which costs c, and returns the cost and first choice of the cheapest.
// cost[r] is the cost of coding the path lengths after a run of r.
func cheapestRun(cost []uint32, sym uint16, c uint32, lo, hi int, best uint32, choice uint16) (uint32, uint16) {
	for r := lo; r <= hi; r++ {
		if c+cost[r] < best {
			best, choice = c+cost[r], sym|uint16(r)<<pretreeValueShift
		}
	}
	return best, choice
}

// write writes the path lengths of the pretree and then the pretree codes.
func (t *pretree) write(w *bitWriter) {
	for _, l := range t.lens {
		w.writeBits(uint32(l), pretreeLenBits)
	}
	for _, c := range t.codes {
		sym := c & pretreeSymbolMask
		w.writeBits(uint32(t.codewords[sym]), uint(t.lens[sym]))
		w.writeBits(uint32(c>>pretreeValueShift), uint(pretreeExtraBits[sym]))
	}
}
