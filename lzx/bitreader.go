package lzx

import (
	"encoding/binary"
	"errors"
	"io"
)

var errCorrupt = errors.New("lzx: corrupt input")

// bitReader reads bits, most significant first, from the 16-bit little-endian
// words of a frame's compressed data.
type bitReader struct {
	src  []byte
	pos  int    // next byte of src to load
	bits uint64 // buffered bits, aligned to the most significant end
	n    uint   // number of buffered bits
}

// fill buffers at least n bits if available. n must not exceed 32.
func (b *bitReader) fill(n uint) {
	for b.n < n && b.pos+1 < len(b.src) {
		b.bits |= uint64(binary.LittleEndian.Uint16(b.src[b.pos:])) << (48 - b.n)
		b.n += 16
		b.pos += 2
	}
}

func (b *bitReader) consume(n uint) error {
	if n > b.n {
		return io.ErrUnexpectedEOF
	}
	b.bits <<= n
	b.n -= n
	return nil
}

func (b *bitReader) readBits(n uint) (uint32, error) {
	b.fill(n)
	v := uint32(b.bits >> (64 - n))
	return v, b.consume(n)
}

// skipPadding moves to the bytes of an uncompressed block, which start on the
// next word boundary. The format requires a whole word to be skipped if
// already on a boundary. Words buffered beyond the padding are unread.
func (b *bitReader) skipPadding() error {
	pad := b.n % 16
	if pad == 0 {
		pad = 16
	}
	pos := b.pos - (int(b.n)-int(pad))/8
	if pos > len(b.src) {
		return io.ErrUnexpectedEOF
	}
	*b = bitReader{src: b.src, pos: pos}
	return nil
}

// readFull reads bytes directly from the frame. No bits may be buffered.
func (b *bitReader) readFull(p []byte) error {
	if len(b.src)-b.pos < len(p) {
		return io.ErrUnexpectedEOF
	}
	b.pos += copy(p, b.src[b.pos:])
	return nil
}

// skipByte skips a byte and reports whether one was available.
func (b *bitReader) skipByte() bool {
	if b.pos == len(b.src) {
		return false
	}
	b.pos++
	return true
}

const tableBits = 10

// huffmanDecoder looks up codewords of up to tableBits bits in a table, and
// finds longer ones by comparing them with the codeword range of each length.
type huffmanDecoder struct {
	table  [1 << tableBits]uint16 // symbol<<5 | length, or 0 for longer codewords
	count  [maxCodeLen + 1]uint32
	first  [maxCodeLen + 1]uint32 // first codeword of each length
	index  [maxCodeLen + 1]uint32 // position in syms of the first symbol of each length
	syms   [maxMainSyms]uint16    // symbols ordered by codeword
	maxLen int
}

// init rejects an incomplete code. An empty code is allowed, since a block
// with no long matches may send an empty length tree.
func (h *huffmanDecoder) init(lens []uint8) error {
	h.count = [maxCodeLen + 1]uint32{}
	for _, l := range lens {
		h.count[l]++
	}
	h.count[0] = 0

	left, code, idx := 1, uint32(0), uint32(0)
	h.maxLen = 0
	for l := 1; l <= maxCodeLen; l++ {
		left = left<<1 - int(h.count[l])
		if left < 0 {
			return errCorrupt
		}
		h.first[l], h.index[l] = code, idx
		code = (code + h.count[l]) << 1
		idx += h.count[l]
		if h.count[l] > 0 {
			h.maxLen = l
		}
	}
	if h.maxLen > 0 && left > 0 {
		return errCorrupt
	}

	next := h.index
	for sym, l := range lens {
		if l > 0 {
			h.syms[next[l]] = uint16(sym)
			next[l]++
		}
	}

	h.table = [1 << tableBits]uint16{}
	for l := 1; l <= min(h.maxLen, tableBits); l++ {
		shift := tableBits - l
		for k := range h.count[l] {
			entry := h.syms[h.index[l]+k]<<5 | uint16(l)
			c := (h.first[l] + k) << shift
			for i := range uint32(1) << shift {
				h.table[c+i] = entry
			}
		}
	}
	return nil
}

func (b *bitReader) decode(h *huffmanDecoder) (int, error) {
	if h.maxLen == 0 {
		return 0, errCorrupt
	}
	b.fill(maxCodeLen)
	v := uint32(b.bits >> (64 - maxCodeLen))
	if e := h.table[v>>(maxCodeLen-tableBits)]; e != 0 {
		return int(e >> 5), b.consume(uint(e & 31))
	}
	for l := tableBits + 1; l <= h.maxLen; l++ {
		c := v>>(maxCodeLen-l) - h.first[l]
		if c < h.count[l] {
			return int(h.syms[h.index[l]+c]), b.consume(uint(l))
		}
	}
	return 0, errCorrupt
}
