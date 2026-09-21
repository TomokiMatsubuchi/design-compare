package comparator

import (
	"fmt"
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
		if result.AbsoluteModePairs != 1 {
			t.Errorf("Expected absolute_mode_pairs=1 (Figma side has no parent), got %d", result.AbsoluteModePairs)
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
		if result.AbsoluteModePairs != 1 {
			t.Errorf("Expected absolute_mode_pairs=1 even when the abs pair mismatches, got %d", result.AbsoluteModePairs)
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
		if result.AbsoluteModePairs != 2 {
			t.Errorf("Expected absolute_mode_pairs=2 (root parents + Web child without parent), got %d", result.AbsoluteModePairs)
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
		if result.AbsoluteModePairs != 2 {
			t.Errorf("Expected absolute_mode_pairs=2 (0-size parent + child fallback), got %d", result.AbsoluteModePairs)
		}
	})
}

// TestLayoutTree_AbsoluteModePairs verifies that pairs compared in absolute
// pixel space (no usable parent, unresolved parent, or zero-size parent) are
// counted in AbsoluteModePairs and noted in details. Relative-ratio pairs
// (usable parent on both sides) are not counted.
func TestLayoutTree_AbsoluteModePairs(t *testing.T) {
	const tolerance = 0.15
	const passRate = 98.0
	wantNote := func(n int) string {
		return fmt.Sprintf("%d pairs were compared in absolute pixel space (no usable parent); tolerance applies to pixel units there", n)
	}

	t.Run("parentless_roots", func(t *testing.T) {
		figmaJSON := `[
			{"id":"1","name":"root","x":0,"y":0,"w":1000,"h":1000},
			{"id":"2","name":"hero","x":10,"y":10,"w":100,"h":100}
		]`
		webJSON := `[
			{"selector":"#root","x":0,"y":0,"w":1010,"h":1000},
			{"selector":".hero","x":10,"y":10,"w":100,"h":100}
		]`

		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, nil)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.AbsoluteModePairs != 2 {
			t.Errorf("Expected absolute_mode_pairs=2 for parentless nodes, got %d", result.AbsoluteModePairs)
		}
		if result.TotalNodes != 2 {
			t.Errorf("Expected 2 compared pairs, got total=%d", result.TotalNodes)
		}
		var found bool
		for _, d := range result.Details {
			if d == wantNote(2) {
				found = true
			}
		}
		if !found {
			t.Errorf("Expected absolute-mode detail line, got details: %v", result.Details)
		}
	})

	t.Run("relative_children_not_counted", func(t *testing.T) {
		figmaJSON := `[
			{"id":"1","name":"root","x":0,"y":0,"w":1000,"h":1000},
			{"id":"2","name":"hero","x":10,"y":10,"w":100,"h":100,"parent":"1"}
		]`
		webJSON := `[
			{"selector":"#root","x":0,"y":0,"w":1000,"h":1000},
			{"selector":".hero","x":10,"y":10,"w":100,"h":100,"parent":"#root"}
		]`

		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, nil)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.AbsoluteModePairs != 1 {
			t.Errorf("Expected absolute_mode_pairs=1 (root only), got %d", result.AbsoluteModePairs)
		}
		var found bool
		for _, d := range result.Details {
			if d == wantNote(1) {
				found = true
			}
		}
		if !found {
			t.Errorf("Expected absolute-mode detail for the root pair, got details: %v", result.Details)
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

// TestLayoutTree_DetailsOrderPutsFailuresFirst verifies that after the
// summary line, mismatch and extra-Web rows come before matched pairs so
// failures are not buried behind hundreds of "Matched: …" lines.
func TestLayoutTree_DetailsOrderPutsFailuresFirst(t *testing.T) {
	figmaJSON := `[
		{"id":"1","name":"header","x":0,"y":0,"w":1000,"h":100},
		{"id":"2","name":"logo","x":10,"y":10,"w":100,"h":80,"parent":"1"},
		{"id":"3","name":"nav","x":600,"y":10,"w":380,"h":80,"parent":"1"},
		{"id":"4","name":"hero","x":0,"y":200,"w":100,"h":100}
	]`
	webJSON := `[
		{"selector":"#header","x":0,"y":0,"w":1000,"h":100},
		{"selector":".logo","x":10,"y":10,"w":100,"h":80,"parent":"#header"},
		{"selector":".nav","x":600,"y":10,"w":380,"h":80,"parent":"#header"},
		{"selector":".hero","x":50,"y":200,"w":100,"h":100},
		{"selector":".banner","x":0,"y":400,"w":200,"h":50}
	]`

	result, err := CompareLayoutTrees(figmaJSON, webJSON, 0.15, 98.0, nil, false, nil)
	if err != nil {
		t.Fatalf("CompareLayoutTrees failed: %v", err)
	}
	if len(result.Details) < 2 {
		t.Fatalf("Expected summary plus detail rows, got %v", result.Details)
	}
	if !strings.Contains(result.Details[0], "Matched 3 out of 4") {
		t.Errorf("Expected summary first, got %q", result.Details[0])
	}

	firstMatchIdx := -1
	var sawMismatch, sawExtra bool
	for i, d := range result.Details {
		if i == 0 {
			continue
		}
		if strings.HasPrefix(d, "Matched:") {
			firstMatchIdx = i
			break
		}
		if strings.Contains(d, "did not match") && strings.Contains(d, "hero") {
			sawMismatch = true
		}
		if strings.Contains(d, "extra element") {
			sawExtra = true
		}
	}
	if firstMatchIdx < 0 {
		t.Fatalf("Expected matched-pair rows after failures, got %v", result.Details)
	}
	if !sawMismatch {
		t.Errorf("Expected a hero mismatch row before matched pairs, got %v", result.Details)
	}
	if !sawExtra {
		t.Errorf("Expected extra Web rows before matched pairs, got %v", result.Details)
	}
	if firstMatchIdx < 2 {
		t.Errorf("Expected at least one failure row immediately after summary, first Matched at %d: %v", firstMatchIdx, result.Details)
	}
	for i := firstMatchIdx; i < len(result.Details); i++ {
		d := result.Details[i]
		if !strings.HasPrefix(d, "Matched:") {
			t.Errorf("Expected only matched pairs after first Matched row, details[%d]=%q", i, d)
		}
	}
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
	if len(result.UnmatchedIgnoreRegions) != 0 {
		t.Errorf("Expected no unmatched_ignore_regions when the region hits node centers, got %v", result.UnmatchedIgnoreRegions)
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
	if len(resultOverlap.UnmatchedIgnoreRegions) != 1 || resultOverlap.UnmatchedIgnoreRegions[0] != "600,400,100,100" {
		t.Errorf("Expected unmatched_ignore_regions=[600,400,100,100], got %v", resultOverlap.UnmatchedIgnoreRegions)
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
	if len(resultAll.UnmatchedIgnoreRegions) != 0 {
		t.Errorf("Expected no unmatched_ignore_regions when the region hits every node, got %v", resultAll.UnmatchedIgnoreRegions)
	}
}

// TestLayoutTree_UnmatchedIgnoreRegions verifies that ignore_region rectangles
// whose half-open bounds contain no BoundingBox center on either side are
// reported as "x,y,w,h" strings (same format as out_of_bounds_regions), only
// when at least one such region exists.
func TestLayoutTree_UnmatchedIgnoreRegions(t *testing.T) {
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

	// Typo-like coordinates far from every node center: nothing is excluded
	// and the region is reported so the caller can see the ignore did not apply.
	miss := []Region{{X: 10, Y: 800, W: 50, H: 50}}
	result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, miss)
	if err != nil {
		t.Fatalf("CompareLayoutTrees failed: %v", err)
	}
	if result.IgnoredCount != 0 {
		t.Errorf("Expected IgnoredCount=0 for a region hitting no node center, got %d", result.IgnoredCount)
	}
	if len(result.UnmatchedIgnoreRegions) != 1 || result.UnmatchedIgnoreRegions[0] != "10,800,50,50" {
		t.Errorf("Expected unmatched_ignore_regions=[10,800,50,50], got %v", result.UnmatchedIgnoreRegions)
	}

	// Mixed list: a region that hits both banner centers stays off the list;
	// only the miss is reported.
	mixed := []Region{
		{X: 400, Y: 400, W: 500, H: 100},
		{X: 10, Y: 800, W: 50, H: 50},
	}
	resultMixed, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, mixed)
	if err != nil {
		t.Fatalf("CompareLayoutTrees failed: %v", err)
	}
	if resultMixed.IgnoredCount != 2 {
		t.Errorf("Expected IgnoredCount=2 from the matching region, got %d", resultMixed.IgnoredCount)
	}
	if len(resultMixed.UnmatchedIgnoreRegions) != 1 || resultMixed.UnmatchedIgnoreRegions[0] != "10,800,50,50" {
		t.Errorf("Expected unmatched_ignore_regions=[10,800,50,50] (miss only), got %v", resultMixed.UnmatchedIgnoreRegions)
	}
}

// TestLayoutTree_IgnoreNodesWildcard verifies that ignore_nodes entries ending
// with '*' are treated as prefix matches while all other entries keep the
// exact-match semantics:
//
//  1. prefix_excludes_group          – 'ad-*' excludes every ad node on both
//     sides (Figma IDs / names and Web selectors, via both the raw prefix and
//     its cleanNodeName-applied form), leaving only the header pair compared.
//  2. dotted_prefix_matches_clean_values – '.ad-ba*' matches Figma names like
//     'ad-banner-1' through the cleaned prefix 'ad-ba' (the same raw/clean
//     duality as exact matching).
//  3. unmatched_prefix_reported     – a prefix that matches no node value
//     excludes nothing and is reported in unmatched_ignores.
//  4. mid_string_star_is_literal    – an entry whose '*' is not trailing keeps
//     exact-match semantics and is reported in unmatched_ignores when no node
//     is literally named that way.
func TestLayoutTree_IgnoreNodesWildcard(t *testing.T) {
	const tolerance = 0.15
	const passRate = 98.0

	figmaJSON := `[
		{"id":"1","name":"header","x":0,"y":0,"w":1000,"h":100},
		{"id":"ad-1","name":"ad-banner-1","x":400,"y":400,"w":200,"h":80},
		{"id":"ad-2","name":"ad-banner-2","x":400,"y":500,"w":200,"h":80}
	]`
	webJSON := `[
		{"selector":"#header","x":0,"y":0,"w":1000,"h":100},
		{"selector":".ad-banner-1","x":400,"y":400,"w":200,"h":80},
		{"selector":".ad-banner-2","x":400,"y":500,"w":200,"h":80}
	]`

	t.Run("prefix_excludes_group", func(t *testing.T) {
		// 'ad-*' matches the Figma IDs ('ad-1', 'ad-2') and names
		// ('ad-banner-*') directly, and the Web selectors ('.ad-banner-*')
		// through their cleaned values: all four ad nodes are excluded and
		// only the header pair remains to compare.
		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, []string{"ad-*"}, false, nil)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.IgnoredCount != 4 {
			t.Errorf("Expected IgnoredCount=4 (ad nodes on both sides), got %d", result.IgnoredCount)
		}
		if result.MatchedNodes != 1 || result.TotalNodes != 1 {
			t.Errorf("Expected only the header pair left (matched=1, total=1), got matched=%d, total=%d", result.MatchedNodes, result.TotalNodes)
		}
		if result.Status != "success" {
			t.Errorf("Expected status 'success' after prefix exclusion, got '%s'", result.Status)
		}
		if len(result.UnmatchedIgnores) != 0 {
			t.Errorf("Expected no unmatched_ignores for matching prefix 'ad-*', got %v", result.UnmatchedIgnores)
		}
	})

	t.Run("dotted_prefix_matches_clean_values", func(t *testing.T) {
		// '.ad-ba*' has no node value starting with the raw prefix '.ad-ba',
		// but its cleaned form 'ad-ba' matches the Figma names 'ad-banner-*'
		// and the cleaned Web selectors (raw/clean duality, same as exact
		// matching).
		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, []string{".ad-ba*"}, false, nil)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.IgnoredCount != 4 {
			t.Errorf("Expected IgnoredCount=4 (ad nodes on both sides via cleaned prefix), got %d", result.IgnoredCount)
		}
		if len(result.UnmatchedIgnores) != 0 {
			t.Errorf("Expected no unmatched_ignores for '.ad-ba*', got %v", result.UnmatchedIgnores)
		}
	})

	t.Run("unmatched_prefix_reported", func(t *testing.T) {
		// A prefix matching no node value excludes nothing and is reported in
		// unmatched_ignores so that typos stay noticeable.
		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, []string{"zz-*"}, false, nil)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.IgnoredCount != 0 {
			t.Errorf("Expected IgnoredCount=0 for prefix matching nothing, got %d", result.IgnoredCount)
		}
		if len(result.UnmatchedIgnores) != 1 || result.UnmatchedIgnores[0] != "zz-*" {
			t.Errorf("Expected unmatched_ignores=[zz-*], got %v", result.UnmatchedIgnores)
		}
	})

	t.Run("mid_string_star_is_literal", func(t *testing.T) {
		// '*' that is not trailing keeps exact-match semantics: 'ad-*-1' is
		// not a wildcard, matches no node, excludes nothing and is reported in
		// unmatched_ignores.
		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, []string{"ad-*-1"}, false, nil)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.IgnoredCount != 0 {
			t.Errorf("Expected IgnoredCount=0 for non-trailing '*' entry, got %d", result.IgnoredCount)
		}
		if len(result.UnmatchedIgnores) != 1 || result.UnmatchedIgnores[0] != "ad-*-1" {
			t.Errorf("Expected unmatched_ignores=[ad-*-1], got %v", result.UnmatchedIgnores)
		}
	})
}

