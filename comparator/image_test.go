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
// anti-aliased boundary pixels from the diff count, as documented in the
// README ("アンチエイリアスの境界は自動除外").
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

		_, _, diffCount, _, err := RunPixelMatch(
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

		_, _, diffCount, _, err := RunPixelMatch(
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