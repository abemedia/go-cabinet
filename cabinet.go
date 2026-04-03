// Package cabinet reads and writes Microsoft Cabinet (.cab) files.
package cabinet

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/abemedia/go-cabinet/mszip"
)

// FileHeader describes a file within a Cabinet archive.
type FileHeader struct {
	// Name is the name of the file.
	Name string

	// Modified is the modification time of the file.
	Modified time.Time

	// ReadOnly indicates the file is read-only.
	ReadOnly bool

	// Hidden indicates the file is hidden.
	Hidden bool

	// System indicates the file is a system file.
	System bool

	// Archive indicates the file has the archive attribute set.
	Archive bool

	// Exec indicates the file should be run after extraction.
	Exec bool

	// NonUTF8 indicates that Name is not encoded in UTF-8.
	NonUTF8 bool
}

// Compression identifies the compression algorithm used in a Cabinet folder.
type Compression uint16

// Compression methods supported by the Cabinet format.
const (
	None    Compression = 0 // no compression
	MSZip   Compression = 1 // MS-ZIP compression
	Quantum Compression = 2 // Quantum compression; requires a third-party decompressor
	LZX     Compression = 3 // LZX compression; requires a third-party decompressor
)

// String returns the human-readable name of the compression method.
func (c Compression) String() string {
	switch c {
	case None:
		return "None"
	case MSZip:
		return "MS-ZIP"
	case Quantum:
		return "Quantum"
	case LZX:
		return "LZX"
	default:
		return fmt.Sprintf("Compression(%d)", uint16(c))
	}
}

// ErrAlgorithm is returned when a folder uses a compression method that
// has no registered compressor or decompressor.
var ErrAlgorithm = errors.New("cabinet: unsupported compression algorithm")

// A Compressor returns a new compressing writer, writing to w.
// The WriteCloser's Close method must be used to flush pending data to w.
// If the returned writer implements `Flush() error`, it will be called after
// each block of input to ensure the compressed output for that block is
// complete before a data block boundary is written.
type Compressor func(w io.Writer) (io.WriteCloser, error)

// A Decompressor returns a new decompressing reader, reading from r.
// The [io.ReadCloser]'s Close method must be used to release associated resources.
type Decompressor func(r io.Reader) io.ReadCloser

var compressors, decompressors sync.Map

// RegisterCompressor registers a custom compressor for the given method.
// The built-in methods [None] and [MSZip] are registered by default.
func RegisterCompressor(method Compression, comp Compressor) {
	compressors.Store(method, comp)
}

// RegisterDecompressor registers a custom decompressor for the given method.
// The built-in methods [None] and [MSZip] are registered by default.
func RegisterDecompressor(method Compression, dcomp Decompressor) {
	decompressors.Store(method, dcomp)
}

func compressor(method Compression) Compressor {
	v, ok := compressors.Load(method)
	if !ok {
		return nil
	}
	return v.(Compressor)
}

func decompressor(method Compression) Decompressor {
	v, ok := decompressors.Load(method)
	if !ok {
		return nil
	}
	return v.(Decompressor)
}

func init() {
	// None: pass-through (identity) compressor.
	RegisterCompressor(None, func(w io.Writer) (io.WriteCloser, error) {
		return nopWriteCloser{w}, nil
	})
	RegisterDecompressor(None, io.NopCloser)

	// MS-ZIP.
	RegisterCompressor(MSZip, func(w io.Writer) (io.WriteCloser, error) {
		return mszip.NewWriter(w, mszip.DefaultCompression)
	})
	RegisterDecompressor(MSZip, func(r io.Reader) io.ReadCloser {
		return mszip.NewReader(r)
	})
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
