package lzx

// A token is a literal or a match. It holds its length, which is 1 for a
// literal, in the low bits and a match's formatted offset above them.
type token uint32

const (
	tokenLenBits = 9

	literal token = 1

	// chained marks a match at offset R0 that follows a literal. The parse
	// takes it, the literal and the match just before the literal, if any, as
	// one step of a path.
	chained token = 1 << 31
)

// matchToken returns the token of a match of the given length and formatted
// offset.
func matchToken(length int, formatted uint32) token {
	return token(formatted<<tokenLenBits | uint32(length))
}

// length returns the length of t.
func (t token) length() int {
	return int(t & (1<<tokenLenBits - 1))
}

// formatted returns the formatted offset of the match t, which must not be
// chained.
func (t token) formatted() uint32 {
	return uint32(t) >> tokenLenBits
}

// mainSymbol returns the main tree symbol of the match t and its position
// slot.
func (t token) mainSymbol() (sym, slot int) {
	slot = positionSlot(t.formatted())
	return numChars + slot*numLenHeaders + min(t.length()-minMatch, numPrimaryLens), slot
}
