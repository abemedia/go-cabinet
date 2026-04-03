package mszip

import (
	"io"

	"github.com/klauspost/compress/flate"
)

// Compression levels for use with [NewWriter].
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

// Writer implements MS-ZIP compression. It buffers data internally and emits
// a CK-prefixed compressed block every 32,768 bytes. The final (possibly
// smaller) block is emitted on [Writer.Close] or [Writer.Flush].
type Writer struct {
	w    io.Writer
	buf  [blockSize]byte
	n    int
	dict []byte
	err  error
	fw   *flate.Writer
}

// NewWriter returns a [Writer] that writes MS-ZIP compressed data to w.
// The `level` parameter controls the deflate compression level
// (e.g., `flate.DefaultCompression`, `flate.BestSpeed`, `flate.BestCompression`).
func NewWriter(w io.Writer, level int) (*Writer, error) {
	fw, err := flate.NewWriterDict(w, level, nil)
	if err != nil {
		return nil, err
	}
	return &Writer{w: w, dict: make([]byte, 0, blockSize), fw: fw}, nil
}

// Write writes p to the writer.
func (w *Writer) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	total := 0
	for len(p) > 0 {
		n := copy(w.buf[w.n:], p)
		w.n += n
		p = p[n:]
		total += n
		if w.n == blockSize {
			if err := w.emitBlock(w.buf[:w.n]); err != nil {
				w.err = err
				return total, err
			}
			w.n = 0
		}
	}
	return total, nil
}

// Flush emits any buffered data as a compressed block.
// It is a no-op if the buffer is empty.
func (w *Writer) Flush() error {
	if w.err != nil {
		return w.err
	}
	if w.n == 0 {
		return nil
	}
	if err := w.emitBlock(w.buf[:w.n]); err != nil {
		w.err = err
		return err
	}
	w.n = 0
	return nil
}

// emitBlock compresses data and writes a CK-prefixed deflate block to w.w.
func (w *Writer) emitBlock(data []byte) error {
	if _, err := w.w.Write(blockSig[:]); err != nil {
		return err
	}
	w.fw.ResetDict(w.w, w.dict)
	if _, err := w.fw.Write(data); err != nil {
		return err
	}
	if err := w.fw.Close(); err != nil {
		return err
	}

	// Save the uncompressed block as the dictionary for the next block.
	w.dict = append(w.dict[:0], data...)

	return nil
}

// Close flushes any remaining buffered data and closes the writer.
func (w *Writer) Close() error {
	if w.err == errClosed {
		return nil
	}
	if err := w.Flush(); err != nil {
		return err
	}
	w.err = errClosed
	return nil
}
