// Package cabinet reads and writes Microsoft Cabinet (.cab) files.
package cabinet

import (
	"errors"
	"fmt"
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

// A Compression is the compression method of a cabinet folder and its settings.
type Compression uint16

// Compression methods supported by the Cabinet format.
const (
	None    Compression = 0 // no compression
	MSZIP   Compression = 1 // MSZIP compression
	Quantum Compression = 2 // Quantum compression; requires a third-party decompressor
	LZX     Compression = 3 // LZX compression; requires a third-party decompressor
)

// LZXWindow returns LZX compression with a sliding window of 2^window bytes,
// where window ranges from 15 to 21.
func LZXWindow(window int) Compression {
	return TypeLevelMemory(LZX, 0, window)
}

// TypeLevelMemory returns the method of typ with the given level and memory.
// An unrepresentable level or memory is stored as its maximum, which is invalid.
func TypeLevelMemory(typ Compression, level, memory int) Compression {
	if level < 0 || level > 15 {
		level = 15
	}
	if memory < 0 || memory > 31 {
		memory = 31
	}
	return typ.Type() | Compression(level)<<tcompShiftLevel | Compression(memory)<<tcompShiftMemory
}

// Type returns the compression method without its settings.
func (c Compression) Type() Compression {
	return c & tcompMaskType
}

// Level returns the compression level.
func (c Compression) Level() int {
	return int(c&tcompMaskLevel) >> tcompShiftLevel
}

// Memory returns the base-2 logarithm of the window size.
func (c Compression) Memory() int {
	return int(c&tcompMaskMemory) >> tcompShiftMemory
}

// String returns the name of the method of c, followed by its settings if set.
func (c Compression) String() string {
	switch c.Type() {
	case None:
		return "None"
	case MSZIP:
		return "MSZIP"
	case Quantum:
		if c.Level() == 0 && c.Memory() == 0 {
			return "Quantum"
		}
		return fmt.Sprintf("Quantum:%d,%d", c.Level(), c.Memory())
	case LZX:
		if c.Memory() == 0 {
			return "LZX"
		}
		return fmt.Sprintf("LZX:%d", c.Memory())
	default:
		return fmt.Sprintf("Compression(%d)", uint16(c))
	}
}

func (c Compression) validate() error {
	var levelLo, levelHi, memoryLo, memoryHi int
	switch c.Type() {
	case None, MSZIP:
	case Quantum:
		levelLo, levelHi = 1, 7
		memoryLo, memoryHi = 10, 21
	case LZX:
		memoryLo, memoryHi = 15, 21
	default:
		return fmt.Errorf("cabinet: invalid compression type %d", c.Type())
	}
	if l := c.Level(); l < levelLo || l > levelHi {
		return compressionError(c.Type(), "level", levelLo, levelHi)
	}
	if m := c.Memory(); m < memoryLo || m > memoryHi {
		return compressionError(c.Type(), "memory", memoryLo, memoryHi)
	}
	if c&^(tcompMaskType|tcompMaskLevel|tcompMaskMemory) != 0 {
		return errors.New("cabinet: reserved compression bits set")
	}
	return nil
}

func compressionError(typ Compression, field string, lo, hi int) error {
	if lo == hi {
		return fmt.Errorf("cabinet: invalid %s %s: want %d", typ, field, lo)
	}
	return fmt.Errorf("cabinet: invalid %s %s: want value in range [%d, %d]", typ, field, lo, hi)
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
// level and memory.
type Compressor func(level, memory int) (Encoder, error)

// A Decompressor returns a new Decoder for a folder with the given compression
// level and memory.
type Decompressor func(level, memory int) (Decoder, error)

var compressors, decompressors sync.Map

// RegisterCompressor registers a custom compressor for the given method.
// The built-in methods [None] and [MSZIP] are registered by default.
func RegisterCompressor(method Compression, comp Compressor) {
	if comp == nil {
		panic("cabinet: RegisterCompressor with nil compressor")
	}
	compressors.Store(method.Type(), comp)
}

// RegisterDecompressor registers a custom decompressor for the given method.
// The built-in methods [None] and [MSZIP] are registered by default.
func RegisterDecompressor(method Compression, dcomp Decompressor) {
	if dcomp == nil {
		panic("cabinet: RegisterDecompressor with nil decompressor")
	}
	decompressors.Store(method.Type(), dcomp)
}

func init() {
	// None: pass-through (identity) compressor.
	RegisterCompressor(None, func(int, int) (Encoder, error) { return store{}, nil })
	RegisterDecompressor(None, func(int, int) (Decoder, error) { return store{}, nil })

	// MSZIP.
	RegisterCompressor(MSZIP, func(int, int) (Encoder, error) { return mszip.NewEncoder(mszip.DefaultCompression) })
	RegisterDecompressor(MSZIP, func(int, int) (Decoder, error) { return mszip.NewDecoder(), nil })
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
