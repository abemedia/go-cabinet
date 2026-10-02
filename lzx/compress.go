package lzx

const (
	// minBlockSize is the fewest bytes a block ended before the end of its frame
	// may hold, and the fewest it may leave for the rest of the frame.
	minBlockSize = 6500

	// observationsPerCheck is the number of literals and matches observed
	// between checks of whether to end the block.
	observationsPerCheck = 400

	// maxCachedMatches limits the matches remembered for a block. The block is
	// ended early when it is reached.
	maxCachedMatches = 500000

	niceMatchLen       = 67
	maxSearchDepth     = 33
	optimizationPasses = 3
)

type compressor struct {
	numMainSyms int
	maxOffset   int

	mf      *matchFinder
	counts  []uint16 // number of matches cached for each block position
	matches []match
	nodes   []optimumNode
	items   []arrival
	precode []precodeItem

	freqs   frequencies
	costs   costModel
	codes   [2]codeSet // current and previous block's codes
	cur     int
	queue   recentOffsets
	split   splitStats
	builder codeBuilder
	bw      bitWriter

	nextSearch      int
	nextObservation int
}

type frequencies struct {
	main    [maxMainSyms]uint32
	length  [numLenSyms]uint32
	aligned [numAlignedSyms]uint32
}

func newCompressor(window int) *compressor {
	c := &compressor{
		numMainSyms: numMainSyms(window),
		maxOffset:   1<<window - 3,
		mf:          new(matchFinder),
	}
	c.reset()
	return c
}

// reset prepares the compressor for a new stream.
func (c *compressor) reset() {
	c.mf.reset()
	c.codes = [2]codeSet{}
	c.cur = 0
	c.queue = recentOffsets{1, 1, 1}
}

// compressFrame appends buf[start:], compressed as one frame, to dst. The
// data before start is the history that matches may refer to.
func (c *compressor) compressFrame(dst, buf []byte, start int) []byte {
	c.bw = bitWriter{buf: dst}
	if start == 0 {
		c.bw.writeBits(1, 1)
		c.bw.writeBits(e8FileSize>>16, 16)
		c.bw.writeBits(e8FileSize, 16)
	}
	c.mf.grow(len(buf))
	// The last positions of a frame are not inserted until the data that
	// follows them is known. See matchFinder.
	for pos := c.mf.next; pos < start && len(buf)-pos >= niceMatchLen; pos++ {
		minPos := max(0, pos-c.maxOffset)
		c.mf.advance(buf, pos, minPos, niceMatchLen, niceMatchLen, true, false, nil)
	}
	for pos := start; pos < len(buf); {
		blockEnd := c.collectMatches(buf, pos)
		c.compressBlock(buf, pos, blockEnd)
		pos = blockEnd
	}
	c.bw.align()
	return c.bw.buf
}

// collectMatches caches the matches at each position from start until it
// decides to end the block, and returns where. It also tallies the frequencies
// of a greedy parse, which seed the block's cost model.
func (c *compressor) collectMatches(buf []byte, start int) int {
	c.counts = c.counts[:0]
	c.matches = c.matches[:0]
	c.freqs = frequencies{}
	c.split = splitStats{}
	c.nextSearch = start
	c.nextObservation = start

	pos := start
	for pos < len(buf) && len(c.matches) < maxCachedMatches {
		c.findMatches(buf, pos)
		pos++
		if pos-start >= minBlockSize && len(buf)-pos >= minBlockSize && c.split.shouldEndBlock() {
			break
		}
	}
	return pos
}

func (c *compressor) findMatches(buf []byte, pos int) {
	maxLen := min(maxMatch, len(buf)-pos)
	if maxLen < matchFinderMinBytes {
		c.counts = append(c.counts, 0)
		return
	}
	niceLen := min(niceMatchLen, maxLen)
	minPos := max(0, pos-c.maxOffset)
	insert := pos == c.mf.next && niceLen == niceMatchLen

	if pos < c.nextSearch {
		if insert {
			c.mf.advance(buf, pos, minPos, niceLen, niceLen, true, false, nil)
		}
		c.counts = append(c.counts, 0)
		return
	}

	n := len(c.matches)
	c.matches = c.mf.advance(buf, pos, minPos, maxLen, niceLen, insert, true, c.matches)
	found := c.matches[n:]
	c.counts = append(c.counts, uint16(len(found)))

	bestLen := 0
	if len(found) > 0 {
		bestLen = int(found[len(found)-1].length)
	}
	if pos >= c.nextObservation {
		if bestLen >= 3 {
			c.freqs.main[numChars]++
			c.freqs.aligned[found[len(found)-1].offset%numAlignedSyms]++
			c.split.observeMatch(bestLen)
			c.nextObservation = pos + bestLen
		} else {
			c.freqs.main[buf[pos]]++
			c.split.observeLiteral(buf[pos])
			c.nextObservation = pos + 1
		}
	}
	if bestLen >= niceLen {
		c.nextSearch = pos + bestLen
	}
}

// compressBlock refines the parse of buf[start:end] over several passes, as
// the cost of each symbol depends on the codes that the parse produces.
func (c *compressor) compressBlock(buf []byte, start, end int) {
	c.setDefaultCosts()
	var queue recentOffsets
	for pass := 1; ; pass++ {
		c.computeMatchCosts()
		queue = c.findMinCostPath(buf, start, end)
		c.walkPath(buf, start, end)
		c.tallyPath()
		if pass == optimizationPasses {
			break
		}
		c.buildCodes(&c.codes[c.cur])
		c.setCostsFromCodes(&c.codes[c.cur])
	}
	c.writeBlock(end - start)
	c.queue = queue
}

// splitStats ends a block when the distribution of recently observed literals
// and matches drifts from the distribution so far. Literals are classified by
// their top two and lowest bits, and matches by whether they are long.
type splitStats struct {
	newObs [10]uint32
	obs    [10]uint32
	numNew uint32
	num    uint32
}

func (s *splitStats) observeLiteral(b byte) {
	s.newObs[(b>>5)&6|b&1]++
	s.numNew++
}

func (s *splitStats) observeMatch(length int) {
	if length >= 5 {
		s.newObs[9]++
	} else {
		s.newObs[8]++
	}
	s.numNew++
}

func (s *splitStats) shouldEndBlock() bool {
	if s.numNew < observationsPerCheck {
		return false
	}
	if s.num > 0 {
		var delta uint32
		for i := range s.obs {
			expected := s.obs[i] * s.numNew
			actual := s.newObs[i] * s.num
			delta += max(expected, actual) - min(expected, actual)
		}
		if delta >= s.numNew*7/8*s.num {
			return true
		}
	}
	for i := range s.obs {
		s.obs[i] += s.newObs[i]
		s.newObs[i] = 0
	}
	s.num += s.numNew
	s.numNew = 0
	return false
}
