package lzx

import (
	"encoding/binary"
	"errors"
)

var errCorrupt = errors.New("lzx: corrupt input")

// Decoder decompresses LZX data one frame at a time.
type Decoder struct {
	br             bitReader
	window         []byte
	numMainSymbols int

	pos        int   // current position in the window
	frameStart int   // window position of the current frame
	total      int64 // uncompressed bytes before the current frame
	e8FileSize int32
	final      bool // a frame shorter than 32 KiB has been decoded

	blockType int
	blockSize int
	remaining int  // bytes left in the current block
	padding   bool // the padding byte of an uncompressed block is in the next frame
	repeated  repeatedOffsets

	mainLens [maxMainSymbols]uint8
	lenLens  [numSecondaryLens]uint8
	main     huffmanDecoder
	length   huffmanDecoder
	aligned  huffmanDecoder
	pretree  huffmanDecoder

	err error
}

// NewDecoder returns a new [Decoder] for data that was compressed with a
// sliding window of 2^window bytes.
func NewDecoder(window int) (*Decoder, error) {
	if window < MinWindow || window > MaxWindow {
		return nil, windowError(window)
	}
	d := &Decoder{
		window:         make([]byte, 1<<window),
		numMainSymbols: numMainSymbols(window),
	}
	d.Reset()
	return d, nil
}

// Reset discards the decoder's state, making it ready to decode a new stream.
func (d *Decoder) Reset() {
	*d = Decoder{window: d.window, numMainSymbols: d.numMainSymbols, repeated: repeatedOffsets{1, 1, 1}}
}

// Decode decompresses the frame src into dst, whose length must be the frame's
// uncompressed size, which is 32 KiB for all frames of a stream but the last.
// It returns an error if src does not hold len(dst) bytes of data.
// After an error, every call returns the same error until Reset is called.
func (d *Decoder) Decode(dst, src []byte) ([]byte, error) {
	if d.err == nil {
		d.err = d.decode(dst, src)
	}
	if d.err != nil {
		return nil, d.err
	}
	return dst, nil
}

func (d *Decoder) decode(dst, src []byte) error {
	if len(dst) == 0 || len(dst) > frameSize || d.final {
		return errFrameSize
	}
	d.final = len(dst) < frameSize
	d.br = bitReader{src: src}
	if d.padding {
		d.padding = !d.br.skipByte()
	}
	if d.total == 0 {
		if err := d.readHeader(); err != nil {
			return err
		}
	}

	d.frameStart = d.pos
	end := d.pos + len(dst)
	for d.pos < end {
		if d.remaining == 0 {
			if err := d.nextBlock(); err != nil {
				return err
			}
		}
		if err := d.decodeRun(min(d.pos+d.remaining, end)); err != nil {
			return err
		}
	}

	copy(dst, d.window[d.frameStart:end])
	if d.e8FileSize != 0 && d.total < e8MaxOffset {
		translateE8(dst, int32(d.total), d.e8FileSize, false)
	}
	d.total += int64(len(dst))
	if d.pos == len(d.window) {
		d.pos = 0
	}
	return nil
}

// corrupt returns the error for data found invalid, which is
// io.ErrUnexpectedEOF if the data ran out first.
func (d *Decoder) corrupt() error {
	if err := d.br.err(); err != nil {
		return err
	}
	return errCorrupt
}

func (d *Decoder) readHeader() error {
	if d.br.readBits(1) != 0 {
		hi := d.br.readBits(16)
		lo := d.br.readBits(16)
		d.e8FileSize = int32(hi<<16 | lo)
	}
	return d.br.err()
}