// TestLayoutTree_IgnoredParentKeepsRelativeCoords verifies that excluding a
// parent via ignore_nodes still lets children use that parent's geometry for
// relative comparison. Absolute px would mismatch because the Web tree is
// scaled 2x; relative ratios stay (0.25, 0.25, 0.25, 0.25) on both sides.
//
//  1. one_side_parent_ignored  – only the Figma parent id is ignored. The
//     Web parent remains in the match set as an extra node, but the child
//     pair must still match relatively (not fall into mixed absolute mode).
//  2. both_sides_parent_ignored – both parents are ignored; only children
//     remain and must match relatively. Ignored parents must not appear in
//     UnresolvedParentRefs.
func TestLayoutTree_IgnoredParentKeepsRelativeCoords(t *testing.T) {
	const tolerance = 0.15
	const passRate = 98.0

	figmaJSON := `[
		{"id":"P","name":"dyn-container","x":0,"y":0,"w":400,"h":400},
		{"id":"1","name":"card","x":100,"y":100,"w":100,"h":100,"parent":"P"}
	]`
	webJSON := `[
		{"selector":".dyn-container","x":0,"y":0,"w":800,"h":800},
		{"selector":".card","x":200,"y":200,"w":200,"h":200,"parent":".dyn-container"}
	]`

	t.Run("one_side_parent_ignored", func(t *testing.T) {
		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, []string{"P"}, false, nil)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.IgnoredCount != 1 {
			t.Errorf("Expected IgnoredCount=1 (Figma parent only), got %d", result.IgnoredCount)
		}
		if result.MatchedNodes != 1 {
			t.Errorf("Expected child pair to match relatively, got MatchedNodes=%d (rate=%.1f%% details=%v)", result.MatchedNodes, result.MatchRate, result.Details)
		}
		if result.Status != "success" {
			t.Errorf("Expected status 'success', got '%s' (rate=%.1f%% details=%v)", result.Status, result.MatchRate, result.Details)
		}
		if len(result.UnresolvedParentRefs) != 0 {
			t.Errorf("Expected ignored parent not to be reported as unresolved, got %v", result.UnresolvedParentRefs)
		}
	})

	t.Run("both_sides_parent_ignored", func(t *testing.T) {
		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, []string{"dyn-container"}, false, nil)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.IgnoredCount != 2 {
			t.Errorf("Expected IgnoredCount=2 (both parents), got %d", result.IgnoredCount)
		}
		if result.MatchedNodes != 1 || result.TotalNodes != 1 {
			t.Errorf("Expected only the child pair (matched=1, total=1), got matched=%d, total=%d (details=%v)", result.MatchedNodes, result.TotalNodes, result.Details)
		}
		if result.Status != "success" {
			t.Errorf("Expected status 'success', got '%s' (rate=%.1f%% details=%v)", result.Status, result.MatchRate, result.Details)
		}
		if len(result.UnresolvedParentRefs) != 0 {
			t.Errorf("Expected ignored parents not to be reported as unresolved, got %v", result.UnresolvedParentRefs)
		}
	})
}

