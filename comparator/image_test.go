package comparator

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"
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

		_, _, diffCount, _, _, _, _, err := RunPixelMatch(
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

		_, _, diffCount, _, _, _, _, err := RunPixelMatch(
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

// TestRunPixelMatch_UnsupportedFormatHint verifies that decode failures caused
// by unsupported image formats (e.g. WebP, SVG) include a hint about the
// supported formats (PNG, JPEG, GIF) in the error message, so callers can
// determine the corrective action (format conversion) without an extra
// round-trip (Issue #122).
func TestRunPixelMatch_UnsupportedFormatHint(t *testing.T) {
	// WebP のマジックナンバー ("RIFF" + "WEBP") を含むバイト列。
	// Go 標準の image パッケージはデコードできず "image: unknown format" になる。
	webpBytes := []byte("RIFF\x00\x00\x00\x00WEBPVP8 fake payload")
	pngBytes := encodePNGBytes(t, image.NewRGBA(image.Rect(0, 0, 2, 2)))

	for _, tc := range []struct {
		name        string
		imgA, imgB  []byte
		wantKeyword string
	}{
		{"webp_as_design_image", webpBytes, pngBytes, "failed to decode design image"},
		{"webp_as_web_screenshot", pngBytes, webpBytes, "failed to decode web screenshot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// RunPixelMatch は8戻り値 (matchRate, totalPixels, diffPixels, diffImage,
			// outOfBounds, imageSize, diffRegions, err) を返すため、デコードエラー時の
			// 不要な戻り値も含めて受ける。
			_, _, _, _, _, _, _, err := RunPixelMatch(tc.imgA, tc.imgB, 0.1, false, nil)
			if err == nil {
				t.Fatal("expected decode error for WebP bytes, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantKeyword) {
				t.Errorf("expected error to contain %q, got %q", tc.wantKeyword, err.Error())
			}
			if !strings.Contains(err.Error(), "supported: PNG, JPEG, GIF") {
				t.Errorf("expected error to contain supported-format hint, got %q", err.Error())
			}
			if !strings.Contains(err.Error(), "WebP/SVG are not supported") {
				t.Errorf("expected error to mention unsupported formats, got %q", err.Error())
			}
		})
	}
}

// TestRunPixelMatch_CorruptImageOmitsFormatHint verifies that decode failures
// for a recognized but corrupted/truncated PNG (image.ErrFormat ではない失敗、
// 例: unexpected EOF) do NOT append UnsupportedImageFormatHint. PNG/JPEG/GIF
// だが破損・途中切れのファイルで「WebP/SVG は非対応」と読める文面が付くと、
// 原因を形式違いだと誤認するためである (Issue #122)。
func TestRunPixelMatch_CorruptImageOmitsFormatHint(t *testing.T) {
	// PNG シグネチャは有効だが途中で切れたバイト列 → image.Decode は
	// "unexpected EOF" を返し image.ErrFormat ではない。
	corruptBytes := encodePNGBytes(t, image.NewRGBA(image.Rect(0, 0, 4, 4)))[:20]
	validBytes := encodePNGBytes(t, image.NewRGBA(image.Rect(0, 0, 2, 2)))

	_, _, _, _, _, _, _, err := RunPixelMatch(corruptBytes, validBytes, 0.1, false, nil)
	if err == nil {
		t.Fatal("expected decode error for truncated PNG bytes, got nil")
	}
	if !strings.Contains(err.Error(), "failed to decode design image") {
		t.Errorf("expected decode error prefix, got %q", err.Error())
	}
	if strings.Contains(err.Error(), UnsupportedImageFormatHint) {
		t.Errorf("expected no unsupported-format hint for a corrupted image, got %q", err.Error())
	}
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
		_, _, _, _, outOfBounds, _, _, err := RunPixelMatch(
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

	t.Run("overflowing_region_does_not_mask_entire_image", func(t *testing.T) {
		imgA, imgB := newIgnoreRegionTestImages()
		// x+w が int の最大値を超える値。image.Rect に渡すと正規化で全面マスクになる。
		x := math.MaxInt/2 + 1
		_, _, diffCount, _, outOfBounds, _, _, err := RunPixelMatch(
			encodePNGBytes(t, imgA), encodePNGBytes(t, imgB),
			0.1, false, []Region{{X: x, Y: 0, W: x, H: 10}},
		)
		if err != nil {
			t.Fatalf("RunPixelMatch failed: %v", err)
		}
		if diffCount == 0 {
			t.Errorf("Expected remaining diffs (overflow region must not white-out the whole image), got 0")
		}
		if len(outOfBounds) != 1 {
			t.Errorf("Expected overflowing region reported as out-of-bounds, got %v", outOfBounds)
		}
	})

	t.Run("in_bounds_and_partial_regions_not_reported", func(t *testing.T) {
		imgA, imgB := newIgnoreRegionTestImages()
		// "0,0,100,100" は画像内 / "150,150,100,100" は右下が画像外にはみ出すが
		// 一部だけクリップされてマスクは機能するため、いずれも警告対象外。
		_, _, _, _, outOfBounds, _, _, err := RunPixelMatch(
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

// TestRunPixelMatch_ImageSize verifies that the compared dimensions are
// returned as "WxH" so callers can echo them without reconstructing from
// totalPixels (w*h) alone.
func TestRunPixelMatch_ImageSize(t *testing.T) {
	imgA, imgB := newIgnoreRegionTestImages()
	_, totalPixels, _, _, _, imageSize, _, err := RunPixelMatch(
		encodePNGBytes(t, imgA), encodePNGBytes(t, imgB),
		0.1, false, nil,
	)
	if err != nil {
		t.Fatalf("RunPixelMatch failed: %v", err)
	}
	if imageSize != "200x200" {
		t.Errorf("Expected imageSize=200x200, got %q", imageSize)
	}
	if totalPixels != 200*200 {
		t.Errorf("Expected totalPixels=40000, got %d", totalPixels)
	}
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
	_, _, _, outOfBounds, _, _, err := CalculateLayoutSimilarityWithDiff(imgA, imgB, false, []Region{{X: 500, Y: 500, W: 100, H: 100}})
	if err != nil {
		t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
	}
	if len(outOfBounds) != 1 || outOfBounds[0] != "500,500,100,100" {
		t.Errorf("Expected outOfBounds=[500,500,100,100], got %v", outOfBounds)
	}

	// 範囲内の領域は報告されない
	_, _, _, outOfBounds, _, _, err = CalculateLayoutSimilarityWithDiff(imgA, imgB, false, []Region{{X: 0, Y: 0, W: 100, H: 100}})
	if err != nil {
		t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
	}
	if len(outOfBounds) != 0 {
		t.Errorf("Expected no outOfBounds for in-bounds region, got %v", outOfBounds)
	}

	// 画像B (200x200) には範囲内だが画像A (100x100) では範囲外の領域も報告される
	_, _, _, outOfBounds, _, _, err = CalculateLayoutSimilarityWithDiff(imgA, imgB, false, []Region{{X: 150, Y: 150, W: 50, H: 50}})
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

		matchRate, _, _, _, warnings, _, err := CalculateLayoutSimilarityWithDiff(imgA, imgB, false, nil)
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

		_, _, _, _, warnings, _, err := CalculateLayoutSimilarityWithDiff(imgA, imgB, false, nil)
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

		_, _, _, _, warnings, _, err := CalculateLayoutSimilarityWithDiff(imgA, imgB, false, nil)
		if err != nil {
			t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
		}
		if len(warnings) != 0 {
			t.Errorf("Expected no warnings for non-uniform pair, got %v", warnings)
		}
	})
}

// TestCalculateLayoutSimilarityWithDiff_AspectRatioMismatchWarning verifies that
// pairs whose aspect ratios (w/h) differ by more than 2.0 are reported in
// warnings while match_rate is left unchanged. perceptual は各画像を独立に
// 16x16 へ引き伸ばすため、幾何が大きく違うペアでも明暗パターンが似ていれば
// 高一致率になる。status/match_rate は変えずに警告で気付かせる (Issue #199)。
func TestCalculateLayoutSimilarityWithDiff_AspectRatioMismatchWarning(t *testing.T) {
	white := color.RGBA{255, 255, 255, 255}
	black := color.RGBA{0, 0, 0, 255}

	// 左右分割の非一様パターンを塗り、一様画像警告と混ざらないようにする。
	splitLR := func(w, h int) *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(img, image.Rect(0, 0, w/2, h), &image.Uniform{white}, image.Point{}, draw.Src)
		draw.Draw(img, image.Rect(w/2, 0, w, h), &image.Uniform{black}, image.Point{}, draw.Src)
		return img
	}

	t.Run("mismatched_aspect_warns", func(t *testing.T) {
		imgA := splitLR(100, 100)  // aspect 1.00
		imgB := splitLR(1000, 100) // aspect 10.00
		_, _, _, _, warnings, _, err := CalculateLayoutSimilarityWithDiff(imgA, imgB, false, nil)
		if err != nil {
			t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
		}
		want := "aspect ratio mismatch: image A is 100x100 (aspect 1.00), image B is 1000x100 (aspect 10.00); perceptual comparison stretches both to 16x16"
		found := false
		for _, w := range warnings {
			if w == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected warnings to contain %q, got %v", want, warnings)
		}
	})

	t.Run("same_aspect_no_warning", func(t *testing.T) {
		imgA := splitLR(100, 100) // aspect 1.00
		imgB := splitLR(200, 200) // aspect 1.00
		_, _, _, _, warnings, _, err := CalculateLayoutSimilarityWithDiff(imgA, imgB, false, nil)
		if err != nil {
			t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
		}
		for _, w := range warnings {
			if strings.Contains(w, "aspect ratio mismatch") {
				t.Errorf("Expected no aspect ratio warning for same-aspect pair, got %v", warnings)
			}
		}
	})
}

// TestCalculateLayoutSimilarityWithDiff_TransparentCompositesOntoWhite verifies
// that fully transparent pixels are composited onto white before aHash
// (Issue #168)。RGBA() は premultiplied 値を返すため透過部分をそのまま輝度化
// すると RGB=0（黒）になり、白背景の Web スクショと大規模な誤不一致になる。
// aHash は各画像自身の平均輝度で2値化するため、全面透過 vs 全面白のような
// 一様なペアは合成の有無にかかわらず全セルが同一ビット (diffBits=0) になり
// 回帰を検出できない。そこで非一様な内容で検証する: 「透過背景＋右半分に黒
// 矩形」vs「白背景＋同じ黒矩形」は、白合成されていれば 100% 一致し、合成が
// 壊れて透過が黒として扱われると透過側がほぼ全面黒になって約半分のセルが
// 不一致になる。
func TestCalculateLayoutSimilarityWithDiff_TransparentCompositesOntoWhite(t *testing.T) {
	black := color.RGBA{0, 0, 0, 255}
	white := color.RGBA{255, 255, 255, 255}

	// A: 透過背景 + 右半分に黒矩形 (Figma の背景透過書き出しを模擬)。
	// NewRGBA はゼロ初期化のため、矩形を描いていない左半分は全面透過のまま。
	imgA := image.NewRGBA(image.Rect(0, 0, 64, 64))
	draw.Draw(imgA, image.Rect(32, 0, 64, 64), &image.Uniform{black}, image.Point{}, draw.Src)

	// B: 白背景 + 同じ黒矩形 (不透明な Web スクショ)。
	imgB := image.NewRGBA(image.Rect(0, 0, 64, 64))
	draw.Draw(imgB, imgB.Bounds(), &image.Uniform{white}, image.Point{}, draw.Src)
	draw.Draw(imgB, image.Rect(32, 0, 64, 64), &image.Uniform{black}, image.Point{}, draw.Src)

	matchRate, diffBits, _, _, _, _, err := CalculateLayoutSimilarityWithDiff(imgA, imgB, false, nil)
	if err != nil {
		t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
	}
	if matchRate != 100 {
		t.Errorf("Expected matchRate=100 (transparent-bg content vs white-bg content after white compositing), got %v", matchRate)
	}
	if diffBits != 0 {
		t.Errorf("Expected diffBits=0, got %d", diffBits)
	}
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

	matchRate, diffBits, _, _, _, _, err := CalculateLayoutSimilarityWithDiff(imgA, imgB, false, nil)
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
	_, diffBits, _, _, _, _, err = CalculateLayoutSimilarityWithDiff(imgA, imgA, false, nil)
	if err != nil {
		t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
	}
	if diffBits != 0 {
		t.Errorf("Expected diffBits=0 for identical images, got %d", diffBits)
	}
}

// TestCalculateLayoutSimilarityWithDiff_DiffCells verifies that mismatched
// aHash cells are returned as 16x16 grid coordinates in row-major order, even
// when generateDiff is false (Issue #186)。左右分割 vs 上下分割は右上・左下
// の 2 象限 = 128 セルが不一致になる。
func TestCalculateLayoutSimilarityWithDiff_DiffCells(t *testing.T) {
	white := color.RGBA{255, 255, 255, 255}
	black := color.RGBA{0, 0, 0, 255}

	imgA := image.NewRGBA(image.Rect(0, 0, 200, 200))
	draw.Draw(imgA, image.Rect(0, 0, 100, 200), &image.Uniform{white}, image.Point{}, draw.Src)
	draw.Draw(imgA, image.Rect(100, 0, 200, 200), &image.Uniform{black}, image.Point{}, draw.Src)
	imgB := image.NewRGBA(image.Rect(0, 0, 200, 200))
	draw.Draw(imgB, image.Rect(0, 0, 200, 100), &image.Uniform{white}, image.Point{}, draw.Src)
	draw.Draw(imgB, image.Rect(0, 100, 200, 200), &image.Uniform{black}, image.Point{}, draw.Src)

	_, diffBits, _, _, _, diffCells, err := CalculateLayoutSimilarityWithDiff(imgA, imgB, false, nil)
	if err != nil {
		t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
	}
	want := expectedLeftRightVsTopBottomDiffCells()
	if diffBits != len(want) {
		t.Errorf("Expected diffBits=%d, got %d", len(want), diffBits)
	}
	if len(diffCells) != len(want) {
		t.Fatalf("Expected %d diffCells, got %d", len(want), len(diffCells))
	}
	for i, cell := range diffCells {
		if cell != want[i] {
			t.Errorf("diffCells[%d]=%v, want %v", i, cell, want[i])
			break
		}
	}

	_, _, _, _, _, sameCells, err := CalculateLayoutSimilarityWithDiff(imgA, imgA, false, nil)
	if err != nil {
		t.Fatalf("CalculateLayoutSimilarityWithDiff failed: %v", err)
	}
	if len(sameCells) != 0 {
		t.Errorf("Expected no diffCells for identical images, got %v", sameCells)
	}
}

func expectedLeftRightVsTopBottomDiffCells() []DiffCell {
	var cells []DiffCell
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			bitA := x < 8 // left half white
			bitB := y < 8 // top half white
			if bitA != bitB {
				cells = append(cells, DiffCell{GridX: x, GridY: y})
			}
		}
	}
	return cells
}

// TestMaxImageDimensionLimit verifies that images whose width or height
// exceeds maxImageDimension are rejected with an explicit, actionable error
// instead of being processed. 巨大または圧縮爆弾的な画像は image.Decode だけでも
// 数百MB〜数GB を確保し、maskRegions の RGBA コピーや pixelmatch の差分画像で
// 追加確保されるため、長時間稼働する MCP サーバープロセスが OOM で落ちない
// よう修復可能なエラーとして弾く (Issue #158)。
func TestMaxImageDimensionLimit(t *testing.T) {
	// strict モード: 上限を 1px 超える 8193x1 の PNG はエラーになる
	t.Run("RunPixelMatch_rejects_oversized", func(t *testing.T) {
		img := image.NewRGBA(image.Rect(0, 0, maxImageDimension+1, 1))
		_, _, _, _, _, _, _, err := RunPixelMatch(
			encodePNGBytes(t, img), encodePNGBytes(t, img),
			0.1, false, nil,
		)
		if err == nil {
			t.Fatal("Expected an error for an oversized image (8193x1), got nil")
		}
		want := fmt.Sprintf("image is %dx%d; maximum supported dimension is %d, resize the images before comparison", maxImageDimension+1, 1, maxImageDimension)
		if err.Error() != want {
			t.Errorf("Expected error %q, got %q", want, err.Error())
		}
	})

	// strict モード: 上限ピッタリ (8192x1) はエラーにならない (実用画像を弾かない)
	t.Run("RunPixelMatch_allows_max_dimension", func(t *testing.T) {
		img := image.NewRGBA(image.Rect(0, 0, maxImageDimension, 1))
		_, _, diffCount, _, _, _, _, err := RunPixelMatch(
			encodePNGBytes(t, img), encodePNGBytes(t, img),
			0.1, false, nil,
		)
		if err != nil {
			t.Fatalf("RunPixelMatch failed for a %dx1 image: %v", maxImageDimension, err)
		}
		if diffCount != 0 {
			t.Errorf("Expected diffCount=0 for identical images, got %d", diffCount)
		}
	})

	// perceptual モード: 幅・高さのいずれかが上限を超えたら A/B それぞれエラーになる
	t.Run("CalculateLayoutSimilarityWithDiff_rejects_oversized", func(t *testing.T) {
		oversized := image.NewRGBA(image.Rect(0, 0, 1, maxImageDimension+1))
		small := image.NewRGBA(image.Rect(0, 0, 100, 100))

		_, _, _, _, _, _, err := CalculateLayoutSimilarityWithDiff(oversized, small, false, nil)
		if err == nil {
			t.Fatal("Expected an error for oversized image A (1x8193), got nil")
		}
		wantA := fmt.Sprintf("image A is 1x%d; maximum supported dimension is %d, resize the images before comparison", maxImageDimension+1, maxImageDimension)
		if err.Error() != wantA {
			t.Errorf("Expected error %q, got %q", wantA, err.Error())
		}

		_, _, _, _, _, _, err = CalculateLayoutSimilarityWithDiff(small, oversized, false, nil)
		if err == nil {
			t.Fatal("Expected an error for oversized image B (1x8193), got nil")
		}
		wantB := fmt.Sprintf("image B is 1x%d; maximum supported dimension is %d, resize the images before comparison", maxImageDimension+1, maxImageDimension)
		if err.Error() != wantB {
			t.Errorf("Expected error %q, got %q", wantB, err.Error())
		}
	})
}

// strict と perceptual の両モードで共有される対応フォーマットヒント (Issue #122)。
const supportedImageFormatsHint = "(supported: PNG, JPEG, GIF; WebP/SVG are not supported)"

// TestRunPixelMatch_DecodeErrorListsSupportedFormats verifies that undecodable
// input (empty bytes or a text payload) reports the formats callers can retry
// with, matching the self-repair pattern used for unknown comparison modes.
func TestRunPixelMatch_DecodeErrorListsSupportedFormats(t *testing.T) {
	valid := encodePNGBytes(t, image.NewRGBA(image.Rect(0, 0, 2, 2)))

	t.Run("empty_design_bytes", func(t *testing.T) {
		_, _, _, _, _, _, _, err := RunPixelMatch(nil, valid, 0.1, false, nil)
		if err == nil {
			t.Fatal("Expected decode error for empty design image bytes, got nil")
		}
		if !strings.Contains(err.Error(), "failed to decode design image") {
			t.Errorf("Expected design-image decode prefix, got %q", err.Error())
		}
		if !strings.Contains(err.Error(), supportedImageFormatsHint) {
			t.Errorf("Expected supported-formats hint, got %q", err.Error())
		}
	})

	t.Run("text_web_screenshot", func(t *testing.T) {
		_, _, _, _, _, _, _, err := RunPixelMatch(valid, []byte("this is not an image"), 0.1, false, nil)
		if err == nil {
			t.Fatal("Expected decode error for text web screenshot bytes, got nil")
		}
		if !strings.Contains(err.Error(), "failed to decode web screenshot") {
			t.Errorf("Expected web-screenshot decode prefix, got %q", err.Error())
		}
		if !strings.Contains(err.Error(), supportedImageFormatsHint) {
			t.Errorf("Expected supported-formats hint, got %q", err.Error())
		}
	})
}

// TestCollectDiffRegions verifies 4-neighborhood components, yellow AA exclusion,
// ranking by pixel count, and the max-10 cap (Issue #187).
func TestCollectDiffRegions(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	yellow := color.RGBA{255, 255, 0, 255}
	gray := color.RGBA{200, 200, 200, 255}

	t.Run("yellow_excluded_and_4_neighborhood", func(t *testing.T) {
		img := image.NewRGBA(image.Rect(0, 0, 8, 8))
		draw.Draw(img, img.Bounds(), &image.Uniform{gray}, image.Point{}, draw.Src)
		img.SetRGBA(1, 1, red)
		img.SetRGBA(2, 2, red) // 斜めのみ接続 → 別成分
		img.SetRGBA(4, 1, yellow)

		got := collectDiffRegions(img)
		if len(got) != 2 {
			t.Fatalf("Expected 2 red 4-neighborhood regions (yellow ignored), got %v", got)
		}
		for _, r := range got {
			if r.W != 1 || r.H != 1 || r.DiffPixels != 1 {
				t.Errorf("Expected 1x1 regions, got %+v", r)
			}
		}
	})

	t.Run("ranked_by_diff_pixels_capped_at_10", func(t *testing.T) {
		img := image.NewRGBA(image.Rect(0, 0, 40, 4))
		draw.Draw(img, img.Bounds(), &image.Uniform{gray}, image.Point{}, draw.Src)
		// 11 個の孤立した赤ピクセル列 (1 行目、偶数列)。11 件目は捨てられる。
		for i := 0; i < 11; i++ {
			img.SetRGBA(i*2, 0, red)
		}
		// より大きい成分を (0,2)-(4,2) に置き、先頭になることを確認する。
		for x := 0; x < 5; x++ {
			img.SetRGBA(x, 2, red)
		}

		got := collectDiffRegions(img)
		if len(got) != maxDiffRegions {
			t.Fatalf("Expected %d regions, got %d: %v", maxDiffRegions, len(got), got)
		}
		if got[0] != (DiffRegion{X: 0, Y: 2, W: 5, H: 1, DiffPixels: 5}) {
			t.Errorf("Expected largest region first, got %+v", got[0])
		}
	})
}

// TestRunPixelMatch_DiffRegions は左上 100x100 の黒矩形 vs 全面白で
// generateDiff=true のときその bounding box が返り、false では返らないこと。
func TestRunPixelMatch_DiffRegions(t *testing.T) {
	imgA, imgB := newIgnoreRegionTestImages()

	t.Run("top_left_100x100_when_generate_diff", func(t *testing.T) {
		_, _, diffCount, _, _, _, regions, err := RunPixelMatch(
			encodePNGBytes(t, imgA), encodePNGBytes(t, imgB),
			0.1, true, nil,
		)
		if err != nil {
			t.Fatalf("RunPixelMatch failed: %v", err)
		}
		if diffCount == 0 {
			t.Fatal("Expected positive diffCount")
		}
		if len(regions) != 1 {
			t.Fatalf("Expected 1 diff region, got %v", regions)
		}
		want := DiffRegion{X: 0, Y: 0, W: 100, H: 100, DiffPixels: 10000}
		if regions[0] != want {
			t.Errorf("Expected %+v, got %+v", want, regions[0])
		}
	})

	t.Run("omitted_when_generate_diff_false", func(t *testing.T) {
		_, _, diffCount, _, _, _, regions, err := RunPixelMatch(
			encodePNGBytes(t, imgA), encodePNGBytes(t, imgB),
			0.1, false, nil,
		)
		if err != nil {
			t.Fatalf("RunPixelMatch failed: %v", err)
		}
		if diffCount == 0 {
			t.Fatal("Expected positive diffCount even without a diff image")
		}
		if regions != nil {
			t.Errorf("Expected no diff regions when generateDiff=false, got %v", regions)
		}
	})
}
