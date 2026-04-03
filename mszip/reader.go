package mszip

import (
	"errors"
	"io"

	"github.com/klauspost/compress/flate"
)

// byteReader wraps an io.Reader and implements io.ByteReader so that
// compress/flate does not buffer ahead past a deflate block boundary.
type byteReader struct {
	r io.Reader
	b [1]byte
}

func (br *byteReader) ReadByte() (byte, error) {
	_, err := io.ReadFull(br.r, br.b[:])
	if err != nil {
		return 0, err
	}
	return br.b[0], nil
}

func (br *byteReader) Read(p []byte) (int, error) {
	return br.r.Read(p)
}

type flateReader interface {
	io.ReadCloser
	flate.Resetter
}

// Reader implements MS-ZIP decompression. It reads CK-prefixed compressed
// blocks from an underlying stream, decompressing each block independently
// using the previous block's uncompressed output as a preset dictionary.
type Reader struct {
	br     byteReader
	fr     flateReader
	buf    [blockSize + 1]byte
	bufLen int
	bufPos int
	dict   []byte
	err    error
}

// NewReader returns a [Reader] that decompresses MS-ZIP data from r.
func NewReader(r io.Reader) *Reader {
	rd := &Reader{dict: make([]byte, 0, blockSize)}
	rd.br.r = r
	rd.fr = flate.NewReaderDict(&rd.br, nil).(flateReader)
	return rd
}

// Read reads decompressed bytes into p.
func (r *Reader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	total := 0
	for len(p) > 0 {
		if r.bufPos < r.bufLen {
			n := copy(p, r.buf[r.bufPos:r.bufLen])
			r.bufPos += n
			p = p[n:]
			total += n
			continue
		}
		if err := r.readBlock(); err != nil {
			r.err = err
			if err == io.EOF && total > 0 {
				return total, nil
			}
			return total, err
		}
	}
	return total, nil
}

// readBlock reads the next CK-prefixed compressed block and decompresses it.
func (r *Reader) readBlock() (err error) {
	var ck [2]byte
	if _, err := io.ReadFull(r.br.r, ck[:]); err != nil {
		return err
	}
	if ck != blockSig {
		return errors.New("mszip: invalid block signature")
	}

	if err := r.fr.Reset(&r.br, r.dict); err != nil {
		return err
	}

	n := 0
	for n < blockSize+1 {
		nn, err := r.fr.Read(r.buf[n:])
		n += nn
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if n > blockSize {
		return errors.New("mszip: decompressed block exceeds 32 KiB")
	}
	r.bufLen = n
	r.bufPos = 0

	// Save the decompressed block as the dictionary for the next block.
	r.dict = append(r.dict[:0], r.buf[:n]...)

	return nil
}

// Close closes the [Reader] r.
func (r *Reader) Close() error {
	if r.err == errClosed {
		return nil
	}
	if err := r.fr.Close(); err != nil {
		return err
	}
	r.err = errClosed
	return nil
}