// TestLayoutTree_ZeroGeometryWarning verifies that when layout JSON uses
// width/height (or other names that do not unmarshal into w/h), most nodes
// become 0×0. CompareLayoutTrees still returns the same status as before
// (non-destructive) but sets ZeroGeometryWarning so the silent 100% match
// is noticeable. A minority of genuine 0×0 nodes must not trigger it.
func TestLayoutTree_ZeroGeometryWarning(t *testing.T) {
	const tolerance = 0.15
	const passRate = 98.0
	wantWarning := `Most nodes have zero width/height; check the layout JSON keys are {"id","name","x","y","w","h","parent"}`

	t.Run("width_height_keys_warn_status_unchanged", func(t *testing.T) {
		figmaJSON := `[
			{"id":"1","name":"header","x":0,"y":0,"width":1000,"height":100},
			{"id":"2","name":"logo","x":10,"y":10,"width":100,"height":80,"parent":"1"}
		]`
		webJSON := `[
			{"selector":"#header","x":0,"y":0,"width":1000,"height":100},
			{"selector":".logo","x":10,"y":10,"width":100,"height":80,"parent":"#header"}
		]`

		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, nil)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.Status != "success" {
			t.Errorf("Expected status 'success' (non-destructive), got '%s'", result.Status)
		}
		if result.MatchRate != 100.0 {
			t.Errorf("Expected match_rate 100%% (all-zero geometry still matches), got %.1f%%", result.MatchRate)
		}
		if result.ZeroGeometryWarning != wantWarning {
			t.Errorf("Expected ZeroGeometryWarning=%q, got %q", wantWarning, result.ZeroGeometryWarning)
		}
	})

	t.Run("valid_w_h_keys_no_warning", func(t *testing.T) {
		figmaJSON := `[{"id":"1","name":"header","x":0,"y":0,"w":1000,"h":100}]`
		webJSON := `[{"selector":"#header","x":0,"y":0,"w":1000,"h":100}]`

		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, nil)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.Status != "success" {
			t.Errorf("Expected status 'success', got '%s'", result.Status)
		}
		if result.ZeroGeometryWarning != "" {
			t.Errorf("Expected no ZeroGeometryWarning for valid w/h keys, got %q", result.ZeroGeometryWarning)
		}
	})

	t.Run("minority_zero_size_no_warning", func(t *testing.T) {
		figmaJSON := `[
			{"id":"1","name":"a","x":0,"y":0,"w":100,"h":100},
			{"id":"2","name":"b","x":0,"y":0,"w":0,"h":0}
		]`
		webJSON := `[
			{"selector":"a","x":0,"y":0,"w":100,"h":100},
			{"selector":"b","x":0,"y":0,"w":0,"h":0}
		]`

		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, nil)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.ZeroGeometryWarning != "" {
			t.Errorf("Expected no warning when zeros are not a majority, got %q", result.ZeroGeometryWarning)
		}
	})
}

