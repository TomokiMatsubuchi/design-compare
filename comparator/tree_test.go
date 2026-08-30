package comparator

import (
	"testing"
)

// TestLayoutTree_MixedModeSymmetricCompare verifies that when one side of a
// comparison pair falls back to absolute coordinates (because the node has no
// parent or the parent has zero size) while the other side uses relative
// ratios (0–1), the comparison is symmetric: both sides are converted to
// absolute coordinate space before computing the diff. This prevents the
// asymmetry where absolute px values were directly subtracted from relative
// ratios, always producing a mismatch.
//
// Sub-tests:
//  1. figma_only_absolute_match       – Figma absolute, Web relative, same
//     absolute position → match (symmetric conversion).
//  2. figma_only_absolute_mismatch    – Same setup but different absolute
//     positions → mismatch (no false positive).
//  3. web_only_absolute_match         – Reverse: Web node absolute, Figma node
//     relative, same absolute position → match (symmetry from the other
//     direction). Both sides include a matching parent so the overall match
//     rate is 100%.
//  4. zero_size_parent_no_inflation   – Both sides have 0-size parents. The
//     child nodes are at different positions and must NOT match. Previously,
//     0-size parents caused (0,0,0,0) to be returned for both sides, resulting
//     in diff=0 and a false match (inflated match rate). Now, absolute
//     coordinates are used, so different child positions → mismatch. The 0×0
//     parents themselves correctly match (both at origin), but the children
//     must not be inflated.
func TestLayoutTree_MixedModeSymmetricCompare(t *testing.T) {
	const tolerance = 0.15
	const passRate = 98.0

	// Sub-test 1: Figma absolute (no parent), Web relative (has parent),
	// same absolute coordinates → should match.
	t.Run("figma_only_absolute_match", func(t *testing.T) {
		figmaJSON := `[{"id":"1","name":"A","x":100,"y":100,"w":200,"h":200}]`
		webJSON := `[
			{"selector":"P","x":0,"y":0,"w":400,"h":400},
			{"selector":"A","x":100,"y":100,"w":200,"h":200,"parent":"P"}
		]`

		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.MatchedNodes != 1 {
			t.Errorf("Expected 1 matched node, got %d (matchRate=%.1f%%)", result.MatchedNodes, result.MatchRate)
		}
		if result.Status != "success" {
			t.Errorf("Expected status 'success', got '%s' (matchRate=%.1f%%)", result.Status, result.MatchRate)
		}
	})

	// Sub-test 2: Figma absolute (no parent), Web relative (has parent),
	// different absolute coordinates → should NOT match (no false positive).
	t.Run("figma_only_absolute_mismatch", func(t *testing.T) {
		figmaJSON := `[{"id":"1","name":"A","x":100,"y":100,"w":200,"h":200}]`
		webJSON := `[
			{"selector":"P","x":0,"y":0,"w":400,"h":400},
			{"selector":"A","x":150,"y":150,"w":200,"h":200,"parent":"P"}
		]`

		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.MatchedNodes != 0 {
			t.Errorf("Expected 0 matched nodes (different absolute positions should not match), got %d", result.MatchedNodes)
		}
	})

	// Sub-test 3: Reverse direction – Web node A has no parent (absolute mode),
	// Figma node A has a parent (relative mode), same absolute coordinates.
	// Both sides also include a parent P at the same position so the parent
	// also matches, giving an overall 100% match rate.
	t.Run("web_only_absolute_match", func(t *testing.T) {
		figmaJSON := `[
			{"id":"P","name":"P","x":0,"y":0,"w":400,"h":400},
			{"id":"1","name":"A","x":100,"y":100,"w":200,"h":200,"parent":"P"}
		]`
		webJSON := `[
			{"selector":"P","x":0,"y":0,"w":400,"h":400},
			{"selector":"A","x":100,"y":100,"w":200,"h":200}
		]`

		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.MatchedNodes != 2 {
			t.Errorf("Expected 2 matched nodes (both parent and child match symmetrically), got %d (matchRate=%.1f%%)", result.MatchedNodes, result.MatchRate)
		}
		if result.Status != "success" {
			t.Errorf("Expected status 'success', got '%s' (matchRate=%.1f%%)", result.Status, result.MatchRate)
		}
	})

	// Sub-test 4: Both sides have 0-size parents but different child positions.
	// Previously, 0-size parents caused (0,0,0,0) to be returned for both
	// sides, resulting in diff=0 and a false match (inflated match rate).
	// Now, absolute coordinates are used, so children at different positions
	// do not match. The 0×0 parents themselves correctly match (both at
	// origin), but the children must not be inflated into a match.
	t.Run("zero_size_parent_no_inflation", func(t *testing.T) {
		figmaJSON := `[
			{"id":"P","name":"P","x":0,"y":0,"w":0,"h":0},
			{"id":"1","name":"A","x":100,"y":100,"w":50,"h":50,"parent":"P"}
		]`
		webJSON := `[
			{"selector":"P","x":0,"y":0,"w":0,"h":0},
			{"selector":"A","x":200,"y":200,"w":50,"h":50,"parent":"P"}
		]`

		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		// The parents (both at 0,0,0,0) match correctly, but the children at
		// different positions must NOT match. If the old (0,0,0,0) inflation
		// were still present, all 2 nodes would match → 100%. We verify the
		// child mismatch by checking that the match rate is below 100%.
		if result.MatchRate >= 100.0 {
			t.Errorf("Expected match rate < 100%% (child nodes at different positions should not be inflated to match), got %.1f%% (matched=%d/%d)", result.MatchRate, result.MatchedNodes, result.TotalNodes)
		}
	})
}
