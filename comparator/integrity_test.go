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
		raw, _ := json.Marshal(res)
		if strings.Contains(string(raw), `"issues"`) {
			t.Errorf("issues should be omitempty when empty, got %s", raw)
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
		if res.Status != "success" {
			t.Fatalf("got %#v", res)
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
