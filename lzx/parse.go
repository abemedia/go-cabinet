package lzx

import (
	"math"
	"math/bits"
)

// bitCost is the cost of one bit, allowing for fractional costs.
const bitCost = 64

// Codeword length limits used by the compressor. They are below the format's
// limits where that makes little difference to the compression ratio.
const (
	mainCodeLimit    = 16
	lengthCodeLimit  = 12
	alignedCodeLimit = 7
	precodeLimit     = 7
)

type costModel struct {
	main    [maxMainSyms]uint32
	length  [numLenSyms]uint32
	aligned [numAlignedSyms]uint32

	// match[slot][length-minMatch] is the cost of a match excluding any aligned
	// offset symbol.
	match [maxOffsetSlots][numLens]uint32
}

type recentOffsets [numRecentOffsets]uint32

func (q recentOffsets) push(offset uint32) recentOffsets {
	return recentOffsets{offset, q[0], q[1]}
}

func (q recentOffsets) swap(i uint32) recentOffsets {
	q[0], q[i] = q[i], q[0]
	return q
}

type arrival struct {
	length uint16 // match length, or 0 for a literal
	offset uint32 // formatted offset or recent offset index of a match, or the literal
}

type optimumNode struct {
	cost  uint32
	item  arrival
	gap   arrival // for a "gap match", the match preceding the gap literal
	queue recentOffsets
}

// literalScaledProbs maps the number of distinct literals to a heuristic
// probability of each literal, scaled by 6870.
// Element n is round(6870 * 2^-((304+n)/64)).
var literalScaledProbs = [257]uint8{
	255, 253, 250, 247, 244, 242, 239, 237, 234, 232, 229, 227, 224, 222,
	219, 217, 215, 212, 210, 208, 206, 203, 201, 199, 197, 195, 193, 191,
	189, 186, 184, 182, 181, 179, 177, 175, 173, 171, 169, 167, 166, 164,
	162, 160, 159, 157, 155, 153, 152, 150, 149, 147, 145, 144, 142, 141,
	139, 138, 136, 135, 133, 132, 130, 129, 128, 126, 125, 124, 122, 121,
	120, 118, 117, 116, 115, 113, 112, 111, 110, 109, 107, 106, 105, 104,
	103, 102, 101, 100, 98, 97, 96, 95, 94, 93, 92, 91, 90, 89, 88, 87, 86,
	86, 85, 84, 83, 82, 81, 80, 79, 78, 78, 77, 76, 75, 74, 73, 73, 72, 71,
	70, 70, 69, 68, 67, 67, 66, 65, 65, 64, 63, 62, 62, 61, 60, 60, 59, 59,
	58, 57, 57, 56, 55, 55, 54, 54, 53, 53, 52, 51, 51, 50, 50, 49, 49, 48,
	48, 47, 47, 46, 46, 45, 45, 44, 44, 43, 43, 42, 42, 41, 41, 40, 40, 40,
	39, 39, 38, 38, 38, 37, 37, 36, 36, 36, 35, 35, 34, 34, 34, 33, 33, 33,
	32, 32, 32, 31, 31, 31, 30, 30, 30, 29, 29, 29, 28, 28, 28, 27, 27, 27,
	27, 26, 26, 26, 25, 25, 25, 25, 24, 24, 24, 24, 23, 23, 23, 23, 22, 22,
	22, 22, 21, 21, 21, 21, 20, 20, 20, 20, 20, 19, 19, 19, 19, 19, 18, 18,
	18, 18, 18, 17, 17, 17, 17, 17, 16, 16, 16, 16,
}

