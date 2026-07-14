package rank

import (
	"context"
)

// RankPairwise picks the best candidate among 2-3 images by asking the Claude
// API. When apiKey is empty, returns -1 immediately so the caller can fall
// back to its own heuristic.
//
// Errors are returned but never panic. Callers should treat any error as
// "no opinion" and pick the best by their geometric scorer.
func RankPairwise(ctx context.Context, apiKey string, candidates []ImageCandidate) (winnerIdx int, reason string, err error) {
	if apiKey == "" {
		Logf("visual rank disabled (no anthropic key) — using geometric score only")
		return -1, "", nil
	}
	if len(candidates) < 2 {
		return -1, "", nil
	}
	if len(candidates) > 4 {
		// Keep only the first 4; caller should have pre-filtered to top-K.
		candidates = candidates[:4]
	}
	idx, reason, err := rank(ctx, apiKey, candidates)
	if err != nil {
		Logf("rank failed: %v — falling back to geometric score", err)
		return -1, "", err
	}
	return idx, reason, nil
}
