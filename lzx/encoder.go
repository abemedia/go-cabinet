package lzx

import "slices"

const (
	// maxCompressedFrame is the most compressed data that a frame may hold.
	maxCompressedFrame = frameSize + 6144

	// e8FileSize is the translation size of the encoder's E8 preprocessing.
	e8FileSize = 1 << 24

	// minSlide is the least distance by which the window slides. It is a power
	// of two, so that it is a multiple of every smaller window size.
	minSlide = 1 << 18

	// bufPadding is the space after the data in the encoder's buffer that
	// matchLen may read.
	bufPadding = 8
)

// Tuning parameters, chosen by measurement on the benchmark data.
const (
	// searchDepth is the most tree nodes that a match search visits.
	searchDepth = 32

	// A match of at least niceLength bytes ends a match search, and the parse
	// takes it whatever the costs. Of the positions within it, only the last
	// insertTail are added to the match finder.
	niceLength = 128
	insertTail = 32

	// Within a match of at least longLength bytes, the parse skips all but the
	// last longTail positions.
	longLength = 64
	longTail   = 16

	// numPasses is the most times the parse codes a frame. Each pass after the
	// first uses the code of the previous one, and is skipped if that code
	// costs the previous pass's items within 1/convergence of the estimate.
	numPasses   = 2
	convergence = 200

	// unusedCost is the estimated path length of a symbol absent from the code
	// that the parse costs symbols by.
	unusedCost = 14

	// The parse adds these costs, in units of 1/2^costShift bit, to each match
	// and literal, so that of paths of nearly equal size it prefers those that
	// take less time to decode.
	matchPenalty   = 16
	literalPenalty = 4

	// pretreePasses is the number of times the pretree codes of a tree are
	// chosen, each time under the costs of the pretree of the previous choice.
	pretreePasses = 2
)

// Encoder compresses data in the LZX format one frame at a time.
type Encoder struct {
	window int
	buf    []byte // the window followed by the frame being compressed
	pos    int    // the position in buf of the next frame
	total  int64  // uncompressed bytes before the next frame
	final  bool   // a frame shorter than 32 KiB has been encoded
	recent recentOffsets

	mf     matchFinder
	parser parser
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
	e.recent = recentOffsets{1, 1, 1}
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
	p := &e.parser
	p.findMatches(&e.mf, e.pos, len(frame))
	if prev := e.bw.previous(); prev != nil {
		p.model.set(prev)
	} else {
		p.model.setDefault()
	}

	var recent recentOffsets
	for pass := 1; ; pass++ {
		recent = p.parse(e.buf, e.pos, len(frame), e.recent)
		code := e.bw.build(p.items, frame)
		if pass == numPasses {
			break
		}
		est, cost := p.cost[len(frame)], p.model.cost(code, &e.bw.counts)
		if max(est, cost)-min(est, cost) < cost/convergence {
			break
		}
		p.model.set(code)
	}
	if e.bw.write(w, p.items, frame, e.recent) {
		e.recent = recent
	}
}