// TestLayoutTree_UnresolvedParentRefs verifies that a non-empty parent that
// matches no id / selector is reported in UnresolvedParentRefs without
// changing match status (still falls back to absolute coordinates).
func TestLayoutTree_UnresolvedParentRefs(t *testing.T) {
	const tolerance = 0.15
	const passRate = 98.0

	t.Run("missing_parent_id_reported_status_unchanged", func(t *testing.T) {
		figmaJSON := `[
			{"id":"1","name":"header","x":0,"y":0,"w":1000,"h":100},
			{"id":"2","name":"logo","x":10,"y":10,"w":100,"h":80,"parent":"999"}
		]`
		webJSON := `[
			{"selector":"#header","x":0,"y":0,"w":1000,"h":100},
			{"selector":".logo","x":10,"y":10,"w":100,"h":80,"parent":".foo"}
		]`

		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, nil)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.Status != "success" {
			t.Errorf("Expected status 'success' (non-destructive fallback), got '%s' rate=%.1f%%", result.Status, result.MatchRate)
		}
		if result.MatchRate != 100.0 {
			t.Errorf("Expected match rate 100%% with absolute-coordinate fallback, got %.1f%%", result.MatchRate)
		}
		if len(result.UnresolvedParentRefs) != 2 || result.UnresolvedParentRefs[0] != "Figma: '999'" || result.UnresolvedParentRefs[1] != "Web: '.foo'" {
			t.Errorf("Expected unresolved_parent_refs=[Figma: '999' Web: '.foo'], got %v", result.UnresolvedParentRefs)
		}
	})

	t.Run("resolved_parent_not_reported", func(t *testing.T) {
		figmaJSON := `[
			{"id":"1","name":"header","x":0,"y":0,"w":1000,"h":100},
			{"id":"2","name":"logo","x":10,"y":10,"w":100,"h":80,"parent":"1"}
		]`
		webJSON := `[
			{"selector":"#header","x":0,"y":0,"w":1000,"h":100},
			{"selector":".logo","x":10,"y":10,"w":100,"h":80,"parent":"#header"}
		]`

		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, nil)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if len(result.UnresolvedParentRefs) != 0 {
			t.Errorf("Expected no unresolved_parent_refs for valid parents, got %v", result.UnresolvedParentRefs)
		}
	})
}

