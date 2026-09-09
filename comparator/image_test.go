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
	_, _, outOfBounds, err := CalculateLayoutSimilarityWithDiff(imgA, imgB, false, []Region{{X: 500, Y: 500, W: 100, H: 100}})
	if err != nil {
		t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
	}
	if len(outOfBounds) != 1 || outOfBounds[0] != "500,500,100,100" {
		t.Errorf("Expected outOfBounds=[500,500,100,100], got %v", outOfBounds)
	}

	// 範囲内の領域は報告されない
	_, _, outOfBounds, err = CalculateLayoutSimilarityWithDiff(imgA, imgB, false, []Region{{X: 0, Y: 0, W: 100, H: 100}})
	if err != nil {
		t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
	}
	if len(outOfBounds) != 0 {
		t.Errorf("Expected no outOfBounds for in-bounds region, got %v", outOfBounds)
	}

	// 画像B (200x200) には範囲内だが画像A (100x100) では範囲外の領域も報告される
	_, _, outOfBounds, err = CalculateLayoutSimilarityWithDiff(imgA, imgB, false, []Region{{X: 150, Y: 150, W: 50, H: 50}})
	if err != nil {
		t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
	}
	if len(outOfBounds) != 1 || outOfBounds[0] != "150,150,50,50" {
		t.Errorf("Expected outOfBounds=[150,150,50,50] (out of bounds only for image A), got %v", outOfBounds)
	}
}
