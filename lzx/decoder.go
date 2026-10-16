package lzx

import (
	"encoding/binary"
	"errors"
)

var errFrameSize = errors.New("lzx: invalid frame size")

// Decoder decompresses LZX data one frame at a time.
type Decoder struct {
	br          bitReader
	window      []byte
	numMainSyms int

	pos        int   // current position in the window
	frameStart int   // window position of the current frame
	total      int64 // uncompressed bytes before the current frame
	e8Size     int32
	final      bool // a frame shorter than 32 KiB has been decoded

	blockType int
	blockLen  int
	remaining int  // bytes left in the current block
	padding   bool // the padding byte of an uncompressed block is in the next frame
	recent    recentOffsets

	mainLens [maxMainSyms]uint8
	lenLens  [numLenSyms]uint8
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
		window:      make([]byte, 1<<window),
		numMainSyms: numMainSyms(window),
	}
	d.Reset()
	return d, nil
}

// Reset discards the decoder's state, making it ready to decode a new stream.
func (d *Decoder) Reset() {
	*d = Decoder{window: d.window, numMainSyms: d.numMainSyms, recent: recentOffsets{1, 1, 1}}
}

// Decode decompresses the frame src into dst, whose length must be the frame's
// uncompressed size, which is 32 KiB for all frames of a stream but the last.
// It returns an error if src does not hold len(dst) bytes of data.
// After an error, every call returns the same error until Reset is called.
func (d *Decoder) Decode(dst, src []byte) ([]byte, error) {
	if d.err == nil {
		d.err = d.decode(dst, src)
	}
	return dst, d.err
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
	if d.final && d.remaining != 0 {
		return errFrameSize
	}

	copy(dst, d.window[d.frameStart:end])
	if d.e8Size != 0 && d.total < e8MaxOffset {
		translateE8(dst, int32(d.total), d.e8Size, true)
	}
	d.total += int64(len(dst))
	if d.pos == len(d.window) {
		d.pos = 0
	}
	return nil
}

func (d *Decoder) readHeader() error {
	enabled, err := d.br.readBits(1)
	if err != nil || enabled == 0 {
		return err
	}
	hi, err := d.br.readBits(16)
	if err != nil {
		return err
	}
	lo, err := d.br.readBits(16)
	d.e8Size = int32(hi<<16 | lo)
	return err
}

func (d *Decoder) nextBlock() error {
	typ, err := d.br.readBits(3)
	if err != nil {
		return err
	}
	size, err := d.br.readBits(24)
	if err != nil {
		return err
	}
	d.blockType, d.blockLen, d.remaining = int(typ), int(size), int(size)

	switch typ {
	case blockVerbatim:
		return d.readTrees()
	case blockAligned:
		var lens [numAlignedSyms]uint8
		for i := range lens {
			l, err := d.br.readBits(3)
			if err != nil {
				return err
			}
			lens[i] = uint8(l)
		}
		if err := d.aligned.init(lens[:]); err != nil {
			return err
		}
		return d.readTrees()
	case blockUncompressed:
		var b [4 * numRecentOffsets]byte
		if err := d.br.skipPadding(); err != nil {
			return err
		}
		if err := d.br.readFull(b[:]); err != nil {
			return err
		}
		for i := range d.recent {
			d.recent[i] = binary.LittleEndian.Uint32(b[4*i:])
		}
		return nil
	default:
		return errCorrupt
	}
}

func (d *Decoder) readTrees() error {
	if err := d.readLengths(d.mainLens[:numChars]); err != nil {
		return err
	}
	if err := d.readLengths(d.mainLens[numChars:d.numMainSyms]); err != nil {
		return err
	}
	if err := d.main.init(d.mainLens[:d.numMainSyms]); err != nil {
		return err
	}
	if err := d.readLengths(d.lenLens[:]); err != nil {
		return err
	}
	return d.length.init(d.lenLens[:])
}

// readLengths updates lens in place, as they are coded as differences from
// the previous block's lengths.
func (d *Decoder) readLengths(lens []uint8) error {
	var plens [numPretreeSyms]uint8
	for i := range plens {
		l, err := d.br.readBits(4)
		if err != nil {
			return err
		}
		plens[i] = uint8(l)
	}
	if err := d.pretree.init(plens[:]); err != nil {
		return err
	}

	for i := 0; i < len(lens); {
		sym, err := d.br.decode(&d.pretree)
		if err != nil {
			return err
		}
		run, l, err := d.readLengthRun(sym, lens[i])
		if err != nil {
			return err
		}
		if run > len(lens)-i {
			return errCorrupt
		}
		for range run {
			lens[i] = l
			i++
		}
	}
	return nil
}

func (d *Decoder) readLengthRun(sym int, prev uint8) (int, uint8, error) {
	switch sym {
	case 17:
		n, err := d.br.readBits(4)
		return 4 + int(n), 0, err
	case 18:
		n, err := d.br.readBits(5)
		return 20 + int(n), 0, err
	case 19:
		n, err := d.br.readBits(1)
		if err != nil {
			return 0, 0, err
		}
		if sym, err = d.br.decode(&d.pretree); err != nil {
			return 0, 0, err
		}
		if sym > 16 {
			return 0, 0, errCorrupt
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
		if err := d.br.readFull(d.window[d.pos:end]); err != nil {
			return err
		}
		d.pos = end
		d.remaining -= end - start
		// An uncompressed block of odd length is followed by a padding byte,
		// which is in the next frame if this one ends with the block.
		if d.remaining == 0 && d.blockLen%2 == 1 {
			d.padding = !d.br.skipByte()
		}
		return nil
	}
	for d.pos < end {
		sym, err := d.br.decode(&d.main)
		if err != nil {
			return err
		}
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
	return nil
}

func (d *Decoder) copyMatch(m, end int) error {
	length, offset, err := d.readMatch(m)
	if err != nil {
		return err
	}
	decoded := d.total + int64(d.pos-d.frameStart)
	if length > end-d.pos || offset == 0 || int64(offset) > decoded || int(offset) > len(d.window) {
		return errCorrupt
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

func (d *Decoder) readMatch(m int) (int, uint32, error) {
	length := m%numLenHeaders + minMatch
	if length == minSecondaryLen {
		sym, err := d.br.decode(&d.length)
		if err != nil {
			return 0, 0, err
		}
		length += sym
	}

	slot := m / numLenHeaders
	if slot < numRecentOffsets {
		d.recent = d.recent.swap(uint32(slot))
		return length, d.recent[0], nil
	}

	n := uint(extraBits[slot])
	formatted := positionBase[slot]
	if d.blockType == blockAligned && n >= numAlignedBits {
		v, err := d.br.readBits(n - numAlignedBits)
		if err != nil {
			return 0, 0, err
		}
		a, err := d.br.decode(&d.aligned)
		if err != nil {
			return 0, 0, err
		}
		formatted += v<<numAlignedBits | uint32(a)
	} else {
		v, err := d.br.readBits(n)
		if err != nil {
			return 0, 0, err
		}
		formatted += v
	}
	offset := formatted - (numRecentOffsets - 1)
	d.recent = d.recent.push(offset)
	return length, offset, nil
}
