package comparator

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"testing"
)

func encodePNGBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("failed to encode PNG: %v", err)
	}
	return buf.Bytes()
}

// TestRunPixelMatch_AntiAliasExclusion verifies that with the default
// configuration (no IncludeAntiAlias option), pixelmatch excludes
// anti-aliased boundary pixels from the diff count, as documented in
// the README ("アンチエイリアスの境界は自動除外").
//
// This serves as a regression test for the removal of pixelmatch.IncludeAntiAlias
// from RunPixelMatch's option list. When IncludeAntiAlias is not passed,
// includeAA defaults to false, causing pixelmatch's isAntiAliased heuristic to
// skip anti-aliased edge pixels when counting diffs.
func TestRunPixelMatch_AntiAliasExclusion(t *testing.T) {
	const w, h = 12, 12
	black := color.RGBA{0, 0, 0, 255}
	white := color.RGBA{255, 255, 255, 255}
	grayDark := color.RGBA{64, 64, 64, 255}
	grayLight := color.RGBA{192, 192, 192, 255}

	// Sub-test 1: differences only at an anti-aliased luminance boundary.
	//
	//   Image A: cols 0-4 black, col 5 gray(64),  cols 6-11 white
	//   Image B: cols 0-4 black, col 5 gray(192), cols 6-11 white
	//
	// Column 5 pixels sit at the boundary between flat-black (col 4) and
	// flat-white (col 6) regions. pixelmatch's isAntiAliased heuristic
	// detects these as anti-aliased edge pixels (the gray pixel has
	// luminance contrast with both its dark and bright neighbours, and
	// those contrasting neighbours are in flat regions with many identical
	// siblings in both images). With includeAA=false (the default after
	// removing IncludeAntiAlias), all 12 boundary pixels are excluded
	// from diffCount.
	t.Run("AA_boundary_pixels_excluded", func(t *testing.T) {
		imgA := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(imgA, imgA.Bounds(), &image.Uniform{white}, image.Point{}, draw.Src)
		draw.Draw(imgA, image.Rect(0, 0, 5, h), &image.Uniform{black}, image.Point{}, draw.Src)
		draw.Draw(imgA, image.Rect(5, 0, 6, h), &image.Uniform{grayDark}, image.Point{}, draw.Src)

		imgB := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(imgB, imgB.Bounds(), &image.Uniform{white}, image.Point{}, draw.Src)
		draw.Draw(imgB, image.Rect(0, 0, 5, h), &image.Uniform{black}, image.Point{}, draw.Src)
		draw.Draw(imgB, image.Rect(5, 0, 6, h), &image.Uniform{grayLight}, image.Point{}, draw.Src)

		_, _, diffCount, _, _, err := RunPixelMatch(
			encodePNGBytes(t, imgA), encodePNGBytes(t, imgB),
			0.1, false, nil,
		)
		if err != nil {
			t.Fatalf("RunPixelMatch failed: %v", err)
		}
		if diffCount != 0 {
			t.Errorf("Expected diffCount=0 (AA boundary pixels excluded by default), got %d", diffCount)
		}
	})

	// Sub-test 2: a genuine difference in a flat region (not at any
	// luminance boundary) is still counted as a diff. This ensures the
	// AA exclusion does not mask real pixel differences.
	t.Run("non_AA_difference_counted", func(t *testing.T) {
		imgA := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(imgA, imgA.Bounds(), &image.Uniform{white}, image.Point{}, draw.Src)

		imgB := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(imgB, imgB.Bounds(), &image.Uniform{white}, image.Point{}, draw.Src)
		imgB.SetRGBA(6, 6, black) // single black pixel in flat white region

		_, _, diffCount, _, _, err := RunPixelMatch(
			encodePNGBytes(t, imgA), encodePNGBytes(t, imgB),
			0.1, false, nil,
		)
		if err != nil {
			t.Fatalf("RunPixelMatch failed: %v", err)
		}
		if diffCount == 0 {
			t.Errorf("Expected diffCount>0 (non-AA difference should be counted), got 0")
		}
	})
}

