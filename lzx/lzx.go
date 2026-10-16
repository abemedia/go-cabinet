// Package lzx implements the LZX compression format as defined by the
// Microsoft LZX Data Compression Format specification and by
// [MS-PATCH]: LZX DELTA Compression and Decompression
// https://learn.microsoft.com/en-us/openspecs/exchange_server_protocols/ms-patch/cc78752a-b4af-4eee-88cb-01f4d8a4c2bf
//
// An LZX stream is a sequence of frames, each of which decompresses to 32 KiB
// except for the last, which may be shorter. Every frame starts on a 16-bit
// boundary of the compressed stream, and in a cabinet each frame is stored in
// its own data block.
package lzx

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/bits"
)

// Supported sliding window sizes, given as the base-2 logarithm of the window
// size in bytes.
const (
	MinWindow = 15
	MaxWindow = 21
)

const (
	frameSize = 1 << 15

	numChars         = 256
	minMatch         = 2
	maxMatch         = 257
	numLens          = maxMatch - minMatch + 1
	numPrimaryLens   = 7
	numLenHeaders    = numPrimaryLens + 1
	minSecondaryLen  = minMatch + numPrimaryLens
	numLenSyms       = maxMatch - minSecondaryLen + 1
	maxOffsetSlots   = 50
	maxMainSyms      = numChars + maxOffsetSlots*numLenHeaders
	numPretreeSyms   = 20
	numAlignedBits   = 3
	numAlignedSyms   = 1 << numAlignedBits
	numRecentOffsets = 3
	minAlignedSlot   = 8
	maxCodeLen       = 16

	blockVerbatim     = 1
	blockAligned      = 2
	blockUncompressed = 3

	e8FileSize  = 12000000
	e8MaxOffset = 1 << 30
	e8TailSize  = 10
)

func windowError(window int) error {
	return fmt.Errorf("lzx: invalid window size %d: want value in range [%d, %d]", window, MinWindow, MaxWindow)
}

// positionBase[s] is the first formatted offset (match offset plus 2) coded
// with offset slot s, and extraBits[s] the number of bits that follow it.
var positionBase, extraBits = offsetSlotTables()

func offsetSlotTables() (base [maxOffsetSlots + 1]uint32, extra [maxOffsetSlots]uint8) {
	for s := range maxOffsetSlots {
		switch {
		case s < 4:
			extra[s] = 0
		case s < 36:
			extra[s] = uint8(s/2 - 1)
		default:
			extra[s] = 17
		}
		base[s+1] = base[s] + 1<<extra[s]
	}
	return base, extra
}

func numMainSyms(window int) int {
	slots := 0
	for positionBase[slots] < 1<<window {
		slots++
	}
	return numChars + slots*numLenHeaders
}

func offsetSlot(formatted uint32) int {
	switch {
	case formatted < 4:
		return int(formatted)
	case formatted < positionBase[36]:
		n := bits.Len32(formatted) - 1
		return 2*n + int(formatted>>(n-1))&1
	default:
		return 36 + int((formatted-positionBase[36])>>17)
	}
}

// translateE8 converts the operands of x86 CALL instructions (opcode 0xE8)
// between relative and absolute targets, which makes repeated calls to the
// same function compress better. off is the stream offset of b.
func translateE8(b []byte, off, fileSize int32, decode bool) {
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
			case decode && v >= 0:
				v -= pos
			case decode:
				v += fileSize
			case v < fileSize-pos:
				v += pos
			default:
				v -= fileSize
			}
			binary.LittleEndian.PutUint32(b[i+1:], uint32(v))
		}
		i += 5
	}
}
