// Package lzx implements the LZX compression format as defined by
// Microsoft LZX Data Compression Format
// https://learn.microsoft.com/en-us/previous-versions/bb417343(v=msdn.10)#microsoft-lzx-data-compression-format
//
// An LZX stream is a sequence of frames, each of which decompresses to 32 KiB
// except for the last, which may be shorter. Every frame starts on a 16-bit
// boundary of the compressed stream, and in a cabinet each frame is stored in
// its own data block.
package lzx

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// Supported sliding window sizes, given as the base-2 logarithm of the window
// size in bytes.
const (
	MinWindow = 15
	MaxWindow = 21
)

const (
	frameSize = 1 << 15

	numChars           = 256
	minMatch           = 2
	maxMatch           = 257
	numPrimaryLens     = 7
	numLenHeaders      = numPrimaryLens + 1
	minSecondaryLen    = minMatch + numPrimaryLens
	numSecondaryLens   = maxMatch - minSecondaryLen + 1
	maxPositionSlots   = 50
	maxMainSymbols     = numChars + maxPositionSlots*numLenHeaders
	numPretreeSymbols  = 20
	numAlignedBits     = 3
	numAlignedSymbols  = 1 << numAlignedBits
	numRepeatedOffsets = 3
	maxCodeLen         = 16

	blockVerbatim     = 1
	blockAligned      = 2
	blockUncompressed = 3

	// Sizes in bits of the fields of a block header and of the path lengths
	// of the trees that are not coded with a pretree.
	blockTypeBits  = 3
	blockSizeBits  = 24
	pretreeLenBits = 4
	alignedLenBits = 3

	e8MaxOffset = 1 << 30
	e8TailSize  = 10 // a call's 4-byte operand must end before the last 6 bytes
)

var errFrameSize = errors.New("lzx: invalid frame size")

func windowError(window int) error {
	return fmt.Errorf("lzx: invalid window size %d: want value in range [%d, %d]", window, MinWindow, MaxWindow)
}

// positionBase[s] is the first formatted offset (match offset plus 2) coded
// with position slot s.
var positionBase = [...]uint32{
	0, 1, 2, 3, 4, 6, 8, 12, 16, 24,
	32, 48, 64, 96, 128, 192, 256, 384, 512, 768,
	1024, 1536, 2048, 3072, 4096, 6144, 8192, 12288, 16384, 24576,
	32768, 49152, 65536, 98304, 131072, 196608, 262144, 393216, 524288, 655360,
	786432, 917504, 1048576, 1179648, 1310720, 1441792, 1572864, 1703936, 1835008, 1966080,
}

// extraBits[s] is the number of bits that follow position slot s.
var extraBits = [...]uint8{
	0, 0, 0, 0, 1, 1, 2, 2, 3, 3,
	4, 4, 5, 5, 6, 6, 7, 7, 8, 8,
	9, 9, 10, 10, 11, 11, 12, 12, 13, 13,
	14, 14, 15, 15, 16, 16, 17, 17, 17, 17,
	17, 17, 17, 17, 17, 17, 17, 17, 17, 17,
}

// numPositionSlots[i] is the number of position slots for window i + MinWindow,
// which are the slots whose base is below the window size.
var numPositionSlots = [...]int{30, 32, 34, 36, 38, 42, 50}

// numMainSymbols returns the number of main tree symbols for a sliding window of
// 2^window bytes.
func numMainSymbols(window int) int {
	return numChars + numPositionSlots[window-MinWindow]*numLenHeaders
}

// repeatedOffsets holds the repeated offsets R0, R1 and R2.
type repeatedOffsets [numRepeatedOffsets]uint32

// push records a match whose offset is not one of the repeated offsets.
func (r repeatedOffsets) push(offset uint32) repeatedOffsets {
	return repeatedOffsets{offset, r[0], r[1]}
}

// swap records a match that uses the repeated offset Ri.
func (r repeatedOffsets) swap(i uint32) repeatedOffsets {
	r[0], r[i] = r[i], r[0]
	return r
}

// after returns the repeated offsets after the token t, which may be absent.
func (r repeatedOffsets) after(t token) repeatedOffsets {
	switch f := t.formatted(); {
	case t.length() <= 1:
		return r
	case f < numRepeatedOffsets:
		return r.swap(f)
	default:
		return r.push(f - 2)
	}
}

// lengthDelta applies the pretree code sym to the previous path length prev
// and returns the new path length.
func lengthDelta(prev, sym uint8) uint8 {
	return (prev + 17 - sym) % 17
}

// translateE8 converts the operands of x86 CALL instructions (opcode 0xE8)
// from relative to absolute targets if encode is set, and back otherwise.
// off is the stream offset of b.
func translateE8(b []byte, off, fileSize int32, encode bool) {
	limit := len(b) - e8TailSize
	for i := 0; i < limit; {
		j := bytes.IndexByte(b[i:limit], 0xE8)
		if j < 0 {
			return
		}
		i += j
		pos := off + int32(i)
		v := int32(binary.LittleEndian.Uint32(b[i+1:]))
		if v >= -pos && v < fileSize {
			switch {
			case encode && v < fileSize-pos:
				v += pos
			case encode:
				v -= fileSize
			case v >= 0:
				v -= pos
			default:
				v += fileSize
			}
			binary.LittleEndian.PutUint32(b[i+1:], uint32(v))
		}
		i += 5
	}
}
