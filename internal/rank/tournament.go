package rank

import (
	"context"
	"sync"
)

// TournamentRank picks the best candidate among any K ≥ 2 images.
//
//   K=2 → 1 API call.
//   K=3 → 3 round-robin calls in parallel; majority vote breaks ties; if a
//         cycle (A>B, B>C, C>A) is detected the result is "no opinion".
//   K>3 → caller should pre-filter to top-3 by geometric score before calling
//         this. We still accept it but only consider the first 3.
//
// Returns -1 when the API key is empty, the round-robin produces a cycle, or
// any per-pair call fails consistently. Callers should fall back to their
// geometric scorer in those cases.
func TournamentRank(ctx context.Context, apiKey string, candidates []ImageCandidate) (winnerIdx int, reason string, err error) {
	if apiKey == "" {
		return -1, "", nil
	}
	if len(candidates) < 2 {
		return -1, "", nil
	}
	if len(candidates) == 2 {
		return RankPairwise(ctx, apiKey, candidates)
	}
	// Cap at 3 — beyond that the round-robin grows quadratically and the
	// caller is supposed to pre-filter.
	if len(candidates) > 3 {
		candidates = candidates[:3]
	}

	type pair struct{ i, j int }
	pairs := []pair{{0, 1}, {0, 2}, {1, 2}}

	type res struct {
		pair    pair
		winner  int
		reason  string
		ok      bool
	}
	results := make(chan res, len(pairs))
	var wg sync.WaitGroup
	for _, p := range pairs {
		wg.Add(1)
		go func(p pair) {
			defer wg.Done()
			subset := []ImageCandidate{candidates[p.i], candidates[p.j]}
			idx, reason, err := rank(ctx, apiKey, subset)
			r := res{pair: p, reason: reason}
			if err == nil {
				r.ok = true
				if idx == 0 {
					r.winner = p.i
				} else {
					r.winner = p.j
				}
			} else {
				Logf("pair %d-%d failed: %v", p.i, p.j, err)
			}
			results <- r
		}(p)
	}
	wg.Wait()
	close(results)

	wins := make([]int, len(candidates))
	reasons := make(map[int]string)
	any := false
	for r := range results {
		if !r.ok {
			continue
		}
		any = true
		wins[r.winner]++
		reasons[r.winner] = r.reason
	}
	if !any {
		return -1, "", nil
	}
	// Find the maximum win count. Tie → "no opinion" (cycle).
	best := -1
	tied := false
	for i, w := range wins {
		if best < 0 || w > wins[best] {
			best = i
			tied = false
			continue
		}
		if w == wins[best] {
			tied = true
		}
	}
	if best < 0 || tied {
		return -1, "round-robin tied (no clear winner)", nil
	}
	return best, reasons[best], nil
}
