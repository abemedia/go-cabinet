package lzx

import (
	"encoding/binary"
	"math/bits"
)

const (
	hash2Bits = 12
	hash3Bits = 15
	hash4Bits = 16

	// matchFinderMinBytes is the number of bytes that must be available from
	// a position for the matchfinder to process it.
	matchFinderMinBytes = 5
)

type match struct {
	length uint32
	offset uint32
}

// matchFinder finds matches using hash tables for lengths 2 and 3, and for
// longer matches a hash table of binary trees. Each tree holds the positions
// whose first 4 bytes share a hash, ordered lexicographically by the data at
// those positions, and is re-rooted at the current position whenever it is
// searched. Positions are indices into the caller's buffer, and -1 marks an
// empty slot.
//
// Positions must be inserted in order, and the trees treat positions that
// share their first niceLen bytes as equal. A position may therefore only be
// inserted once at least niceLen bytes following it are known, or the trees
// could become misordered and report incorrect match lengths.
type matchFinder struct {
	hash2 [1 << hash2Bits]int32
	hash3 [1 << hash3Bits][2]int32
	hash4 [1 << hash4Bits]int32
	child []int32 // left and right child of each position
	next  int     // next position to insert
}

func (mf *matchFinder) reset() {
	fill(mf.hash2[:], -1)
	for i := range mf.hash3 {
		mf.hash3[i] = [2]int32{-1, -1}
	}
	fill(mf.hash4[:], -1)
	mf.child = mf.child[:0]
	mf.next = 0
}

func (mf *matchFinder) grow(n int) {
	if len(mf.child) < 2*n {
		mf.child = append(mf.child, make([]int32, 2*n-len(mf.child))...)
	}
}

// slide follows the caller discarding the first n bytes of its buffer.
func (mf *matchFinder) slide(n int) {
	kept := copy(mf.child, mf.child[2*n:])
	mf.child = mf.child[:kept]
	rebase(mf.hash2[:], n)
	for i := range mf.hash3 {
		rebase(mf.hash3[i][:], n)
	}
	rebase(mf.hash4[:], n)
	rebase(mf.child, n)
	mf.next -= n
}

// advance appends the matches at pos to ms if record is set, in order of
// strictly increasing length, and inserts pos if insert is set. maxLen must be
// at least matchFinderMinBytes.
func (mf *matchFinder) advance(
	buf []byte,
	pos, minPos, maxLen, niceLen int,
	insert, record bool,
	ms []match,
) []match {
	p := int32(pos)
	if insert {
		mf.next = pos + 1
	}

	seq2 := binary.LittleEndian.Uint16(buf[pos:])
	h2 := hash(uint32(seq2), hash2Bits)
	cur := mf.hash2[h2]
	if insert {
		mf.hash2[h2] = p
	}
	if record && int(cur) >= minPos && binary.LittleEndian.Uint16(buf[cur:]) == seq2 {
		ms = append(ms, match{2, uint32(pos - int(cur))})
	}

	seq4 := binary.LittleEndian.Uint32(buf[pos:])
	h3 := hash(seq4&0xFFFFFF, hash3Bits)
	cur, cur2 := mf.hash3[h3][0], mf.hash3[h3][1]
	if insert {
		mf.hash3[h3] = [2]int32{p, cur}
	}
	if record && int(cur) >= minPos {
		switch {
		case load24(buf, int(cur)) == seq4&0xFFFFFF:
			ms = append(ms, match{3, uint32(pos - int(cur))})
		case int(cur2) >= minPos && load24(buf, int(cur2)) == seq4&0xFFFFFF:
			ms = append(ms, match{3, uint32(pos - int(cur2))})
		}
	}

	h4 := hash(seq4, hash4Bits)
	cur = mf.hash4[h4]
	if insert {
		mf.hash4[h4] = p
	}
	return mf.searchTree(buf, pos, int(cur), minPos, maxLen, niceLen, insert, record, ms)
}

func (mf *matchFinder) searchTree(
	buf []byte,
	pos, node, minPos, maxLen, niceLen int,
	insert, record bool,
	ms []match,
) []match {
	lt, gt := 2*pos, 2*pos+1
	bestLt, bestGt, length := 0, 0, 0
	bestLen := 3
	for depth := maxSearchDepth; node >= minPos && depth > 0; depth-- {
		if buf[node+length] == buf[pos+length] {
			length = extend(buf, pos, node, length+1, maxLen)
			if record && length > bestLen {
				bestLen = length
				ms = append(ms, match{uint32(length), uint32(pos - node)})
			}
			if length >= niceLen {
				if insert {
					mf.child[lt] = mf.child[2*node]
					mf.child[gt] = mf.child[2*node+1]
				}
				return ms
			}
		}
		if buf[node+length] < buf[pos+length] {
			if insert {
				mf.child[lt] = int32(node)
			}
			lt = 2*node + 1
			node = int(mf.child[lt])
			bestLt = length
			length = min(length, bestGt)
		} else {
			if insert {
				mf.child[gt] = int32(node)
			}
			gt = 2 * node
			node = int(mf.child[gt])
			bestGt = length
			length = min(length, bestLt)
		}
	}
	if insert {
		mf.child[lt] = -1
		mf.child[gt] = -1
	}
	return ms
}

func hash(seq uint32, n uint) uint32 {
	return seq * 0x1E35A7BD >> (32 - n)
}

func load24(buf []byte, i int) uint32 {
	return binary.LittleEndian.Uint32(buf[i:]) & 0xFFFFFF
}

func extend(buf []byte, a, b, n, limit int) int {
	for n+8 <= limit {
		if x := binary.LittleEndian.Uint64(buf[a+n:]) ^ binary.LittleEndian.Uint64(buf[b+n:]); x != 0 {
			return n + bits.TrailingZeros64(x)/8
		}
		n += 8
	}
	for n < limit && buf[a+n] == buf[b+n] {
		n++
	}
	return n
}

func fill(s []int32, v int32) {
	for i := range s {
		s[i] = v
	}
}

func rebase(s []int32, n int) {
	for i, p := range s {
		if int(p) < n {
			s[i] = -1
		} else {
			s[i] = p - int32(n)
		}
	}
}