// defaultLenCosts is the initial cost of each length symbol. Shorter lengths
// are cheaper because they are more common.
var defaultLenCosts = [numLenSyms]uint16{
	300, 310, 320, 330, 360, 396, 399, 416, 451, 448, 463, 466, 505, 492,
	503, 514, 547, 531, 566, 561, 589, 563, 592, 586, 623, 602, 639, 627,
	659, 643, 657, 650, 685, 662, 661, 672, 685, 686, 696, 680, 657, 682,
	666, 699, 674, 699, 679, 709, 688, 712, 692, 714, 694, 716, 698, 712,
	706, 727, 714, 727, 713, 723, 712, 718, 719, 719, 720, 735, 725, 735,
	728, 740, 727, 739, 727, 742, 716, 733, 733, 740, 738, 746, 737, 747,
	738, 745, 736, 748, 742, 749, 745, 749, 743, 748, 741, 752, 745, 752,
	747, 750, 747, 752, 748, 753, 750, 752, 753, 753, 749, 744, 752, 755,
	753, 756, 745, 748, 746, 745, 723, 757, 755, 758, 755, 758, 752, 757,
	754, 757, 755, 759, 755, 758, 753, 755, 755, 758, 757, 761, 755, 750,
	758, 759, 759, 760, 758, 751, 757, 757, 759, 759, 758, 759, 758, 761,
	750, 761, 758, 760, 759, 761, 758, 761, 760, 752, 759, 760, 759, 759,
	757, 762, 760, 761, 761, 748, 761, 760, 762, 763, 752, 762, 762, 763,
	762, 762, 763, 763, 762, 763, 762, 763, 762, 763, 763, 764, 763, 762,
	763, 762, 762, 762, 764, 764, 763, 764, 763, 763, 763, 762, 763, 763,
	762, 764, 764, 763, 762, 763, 763, 763, 763, 762, 764, 763, 762, 764,
	764, 763, 763, 765, 764, 764, 762, 763, 764, 765, 763, 764, 763, 764,
	762, 764, 764, 754, 763, 764, 763, 763, 762, 763, 584,
}

// setDefaultCosts derives the first pass's cost model from the frequencies of
// the greedy parse made while collecting matches, in which every match was
// tallied as main symbol numChars and as the aligned symbol of its offset.
func (c *compressor) setDefaultCosts() {
	numMatches := uint64(c.freqs.main[numChars])
	var numLiterals, numUsed uint64
	for _, f := range c.freqs.main[:numChars] {
		numLiterals += uint64(f)
		if f > 0 {
			numUsed++
		}
	}

	// A literal's probability is the average of its observed frequency and a
	// guess based on how many distinct literals there are, which is
	// (freq/items + scaled/6870) / 2 = num / den.
	items := numLiterals + numMatches
	scaled := uint64(literalScaledProbs[numUsed])
	den := 2 * 6870 * items
	matchNum := int64(den)
	for i, f := range c.freqs.main[:numChars] {
		if f == 0 {
			c.costs.main[i] = 11 * bitCost
			continue
		}
		num := uint64(f)*6870 + scaled*items
		c.costs.main[i] = probabilityCost(num, den)
		matchNum -= int64(num)
	}

	// The remaining probability, but at least 15%, is shared equally by the
	// match symbols.
	matchNum = max(matchNum, int64(den*15/100))
	matchCost := probabilityCost(uint64(matchNum), den*uint64(c.numMainSyms-numChars))
	for i := numChars; i < c.numMainSyms; i++ {
		c.costs.main[i] = matchCost
	}

	for i, cost := range defaultLenCosts {
		c.costs.length[i] = uint32(cost)
	}

	for i := range c.costs.aligned {
		f := uint64(c.freqs.aligned[(i+numAlignedSyms-(numRecentOffsets-1))%numAlignedSyms])
		if f == 0 {
			c.costs.aligned[i] = 2 * numAlignedBits * bitCost
		} else {
			c.costs.aligned[i] = probabilityCost(f, numMatches)
		}
	}
}

func (c *compressor) setCostsFromCodes(codes *codeSet) {
	for i, l := range codes.mainLens[:c.numMainSyms] {
		c.costs.main[i] = codeCost(l, mainCodeLimit)
	}
	for i, l := range codes.lenLens {
		c.costs.length[i] = codeCost(l, lengthCodeLimit)
	}
	for i, l := range codes.alignedLens {
		c.costs.aligned[i] = codeCost(l, alignedCodeLimit)
	}
}

