package comparator

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestViewportSizeForPreset(t *testing.T) {
	w, h, err := ViewportSizeForPreset("")
	if err != nil {
		t.Fatalf("empty preset: %v", err)
	}
	if w != 768 || h != 1024 {
		t.Errorf("empty preset: got %.0fx%.0f, want 768x1024", w, h)
	}

	w, h, err = ViewportSizeForPreset(ViewportPresetIPadPortrait)
	if err != nil {
		t.Fatalf("portrait: %v", err)
	}
	if w != DefaultIPadViewportWidth || h != DefaultIPadViewportHeight {
		t.Errorf("portrait: got %.0fx%.0f", w, h)
	}

	w, h, err = ViewportSizeForPreset(ViewportPresetIPadLandscape)
	if err != nil {
		t.Fatalf("landscape: %v", err)
	}
	if w != DefaultIPadLandscapeViewportWidth || h != DefaultIPadLandscapeViewportHeight {
		t.Errorf("landscape: got %.0fx%.0f, want 1024x768", w, h)
	}
	if w != 1024 || h != 768 {
		t.Errorf("landscape constants: got %.0fx%.0f", w, h)
	}

	_, _, err = ViewportSizeForPreset("iphone")
	if err == nil {
		t.Fatal("expected unknown preset error")
	}
	if !strings.Contains(err.Error(), "unknown viewport_preset") {
		t.Errorf("error should mention unknown viewport_preset, got %v", err)
	}
	if !strings.Contains(err.Error(), ViewportPresetIPadPortrait) || !strings.Contains(err.Error(), ViewportPresetIPadLandscape) {
		t.Errorf("error should list valid presets, got %v", err)
	}
}

func TestDefaultIPadConstants(t *testing.T) {
	if DefaultIPadViewportWidth != 768 || DefaultIPadViewportHeight != 1024 {
		t.Errorf("portrait constants = %dx%d", DefaultIPadViewportWidth, DefaultIPadViewportHeight)
	}
	if DefaultIPadLandscapeViewportWidth != 1024 || DefaultIPadLandscapeViewportHeight != 768 {
		t.Errorf("landscape constants = %dx%d", DefaultIPadLandscapeViewportWidth, DefaultIPadLandscapeViewportHeight)
	}
}

