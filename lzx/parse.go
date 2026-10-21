package lzx

import (
	"encoding/binary"
	"math"
	"math/bits"
	"slices"
)

const (
	// costShift sets the unit of costs to 1/2^costShift bit.
	costShift = 4

	// insertTail is how many of the positions within a match of at least
	// niceLength bytes, counted from its end, are added to the match finder.
	insertTail = 32

	// longLength is the length of match from which the parse skips all but
	// the last longTail positions of the match.
	longLength = 64
	longTail   = 16

	// unusedCost is the estimated path length of a symbol absent from the code
	// that the parse costs symbols by.
	unusedCost = 14

	// The parse adds these costs, in units of 1/2^costShift bit, to each match
	// and literal, so that of paths of nearly equal size it prefers those that
	// take less time to decode.
	matchPenalty   = 16
	literalPenalty = 4
)

// costModel holds the estimated cost of each symbol of a block.
type costModel struct {
	main    [maxMainSymbols]uint32
	length  [numSecondaryLens]uint32
	aligned [numAlignedSymbols]uint32

	alignedBlock bool
}

// set sets the costs to the path lengths of c.
func (m *costModel) set(c *blockCode) {
	setCosts(m.main[:], c.mainLens[:], unusedCost)
	setCosts(m.length[:], c.lenLens[:], unusedCost)
	setCosts(m.aligned[:], c.alignedLens[:], maxAlignedLen)
	for i := range m.main {
		m.main[i] += symbolPenalty(i)
	}
	m.alignedBlock = c.aligned
}

// setDefault sets the costs to estimates for a stream's first block, which no
// code precedes.
func (m *costModel) setDefault() {
	for i := range m.main {
		m.main[i] = 9<<costShift + symbolPenalty(i)
	}
	fill(m.length[:], 6<<costShift)
	fill(m.aligned[:], numAlignedBits<<costShift)
	m.alignedBlock = false
}

// cost returns the cost of the symbols that s counts under the code c.
func (m *costModel) cost(c *blockCode, s *symbolCounts) uint32 {
	n := uint32(c.symbolBits(s, c.aligned)) << costShift
	for i, f := range s.main {
		n += f * symbolPenalty(i)
	}
	return n
}

// setCosts sets costs to the path lengths lens, and to unused for the symbols
// that the code lacks.
func setCosts(costs []uint32, lens []uint8, unused uint8) {
	for i, l := range lens {
		if l == 0 {
			l = unused
		}
		costs[i] = uint32(l) << costShift
	}
}

// symbolPenalty returns the cost that the parse adds to the main tree symbol
// sym for the time its token takes to decode.
func symbolPenalty(sym int) uint32 {
	if sym < numChars {
		return literalPenalty
	}
	return matchPenalty
}

// footer returns the cost of the position footer of a formatted offset in the
// given position slot.
func (m *costModel) footer(slot int, formatted uint32) uint32 {
	n := uint32(extraBits[slot])
	if m.alignedBlock && n >= numAlignedBits {
		return (n-numAlignedBits)<<costShift + m.aligned[formatted%numAlignedSymbols]
	}
	return n << costShift
}

// matchCost returns the cost of the main and length tree symbols of a match
// of length l. sym is the main tree symbol of the match with length header 0.
func (m *costModel) matchCost(sym, l int) uint32 {
	if l < minSecondaryLen {
		return m.main[sym+l-minMatch]
	}
	return m.main[sym+numPrimaryLens] + m.length[l-minSecondaryLen]
}

// parser chooses the tokens that code a frame. It finds the matches of every
// position once, and then finds the cheapest path through the frame under a
// cost model, which may be refined from the result and the path found again.
type parser struct {
	buf      []byte
	start, n int

	matches []match
	first   [frameSize + 1]int32 // the matches of position i are matches[first[i]:first[i+1]]
	model   costModel

	// The cheapest path found so far to each position.
	cost  [frameSize + 1]uint32
	last  [frameSize + 1]token
	prior [frameSize + 1]token           // the match before the literal before a chained last token
	reps  [frameSize + 1]repeatedOffsets // repeated offsets on arrival at each position

	tokens []token
}

func newParser() *parser {
	return &parser{
		matches: make([]match, 0, 8*frameSize),
		tokens:  make([]token, 0, frameSize),
	}
}