func (d *Decoder) nextBlock() error {
	typ := d.br.readBits(blockTypeBits)
	size := d.br.readBits(blockSizeBits)
	d.blockType, d.blockSize, d.remaining = int(typ), int(size), int(size)

	switch typ {
	case blockVerbatim:
		return d.readTrees()
	case blockAligned:
		var lens [numAlignedSymbols]uint8
		for i := range lens {
			lens[i] = uint8(d.br.readBits(alignedLenBits))
		}
		if !d.aligned.init(lens[:]) {
			return d.corrupt()
		}
		return d.readTrees()
	case blockUncompressed:
		var b [4 * numRepeatedOffsets]byte
		d.br.skipPadding()
		d.br.readFull(b[:])
		for i := range d.repeated {
			d.repeated[i] = binary.LittleEndian.Uint32(b[4*i:])
		}
		return d.br.err()
	default:
		return d.corrupt()
	}
}

func (d *Decoder) readTrees() error {
	if err := d.readLengths(d.mainLens[:numChars]); err != nil {
		return err
	}
	if err := d.readLengths(d.mainLens[numChars:d.numMainSymbols]); err != nil {
		return err
	}
	if !d.main.init(d.mainLens[:d.numMainSymbols]) || d.remaining > 0 && d.main.maxLen == 0 {
		return d.corrupt()
	}
	if err := d.readLengths(d.lenLens[:]); err != nil {
		return err
	}
	if !d.length.init(d.lenLens[:]) {
		return d.corrupt()
	}
	return nil
}

// readLengths updates lens in place, as they are coded as differences from
// the previous block's lengths.
func (d *Decoder) readLengths(lens []uint8) error {
	var plens [numPretreeSymbols]uint8
	for i := range plens {
		plens[i] = uint8(d.br.readBits(pretreeLenBits))
	}
	if !d.pretree.init(plens[:]) || d.pretree.maxLen == 0 {
		return d.corrupt()
	}

	for i := 0; i < len(lens); {
		run, l, err := d.readLengthRun(d.pretree.decode(&d.br), lens[i])
		if err != nil {
			return err
		}
		if run > len(lens)-i {
			return d.corrupt()
		}
		for range run {
			lens[i] = l
			i++
		}
	}
	return d.br.err()
}

// readLengthRun returns how many path lengths pretree symbol sym codes and
// their value, which sym may code as a change from prev.
func (d *Decoder) readLengthRun(sym int, prev uint8) (int, uint8, error) {
	switch sym {
	case 17:
		return 4 + int(d.br.readBits(4)), 0, nil
	case 18:
		return 20 + int(d.br.readBits(5)), 0, nil
	case 19:
		n := d.br.readBits(1)
		sym = d.pretree.decode(&d.br)
		if sym > 16 {
			return 0, 0, d.corrupt()
		}
		return 4 + int(n), lengthDelta(prev, uint8(sym)), nil
	default:
		return 1, lengthDelta(prev, uint8(sym)), nil
	}
}

// decodeRun decodes the current block up to window position end.
func (d *Decoder) decodeRun(end int) error {
	start := d.pos
	if d.blockType == blockUncompressed {
		d.br.readFull(d.window[d.pos:end])
		d.pos = end
		d.remaining -= end - start
		// An uncompressed block of odd length is followed by a padding byte,
		// which is in the next frame if this one ends with the block.
		if d.remaining == 0 && d.blockSize%2 == 1 {
			d.padding = !d.br.skipByte()
		}
		return d.br.err()
	}
	for d.pos < end {
		sym := d.main.decode(&d.br)
		if sym < numChars {
			d.window[d.pos] = byte(sym)
			d.pos++
			continue
		}
		if err := d.copyMatch(sym-numChars, end); err != nil {
			return err
		}
	}
	d.remaining -= d.pos - start
	return d.br.err()
}

// copyMatch reads the match whose main tree symbol is numChars+m and copies it
// into the window, where it must end by position end.
func (d *Decoder) copyMatch(m, end int) error {
	length, offset, err := d.readMatch(m)
	if err != nil {
		return err
	}
	decoded := d.total + int64(d.pos-d.frameStart)
	if length > end-d.pos || offset == 0 || int64(offset) > decoded || int(offset) > len(d.window) {
		return d.corrupt()
	}

	src := d.pos - int(offset)
	if src < 0 {
		src += len(d.window)
	}
	if int(offset) >= length && src+length <= len(d.window) {
		copy(d.window[d.pos:d.pos+length], d.window[src:])
	} else {
		mask := len(d.window) - 1
		for i := range length {
			d.window[d.pos+i] = d.window[(src+i)&mask]
		}
	}
	d.pos += length
	return nil
}

