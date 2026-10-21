package lzx

import (
	"encoding/binary"
	"io"
)

// bitReader reads bits, most significant first, from the 16-bit little-endian
// words of a frame's compressed data. Reads past the end of the data yield
// zero bits and leave n negative.
type bitReader struct {
	src  []byte
	pos  int    // next byte of src to load
	bits uint64 // buffered bits, aligned to the most significant end
	n    int    // number of buffered bits, negative once read past the end
}

// fill buffers at least n bits if available. n must not exceed 32.
func (b *bitReader) fill(n uint) {
	for b.n < int(n) && b.pos+1 < len(b.src) {
		b.bits |= uint64(binary.LittleEndian.Uint16(b.src[b.pos:])) << uint(48-b.n)
		b.n += 16
		b.pos += 2
	}
}

func (b *bitReader) consume(n uint) {
	b.bits <<= n
	b.n -= int(n)
}

func (b *bitReader) readBits(n uint) uint32 {
	b.fill(n)
	v := uint32(b.bits >> (64 - n))
	b.bits <<= n
	b.n -= int(n)
	return v
}

// err returns io.ErrUnexpectedEOF if a read went past the end of the data.
func (b *bitReader) err() error {
	if b.n < 0 {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// skipPadding moves to the bytes of an uncompressed block, which start on the
// next word boundary. The format requires a whole word to be skipped if
// already on a boundary. Words buffered beyond the padding are unread.
func (b *bitReader) skipPadding() {
	if b.n < 0 {
		return
	}
	pad := b.n % 16
	if pad == 0 {
		pad = 16
	}
	pos := b.pos - (b.n-pad)/8
	if pos > len(b.src) {
		b.n -= 16
		return
	}
	*b = bitReader{src: b.src, pos: pos}
}

// readFull reads bytes directly from the frame. No bits may be buffered.
func (b *bitReader) readFull(p []byte) {
	n := copy(p, b.src[b.pos:])
	b.pos += n
	b.n -= 8 * (len(p) - n)
}

// skipByte skips a byte and reports whether one was available.
func (b *bitReader) skipByte() bool {
	if b.pos == len(b.src) {
		return false
	}
	b.pos++
	return true
}