// findMatches finds the matches of the n bytes at buf[start:] at the positions
// that the parse visits. It adds the others to the match finder too.
func (p *parser) findMatches(mf *matchFinder, start, n int) {
	ms := p.matches[:0]
	first := p.first[:n+1]
	end := start + n
	mf.insertPending(start, end)
	for i := 0; i < n; {
		first[i] = int32(len(ms))
		ms = mf.find(ms, start+i, end)
		l := longestLen(ms[first[i]:])
		s := step(l)
		for j := 1; j < s; j++ {
			first[i+j] = int32(len(ms))
			if l < niceLength || j >= s-insertTail {
				mf.skip(start+i+j, end)
			}
		}
		i += s
	}
	first[n] = int32(len(ms))
	p.matches = ms
}

// longestLen returns the length of the last and longest of ms, or 0 if ms is
// empty.
func longestLen(ms []match) int {
	if len(ms) == 0 {
		return 0
	}
	return int(ms[len(ms)-1].length)
}

// step returns how far the parse moves on from a position whose longest match
// has length l.
func step(l int) int {
	switch {
	case l >= niceLength:
		return l
	case l >= longLength:
		return l - longTail
	default:
		return 1
	}
}

// parse finds the cheapest path through the n bytes at buf[start:], starting
// with the given repeated offsets, and stores its tokens in p.tokens. It
// returns the repeated offsets at the end of the path.
func (p *parser) parse(buf []byte, start, n int, repeated repeatedOffsets) repeatedOffsets {
	p.buf, p.start, p.n = buf, start, n
	cost, last := p.cost[:n+1], p.last[:n+1]
	prior, reps := p.prior[:n+1], p.reps[:n+1]
	fill(cost, math.MaxUint32)
	cost[0], last[0], reps[0] = 0, 0, repeated
	for i := 0; ; {
		t := last[i]
		j := i - t.length()
		if t&chained != 0 {
			// The chained match repeats R0, which leaves the offsets as they
			// are after the match before its literal.
			t = prior[i]
			j -= 1 + t.length()
		}
		rep := reps[j].after(t)
		reps[i] = rep
		if i == n {
			break
		}
		c := cost[i]
		pos := start + i
		ms := p.matches[p.first[i]:p.first[i+1]]
		longest := longestLen(ms)

		// diffs holds the differences between the 8 bytes at pos and those at
		// each repeated offset, which also tell whether the data after a short
		// match and a literal repeats.
		var repLens [numRepeatedOffsets]int
		var diffs [numRepeatedOffsets]uint64
		data := binary.LittleEndian.Uint64(buf[pos:])
		for k, r := range rep {
			if int(r) > pos || k > 0 && r == rep[0] || k > 1 && r == rep[1] {
				continue
			}
			x := data ^ binary.LittleEndian.Uint64(buf[pos-int(r):])
			diffs[k] = x
			switch {
			case x&0xffff != 0:
			case x != 0:
				repLens[k] = min(bits.TrailingZeros64(x)/8, n-i, maxMatch)
			default:
				repLens[k] = matchLen(buf, pos, pos-int(r), 8, min(n-i, maxMatch))
			}
		}

		if longest >= niceLength {
			p.takeLongMatch(i, c, ms[len(ms)-1], repLens)
			i += longest
			continue
		}

		if lc := c + p.model.main[buf[pos]]; lc < cost[i+1] {
			cost[i+1] = lc
			last[i+1] = literal
		}
		if int(rep[0]) <= pos && i+1+minMatch <= n && diffs[0]>>8&0xffff == 0 {
			p.relaxRep0(i, c, 0, int(rep[0]))
		}
		p.relaxRepeats(i, c, &rep, &repLens, &diffs)
		p.relaxMatches(i, c, &rep, ms)
		i += step(longest)
	}
	p.collect(n)
	return reps[n]
}

// takeLongMatch takes the match mt from position i, which costs c to reach,
// preferring a repeated offset that matches at least as far.
func (p *parser) takeLongMatch(i int, c uint32, mt match, repLens [numRepeatedOffsets]int) {
	m := &p.model
	l := int(mt.length)
	formatted := mt.offset + 2
	slot := positionSlot(formatted)
	best := matchToken(l, formatted)
	cost := c + m.footer(slot, formatted) + m.matchCost(numChars+slot*numLenHeaders, l)
	for k, rl := range repLens {
		if rl < l {
			continue
		}
		if rc := c + m.matchCost(numChars+k*numLenHeaders, l); rc < cost {
			best, cost = matchToken(l, uint32(k)), rc
		}
	}
	if cost < p.cost[i+l] {
		p.cost[i+l] = cost
		p.last[i+l] = best
	}
}