func TestCheckLayoutIntegrity(t *testing.T) {
	t.Run("portrait_in_bounds_success", func(t *testing.T) {
		web := `[{"selector":"#page","x":0,"y":0,"w":768,"h":200}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "success" || res.IssueCount != 0 || res.CheckedNodes != 1 {
			t.Fatalf("got status=%s issues=%d checked=%d details=%v", res.Status, res.IssueCount, res.CheckedNodes, res.Details)
		}
		if res.ParentCheckedNodes != 0 {
			t.Errorf("parent_checked_nodes=%d, want 0 (no parent refs)", res.ParentCheckedNodes)
		}
		if len(res.Details) < 2 || !strings.Contains(res.Details[1], "parent_overflow was not applied") {
			t.Errorf("expected parent_overflow skip note in details, got %v", res.Details)
		}
		raw, _ := json.Marshal(res)
		if strings.Contains(string(raw), `"issues"`) {
			t.Errorf("issues should be omitempty when empty, got %s", raw)
		}
		if strings.Contains(string(raw), `"unresolved_parent_refs"`) {
			t.Errorf("unresolved_parent_refs should be omitempty when empty, got %s", raw)
		}
	})

	t.Run("wide_900_mismatch_portrait_ok_landscape", func(t *testing.T) {
		web := `[{"selector":"#wide","x":0,"y":0,"w":900,"h":100}]`
		portrait, err := CheckLayoutIntegrity(web, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if portrait.Status != "mismatch" || portrait.IssueCount < 1 || portrait.Issues[0].Type != "viewport_overflow_x" {
			t.Fatalf("portrait: %#v", portrait)
		}

		landscape, err := CheckLayoutIntegrity(web, 1024, 768, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if landscape.Status != "success" || landscape.IssueCount != 0 {
			t.Fatalf("landscape should succeed for 900px width, got %#v", landscape)
		}
	})

	t.Run("wide_1100_mismatch_landscape", func(t *testing.T) {
		web := `[{"selector":"#wide","x":0,"y":0,"w":1100,"h":100}]`
		res, err := CheckLayoutIntegrity(web, 1024, 768, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "mismatch" || res.IssueCount < 1 || res.Issues[0].Type != "viewport_overflow_x" {
			t.Fatalf("got %#v", res)
		}
	})

	t.Run("negative_x_overflow", func(t *testing.T) {
		web := `[{"selector":"#neg","x":-10,"y":0,"w":100,"h":50}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "mismatch" || res.Issues[0].Type != "viewport_overflow_x" {
			t.Fatalf("got %#v", res)
		}
	})

	t.Run("tall_page_vertical_scroll_ok", func(t *testing.T) {
		web := `[{"selector":"#tall","x":0,"y":0,"w":768,"h":2000}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "success" {
			t.Fatalf("vertical overflow of viewport should not fail, got %#v", res)
		}
	})

	t.Run("parent_overflow", func(t *testing.T) {
		web := `[{"selector":"#card","x":0,"y":0,"w":400,"h":200},{"selector":"#child","x":0,"y":0,"w":500,"h":50,"parent":"#card"}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "mismatch" {
			t.Fatalf("status=%s", res.Status)
		}
		if res.ParentCheckedNodes != 1 {
			t.Errorf("parent_checked_nodes=%d, want 1", res.ParentCheckedNodes)
		}
		if len(res.UnresolvedParentRefs) != 0 {
			t.Errorf("unresolved_parent_refs=%v", res.UnresolvedParentRefs)
		}
		found := false
		for _, issue := range res.Issues {
			if issue.Type == "parent_overflow" && issue.Selector == "#child" {
				found = true
			}
			if issue.Type == "viewport_overflow_x" {
				t.Errorf("unexpected viewport overflow: %#v", issue)
			}
		}
		if !found {
			t.Fatalf("missing parent_overflow: %#v", res.Issues)
		}
	})

	t.Run("missing_parent_skips_parent_check", func(t *testing.T) {
		web := `[{"selector":"#orphan","x":0,"y":0,"w":100,"h":50,"parent":"#gone"}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "success" || res.IssueCount != 0 {
			t.Fatalf("got %#v", res)
		}
		if res.ParentCheckedNodes != 0 {
			t.Errorf("parent_checked_nodes=%d, want 0", res.ParentCheckedNodes)
		}
		if len(res.UnresolvedParentRefs) != 1 || res.UnresolvedParentRefs[0] != "Web: '#gone'" {
			t.Fatalf("unresolved_parent_refs=%v", res.UnresolvedParentRefs)
		}
		if res.ParentOverflowSkippedZeroGeom != 0 {
			t.Fatalf("expected no zero-geom skips, got %d", res.ParentOverflowSkippedZeroGeom)
		}
		if len(res.Details) == 0 || !strings.Contains(res.Details[0], "Skipped parent_overflow") || !strings.Contains(res.Details[0], "1 unresolved parent ref") {
			t.Fatalf("details should mention skipped parent_overflow, got %v", res.Details)
		}
		if len(res.Details) < 2 || !strings.Contains(res.Details[1], "parent_overflow was not applied") {
			t.Errorf("expected parent_overflow skip note in details, got %v", res.Details)
		}
	})

	t.Run("no_parent_refs_reports_parent_checked_zero", func(t *testing.T) {
		// 親キーを持たないフラットな DOM。viewport 内で崩れが無くても parent_overflow は未適用。
		web := `[{"selector":"#a","x":0,"y":0,"w":768,"h":100},{"selector":"#b","x":10,"y":10,"w":100,"h":50}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "success" || res.IssueCount != 0 || res.CheckedNodes != 2 {
			t.Fatalf("got %#v", res)
		}
		if res.ParentCheckedNodes != 0 {
			t.Errorf("parent_checked_nodes=%d, want 0", res.ParentCheckedNodes)
		}
		if len(res.UnresolvedParentRefs) != 0 {
			t.Errorf("unresolved_parent_refs=%v", res.UnresolvedParentRefs)
		}
		if len(res.Details) < 2 || !strings.Contains(res.Details[1], "parent_checked_nodes=0") {
			t.Errorf("details=%v", res.Details)
		}
	})

	t.Run("parent_typo_unresolved_ref", func(t *testing.T) {
		web := `[{"selector":"#card","x":0,"y":0,"w":400,"h":200},{"selector":"#child","x":0,"y":0,"w":500,"h":50,"parent":"#card-typo"}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		// 親が解決できないため parent_overflow はスキップされ、viewport にも収まっていれば success。
		if res.Status != "success" {
			t.Fatalf("got %#v", res)
		}
		if res.ParentCheckedNodes != 0 {
			t.Errorf("parent_checked_nodes=%d, want 0", res.ParentCheckedNodes)
		}
		if len(res.UnresolvedParentRefs) != 1 || res.UnresolvedParentRefs[0] != "Web: '#card-typo'" {
			t.Errorf("unresolved_parent_refs=%v, want [Web: '#card-typo']", res.UnresolvedParentRefs)
		}
	})

	t.Run("zero_geometry_parent_skips_parent_check", func(t *testing.T) {
		// 親は入力にあるが w/h=0 のため parent_overflow 不可。status は success のまま件数で可視化。
		web := `[{"selector":"#card","x":0,"y":0,"w":0,"h":0},{"selector":"#child","x":0,"y":0,"w":100,"h":50,"parent":"#card"}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "success" || res.IssueCount != 0 || res.CheckedNodes != 1 {
			t.Fatalf("got %#v", res)
		}
		if res.ParentCheckedNodes != 0 {
			t.Errorf("parent_checked_nodes=%d, want 0", res.ParentCheckedNodes)
		}
		if len(res.UnresolvedParentRefs) != 0 {
			t.Fatalf("zero-geometry parent should not be unresolved_parent_refs, got %v", res.UnresolvedParentRefs)
		}
		if res.ParentOverflowSkippedZeroGeom != 1 {
			t.Fatalf("parent_overflow_skipped_zero_geom=%d", res.ParentOverflowSkippedZeroGeom)
		}
		if len(res.Details) == 0 || !strings.Contains(res.Details[0], "1 zero-geometry parent") {
			t.Fatalf("details should mention zero-geometry skip, got %v", res.Details)
		}
		if len(res.Details) < 2 || !strings.Contains(res.Details[1], "parent_checked_nodes=0") {
			t.Errorf("expected parent_overflow skip note, got %v", res.Details)
		}
	})

	t.Run("ignored_parent_with_zero_geometry_reports_skip", func(t *testing.T) {
		// 除外かつゼロ幾何の親: 親検査はスキップされ、ゼロ幾何スキップとして見える。
		web := `[{"selector":"#card","x":0,"y":0,"w":0,"h":0},{"selector":"#child","x":10,"y":10,"w":80,"h":40,"parent":"#card"}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, []string{"#card"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "success" || res.IgnoredCount != 1 {
			t.Fatalf("got %#v", res)
		}
		if res.ParentOverflowSkippedZeroGeom != 1 {
			t.Fatalf("parent_overflow_skipped_zero_geom=%d", res.ParentOverflowSkippedZeroGeom)
		}
		if len(res.UnresolvedParentRefs) != 0 {
			t.Fatalf("unexpected unresolved_parent_refs=%v", res.UnresolvedParentRefs)
		}
	})

	t.Run("ignore_nodes_excludes_overflow", func(t *testing.T) {
		web := `[{"selector":"#page","x":0,"y":0,"w":100,"h":50},{"selector":"#wide","x":0,"y":0,"w":900,"h":50}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, []string{"#wide"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "success" || res.IgnoredCount != 1 {
			t.Fatalf("got %#v", res)
		}
		if len(res.IgnoredNodes) != 1 || res.IgnoredNodes[0] != "web:#wide" {
			t.Fatalf("IgnoredNodes=%v", res.IgnoredNodes)
		}
	})

	t.Run("ignored_parent_still_resolves_for_child_overflow", func(t *testing.T) {
		// 親を ignore しても、子の parent_overflow には親座標を使う（報告対象外≠親解決不能）。
		web := `[{"selector":"#card","x":0,"y":0,"w":400,"h":200},{"selector":"#child","x":0,"y":0,"w":500,"h":50,"parent":"#card"}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, []string{"#card"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "mismatch" || res.IgnoredCount != 1 {
			t.Fatalf("got %#v", res)
		}
		if res.ParentCheckedNodes != 1 {
			t.Errorf("parent_checked_nodes=%d, want 1 (ignored parent still resolves)", res.ParentCheckedNodes)
		}
		found := false
		for _, issue := range res.Issues {
			if issue.Type == "parent_overflow" && issue.Selector == "#child" {
				found = true
			}
		}
		if !found {
			t.Fatalf("ignored parent must still resolve for child parent_overflow: %#v", res.Issues)
		}
		if len(res.UnresolvedParentRefs) != 0 || res.ParentOverflowSkippedZeroGeom != 0 {
			t.Fatalf("valid ignored parent must not count as skip: unresolved=%v zero=%d",
				res.UnresolvedParentRefs, res.ParentOverflowSkippedZeroGeom)
		}
	})

	t.Run("unresolved_parent_reported_status_unchanged", func(t *testing.T) {
		web := `[{"selector":"#child","x":0,"y":0,"w":100,"h":100,"parent":"#missing"}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "success" {
			t.Fatalf("unresolved parent must not change status, got %#v", res)
		}
		if res.IssueCount != 0 {
			t.Fatalf("expected no layout issues, got %#v", res)
		}
		if res.ParentCheckedNodes != 0 {
			t.Errorf("parent_checked_nodes=%d, want 0", res.ParentCheckedNodes)
		}
		if len(res.UnresolvedParentRefs) != 1 || res.UnresolvedParentRefs[0] != "Web: '#missing'" {
			t.Fatalf("expected unresolved_parent_refs=[Web: '#missing'], got %v", res.UnresolvedParentRefs)
		}
	})

	t.Run("ignore_nodes_wildcard_and_unmatched", func(t *testing.T) {
		web := `[{"selector":".ad-banner","x":0,"y":0,"w":900,"h":50},{"selector":"#ok","x":0,"y":0,"w":100,"h":50}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, []string{".ad-*", "#missing"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "success" {
			t.Fatalf("got %#v", res)
		}
		if len(res.UnmatchedIgnores) != 1 || res.UnmatchedIgnores[0] != "#missing" {
			t.Fatalf("unmatched=%v", res.UnmatchedIgnores)
		}
	})

	t.Run("ignore_region_by_center", func(t *testing.T) {
		web := `[{"selector":"#page","x":0,"y":0,"w":100,"h":50},{"selector":"#wide","x":0,"y":0,"w":900,"h":50}]`
		// #wide center is (450, 25)
		res, err := CheckLayoutIntegrity(web, 768, 1024, nil, []Region{{X: 400, Y: 0, W: 100, H: 50}})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "success" || res.IgnoredCount != 1 {
			t.Fatalf("got %#v", res)
		}
		if len(res.IgnoredNodes) != 1 || res.IgnoredNodes[0] != "web:#wide" {
			t.Fatalf("IgnoredNodes=%v", res.IgnoredNodes)
		}
	})

	// IgnoredNodes は ignore_nodes / ignore_region で除外した web:<selector> の一覧。
	// 件数は IgnoredCount と一致し、プレフィックス除外の過剰一致検証に使う (Issue #488)。
	t.Run("ignored_nodes_prefix_and_skipped", func(t *testing.T) {
		web := `[
			{"selector":"body","x":0,"y":0,"w":768,"h":100},
			{"selector":"body > .ad","x":0,"y":100,"w":900,"h":50},
			{"selector":"#ok","x":0,"y":200,"w":100,"h":50}
		]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, []string{"body*"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "success" || res.IgnoredCount != 2 {
			t.Fatalf("got %#v", res)
		}
		want := []string{"web:body", "web:body > .ad"}
		if !stringSlicesEqual(res.IgnoredNodes, want) {
			t.Fatalf("IgnoredNodes=%v want %v", res.IgnoredNodes, want)
		}

		allIgnored, err := CheckLayoutIntegrity(web, 768, 1024, []string{"body*", "#ok"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if allIgnored.Status != "skipped" || allIgnored.IgnoredCount != 3 {
			t.Fatalf("got %#v", allIgnored)
		}
		wantAll := []string{"web:body", "web:body > .ad", "web:#ok"}
		if !stringSlicesEqual(allIgnored.IgnoredNodes, wantAll) {
			t.Fatalf("IgnoredNodes=%v want %v", allIgnored.IgnoredNodes, wantAll)
		}

		none, err := CheckLayoutIntegrity(web, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if none.IgnoredCount != 0 || len(none.IgnoredNodes) != 0 {
			t.Fatalf("expected empty IgnoredNodes, got %#v", none)
		}
	})

	t.Run("empty_array_mismatch", func(t *testing.T) {
		res, err := CheckLayoutIntegrity("[]", 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "mismatch" || res.CheckedNodes != 0 {
			t.Fatalf("got %#v", res)
		}
	})

	t.Run("all_ignored_skipped", func(t *testing.T) {
		web := `[{"selector":"#wide","x":0,"y":0,"w":900,"h":50}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, []string{"#wide"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "skipped" {
			t.Fatalf("got %#v", res)
		}
	})

	t.Run("invalid_json", func(t *testing.T) {
		_, err := CheckLayoutIntegrity("{", 768, 1024, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "failed to parse Web layout JSON") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("viewport_zero_error", func(t *testing.T) {
		web := `[{"selector":"#a","x":0,"y":0,"w":10,"h":10}]`
		cases := [][2]float64{{0, 1024}, {768, 0}, {-1, 1024}, {768, -1}}
		for _, c := range cases {
			_, err := CheckLayoutIntegrity(web, c[0], c[1], nil, nil)
			if err == nil || !strings.Contains(err.Error(), "viewport_width and viewport_height must be greater than 0") {
				t.Errorf("w=%v h=%v err=%v", c[0], c[1], err)
			}
		}
	})

	t.Run("subpixel_slop", func(t *testing.T) {
		half := `[{"selector":"#a","x":0,"y":0,"w":768.5,"h":10}]`
		res, err := CheckLayoutIntegrity(half, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "success" {
			t.Fatalf("0.5px should not flag: %#v", res)
		}

		exact := `[{"selector":"#a","x":0,"y":0,"w":769,"h":10}]`
		res, err = CheckLayoutIntegrity(exact, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "success" {
			t.Fatalf("exactly 1.0px slop should not flag: %#v", res)
		}

		over := `[{"selector":"#a","x":0,"y":0,"w":769.5,"h":10}]`
		res, err = CheckLayoutIntegrity(over, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "mismatch" {
			t.Fatalf("1.5px should flag: %#v", res)
		}
	})

	t.Run("parent_subpixel_slop", func(t *testing.T) {
		half := `[{"selector":"#p","x":0,"y":0,"w":100,"h":50},{"selector":"#c","x":0,"y":0,"w":100.5,"h":10,"parent":"#p"}]`
		res, err := CheckLayoutIntegrity(half, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "success" {
			t.Fatalf("0.5px parent overflow should not flag: %#v", res)
		}
		over := `[{"selector":"#p","x":0,"y":0,"w":100,"h":50},{"selector":"#c","x":0,"y":0,"w":101.5,"h":10,"parent":"#p"}]`
		res, err = CheckLayoutIntegrity(over, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "mismatch" {
			t.Fatalf("1.5px parent overflow should flag: %#v", res)
		}
	})

	t.Run("zero_size_skipped_not_issue", func(t *testing.T) {
		web := `[{"selector":"#hidden","x":0,"y":0,"w":0,"h":0},{"selector":"#ok","x":0,"y":0,"w":100,"h":50}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "success" || res.CheckedNodes != 1 {
			t.Fatalf("got %#v", res)
		}
	})

	t.Run("negative_geometry_is_input_error", func(t *testing.T) {
		// 一部ノードだけ負の w/h でも、無言の除外ではなく入力エラーにする。
		web := `[{"selector":"#page","x":0,"y":0,"w":768,"h":200},{"selector":"#badge","x":0,"y":0,"w":-120,"h":24}]`
		_, err := CheckLayoutIntegrity(web, 768, 1024, nil, nil)
		if err == nil {
			t.Fatal("expected input error for negative width/height")
		}
		msg := err.Error()
		for _, want := range []string{"Web node", "#badge", "negative", "-120"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q should contain %q", msg, want)
			}
		}
	})

	t.Run("all_zero_geometry_mismatch_message", func(t *testing.T) {
		// 必須キーは揃っているが幾何が全て 0（collapsed のみ）のときは、
		// 「データが空」とは区別したメッセージで幾何欠落を通知する。
		// キー名ミス（width/height 等）は parseWebLayoutNodes で入力エラーになる。
		web := `[{"selector":"#a","x":0,"y":0,"w":0,"h":0}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "mismatch" || res.CheckedNodes != 0 {
			t.Fatalf("got %#v", res)
		}
		if len(res.Details) != 1 || !strings.Contains(res.Details[0], "no Web nodes have non-zero geometry") {
			t.Fatalf("details=%v", res.Details)
		}
		if !strings.Contains(res.Details[0], `check the layout JSON keys are {"selector","x","y","w","h","parent"}`) {
			t.Errorf("details should mention the expected JSON keys, got %q", res.Details[0])
		}
		if strings.Contains(res.Details[0], "Web layout node data is empty") {
			t.Errorf("zero geometry should not be reported as empty data: %q", res.Details[0])
		}
	})

	t.Run("partial_ignore_remaining_zero_geometry_is_mismatch", func(t *testing.T) {
		// ignore で一部だけ除外し、残りがすべて幾何 0 のとき「全件除外」の
		// skipped と誤報せず、ゼロ幾何の mismatch にする（Issue #475）。
		web := `[{"selector":"#a","x":0,"y":0,"w":0,"h":0},{"selector":"#b","x":0,"y":0,"w":10,"h":10}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, []string{"#b"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "mismatch" {
			t.Fatalf("expected mismatch for remaining zero-geometry node, got %#v", res)
		}
		if res.IgnoredCount != 1 || res.CheckedNodes != 0 {
			t.Fatalf("ignored_count=1 checked_nodes=0 expected, got %#v", res)
		}
		if len(res.Details) != 1 || !strings.Contains(res.Details[0], "no Web nodes have non-zero geometry") {
			t.Fatalf("details should report zero geometry, got %v", res.Details)
		}
		if strings.Contains(res.Details[0], "All Web nodes were excluded") {
			t.Errorf("must not report all-excluded when a zero-geometry node remains: %q", res.Details[0])
		}
		if !strings.Contains(res.Details[0], "1 node was ignored by ignore_nodes / ignore_region") {
			t.Errorf("details should note concurrent ignore count, got %q", res.Details[0])
		}
	})

	t.Run("summary_viewport_preset_note", func(t *testing.T) {
		web := `[{"selector":"#page","x":0,"y":0,"w":768,"h":200}]`

		port, err := CheckLayoutIntegrity(web, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(port.Details) == 0 || !strings.Contains(port.Details[0], "viewport 768x1024 CSS px (iPad portrait 768x1024") {
			t.Fatalf("portrait summary=%v", port.Details)
		}

		land, err := CheckLayoutIntegrity(web, 1024, 768, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(land.Details[0], "viewport 1024x768 CSS px (iPad landscape 1024x768") {
			t.Fatalf("landscape summary=%v", land.Details)
		}

		// 明示 viewport_width / viewport_height でプリセットと異なるサイズを指定した
		// 場合は、実サイズと食い違う iPad プリセットの注記を出さない。
		custom, err := CheckLayoutIntegrity(web, 1024, 600, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(custom.Details[0], "viewport 1024x600 CSS px") {
			t.Fatalf("custom summary=%q", custom.Details[0])
		}
		if strings.Contains(custom.Details[0], "iPad") {
			t.Errorf("custom viewport summary should not mention iPad presets: %q", custom.Details[0])
		}
	})

	t.Run("unmatched_ignore_regions", func(t *testing.T) {
		web := `[{"selector":"#ad","x":0,"y":0,"w":900,"h":50},{"selector":"#ok","x":0,"y":0,"w":100,"h":50}]`
		// #ad center (450, 25) は 1 つ目の領域に含まれ、2 つ目（座標ミス相当）は
		// どのノード中心にも当たらないため UnmatchedIgnoreRegions に報告される。
		regions := []Region{{X: 400, Y: 0, W: 100, H: 50}, {X: 900, Y: 900, W: 10, H: 10}}
		res, err := CheckLayoutIntegrity(web, 768, 1024, nil, regions)
		if err != nil {
			t.Fatal(err)
		}
		if res.IgnoredCount != 1 || res.Status != "success" {
			t.Fatalf("got %#v", res)
		}
		if len(res.UnmatchedIgnoreRegions) != 1 || res.UnmatchedIgnoreRegions[0] != "900,900,10,10" {
			t.Fatalf("unmatched_ignore_regions=%v", res.UnmatchedIgnoreRegions)
		}

		// 全領域がノード中心に当たる場合は何も報告しない。
		hit := []Region{{X: 0, Y: 0, W: 100, H: 50}}
		res2, err := CheckLayoutIntegrity(web, 768, 1024, nil, hit)
		if err != nil {
			t.Fatal(err)
		}
		if len(res2.UnmatchedIgnoreRegions) != 0 {
			t.Fatalf("expected no unmatched_ignore_regions, got %v", res2.UnmatchedIgnoreRegions)
		}
	})

	t.Run("both_issue_types", func(t *testing.T) {
		web := `[{"selector":"#card","x":0,"y":0,"w":400,"h":50},{"selector":"#child","x":0,"y":0,"w":900,"h":50,"parent":"#card"}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		types := map[string]bool{}
		for _, issue := range res.Issues {
			if issue.Selector == "#child" {
				types[issue.Type] = true
			}
		}
		if !types["viewport_overflow_x"] || !types["parent_overflow"] {
			t.Fatalf("expected both types, got %#v", res.Issues)
		}
	})
}

// TestLayoutIntegrity_UTF8BOMStripped verifies that a leading UTF-8 BOM (U+FEFF)
// on the Web layout JSON is stripped before Unmarshal, matching layout_tree.
func TestLayoutIntegrity_UTF8BOMStripped(t *testing.T) {
	webJSON := `[{"selector":"#page","x":0,"y":0,"w":768,"h":200}]`

	t.Run("without_bom", func(t *testing.T) {
		result, err := CheckLayoutIntegrity(webJSON, 768, 1024, nil, nil)
		if err != nil {
			t.Fatalf("CheckLayoutIntegrity failed: %v", err)
		}
		if result.Status != "success" || result.IssueCount != 0 {
			t.Errorf("Expected success with 0 issues, got status=%s issues=%d", result.Status, result.IssueCount)
		}
	})

	t.Run("with_bom", func(t *testing.T) {
		result, err := CheckLayoutIntegrity("\uFEFF"+webJSON, 768, 1024, nil, nil)
		if err != nil {
			t.Fatalf("CheckLayoutIntegrity failed with BOM-prefixed JSON: %v", err)
		}
		if result.Status != "success" || result.IssueCount != 0 {
			t.Errorf("Expected success with 0 issues, got status=%s issues=%d", result.Status, result.IssueCount)
		}
	})
}

// TestLayoutIntegrity_RequiredKeys rejects missing or mistyped geometry keys
// instead of treating them as w/h=0 and silently skipping (false success).
func TestLayoutIntegrity_RequiredKeys(t *testing.T) {
	tests := []struct {
		name    string
		webJSON string
		wantSub string
	}{
		{
			name:    "width_height_keys_rejected",
			webJSON: `[{"selector":"body","x":0,"y":0,"w":768,"h":1024},{"selector":"#card","x":900,"y":10,"width":300,"height":100}]`,
			wantSub: "Web layout node at index 1 is missing required field 'w'",
		},
		{
			name:    "missing_h",
			webJSON: `[{"selector":"#card","x":0,"y":0,"w":100}]`,
			wantSub: "Web layout node at index 0 is missing required field 'h'",
		},
		{
			name:    "missing_selector_key",
			webJSON: `[{"x":0,"y":0,"w":100,"h":50}]`,
			wantSub: "Web layout node at index 0 is missing required field 'selector'",
		},
		{
			name:    "empty_selector",
			webJSON: `[{"selector":"","x":0,"y":0,"w":100,"h":50}]`,
			wantSub: "Web layout node at index 0 has empty required field 'selector'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CheckLayoutIntegrity(tc.webJSON, 768, 1024, nil, nil)
			if err == nil {
				t.Fatal("expected input error for invalid layout node fields")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q should contain %q", err.Error(), tc.wantSub)
			}
		})
	}

	t.Run("correct_keys_still_detect_overflow", func(t *testing.T) {
		web := `[{"selector":"body","x":0,"y":0,"w":768,"h":1024},{"selector":"#card","x":900,"y":10,"w":300,"h":100}]`
		res, err := CheckLayoutIntegrity(web, 768, 1024, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "mismatch" || res.CheckedNodes != 2 || res.IssueCount < 1 {
			t.Fatalf("expected mismatch with overflow, got %#v", res)
		}
	})
}
