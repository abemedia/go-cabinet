// Package cabinet reads and writes Microsoft Cabinet (.cab) files.
package cabinet

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/abemedia/go-cabinet/lzx"
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

// Compression identifies the compression algorithm used in a Cabinet folder
// and its settings.
type Compression uint16

// Compression methods supported by the Cabinet format.
const (
	None    Compression = 0 // no compression
	MSZIP   Compression = 1 // MSZIP compression
	Quantum Compression = 2 // Quantum compression; requires a third-party decompressor
	LZX     Compression = 3 // LZX compression; without LZXWindow, the writer uses a 2 MiB window
)

// LZXWindow returns LZX compression with a sliding window of 2^window bytes,
// where window ranges from [lzx.MinWindow] to [lzx.MaxWindow].
func LZXWindow(window int) Compression {
	return TypeLevelMemory(LZX, 0, window)
}

// TypeLevelMemory returns typ with the given level and memory.
// If level is outside 0 to 15 or memory is outside 0 to 31, it returns an invalid value.
func TypeLevelMemory(typ Compression, level, memory int) Compression {
	if level < 0 || level > 15 || memory < 0 || memory > 31 {
		return ^Compression(0)
	}
	return typ | Compression(level)<<shiftLevel | Compression(memory)<<shiftMemory
}

// Type returns the compression method without its settings.
func (c Compression) Type() Compression {
	return c & maskType
}

// Level returns the Quantum compression level.
func (c Compression) Level() int {
	return int(c&maskLevel) >> shiftLevel
}

// Memory returns the base-2 logarithm of the LZX or Quantum window size.
func (c Compression) Memory() int {
	return int(c&maskMemory) >> shiftMemory
}

// String returns the human-readable name of the compression method.
func (c Compression) String() string {
	switch c.Type() {
	case None:
		return "NONE"
	case MSZIP:
		return "MSZIP"
	case Quantum:
		if c.Level() == 0 && c.Memory() == 0 {
			return "QUANTUM"
		}
		return fmt.Sprintf("QUANTUM:%d,%d", c.Level(), c.Memory())
	case LZX:
		if c.Memory() == 0 {
			return "LZX"
		}
		return fmt.Sprintf("LZX:%d", c.Memory())
	default:
		return fmt.Sprintf("Compression(%d)", uint16(c))
	}
}

func (c Compression) canonical() Compression {
	switch c.Type() {
	case None, MSZIP:
		return c.Type()
	case LZX:
		if c.Memory() == 0 {
			return LZXWindow(lzx.MaxWindow)
		}
		return c & (maskType | maskMemory)
	case Quantum:
		return c & (maskType | maskLevel | maskMemory)
	default:
		return c
	}
}

// ErrAlgorithm is returned when a folder uses a compression method that
// has no registered compressor or decompressor.
var ErrAlgorithm = errors.New("cabinet: unsupported compression algorithm")

// An Encoder compresses the data blocks of a folder in order. Every block holds
// 32 KiB of uncompressed data except the last block of a folder, which may be
// shorter.
type Encoder interface {
	// Encode appends src, compressed as one block, to dst and returns the extended buffer.
	Encode(dst, src []byte) ([]byte, error)

	// Reset discards the encoder's state, making it ready for a new folder.
	Reset()
}

// A Decoder decompresses the data blocks of a folder in order. Every block
// holds 32 KiB of uncompressed data except the last block of a folder, which
// may be shorter.
type Decoder interface {
	// Decode decompresses the block src into dst, whose length is the block's
	// uncompressed size, and returns dst.
	Decode(dst, src []byte) ([]byte, error)

	// Reset discards the decoder's state, making it ready for a new folder.
	Reset()
}

// A Compressor returns a new Encoder for a folder with the given compression
// level and memory, as recorded in its header.
type Compressor func(level, memory int) (Encoder, error)

// A Decompressor returns a new Decoder for a folder with the given compression
// level and memory, as recorded in its header.
type Decompressor func(level, memory int) (Decoder, error)

var compressors, decompressors sync.Map

// RegisterCompressor registers a custom compressor for the given method.
// The built-in methods [None], [MSZIP] and [LZX] are registered by default.
func RegisterCompressor(method Compression, comp Compressor) {
	compressors.Store(method.Type(), comp)
}

// RegisterDecompressor registers a custom decompressor for the given method.
// The built-in methods [None], [MSZIP] and [LZX] are registered by default.
func RegisterDecompressor(method Compression, dcomp Decompressor) {
	decompressors.Store(method.Type(), dcomp)
}

func compressor(method Compression) Compressor {
	v, ok := compressors.Load(method.Type())
	if !ok {
		return nil
	}
	return v.(Compressor)
}

func decompressor(method Compression) Decompressor {
	v, ok := decompressors.Load(method.Type())
	if !ok {
		return nil
	}
	return v.(Decompressor)
}

func init() {
	// None: pass-through (identity) compressor.
	RegisterCompressor(None, func(int, int) (Encoder, error) { return store{}, nil })
	RegisterDecompressor(None, func(int, int) (Decoder, error) { return store{}, nil })

	// MSZIP.
	RegisterCompressor(MSZIP, func(int, int) (Encoder, error) { return mszip.NewEncoder(mszip.DefaultCompression) })
	RegisterDecompressor(MSZIP, func(int, int) (Decoder, error) { return mszip.NewDecoder(), nil })

	// LZX.
	RegisterCompressor(LZX, func(_, memory int) (Encoder, error) { return lzx.NewEncoder(memory) })
	RegisterDecompressor(LZX, func(_, memory int) (Decoder, error) { return lzx.NewDecoder(memory) })
}

// store is the codec of folders that are not compressed.
type store struct{}

func (store) Encode(_, src []byte) ([]byte, error) {
	return src, nil
}

func (store) Decode(dst, src []byte) ([]byte, error) {
	if len(src) != len(dst) {
		return nil, errors.New("data block size mismatch")
	}
	return src, nil
}

func (store) Reset() {}