// relaxRep0 offers the path from position i through the match mt, then a
// literal, then a match at the given offset, which the 2 bytes after the
// literal must repeat. mt may be absent, and c is the cost of reaching i and
// coding mt.
func (p *parser) relaxRep0(i int, c uint32, mt token, offset int) {
	q := mt.length() + 1
	a := p.start + i + q
	l := matchLen(p.buf, a, a-offset, minMatch, min(p.n-i-q, maxMatch))
	c += p.model.main[p.buf[a-1]] + p.model.matchCost(numChars, l)
	if x := i + q + l; c < p.cost[x] {
		p.cost[x] = c
		p.last[x] = matchToken(l, 0) | chained
		p.prior[x] = mt
	}
}

// relaxRepeats offers paths from position i, which costs c to reach, through
// the matches with the repeated offsets rep of the lengths lens, whose data
// differs from that at i by diffs.
func (p *parser) relaxRepeats(
	i int, c uint32, rep *repeatedOffsets, lens *[numRepeatedOffsets]int, diffs *[numRepeatedOffsets]uint64,
) {
	for k, l := range lens {
		if l < minMatch {
			continue
		}
		sym := numChars + k*numLenHeaders
		p.relax(i, c, sym, minMatch, l, uint32(k))
		if i+l+1+minMatch > p.n {
			continue
		}
		var repeats bool
		if l+1+minMatch <= 8 {
			repeats = diffs[k]>>(8*(l+1))&0xffff == 0
		} else {
			repeats = repeats2(p.buf, p.start+i+l+1, int(rep[k]))
		}
		if repeats {
			p.relaxRep0(i, c+p.model.matchCost(sym, l), matchToken(l, uint32(k)), int(rep[k]))
		}
	}
}

// relaxMatches offers paths from position i, which costs c to reach, through
// the matches ms, except those with a repeated offset of rep, which are
// cheaper to code as such.
func (p *parser) relaxMatches(i int, c uint32, rep *repeatedOffsets, ms []match) {
	m := &p.model
	pos := p.start + i
	lo := minMatch
	for _, mt := range ms {
		l := int(mt.length)
		if mt.offset != rep[0] && mt.offset != rep[1] && mt.offset != rep[2] {
			formatted := mt.offset + 2
			slot := positionSlot(formatted)
			sym := numChars + slot*numLenHeaders
			fc := c + m.footer(slot, formatted)
			p.relax(i, fc, sym, lo, l, formatted)
			if i+l+1+minMatch <= p.n && repeats2(p.buf, pos+l+1, int(mt.offset)) {
				p.relaxRep0(i, fc+m.matchCost(sym, l), matchToken(l, formatted), int(mt.offset))
			}
		}
		lo = l + 1
	}
}

// relax offers paths from position i through matches of lengths lo to hi at
// the given formatted offset. c is the cost of reaching i and coding the
// match's position footer, and sym is the main tree symbol of the match with
// length header 0.
func (p *parser) relax(i int, c uint32, sym, lo, hi int, formatted uint32) {
	cost := p.cost[i : i+hi+1]
	last := p.last[i : i+hi+1]
	headers := p.model.main[sym : sym+numLenHeaders]
	for l := lo; l <= min(hi, minSecondaryLen-1); l++ {
		if lc := c + headers[l-minMatch]; lc < cost[l] {
			cost[l] = lc
			last[l] = matchToken(l, formatted)
		}
	}
	if hi < minSecondaryLen {
		return
	}
	c += headers[numPrimaryLens]
	lens := p.model.length[:hi-minSecondaryLen+1]
	for l := max(lo, minSecondaryLen); l <= hi; l++ {
		if lc := c + lens[l-minSecondaryLen]; lc < cost[l] {
			cost[l] = lc
			last[l] = matchToken(l, formatted)
		}
	}
}

// repeats2 reports whether the 2 bytes at a in buf repeat at the given offset.
func repeats2(buf []byte, a, offset int) bool {
	return binary.LittleEndian.Uint16(buf[a:]) == binary.LittleEndian.Uint16(buf[a-offset:])
}

// collect stores the tokens of the cheapest path to position n in p.tokens.
func (p *parser) collect(n int) {
	tokens := p.tokens[:0]
	for i := n; i > 0; {
		t := p.last[i]
		if t&chained == 0 {
			tokens = append(tokens, t)
			i -= t.length()
			continue
		}
		mt := p.prior[i]
		tokens = append(tokens, t&^chained, literal)
		if mt != 0 {
			tokens = append(tokens, mt)
		}
		i -= t.length() + 1 + mt.length()
	}
	slices.Reverse(tokens)
	p.tokens = tokens
}