// newIgnoreRegionTestImages は ignore_region の範囲外検出テストで使う
// 200x200 の画像ペア (A: 白地に左上100x100の黒矩形 / B: 全面白) を返す。
func newIgnoreRegionTestImages() (image.Image, image.Image) {
	const w, h = 200, 200
	white := color.RGBA{255, 255, 255, 255}
	black := color.RGBA{0, 0, 0, 255}
	imgA := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(imgA, imgA.Bounds(), &image.Uniform{white}, image.Point{}, draw.Src)
	draw.Draw(imgA, image.Rect(0, 0, 100, 100), &image.Uniform{black}, image.Point{}, draw.Src)
	imgB := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(imgB, imgB.Bounds(), &image.Uniform{white}, image.Point{}, draw.Src)
	return imgA, imgB
}

// TestRunPixelMatch_IgnoreRegionOutOfBounds verifies that ignore regions which
// do not intersect the image at all (silently clipped away by draw.Draw and
// thus masking nothing) are detected and returned as "x,y,w,h" strings, while
// in-bounds and partially clipped regions are not reported.
func TestRunPixelMatch_IgnoreRegionOutOfBounds(t *testing.T) {
	t.Run("out_of_bounds_region_reported", func(t *testing.T) {
		imgA, imgB := newIgnoreRegionTestImages()
		_, _, _, _, outOfBounds, err := RunPixelMatch(
			encodePNGBytes(t, imgA), encodePNGBytes(t, imgB),
			0.1, false, []Region{{X: 500, Y: 500, W: 100, H: 100}},
		)
		if err != nil {
			t.Fatalf("RunPixelMatch failed: %v", err)
		}
		if len(outOfBounds) != 1 || outOfBounds[0] != "500,500,100,100" {
			t.Errorf("Expected outOfBounds=[500,500,100,100], got %v", outOfBounds)
		}
	})

	t.Run("in_bounds_and_partial_regions_not_reported", func(t *testing.T) {
		imgA, imgB := newIgnoreRegionTestImages()
		// "0,0,100,100" は画像内 / "150,150,100,100" は右下が画像外にはみ出すが
		// 一部だけクリップされてマスクは機能するため、いずれも警告対象外。
		_, _, _, _, outOfBounds, err := RunPixelMatch(
			encodePNGBytes(t, imgA), encodePNGBytes(t, imgB),
			0.1, false, []Region{{X: 0, Y: 0, W: 100, H: 100}, {X: 150, Y: 150, W: 100, H: 100}},
		)
		if err != nil {
			t.Fatalf("RunPixelMatch failed: %v", err)
		}
		if len(outOfBounds) != 0 {
			t.Errorf("Expected no outOfBounds for in-bounds/partially-clipped regions, got %v", outOfBounds)
		}
	})
}

