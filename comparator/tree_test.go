package comparator

import (
	"strings"
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

		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, nil)
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

		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, nil)
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

		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, nil)
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

		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, nil)
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

// TestLayoutTree_MismatchMessages verifies the mismatch detail messages
// produced by the layout tree comparison:
//  1. no_candidates_left – when every Web node has already been used by a
//     previous Figma node (Web side has fewer elements), the detail must
//     explicitly state that no unused Web element is left to compare, instead
//     of printing a meaningless empty selector with a MaxFloat64 diff.
//  2. tolerance_exceeded  – when a Figma node is shifted beyond tolerance,
//     the detail must state that the geometric diff exceeds the tolerance,
//     without the old misleading "(type config mismatch or position shifted)"
//     wording (there is no "type config" in the data model).
func TestLayoutTree_MismatchMessages(t *testing.T) {
	t.Run("no_candidates_left", func(t *testing.T) {
		// Figma has 3 nodes but Web has only 2 (#container and .childA), so the
		// third Figma node (childB) has no unused Web node left to compare.
		figmaJSON := `[
			{"id":"1","name":"container","x":0,"y":0,"w":500,"h":500},
			{"id":"2","name":"childA","x":10,"y":10,"w":480,"h":480,"parent":"1"},
			{"id":"3","name":"childB","x":10,"y":10,"w":480,"h":480,"parent":"1"}
		]`
		webJSON := `[
			{"selector":"#container","x":0,"y":0,"w":500,"h":500},
			{"selector":".childA","x":10,"y":10,"w":480,"h":480,"parent":"#container"}
		]`

		result, err := CompareLayoutTrees(figmaJSON, webJSON, 0.15, 98.0, nil, false, nil)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.MatchedNodes != 2 {
			t.Errorf("Expected 2 matched nodes, got %d", result.MatchedNodes)
		}
		var found bool
		for _, d := range result.Details {
			if strings.Contains(d, "no unused Web element is left to compare") && strings.Contains(d, "childB") {
				found = true
			}
			if strings.Contains(d, "did not match closest Web element ''") {
				t.Errorf("Details must not contain an empty closest-element message, got: %s", d)
			}
		}
		if !found {
			t.Errorf("Expected a detail stating no unused Web element is left for 'childB', got details: %v", result.Details)
		}
	})

	t.Run("tolerance_exceeded", func(t *testing.T) {
		// The Web node is shifted 50px from the Figma node, far beyond the
		// tolerance of 0.15, so the detail must state that the geometric diff
		// exceeds the tolerance.
		figmaJSON := `[{"id":"1","name":"hero","x":0,"y":0,"w":100,"h":100}]`
		webJSON := `[{"selector":".hero","x":50,"y":0,"w":100,"h":100}]`

		result, err := CompareLayoutTrees(figmaJSON, webJSON, 0.15, 98.0, nil, false, nil)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.MatchedNodes != 0 {
			t.Errorf("Expected 0 matched nodes, got %d", result.MatchedNodes)
		}
		var found bool
		for _, d := range result.Details {
			if strings.Contains(d, "geometric diff 50.00 exceeds tolerance 0.15") && strings.Contains(d, ".hero") {
				found = true
			}
			if strings.Contains(d, "type config mismatch") {
				t.Errorf("Details must not contain the old misleading 'type config mismatch' wording, got: %s", d)
			}
		}
		if !found {
			t.Errorf("Expected a detail stating the geometric diff exceeds tolerance, got details: %v", result.Details)
		}
	})
}

// TestLayoutTree_IgnoreRegion verifies that nodes whose bounding-box center
// lies inside an ignore_region are excluded from both sides (counted in
// IgnoredCount), that a region overlapping a node but not containing its
// center does not exclude it (center-point semantics), and that excluding
// all nodes results in the existing "skipped" status.
func TestLayoutTree_IgnoreRegion(t *testing.T) {
	const tolerance = 0.15
	const passRate = 98.0

	figmaJSON := `[
		{"id":"1","name":"header","x":0,"y":0,"w":1000,"h":100},
		{"id":"2","name":"banner","x":400,"y":400,"w":200,"h":80}
	]`
	webJSON := `[
		{"selector":"#header","x":0,"y":0,"w":1000,"h":100},
		{"selector":".banner","x":650,"y":420,"w":200,"h":80}
	]`

	// Region [400,900)x[400,500) contains the banner centers on both sides
	// (500,440) and (750,460) but not the header center (500,50): the banners
	// are excluded from both sides and only the header pair is compared.
	regions := []Region{{X: 400, Y: 400, W: 500, H: 100}}
	result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, regions)
	if err != nil {
		t.Fatalf("CompareLayoutTrees failed: %v", err)
	}
	if result.IgnoredCount != 2 {
		t.Errorf("Expected IgnoredCount=2 (banner on both sides), got %d", result.IgnoredCount)
	}
	if result.MatchedNodes != 1 {
		t.Errorf("Expected 1 matched node (header pair), got %d", result.MatchedNodes)
	}
	if result.TotalNodes != 1 {
		t.Errorf("Expected TotalNodes=1 (only the header pair compared), got %d", result.TotalNodes)
	}
	if result.Status != "success" {
		t.Errorf("Expected status 'success', got '%s' (matchRate=%.1f%%)", result.Status, result.MatchRate)
	}

	// Region [600,700)x[400,500) overlaps the Web banner bbox but contains
	// neither center point: nothing is excluded (center-point semantics) and
	// the shifted banner pair keeps the comparison in mismatch.
	overlap := []Region{{X: 600, Y: 400, W: 100, H: 100}}
	resultOverlap, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, overlap)
	if err != nil {
		t.Fatalf("CompareLayoutTrees failed: %v", err)
	}
	if resultOverlap.IgnoredCount != 0 {
		t.Errorf("Expected IgnoredCount=0 for a region containing no node center, got %d", resultOverlap.IgnoredCount)
	}
	if resultOverlap.Status != "mismatch" {
		t.Errorf("Expected status 'mismatch' when nothing is ignored, got '%s'", resultOverlap.Status)
	}

	// A region containing every node center excludes all nodes on both sides
	// and rides the existing "skipped" judgement (like ignore_nodes).
	all := []Region{{X: 0, Y: 0, W: 2000, H: 2000}}
	resultAll, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, all)
	if err != nil {
		t.Fatalf("CompareLayoutTrees failed: %v", err)
	}
	if resultAll.Status != "skipped" {
		t.Errorf("Expected status 'skipped' when all nodes are excluded by region, got '%s'", resultAll.Status)
	}
	if resultAll.IgnoredCount != 4 {
		t.Errorf("Expected IgnoredCount=4 (all nodes on both sides), got %d", resultAll.IgnoredCount)
	}
	if len(resultAll.Details) == 0 || !strings.Contains(resultAll.Details[0], "ignore_region") {
		t.Errorf("Expected skipped detail to mention ignore_region, got %v", resultAll.Details)
	}
}