// TestLayoutTree_SelfReferentialParentNoFalseMatch verifies that a node whose
// parent is its own id / selector is treated as having no parent. Self-parent
// would otherwise yield relative coords (0,0,1,1) on both sides, so nodes at
// completely different positions/sizes would match with diff=0.
func TestLayoutTree_SelfReferentialParentNoFalseMatch(t *testing.T) {
	const tolerance = 0.15
	const passRate = 98.0

	figmaJSON := `[{"id":"1","name":"A","x":0,"y":0,"w":100,"h":100,"parent":"1"}]`
	webJSON := `[{"selector":"A","x":900,"y":900,"w":400,"h":400,"parent":"A"}]`

	result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, nil)
	if err != nil {
		t.Fatalf("CompareLayoutTrees failed: %v", err)
	}
	if result.MatchedNodes != 0 {
		t.Errorf("Expected 0 matched nodes (self-parent must not inflate to (0,0,1,1)), got %d (matchRate=%.1f%%)", result.MatchedNodes, result.MatchRate)
	}
	if result.MatchRate >= 100.0 {
		t.Errorf("Expected match rate < 100%% for geometrically different self-referential nodes, got %.1f%%", result.MatchRate)
	}
	if result.Status == "success" {
		t.Errorf("Expected status not 'success' (false match), got '%s' (matchRate=%.1f%%)", result.Status, result.MatchRate)
	}
}