// TestCalculateLayoutSimilarityWithDiff_IgnoreRegionOutOfBounds verifies the
// same out-of-bounds detection for the perceptual (aHash) comparison path.
// 両画像の寸法が異なる場合は、いずれかの画像で範囲外の領域も報告される
// (片方の画像でしかマスクされず除外が完全には効かないため)。
func TestCalculateLayoutSimilarityWithDiff_IgnoreRegionOutOfBounds(t *testing.T) {
	white := color.RGBA{255, 255, 255, 255}
	imgA := image.NewRGBA(image.Rect(0, 0, 100, 100)) // 100x100
	draw.Draw(imgA, imgA.Bounds(), &image.Uniform{white}, image.Point{}, draw.Src)
	imgB := image.NewRGBA(image.Rect(0, 0, 200, 200)) // 200x200
	draw.Draw(imgB, imgB.Bounds(), &image.Uniform{white}, image.Point{}, draw.Src)

	// 両画像とも範囲外の領域は報告される
	_, _, _, outOfBounds, _, err := CalculateLayoutSimilarityWithDiff(imgA, imgB, false, []Region{{X: 500, Y: 500, W: 100, H: 100}})
	if err != nil {
		t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
	}
	if len(outOfBounds) != 1 || outOfBounds[0] != "500,500,100,100" {
		t.Errorf("Expected outOfBounds=[500,500,100,100], got %v", outOfBounds)
	}

	// 範囲内の領域は報告されない
	_, _, _, outOfBounds, _, err = CalculateLayoutSimilarityWithDiff(imgA, imgB, false, []Region{{X: 0, Y: 0, W: 100, H: 100}})
	if err != nil {
		t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
	}
	if len(outOfBounds) != 0 {
		t.Errorf("Expected no outOfBounds for in-bounds region, got %v", outOfBounds)
	}

	// 画像B (200x200) には範囲内だが画像A (100x100) では範囲外の領域も報告される
	_, _, _, outOfBounds, _, err = CalculateLayoutSimilarityWithDiff(imgA, imgB, false, []Region{{X: 150, Y: 150, W: 50, H: 50}})
	if err != nil {
		t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
	}
	if len(outOfBounds) != 1 || outOfBounds[0] != "150,150,50,50" {
		t.Errorf("Expected outOfBounds=[150,150,50,50] (out of bounds only for image A), got %v", outOfBounds)
	}
}

// TestCalculateLayoutSimilarityWithDiff_UniformImageWarning verifies that
// uniform (solid-color) images are detected and reported as warnings while the
// match rate itself is left unchanged. aHash は各画像自身の平均輝度で2値化
// するため、一様 (ベタ塗り) 画像は全セルが同一ビットになり、全面白 vs 全面黒
// でも diffBits=0 → 一致率100% となる。status/match_rate は変えずに警告で
// 呼び出し側に気付かせる (Issue #131)。
func TestCalculateLayoutSimilarityWithDiff_UniformImageWarning(t *testing.T) {
	white := color.RGBA{255, 255, 255, 255}
	black := color.RGBA{0, 0, 0, 255}

	t.Run("solid_white_vs_solid_black_warns_both", func(t *testing.T) {
		imgA := image.NewRGBA(image.Rect(0, 0, 100, 100))
		draw.Draw(imgA, imgA.Bounds(), &image.Uniform{white}, image.Point{}, draw.Src)
		imgB := image.NewRGBA(image.Rect(0, 0, 100, 100))
		draw.Draw(imgB, imgB.Bounds(), &image.Uniform{black}, image.Point{}, draw.Src)

		matchRate, _, _, _, warnings, err := CalculateLayoutSimilarityWithDiff(imgA, imgB, false, nil)
		if err != nil {
			t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
		}
		// 挙動は変えない: aHash が退化するため全面白 vs 全面黒でも一致率100%のまま
		if matchRate != 100 {
			t.Errorf("Expected matchRate=100 (status/match_rate unchanged), got %v", matchRate)
		}
		// 両画像とも一様のため、A/B それぞれの警告が返る
		want := []string{
			"degenerate aHash: image A is uniform; perceptual match may be unreliable",
			"degenerate aHash: image B is uniform; perceptual match may be unreliable",
		}
		if len(warnings) != 2 || warnings[0] != want[0] || warnings[1] != want[1] {
			t.Errorf("Expected warnings=%v, got %v", want, warnings)
		}
	})

	t.Run("uniform_A_only_warns_A", func(t *testing.T) {
		// A: 全面白 (一様) / B: 左白・右黒 (非一様)
		imgA := image.NewRGBA(image.Rect(0, 0, 100, 100))
		draw.Draw(imgA, imgA.Bounds(), &image.Uniform{white}, image.Point{}, draw.Src)
		imgB := image.NewRGBA(image.Rect(0, 0, 100, 100))
		draw.Draw(imgB, image.Rect(0, 0, 50, 100), &image.Uniform{white}, image.Point{}, draw.Src)
		draw.Draw(imgB, image.Rect(50, 0, 100, 100), &image.Uniform{black}, image.Point{}, draw.Src)

		_, _, _, _, warnings, err := CalculateLayoutSimilarityWithDiff(imgA, imgB, false, nil)
		if err != nil {
			t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
		}
		if len(warnings) != 1 || warnings[0] != "degenerate aHash: image A is uniform; perceptual match may be unreliable" {
			t.Errorf("Expected only image A warning, got %v", warnings)
		}
	})

	t.Run("non_uniform_pair_no_warnings", func(t *testing.T) {
		// 左右分割 (A) vs 上下分割 (B) の通常の明暗パターンを持つペアは警告されない
		imgA := image.NewRGBA(image.Rect(0, 0, 100, 100))
		draw.Draw(imgA, image.Rect(0, 0, 50, 100), &image.Uniform{white}, image.Point{}, draw.Src)
		draw.Draw(imgA, image.Rect(50, 0, 100, 100), &image.Uniform{black}, image.Point{}, draw.Src)
		imgB := image.NewRGBA(image.Rect(0, 0, 100, 100))
		draw.Draw(imgB, image.Rect(0, 0, 100, 50), &image.Uniform{white}, image.Point{}, draw.Src)
		draw.Draw(imgB, image.Rect(0, 50, 100, 100), &image.Uniform{black}, image.Point{}, draw.Src)

		_, _, _, _, warnings, err := CalculateLayoutSimilarityWithDiff(imgA, imgB, false, nil)
		if err != nil {
			t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
		}
		if len(warnings) != 0 {
			t.Errorf("Expected no warnings for non-uniform pair, got %v", warnings)
		}
	})
}

