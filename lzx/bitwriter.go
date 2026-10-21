package lzx

import "encoding/binary"

// bitWriter appends bits, most significant first, to buf as 16-bit
// little-endian words.
type bitWriter struct {
	buf  []byte
	bits uint64 // pending bits, in the low n bits
	n    uint
}

// writeBits writes the low n bits of v. n must not exceed 32.
func (w *bitWriter) writeBits(v uint32, n uint) {
	w.bits = w.bits<<n | uint64(v)
	w.n += n
	if w.n >= 32 {
		w.n -= 32
		v := uint32(w.bits >> w.n)
		w.buf = binary.LittleEndian.AppendUint32(w.buf, v>>16|v<<16)
	}
}

// flush pads the stream with zero bits to a word boundary and writes the
// pending bits to buf.
func (w *bitWriter) flush() {
	w.writeBits(0, -w.n&15)
	for w.n > 0 {
		w.n -= 16
		w.buf = binary.LittleEndian.AppendUint16(w.buf, uint16(w.bits>>w.n))
	}
}