func codeCost(length uint8, limit int) uint32 {
	if length == 0 {
		return uint32(limit) * bitCost
	}
	return uint32(length) * bitCost
}

func (c *compressor) computeMatchCosts() {
	slots := (c.numMainSyms - numChars) / numLenHeaders
	for slot := range slots {
		extra := uint32(extraBits[slot]) * bitCost
		if slot >= minAlignedSlot {
			extra -= numAlignedBits * bitCost
		}
		mainSym := numChars + slot*numLenHeaders
		costs := &c.costs.match[slot]
		for i := range numPrimaryLens {
			costs[i] = c.costs.main[mainSym+i] + extra
		}
		extra += c.costs.main[mainSym+numPrimaryLens]
		for i := numPrimaryLens; i < numLens; i++ {
			costs[i] = c.costs.length[i-numPrimaryLens] + extra
		}
	}
}

// probabilityCost returns the cost of an event with probability num/den, which
// is -log2(num/den) bits but at least one bit. num must not be 0.
func probabilityCost(num, den uint64) uint32 {
	if num >= den {
		return bitCost
	}
	return max(uint32((log2Fixed(den)-log2Fixed(num))>>10), bitCost)
}

// log2Fixed returns log2(x) with 16 fractional bits. It uses integer
// arithmetic only, so that the output does not depend on the platform.
func log2Fixed(x uint64) uint64 {
	n := bits.Len64(x) - 1
	m := x << (63 - n)
	result := uint64(n) << 16
	for i := 15; i >= 0; i-- {
		hi, _ := bits.Mul64(m, m)
		if hi >= 1<<63 {
			result |= 1 << i
			m = hi
		} else {
			m = hi << 1
		}
	}
	return result
}

// findMinCostPath finds the cheapest parse of buf[start:end] and returns the
// recent offsets at its end. As the recent offsets depend on the path taken,
// each node only remembers those of its cheapest path, which makes the result
// an approximation.
func (c *compressor) findMinCostPath(buf []byte, start, end int) recentOffsets {
	size := end - start
	if cap(c.nodes) < size+1 {
		c.nodes = make([]optimumNode, size+1)
	}
	c.nodes = c.nodes[:size+1]
	c.nodes[0] = optimumNode{queue: c.queue}
	for i := 1; i <= size; i++ {
		c.nodes[i].cost = math.MaxUint32
	}

	next := 0
	for i, n := range c.counts[:size] {
		if n > 0 {
			c.relaxMatches(buf, start, i, size, c.matches[next:next+int(n)])
			next += int(n)
		}
		c.relaxLiteral(buf[start+i], i)
	}
	return c.nodes[size].queue
}

// relaxMatches considers, for each length, only the first available of the
// recent offsets and then the found matches, which is usually the cheapest.
func (c *compressor) relaxMatches(buf []byte, start, i, size int, ms []match) {
	node := &c.nodes[i]
	pos := start + i
	maxLen := min(size-i, maxMatch)
	nextLen := minMatch

	for rep, offset := range node.queue {
		src := pos - int(offset)
		length := extend(buf, pos, src, 0, maxLen)
		if length < nextLen {
			continue
		}
		costs := &c.costs.match[rep]
		for ; nextLen <= length; nextLen++ {
			cost := node.cost + costs[nextLen-minMatch]
			if target := &c.nodes[i+nextLen]; cost <= target.cost {
				*target = optimumNode{cost: cost, item: arrival{uint16(nextLen), uint32(rep)}}
			}
		}
		if length == maxLen {
			return
		}
	}

	var cost uint32
	var last match
	for _, m := range ms {
		if int(m.length) < nextLen {
			continue
		}
		last = m
		formatted := m.offset + numRecentOffsets - 1
		base := node.cost
		if formatted >= positionBase[minAlignedSlot] {
			base += c.costs.aligned[formatted%numAlignedSyms]
		}
		costs := &c.costs.match[offsetSlot(formatted)]
		for end := min(int(m.length), maxLen); nextLen <= end; nextLen++ {
			cost = base + costs[nextLen-minMatch]
			if target := &c.nodes[i+nextLen]; cost < target.cost {
				*target = optimumNode{cost: cost, item: arrival{uint16(nextLen), formatted}}
			}
		}
		if nextLen > maxLen {
			return
		}
	}
	if last.length > 0 {
		c.relaxGapMatch(buf, pos, i, size, last, nextLen-1, cost)
	}
}

