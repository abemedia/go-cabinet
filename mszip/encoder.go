package mszip

import (
	"fmt"
	"io"

	"github.com/klauspost/compress/flate"
)

// Compression levels for use with [NewEncoder].
// These correspond to the standard deflate compression levels.
const (
	NoCompression      = 0
	BestSpeed          = 1
	BestCompression    = 9
	DefaultCompression = -1

	// HuffmanOnly disables Lempel-Ziv match searching and only performs Huffman
	// entropy encoding. This mode is useful in compressing data that has
	// already been compressed with an LZ style algorithm (e.g. Snappy or LZ4)
	// that lacks an entropy encoder. Compression gains are achieved when
	// certain bytes in the input stream occur more frequently than others.
	//
	// Note that HuffmanOnly produces a compressed output that is
	// RFC 1951 compliant. That is, any valid DEFLATE decompressor will
	// continue to be able to decompress this output.
	HuffmanOnly = -2
)

// Encoder compresses data in the MSZIP format one block at a time.
type Encoder struct {
	fw   *flate.Writer
	out  appendWriter
	dict []byte
}

// NewEncoder returns a new [Encoder] that compresses at the given deflate
// level, such as [DefaultCompression], [BestSpeed] or [BestCompression].
func NewEncoder(level int) (*Encoder, error) {
	if level < HuffmanOnly || level > BestCompression {
		return nil, fmt.Errorf("mszip: invalid compression level: %d", level)
	}
	fw, err := flate.NewWriterDict(io.Discard, level, nil)
	if err != nil {
		return nil, err
	}
	return &Encoder{fw: fw, dict: make([]byte, 0, blockSize)}, nil
}

// Reset discards the encoder's state, making it ready to encode a new stream.
func (e *Encoder) Reset() {
	e.dict = e.dict[:0]
}

// Encode appends src, compressed as one block, to dst and returns the extended
// buffer. src must hold between 1 byte and 32 KiB.
func (e *Encoder) Encode(dst, src []byte) ([]byte, error) {
	if len(src) == 0 || len(src) > blockSize {
		return dst, errBlockSize
	}
	e.out.buf = dst
	e.out.buf = append(e.out.buf, blockSig[:]...)
	e.fw.ResetDict(&e.out, e.dict)
	if _, err := e.fw.Write(src); err != nil {
		return dst, err
	}
	if err := e.fw.Close(); err != nil {
		return dst, err
	}
	e.dict = append(e.dict[:0], src...)
	return e.out.buf, nil
}

type appendWriter struct{ buf []byte }

func (w *appendWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	return len(p), nil
}