// TestLayoutTree_NegativeWidthHeightIsError verifies that a node with negative
// w or h is rejected as an input error after parse. Zero size remains valid
// (collapsed nodes). The error must name the Figma node or Web selector.
func TestLayoutTree_NegativeWidthHeightIsError(t *testing.T) {
	const tolerance = 0.15
	const passRate = 98.0

	validFigma := `[{"id":"1","name":"A","x":0,"y":0,"w":100,"h":100}]`
	validWeb := `[{"selector":".root","x":0,"y":0,"w":100,"h":100}]`

	tests := []struct {
		name      string
		figmaJSON string
		webJSON   string
		wantSub   []string
	}{
		{
			name:      "figma_negative_w",
			figmaJSON: `[{"id":"1","name":"Hero","x":0,"y":0,"w":-50,"h":100}]`,
			webJSON:   validWeb,
			wantSub:   []string{"Figma node", "Hero", "negative"},
		},
		{
			name:      "figma_negative_h",
			figmaJSON: `[{"id":"1","name":"Hero","x":0,"y":0,"w":100,"h":-1}]`,
			webJSON:   validWeb,
			wantSub:   []string{"Figma node", "Hero", "negative"},
		},
		{
			name:      "web_negative_w",
			figmaJSON: validFigma,
			webJSON:   `[{"selector":".banner","x":0,"y":0,"w":-50,"h":100}]`,
			wantSub:   []string{"Web node", ".banner", "negative"},
		},
		{
			name:      "web_negative_h",
			figmaJSON: validFigma,
			webJSON:   `[{"selector":".banner","x":0,"y":0,"w":100,"h":-8}]`,
			wantSub:   []string{"Web node", ".banner", "negative"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CompareLayoutTrees(tc.figmaJSON, tc.webJSON, tolerance, passRate, nil, false, nil)
			if err == nil {
				t.Fatal("expected input error for negative width/height")
			}
			got := err.Error()
			for _, sub := range tc.wantSub {
				if !strings.Contains(got, sub) {
					t.Errorf("error %q should contain %q", got, sub)
				}
			}
		})
	}

	t.Run("zero_size_allowed", func(t *testing.T) {
		figmaJSON := `[{"id":"1","name":"Collapsed","x":0,"y":0,"w":0,"h":0}]`
		webJSON := `[{"selector":".collapsed","x":0,"y":0,"w":0,"h":0}]`
		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, nil)
		if err != nil {
			t.Fatalf("zero w/h must remain valid: %v", err)
		}
		if result.Status != "success" {
			t.Errorf("expected success for matching collapsed nodes, got %s", result.Status)
		}
	})
}

