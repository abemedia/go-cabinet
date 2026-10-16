package mszip

import (
	"bytes"
	"errors"
	"io"

	"github.com/klauspost/compress/flate"
)

var errLength = errors.New("mszip: block does not match its uncompressed size")

type flateReader interface {
	io.Reader
	io.WriterTo
	flate.Resetter
}

// Decoder decompresses MSZIP data one block at a time.
type Decoder struct {
	fr   flateReader
	src  bytes.Reader
	dict []byte
}

// NewDecoder returns a new [Decoder].
func NewDecoder() *Decoder {
	d := &Decoder{dict: make([]byte, 0, blockSize)}
	d.fr = flate.NewReaderDict(&d.src, nil).(flateReader)
	return d
}

// Reset discards the decoder's state, making it ready to decode a new stream.
func (d *Decoder) Reset() {
	d.dict = d.dict[:0]
}

// Decode decompresses the block src into dst, whose length must be the block's
// uncompressed size. It returns an error if src does not hold exactly len(dst)
// bytes of data.
func (d *Decoder) Decode(dst, src []byte) ([]byte, error) {
	if len(dst) > blockSize {
		return nil, errBlockSize
	}
	if len(src) < len(blockSig) || [2]byte(src) != blockSig {
		return nil, errors.New("mszip: invalid block signature")
	}
	d.src.Reset(src[len(blockSig):])
	if err := d.fr.Reset(&d.src, d.dict); err != nil {
		return nil, err
	}
	var n int
	for n < len(dst) {
		m, err := d.fr.Read(dst[n:])
		n += m
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if n != len(dst) {
		return nil, errLength
	}
	if _, err := d.fr.WriteTo(overflow{}); err != nil {
		return nil, err
	}
	d.dict = append(d.dict[:0], dst...)
	return dst, nil
}

type overflow struct{}

func (overflow) Write([]byte) (int, error) {
	return 0, errLength
}
