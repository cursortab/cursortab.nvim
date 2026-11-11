package text

// maxLevenshteinWork bounds the work of one distance computation, in 64-bit
// words processed. Past it, the distance is taken as its upper bound, the
// length of the longer middle part. This keeps huge lines, such as minified
// code, from stalling the diff.
const maxLevenshteinWork = 1 << 16

// levenshtein returns the edit distance in runes between a and b.
//
// It removes the common prefix and suffix, then runs the bit-parallel
// algorithm of Myers (1999) in its block form, which costs one pass over the
// longer part per 64 runes of the shorter part.
func levenshtein(a, b string) int {
	text, pattern := []rune(a), []rune(b)
	for len(text) > 0 && len(pattern) > 0 && text[0] == pattern[0] {
		text, pattern = text[1:], pattern[1:]
	}
	for len(text) > 0 && len(pattern) > 0 && text[len(text)-1] == pattern[len(pattern)-1] {
		text, pattern = text[:len(text)-1], pattern[:len(pattern)-1]
	}
	if len(text) < len(pattern) {
		text, pattern = pattern, text
	}
	m := len(pattern)
	if m == 0 {
		return len(text)
	}
	words := (m + 63) / 64
	if words*len(text) > maxLevenshteinWork {
		return len(text)
	}

	// peq holds, per rune, the bit mask of its positions in the pattern.
	ascii := make([]uint64, 128*words)
	var other map[rune][]uint64
	for i, r := range pattern {
		bit := uint64(1) << (i % 64)
		if r < 128 {
			ascii[int(r)*words+i/64] |= bit
			continue
		}
		if other == nil {
			other = map[rune][]uint64{}
		}
		if other[r] == nil {
			other[r] = make([]uint64, words)
		}
		other[r][i/64] |= bit
	}
	peq := func(r rune) []uint64 {
		if r < 128 {
			return ascii[int(r)*words : int(r)*words+words]
		}
		return other[r]
	}

	// pv and mv hold the positive and negative vertical deltas of the current
	// column of the distance matrix.
	pv, mv := make([]uint64, words), make([]uint64, words)
	for i := range pv {
		pv[i] = ^uint64(0)
	}
	lastBit := uint64(1) << ((m - 1) % 64)
	score := m
	for _, r := range text {
		eqs := peq(r)
		hin := 1 // The top row grows by one per column
		for w := range words {
			var eq uint64
			if eqs != nil {
				eq = eqs[w]
			}
			p, n := pv[w], mv[w]
			xv := eq | n
			if hin < 0 {
				eq |= 1
			}
			xh := (((eq & p) + p) ^ p) | eq
			ph := n | ^(xh | p)
			mh := p & xh

			high := uint64(1) << 63
			if w == words-1 {
				high = lastBit
			}
			hout := 0
			if ph&high != 0 {
				hout = 1
			} else if mh&high != 0 {
				hout = -1
			}

			ph <<= 1
			mh <<= 1
			if hin < 0 {
				mh |= 1
			} else if hin > 0 {
				ph |= 1
			}
			pv[w] = mh | ^(xv | ph)
			mv[w] = ph & xv
			hin = hout
		}
		score += hin
	}
	return score
}