// TestCalculateLayoutSimilarityWithDiff_DiffBits verifies that the number of
// differing aHash cells (diffBits) is returned alongside the match rate so
// callers can report it as "N of 256 blocks differ" (Issue #141)。
// 左右分割 (A) vs 上下分割 (B) のペアは 16x16 グリッドで不一致セルが
// 右上・左下の 2 象限 = ちょうど 128 個になる。
func TestCalculateLayoutSimilarityWithDiff_DiffBits(t *testing.T) {
	white := color.RGBA{255, 255, 255, 255}
	black := color.RGBA{0, 0, 0, 255}

	// A: 左白・右黒 / B: 上白・下黒
	imgA := image.NewRGBA(image.Rect(0, 0, 200, 200))
	draw.Draw(imgA, image.Rect(0, 0, 100, 200), &image.Uniform{white}, image.Point{}, draw.Src)
	draw.Draw(imgA, image.Rect(100, 0, 200, 200), &image.Uniform{black}, image.Point{}, draw.Src)
	imgB := image.NewRGBA(image.Rect(0, 0, 200, 200))
	draw.Draw(imgB, image.Rect(0, 0, 200, 100), &image.Uniform{white}, image.Point{}, draw.Src)
	draw.Draw(imgB, image.Rect(0, 100, 200, 200), &image.Uniform{black}, image.Point{}, draw.Src)

	matchRate, diffBits, _, _, _, err := CalculateLayoutSimilarityWithDiff(imgA, imgB, false, nil)
	if err != nil {
		t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
	}
	if diffBits != 128 {
		t.Errorf("Expected diffBits=128 (half of 256 cells differ), got %d", diffBits)
	}
	if matchRate != 50 {
		t.Errorf("Expected matchRate=50 ((256-128)/256), got %v", matchRate)
	}

	// 同一画像同士は全セル一致のため diffBits=0
	_, diffBits, _, _, _, err = CalculateLayoutSimilarityWithDiff(imgA, imgA, false, nil)
	if err != nil {
		t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
	}
	if diffBits != 0 {
		t.Errorf("Expected diffBits=0 for identical images, got %d", diffBits)
	}
}
