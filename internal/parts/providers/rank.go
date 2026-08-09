package providers

import "strings"

// Score ranks one indexed part against a query. It is the same shape as the
// ranking parts.FuzzySearchDirs uses on the installed libraries — an exact
// name beats a prefix beats a mention — because a search that ordered remote
// hits differently from local ones would read as two different tools.
//
// Zero means "no match at all" and the caller drops the entry.
func Score(query, name, description, extra string) int {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return 0
	}
	lname := strings.ToLower(name)
	ldesc := strings.ToLower(description)
	lextra := strings.ToLower(extra)

	terms := strings.Fields(q)
	score := 0
	switch {
	case lname == q:
		score += 1000
	case normalizeMPN(lname) == normalizeMPN(q):
		// "LM2596S-5.0" vs "LM2596S 5.0" vs "lm2596s_5v0" are the same order
		// code written three ways, and a distributor will have used all three.
		score += 900
	case strings.HasPrefix(lname, q):
		score += 400
	case strings.Contains(lname, q):
		score += 250
	}
	for _, t := range terms {
		switch {
		case strings.Contains(lname, t):
			score += 100
		case strings.Contains(ldesc, t):
			score += 50
		case strings.Contains(lextra, t):
			score += 30
		}
	}
	if score == 0 {
		return 0
	}
	// Among equal matches the shorter name is the more generic part, which is
	// nearly always the one a plain-word search wanted.
	if penalty := len(name) / 8; penalty < 40 {
		score -= penalty
	}
	return score
}

// normalizeMPN reduces an order code to the characters that identify it:
// separators are noise that every catalogue punctuates differently.
func normalizeMPN(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}