// readMatch returns the length and offset of the match whose main tree symbol
// is numChars+m, and updates the repeated offsets.
func (d *Decoder) readMatch(m int) (int, uint32, error) {
	length := m%numLenHeaders + minMatch
	if length == minSecondaryLen {
		if d.length.maxLen == 0 {
			return 0, 0, d.corrupt()
		}
		length += d.length.decode(&d.br)
	}

	slot := m / numLenHeaders
	if slot < numRepeatedOffsets {
		d.repeated = d.repeated.swap(uint32(slot))
		return length, d.repeated[0], nil
	}

	n := uint(extraBits[slot])
	formatted := positionBase[slot]
	if d.blockType == blockAligned && n >= numAlignedBits {
		if d.aligned.maxLen == 0 {
			return 0, 0, d.corrupt()
		}
		v := d.br.readBits(n - numAlignedBits)
		formatted += v<<numAlignedBits | uint32(d.aligned.decode(&d.br))
	} else {
		formatted += d.br.readBits(n)
	}
	offset := formatted - (numRepeatedOffsets - 1)
	d.repeated = d.repeated.push(offset)
	return length, offset, nil
}

const (
	tableBits         = 10
	huffmanValueShift = 5
	huffmanCountMask  = 1<<huffmanValueShift - 1
)

// huffmanDecoder looks up codewords of up to tableBits bits in a table, and
// finds longer ones by comparing them with the codeword range of each length.
type huffmanDecoder struct {
	// table maps the next tableBits bits of input to
	// symbol<<huffmanValueShift | length for the codeword they start with, or
	// to 0 if that codeword is longer.
	table   [1 << tableBits]uint16
	count   [maxCodeLen + 1]uint32
	first   [maxCodeLen + 1]uint32 // first codeword of each length
	index   [maxCodeLen + 1]uint32 // start of each length's run in symbols
	symbols [maxMainSymbols]uint16 // symbols ordered by codeword
	maxLen  int
}

// init sets h to decode the canonical code with the path lengths lens, and
// reports whether the code is valid. An over-subscribed or incomplete code is
// invalid. An empty code is allowed, since a block with no long matches may
// send an empty length tree.
func (h *huffmanDecoder) init(lens []uint8) bool {
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
			return false
		}
		h.first[l], h.index[l] = code, idx
		code = (code + h.count[l]) << 1
		idx += h.count[l]
		if h.count[l] > 0 {
			h.maxLen = l
		}
	}
	if h.maxLen > 0 && left > 0 {
		return false
	}

	next := h.index
	for sym, l := range lens {
		if l > 0 {
			h.symbols[next[l]] = uint16(sym)
			next[l]++
		}
	}

	h.table = [1 << tableBits]uint16{}
	for l := 1; l <= min(h.maxLen, tableBits); l++ {
		shift := tableBits - l
		for k := range h.count[l] {
			entry := h.symbols[h.index[l]+k]<<huffmanValueShift | uint16(l)
			c := (h.first[l] + k) << shift
			for i := range uint32(1) << shift {
				h.table[c+i] = entry
			}
		}
	}
	return true
}

// decode returns the symbol of the next codeword in b. The code must not be
// empty.
func (h *huffmanDecoder) decode(b *bitReader) int {
	b.fill(maxCodeLen)
	v := uint32(b.bits >> (64 - maxCodeLen))
	if e := h.table[v>>(maxCodeLen-tableBits)]; e != 0 {
		b.consume(uint(e & huffmanCountMask))
		return int(e >> huffmanValueShift)
	}
	for l := tableBits + 1; l <= h.maxLen; l++ {
		c := v>>(maxCodeLen-l) - h.first[l]
		if c < h.count[l] {
			b.consume(uint(l))
			return int(h.symbols[h.index[l]+c])
		}
	}
	return 0
}
