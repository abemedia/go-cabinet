package lzx

import (
	"encoding/binary"
	"slices"
)

type codeSet struct {
	mainLens     [maxMainSyms]uint8
	mainWords    [maxMainSyms]uint32
	lenLens      [numLenSyms]uint8
	lenWords     [numLenSyms]uint32
	alignedLens  [numAlignedSyms]uint8
	alignedWords [numAlignedSyms]uint32
}

func (c *compressor) buildCodes(codes *codeSet) {
	n := c.numMainSyms
	c.builder.build(c.freqs.main[:n], mainCodeLimit, codes.mainLens[:n], codes.mainWords[:n])
	c.builder.build(c.freqs.length[:], lengthCodeLimit, codes.lenLens[:], codes.lenWords[:])
	c.builder.build(c.freqs.aligned[:], alignedCodeLimit, codes.alignedLens[:], codes.alignedWords[:])
}

func blockType(freqs *frequencies, codes *codeSet) uint32 {
	verbatimCost := uint32(0)
	alignedCost := uint32(numAlignedSyms * 3)
	for i, f := range freqs.aligned {
		verbatimCost += numAlignedBits * f
		alignedCost += uint32(codes.alignedLens[i]) * f
	}
	if alignedCost < verbatimCost {
		return blockAligned
	}
	return blockVerbatim
}

func (c *compressor) writeBlock(size int) {
	codes, prev := &c.codes[c.cur], &c.codes[c.cur^1]
	c.buildCodes(codes)
	typ := blockType(&c.freqs, codes)

	c.bw.writeBits(typ, 3)
	c.bw.writeBits(uint32(size)>>8, 16)
	c.bw.writeBits(uint32(size), 8)
	if typ == blockAligned {
		for _, l := range codes.alignedLens {
			c.bw.writeBits(uint32(l), 3)
		}
	}
	n := c.numMainSyms
	c.writeLengths(codes.mainLens[:numChars], prev.mainLens[:numChars])
	c.writeLengths(codes.mainLens[numChars:n], prev.mainLens[numChars:n])
	c.writeLengths(codes.lenLens[:], prev.lenLens[:])
	c.writeItems(typ, codes)
	c.cur ^= 1
}

func (c *compressor) writeItems(typ uint32, codes *codeSet) {
	minAligned := maxOffsetSlots
	if typ == blockAligned {
		minAligned = minAlignedSlot
	}
	for _, it := range slices.Backward(c.items) {
		if it.length == 0 {
			c.bw.writeBits(codes.mainWords[it.offset], uint(codes.mainLens[it.offset]))
			continue
		}

		mainSym := mainSymbol(it)
		c.bw.writeBits(codes.mainWords[mainSym], uint(codes.mainLens[mainSym]))
		if it.length >= minSecondaryLen {
			sym := it.length - minSecondaryLen
			c.bw.writeBits(codes.lenWords[sym], uint(codes.lenLens[sym]))
		}
		slot := (mainSym - numChars) / numLenHeaders
		n := uint(extraBits[slot])
		extra := it.offset - positionBase[slot]
		if slot >= minAligned {
			c.bw.writeBits(extra>>numAlignedBits, n-numAlignedBits)
			sym := extra % numAlignedSyms
			c.bw.writeBits(codes.alignedWords[sym], uint(codes.alignedLens[sym]))
		} else {
			c.bw.writeBits(extra, n)
		}
	}
}

// A precodeItem is a length delta (0-16), a run of zero lengths (17 and 18),
// or a run of a repeated length (19), which is followed by its delta.
type precodeItem struct {
	sym   uint8
	extra uint8
	delta uint8
}

func (c *compressor) writeLengths(lens, prev []uint8) {
	var freqs [numPretreeSyms]uint32
	c.precode = precodeItems(c.precode[:0], lens, prev)
	for _, it := range c.precode {
		freqs[it.sym]++
		if it.sym == 19 {
			freqs[it.delta]++
		}
	}

	var plens [numPretreeSyms]uint8
	var pwords [numPretreeSyms]uint32
	c.builder.build(freqs[:], precodeLimit, plens[:], pwords[:])
	for _, l := range plens {
		c.bw.writeBits(uint32(l), 4)
	}
	for _, it := range c.precode {
		c.bw.writeBits(pwords[it.sym], uint(plens[it.sym]))
		switch it.sym {
		case 17:
			c.bw.writeBits(uint32(it.extra), 4)
		case 18:
			c.bw.writeBits(uint32(it.extra), 5)
		case 19:
			c.bw.writeBits(uint32(it.extra), 1)
			c.bw.writeBits(pwords[it.delta], uint(plens[it.delta]))
		}
	}
}

func precodeItems(items []precodeItem, lens, prev []uint8) []precodeItem {
	for i := 0; i < len(lens); {
		l := lens[i]
		end := i + 1
		for end < len(lens) && lens[end] == l {
			end++
		}

		if l == 0 {
			for end-i >= 20 {
				extra := min(end-i-20, 31)
				items = append(items, precodeItem{sym: 18, extra: uint8(extra)})
				i += 20 + extra
			}
			if end-i >= 4 {
				extra := min(end-i-4, 15)
				items = append(items, precodeItem{sym: 17, extra: uint8(extra)})
				i += 4 + extra
			}
		} else {
			for end-i >= 4 {
				extra := min(end-i-4, 1)
				items = append(items, precodeItem{sym: 19, extra: uint8(extra), delta: lengthDelta(prev[i], l)})
				i += 4 + extra
			}
		}

		for ; i < end; i++ {
			items = append(items, precodeItem{sym: lengthDelta(prev[i], l)})
		}
	}
	return items
}

func lengthDelta(prev, cur uint8) uint8 {
	return (prev + 17 - cur) % 17
}

type bitWriter struct {
	buf []byte
	acc uint64
	n   uint
}

func (w *bitWriter) writeBits(v uint32, n uint) {
	w.acc = w.acc<<n | uint64(v)&(1<<n-1)
	w.n += n
	for w.n >= 16 {
		w.n -= 16
		w.buf = binary.LittleEndian.AppendUint16(w.buf, uint16(w.acc>>w.n))
	}
}

func (w *bitWriter) align() {
	if w.n > 0 {
		w.writeBits(0, 16-w.n)
	}
}
