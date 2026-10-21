package lzx

import "slices"

const (
	// maxCompressedFrame is the most compressed data that a frame may hold.
	maxCompressedFrame = frameSize + 6144

	// e8FileSize is the translation size of the encoder's E8 preprocessing.
	e8FileSize = 1 << 24

	// minSlide is the least distance by which the window slides.
	minSlide = 1 << 18

	// bufPadding is the space after the data in the encoder's buffer that
	// matchLen may read.
	bufPadding = 8

	// numPasses is the most times the parse codes a frame. A pass is skipped
	// once the estimate is within 1/convergence of the cost.
	numPasses   = 2
	convergence = 200
)

// Encoder compresses data in the LZX format one frame at a time.
type Encoder struct {
	window   int
	buf      []byte // the window followed by the frame being compressed
	pos      int    // the position in buf of the next frame
	total    int64  // uncompressed bytes before the next frame
	final    bool   // a frame shorter than 32 KiB has been encoded
	repeated repeatedOffsets

	mf     matchFinder
	parser *parser
	bw     blockWriter
}

// NewEncoder returns a new [Encoder] that compresses data with a sliding
// window of 2^window bytes.
func NewEncoder(window int) (*Encoder, error) {
	if window < MinWindow || window > MaxWindow {
		return nil, windowError(window)
	}
	// The window slides by a multiple of its size, which keeps the place of
	// each position in the match finder's tree.
	buf := make([]byte, 1<<window+max(1<<window, minSlide)+bufPadding)
	e := &Encoder{
		window: window,
		buf:    buf,
		mf:     newMatchFinder(buf, window),
		parser: newParser(),
		bw:     newBlockWriter(numMainSymbols(window)),
	}
	e.Reset()
	return e, nil
}

// Reset discards the encoder's state, making it ready to encode a new stream.
func (e *Encoder) Reset() {
	e.pos, e.total, e.final = 0, 0, false
	e.repeated = repeatedOffsets{1, 1, 1}
	e.mf.reset()
	e.bw.reset()
}

// Encode appends src, compressed as one frame, to dst and returns the extended
// buffer. src must hold between 1 byte and 32 KiB, and only the last frame of
// a stream may hold less than 32 KiB.
func (e *Encoder) Encode(dst, src []byte) ([]byte, error) {
	if len(src) == 0 || len(src) > frameSize || e.final {
		return dst, errFrameSize
	}
	e.final = len(src) < frameSize

	if e.pos+len(src) > len(e.buf)-bufPadding {
		n := e.pos - 1<<e.window
		copy(e.buf, e.buf[n:e.pos])
		e.pos -= n
		e.mf.slide(n)
	}
	frame := e.buf[e.pos : e.pos+len(src)]
	copy(frame, src)
	if e.total < e8MaxOffset {
		translateE8(frame, int32(e.total), e8FileSize, true)
	}

	w := bitWriter{buf: slices.Grow(dst, maxCompressedFrame)}
	if e.total == 0 {
		w.writeBits(1, 1)
		w.writeBits(e8FileSize>>16, 16)
		w.writeBits(e8FileSize&0xffff, 16)
	}
	e.compressFrame(&w, frame)
	w.flush()

	e.pos += len(src)
	e.total += int64(len(src))
	return w.buf, nil
}

// compressFrame writes frame, which is at e.pos in e.buf, as one block.
func (e *Encoder) compressFrame(w *bitWriter, frame []byte) {
	p := e.parser
	p.findMatches(&e.mf, e.pos, len(frame))
	if prev := e.bw.previous(); prev != nil {
		p.model.set(prev)
	} else {
		p.model.setDefault()
	}

	var rep repeatedOffsets
	for pass := 1; ; pass++ {
		rep = p.parse(e.buf, e.pos, len(frame), e.repeated)
		code := e.bw.build(p.tokens, frame)
		if pass == numPasses {
			break
		}
		est, cost := p.cost[len(frame)], p.model.cost(code, &e.bw.counts)
		if max(est, cost)-min(est, cost) < cost/convergence {
			break
		}
		p.model.set(code)
	}
	if e.bw.write(w, p.tokens, frame, e.repeated) {
		e.repeated = rep
	}
}
