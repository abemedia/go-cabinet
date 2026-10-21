package lzx

// An item is a literal or a match. It holds its length, which is 1 for a
// literal, in the low bits and a match's formatted offset above them.
type item uint32

const (
	itemLenBits = 9

	literal item = 1

	// chained marks the last item of a path that ends with a match, which may
	// be absent, a literal and then the item, a match with the same offset.
	chained item = 1 << 31
)

// matchItem returns the item of a match of the given length and formatted
// offset.
func matchItem(length int, formatted uint32) item {
	return item(formatted<<itemLenBits | uint32(length))
}

// length returns the length of it.
func (it item) length() int {
	return int(it & (1<<itemLenBits - 1))
}

// formatted returns the formatted offset of the match it, which must not be
// chained.
func (it item) formatted() uint32 {
	return uint32(it) >> itemLenBits
}

// mainSymbol returns the main tree symbol of the match it and its position
// slot.
func (it item) mainSymbol() (sym, slot int) {
	slot = positionSlot(it.formatted())
	return numChars + slot*numLenHeaders + min(it.length()-minMatch, numPrimaryLens), slot
}

// after returns the repeated offsets after the item it, which may be absent.
func (r recentOffsets) after(it item) recentOffsets {
	switch f := it.formatted(); {
	case it.length() <= 1:
		return r
	case f < numRecentOffsets:
		return r.swap(f)
	default:
		return r.push(f - 2)
	}
}
