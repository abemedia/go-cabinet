package lzx

import (
	"encoding/binary"
	"math"
	"math/bits"
)

const (
	hash3Bits = 16

	// nilPos is the position of no data. It lies further back than any
	// window reaches, so searches stop at it.
	nilPos = math.MinInt32

	// searchDepth is the most tree nodes that a match search visits.
	searchDepth = 32

	// niceLength is the shortest match length that ends a match search, and
	// that the parse takes whatever the costs.
	niceLength = 128
)

// A match is an earlier occurrence of the data at some position.
type match struct {
	length uint32
	offset uint32
}

// matchFinder finds the earlier occurrences of the data at each position of
// buf, which holds the window. The positions whose data starts with the same
// 3-byte hash form a binary search tree, ordered by their data, that the
// search for a new position walks towards the longest matches before making
// the new position its root.
type matchFinder struct {
	buf       []byte
	head2     []int32    // the last position of each 2-byte sequence
	head3     []int32    // the root of the tree of each hash
	tree      [][2]int32 // the subtrees of smaller and larger data of each position
	mask      int        // maps positions to their place in tree
	maxOffset int
	scratch   []match
	pending   int // the first position not yet in the tree
}

// newMatchFinder returns a match finder for the data in buf with a sliding
// window of 2^window bytes. It must be reset before use.
func newMatchFinder(buf []byte, window int) matchFinder {
	return matchFinder{
		buf:   buf,
		head2: make([]int32, 1<<16),
		head3: make([]int32, 1<<hash3Bits),
		tree:  make([][2]int32, 1<<window),
		mask:  1<<window - 1,
		// The formatted offset, the offset plus 2, must be less than the
		// window size.
		maxOffset: 1<<window - 3,
		scratch:   make([]match, 0, maxMatch),
	}
}

// reset empties the match finder, as at the start of a stream.
func (mf *matchFinder) reset() {
	fill(mf.head2, nilPos)
	fill(mf.head3, nilPos)
	mf.pending = 0
}

// slide moves the positions n bytes back, after the data in buf has moved.
func (mf *matchFinder) slide(n int) {
	rebase(mf.head2, int32(n))
	rebase(mf.head3, int32(n))
	for i := range mf.tree {
		rebase(mf.tree[i][:], int32(n))
	}
	mf.pending -= n
}

// insertPending adds to the tree the positions before start that awaited the
// data up to end, and holds back those of the frame from start to end that lie
// within niceLength bytes of end.
func (mf *matchFinder) insertPending(start, end int) {
	for ; mf.pending < start && end-mf.pending >= niceLength; mf.pending++ {
		p := mf.pending
		mf.scratch = mf.searchTree(mf.scratch[:0], p, min(end-p, maxMatch), p-mf.maxOffset, 1)
	}
	if mf.pending == start {
		mf.pending = max(start, end-niceLength+1)
	}
}

// find appends to ms the matches of the data at p, up to end, in order of
// increasing length, and adds p to the tree unless it lies within niceLength
// bytes of end, which leaves it to insertPending.
func (mf *matchFinder) find(ms []match, p, end int) []match {
	buf := mf.buf
	maxLen := min(end-p, maxMatch)
	if maxLen < minMatch {
		return ms
	}
	minPos := p - mf.maxOffset

	best := 1
	h2 := binary.LittleEndian.Uint16(buf[p:])
	cur := int(mf.head2[h2])
	mf.head2[h2] = int32(p)
	if cur >= minPos {
		ms = append(ms, match{minMatch, uint32(p - cur)})
		best = minMatch
	}
	if maxLen < 3 {
		return ms
	}
	// The tree orders positions by their first niceLength bytes, so a position
	// is added only once they are known.
	if end-p < niceLength {
		return mf.search(ms, p, maxLen, minPos, best)
	}
	return mf.searchTree(ms, p, maxLen, minPos, best)
}

// skip adds p to the tree as find does, without reporting its matches.
func (mf *matchFinder) skip(p, end int) {
	mf.scratch = mf.find(mf.scratch[:0], p, end)
}

// searchTree appends to ms the matches of the data at p that are longer than
// best, up to maxLen, and from no further back than minPos, and makes p the
// root of its hash's tree.
func (mf *matchFinder) searchTree(ms []match, p, maxLen, minPos, best int) []match {
	buf := mf.buf
	h := hash3(buf[p:])
	cur := int(mf.head3[h])
	mf.head3[h] = int32(p)

	// The new root's subtrees are filled in as the search goes. lt is the
	// link to fill with the next node of smaller data, and gt with larger.
	tree := mf.tree
	mask := mf.mask
	lt, gt := &tree[p&mask][0], &tree[p&mask][1]
	ltLen, gtLen := 0, 0
	stop := min(niceLength, maxLen)
	for depth := searchDepth; ; depth-- {
		if cur < minPos || depth == 0 {
			*lt, *gt = nilPos, nilPos
			return ms
		}
		node := &tree[cur&mask]
		l := matchLen(buf, p, cur, min(ltLen, gtLen), maxLen)
		if l > best {
			best = l
			ms = append(ms, match{uint32(l), uint32(p - cur)})
			if l >= stop {
				// p takes the place of cur, whose data it matches as far as
				// the search compares.
				*lt, *gt = node[0], node[1]
				return ms
			}
		}
		if buf[cur+l] < buf[p+l] {
			*lt = int32(cur)
			lt = &node[1]
			ltLen = l
			cur = int(node[1])
		} else {
			*gt = int32(cur)
			gt = &node[0]
			gtLen = l
			cur = int(node[0])
		}
	}
}

// search is searchTree without adding p to the tree. maxLen must be less than
// niceLength.
func (mf *matchFinder) search(ms []match, p, maxLen, minPos, best int) []match {
	buf, tree, mask := mf.buf, mf.tree, mf.mask
	cur := int(mf.head3[hash3(buf[p:])])
	ltLen, gtLen := 0, 0
	for depth := searchDepth; cur >= minPos && depth > 0; depth-- {
		node := &tree[cur&mask]
		l := matchLen(buf, p, cur, min(ltLen, gtLen), maxLen)
		if l > best {
			best = l
			ms = append(ms, match{uint32(l), uint32(p - cur)})
			if l == maxLen {
				return ms
			}
		}
		if buf[cur+l] < buf[p+l] {
			ltLen = l
			cur = int(node[1])
		} else {
			gtLen = l
			cur = int(node[0])
		}
	}
	return ms
}

// hash3 returns the hash of the first 3 bytes of b, which must hold at least 4.
func hash3(b []byte) uint32 {
	return (binary.LittleEndian.Uint32(b) << 8) * 0x9E3779B1 >> (32 - hash3Bits)
}

// matchLen returns the length, up to maxLen, of the common prefix of the data
// at a and b, which is known to be at least n bytes long.
func matchLen(buf []byte, a, b, n, maxLen int) int {
	for n < maxLen {
		x := binary.LittleEndian.Uint64(buf[a+n:]) ^ binary.LittleEndian.Uint64(buf[b+n:])
		if x != 0 {
			return min(n+bits.TrailingZeros64(x)/8, maxLen)
		}
		n += 8
	}
	return maxLen
}

func fill[T any](s []T, v T) {
	for i := range s {
		s[i] = v
	}
}

// rebase moves the positions s n bytes back, setting those that would fall
// before the buffer to nilPos.
func rebase(s []int32, n int32) {
	for i, v := range s {
		if v >= n {
			s[i] = v - n
		} else {
			s[i] = nilPos
		}
	}
}
