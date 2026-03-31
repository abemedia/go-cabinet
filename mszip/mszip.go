// Package mszip implements the MS-ZIP compression format as defined by
// [MS-MCI]: Microsoft ZIP (MSZIP) Compression and Decompression Data Structure
// https://learn.microsoft.com/en-us/openspecs/exchange_server_protocols/ms-mci/27f0a9bf-9567-4e40-ad66-6ae9ab9d2786
//
// Each compressed block is prefixed with the two-byte signature 0x43 0x4B ("CK")
// followed by a DEFLATE-compressed payload. The deflate encoder is reset for each
// block, but the previous block's uncompressed output (up to 32,768 bytes) is used
// as the preset dictionary for the next block.
package mszip

import "errors"

const blockSize = 1 << 15

var blockSig = [2]byte{'C', 'K'}

var errClosed = errors.New("mszip: read/write on closed stream")