// TestLayoutTree_NullArrayElementIsError verifies that a JSON null inside
// figma_layout / web_layout is rejected as a parse error instead of becoming
// a zero-value node (empty id/selector, w/h=0) that silently skews match rate.
func TestLayoutTree_NullArrayElementIsError(t *testing.T) {
	const tolerance = 0.15
	const passRate = 98.0

	validFigma := `[{"id":"1","name":"A","x":0,"y":0,"w":100,"h":100}]`
	validWeb := `[{"selector":"A","x":0,"y":0,"w":100,"h":100}]`

	t.Run("figma_null", func(t *testing.T) {
		_, err := CompareLayoutTrees(`[{"id":"1","name":"A","x":0,"y":0,"w":100,"h":100},null]`, validWeb, tolerance, passRate, nil, false, nil)
		if err == nil {
			t.Fatal("expected parse error for null Figma array element")
		}
		got := err.Error()
		if !strings.Contains(got, "failed to parse Figma layout JSON") {
			t.Errorf("error %q should mention Figma layout JSON", got)
		}
		if !strings.Contains(got, "element at index 1 is null") {
			t.Errorf("error %q should name the null index", got)
		}
	})

	t.Run("web_null", func(t *testing.T) {
		_, err := CompareLayoutTrees(validFigma, `[null,{"selector":"A","x":0,"y":0,"w":100,"h":100}]`, tolerance, passRate, nil, false, nil)
		if err == nil {
			t.Fatal("expected parse error for null Web array element")
		}
		got := err.Error()
		if !strings.Contains(got, "failed to parse Web layout JSON") {
			t.Errorf("error %q should mention Web layout JSON", got)
		}
		if !strings.Contains(got, "element at index 0 is null") {
			t.Errorf("error %q should name the null index", got)
		}
	})
}

// TestLayoutTree_TypeMismatchIncludesElementIndex verifies that a type error
// on one node in a multi-element layout names the failing array index.
func TestLayoutTree_TypeMismatchIncludesElementIndex(t *testing.T) {
	const tolerance = 0.15
	const passRate = 98.0

	validFigma := `[{"id":"1","name":"A","x":0,"y":0,"w":100,"h":100},{"id":"2","name":"B","x":0,"y":100,"w":50,"h":50}]`
	validWeb := `[{"selector":"A","x":0,"y":0,"w":100,"h":100},{"selector":"B","x":0,"y":100,"w":50,"h":50}]`

	t.Run("figma_w_string", func(t *testing.T) {
		_, err := CompareLayoutTrees(
			`[{"id":"1","name":"A","x":0,"y":0,"w":100,"h":100},{"id":"2","name":"B","x":0,"y":100,"w":"50","h":50}]`,
			validWeb, tolerance, passRate, nil, false, nil)
		if err == nil {
			t.Fatal("expected parse error for Figma w type mismatch")
		}
		got := err.Error()
		if !strings.Contains(got, "failed to parse Figma layout JSON at element 1") {
			t.Errorf("error %q should include Figma element index 1", got)
		}
	})

	t.Run("web_w_string", func(t *testing.T) {
		_, err := CompareLayoutTrees(validFigma,
			`[{"selector":"A","x":0,"y":0,"w":100,"h":100},{"selector":"B","x":0,"y":100,"w":"50","h":50}]`,
			tolerance, passRate, nil, false, nil)
		if err == nil {
			t.Fatal("expected parse error for Web w type mismatch")
		}
		got := err.Error()
		if !strings.Contains(got, "failed to parse Web layout JSON at element 1") {
			t.Errorf("error %q should include Web element index 1", got)
		}
	})
}