// relaxGapMatch considers following the match m of the given length with a
// literal and then a match at the same offset, which is a common pattern in
// which the cheapest path is not found by looking a single step ahead.
func (c *compressor) relaxGapMatch(buf []byte, pos, i, size int, m match, length int, cost uint32) {
	litPos := pos + length
	repPos := litPos + 1
	remaining := size - i - length - 1
	if remaining < minMatch {
		return
	}
	src := repPos - int(m.offset)
	if buf[repPos] != buf[src] || buf[repPos+1] != buf[src+1] {
		return
	}
	repLen := extend(buf, repPos, src, minMatch, min(remaining, maxMatch))
	cost += c.costs.main[buf[litPos]] + c.costs.match[0][repLen-minMatch]
	if target := &c.nodes[i+length+1+repLen]; cost < target.cost {
		*target = optimumNode{
			cost: cost,
			item: arrival{uint16(repLen), 0},
			gap:  arrival{uint16(length), m.offset + numRecentOffsets - 1},
		}
	}
}

// relaxLiteral also finalizes the recent offsets of position i+1, as every
// way of reaching it has now been considered.
func (c *compressor) relaxLiteral(lit byte, i int) {
	node := &c.nodes[i]
	next := &c.nodes[i+1]
	if cost := node.cost + c.costs.main[lit]; cost <= next.cost {
		*next = optimumNode{cost: cost, queue: node.queue}
		return
	}

	from := i + 1 - int(next.item.length)
	switch {
	case next.gap.length > 0:
		from -= 1 + int(next.gap.length)
		next.queue = c.nodes[from].queue.push(next.gap.offset - (numRecentOffsets - 1))
	case next.item.offset >= numRecentOffsets:
		next.queue = c.nodes[from].queue.push(next.item.offset - (numRecentOffsets - 1))
	default:
		next.queue = c.nodes[from].queue.swap(next.item.offset)
	}
}

// walkPath collects the items of the cheapest path into c.items, in reverse.
func (c *compressor) walkPath(buf []byte, start, end int) {
	c.items = c.items[:0]
	for i := end - start; i > 0; {
		node := &c.nodes[i]
		if node.item.length == 0 {
			c.items = append(c.items, arrival{0, uint32(buf[start+i-1])})
			i--
			continue
		}
		c.items = append(c.items, node.item)
		i -= int(node.item.length)
		if node.gap.length > 0 {
			c.items = append(c.items, arrival{0, uint32(buf[start+i-1])}, node.gap)
			i -= 1 + int(node.gap.length)
		}
	}
}

func (c *compressor) tallyPath() {
	c.freqs = frequencies{}
	for _, it := range c.items {
		if it.length == 0 {
			c.freqs.main[it.offset]++
			continue
		}
		c.freqs.main[mainSymbol(it)]++
		if it.length >= minSecondaryLen {
			c.freqs.length[it.length-minSecondaryLen]++
		}
		if it.offset >= positionBase[minAlignedSlot] {
			c.freqs.aligned[it.offset%numAlignedSyms]++
		}
	}
}

func mainSymbol(m arrival) int {
	header := min(int(m.length)-minMatch, numPrimaryLens)
	return numChars + offsetSlot(m.offset)*numLenHeaders + header
}
