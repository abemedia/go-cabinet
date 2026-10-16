package lzx

// Encoder compresses data in the LZX format one frame at a time.
type Encoder struct {
	c      *compressor
	window int

	// buf holds the history of up to two windows, followed by the frame being
	// compressed. It is kept in its E8 encoded form.
	buf    []byte
	offset int64 // length of the stream so far
	final  bool  // a frame shorter than 32 KiB has been encoded
}

// NewEncoder returns a new [Encoder] that uses a sliding window of
// 2^window bytes. The window size is not recorded in its output and must be
// passed to [NewDecoder] to decompress it.
func NewEncoder(window int) (*Encoder, error) {
	if window < MinWindow || window > MaxWindow {
		return nil, windowError(window)
	}
	return &Encoder{c: newCompressor(window), window: 1 << window}, nil
}

// Reset discards the encoder's state, making it ready to encode a new stream.
func (e *Encoder) Reset() {
	e.c.reset()
	e.buf = e.buf[:0]
	e.offset = 0
	e.final = false
}

// Encode appends src, compressed as one frame, to dst and returns the extended
// buffer. src must hold 32 KiB for all frames of a stream but the last, which
// may be shorter.
func (e *Encoder) Encode(dst, src []byte) ([]byte, error) {
	if len(src) == 0 || len(src) > frameSize || e.final {
		return dst, errFrameSize
	}
	e.final = len(src) < frameSize

	start := len(e.buf)
	e.buf = append(e.buf, src...)
	if e.offset < e8MaxOffset {
		translateE8(e.buf[start:], int32(e.offset), e8FileSize, false)
	}
	dst = e.c.compressFrame(dst, e.buf, start)
	e.offset += int64(len(src))

	if len(e.buf) >= 2*e.window {
		cut := len(e.buf) - e.window
		e.buf = e.buf[:copy(e.buf, e.buf[cut:])]
		e.c.mf.slide(cut)
	}
	return dst, nil
}