// TestLayoutTree_UTF8BOMStripped verifies that a leading UTF-8 BOM (U+FEFF)
// on either JSON input is stripped before Unmarshal, so BOM-prefixed payloads
// parse the same as BOM-less ones. Editors and tools such as PowerShell often
// emit UTF-8 with BOM; without stripping, json.Unmarshal fails with
// `invalid character 'ï' looking for beginning of value`.
func TestLayoutTree_UTF8BOMStripped(t *testing.T) {
	const tolerance = 0.15
	const passRate = 98.0

	figmaJSON := `[{"id":"1","name":"A","x":0,"y":0,"w":100,"h":100}]`
	webJSON := `[{"selector":"A","x":0,"y":0,"w":100,"h":100}]`

	t.Run("without_bom", func(t *testing.T) {
		result, err := CompareLayoutTrees(figmaJSON, webJSON, tolerance, passRate, nil, false, nil)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed: %v", err)
		}
		if result.Status != "success" || result.MatchedNodes != 1 {
			t.Errorf("Expected success with 1 matched node, got status=%s matched=%d", result.Status, result.MatchedNodes)
		}
	})

	t.Run("with_bom", func(t *testing.T) {
		result, err := CompareLayoutTrees("\uFEFF"+figmaJSON, "\uFEFF"+webJSON, tolerance, passRate, nil, false, nil)
		if err != nil {
			t.Fatalf("CompareLayoutTrees failed with BOM-prefixed JSON: %v", err)
		}
		if result.Status != "success" || result.MatchedNodes != 1 {
			t.Errorf("Expected success with 1 matched node, got status=%s matched=%d", result.Status, result.MatchedNodes)
		}
	})
}

func TestLimitLayoutTreeDetails(t *testing.T) {
	details := []string{
		"Matched 3 out of 3 layout nodes.",
		"Matched: 'header' ↔ '#header'",
		"Matched: 'logo' ↔ '.logo'",
		"Matched: 'nav' ↔ '.nav'",
	}

	t.Run("max_details_2_truncates_and_omits", func(t *testing.T) {
		got := LimitLayoutTreeDetails(details, 2)
		if len(got) != 3 {
			t.Fatalf("expected 2 kept + omit line, got %d: %v", len(got), got)
		}
		if got[0] != details[0] {
			t.Errorf("expected summary first, got %q", got[0])
		}
		if got[1] != details[1] {
			t.Errorf("expected first pair kept, got %q", got[1])
		}
		wantOmit := "... and 2 more details omitted (max_details=2)"
		if got[2] != wantOmit {
			t.Errorf("expected omit line %q, got %q", wantOmit, got[2])
		}
	})

	t.Run("unspecified_or_zero_keeps_all", func(t *testing.T) {
		got := LimitLayoutTreeDetails(details, 0)
		if len(got) != len(details) {
			t.Fatalf("expected all %d details, got %d: %v", len(details), len(got), got)
		}
		for i := range details {
			if got[i] != details[i] {
				t.Errorf("details[%d]=%q, want %q", i, got[i], details[i])
			}
		}
	})
}

func TestRoundMatchRateDisplay(t *testing.T) {
	// Issue #233 の例: 97.999846% は表示 98.00 と同じ桁に丸まる
	if got := RoundMatchRateDisplay(97.999846); got != 98.0 {
		t.Errorf("RoundMatchRateDisplay(97.999846)=%v, want 98", got)
	}
	if got := RoundMatchRateDisplay(98.046875); got != 98.05 {
		t.Errorf("RoundMatchRateDisplay(98.046875)=%v, want 98.05", got)
	}
}
