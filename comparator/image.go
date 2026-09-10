package comparator

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"

	"github.com/orisano/pixelmatch"
)

// Region は画像比較時に除外（マスク）する矩形領域を表す（ピクセル座標）。
type Region struct {
	X int
	Y int
	W int
	H int
}

// RunPixelMatch performs strict pixel-by-pixel VRT using pixelmatch. When
// generateDiff is false, the diff image is not rendered and an empty string
// is returned instead of its base64 data URI. ignoreRegions are masked with
// white on both images before comparison so their content is ignored.
// ignoreRegions のうち画像矩形と全く交差しない領域は draw.Draw の自動クリップ
// により何もマスクされないため、"x,y,w,h" 形式の文字列リストとして検出結果を
// 返す (layout_tree モードの unmatched_ignores と同様のフィードバック)。
func RunPixelMatch(imgABytes, imgBBytes []byte, threshold float64, generateDiff bool, ignoreRegions []Region) (float64, int, int, string, []string, error) {
	imgA, _, err := image.Decode(bytes.NewReader(imgABytes))
	if err != nil {
		return 0, 0, 0, "", nil, fmt.Errorf("failed to decode design image: %w", err)
	}

	imgB, _, err := image.Decode(bytes.NewReader(imgBBytes))
	if err != nil {
		return 0, 0, 0, "", nil, fmt.Errorf("failed to decode web screenshot: %w", err)
	}

	normA, normB, err := EnsureSameSize(imgA, imgB)
	if err != nil {
		return 0, 0, 0, "", nil, err
	}

	bounds := normA.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	// 0次元画像は totalPixels=0 となり一致率計算が0除算 (NaN) になるため、
	// 明示的なエラーとして報告する。
	if w == 0 || h == 0 {
		return 0, 0, 0, "", nil, fmt.Errorf("image dimensions are zero (%dx%d); strict comparison requires non-zero image size", w, h)
	}
	totalPixels := w * h

	// 除外領域 (ignore_region) を両画像とも白でマスクしてから比較する。
	// 画像矩形と全く交差しない領域は何もマスクされないため、"x,y,w,h" 形式の
	// 文字列リストとして検出し、応答で警告できるよう呼び出し側へ返す。
	var outOfBounds []string
	if len(ignoreRegions) > 0 {
		var oobA, oobB []string
		normA, oobA = maskRegions(normA, ignoreRegions)
		normB, oobB = maskRegions(normB, ignoreRegions)
		outOfBounds = mergeOutOfBoundsRegions(oobA, oobB)
	}

	opts := []pixelmatch.MatchOption{
		pixelmatch.Threshold(threshold),
		// 注: IncludeAntiAlias を渡さないデフォルト (includeAA=false) では、
		// アンチエイリアス境界ピクセルは差分カウントから自動除外される
		// （README の「アンチエイリアスの境界は自動除外」と整合する）。
	}
	var diffImg image.Image
	if generateDiff {
		diffImg = image.NewRGBA(bounds)
		opts = append(opts, pixelmatch.WriteTo(&diffImg))
	}

	diffCount, err := pixelmatch.MatchPixel(normA, normB, opts...)
	if err != nil {
		return 0, 0, 0, "", nil, fmt.Errorf("pixelmatch error: %w", err)
	}

	matchRate := float64(totalPixels-diffCount) / float64(totalPixels) * 100.0
	if !generateDiff {
		return matchRate, totalPixels, diffCount, "", outOfBounds, nil
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, diffImg); err != nil {
		return 0, 0, 0, "", nil, fmt.Errorf("failed to encode diff PNG: %w", err)
	}
	diffDataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())

	return matchRate, totalPixels, diffCount, diffDataURI, outOfBounds, nil
}

// CalculateLayoutSimilarityWithDiff calculates aHash (16x16) similarity and, when
// generateDiff is true, renders a diff-visualization PNG (256x256) and returns it
// as a base64 data URI. Each cell is rendered as a 16x16 pixel block (256x256
// total): matching cells show the grayscale value from image A; mismatching
// cells are highlighted in red. ignoreRegions are masked with white on both
// images before hashing so their content is ignored. Returns the match rate and
// an empty string when generateDiff is false. The diff image is kept in memory
// (一時ファイルは作成しない), so repeated comparisons never accumulate PNG files
// in the temp directory. ignoreRegions のうち画像矩形と全く交差しない領域は
// draw.Draw の自動クリップにより何もマスクされないため、"x,y,w,h" 形式の
// 文字列リストとして検出結果を返す (両画像の寸法は異なり得るため、いずれかの
// 画像で範囲外の領域を報告する)。grayA / grayB のいずれかが一様 (ベタ塗り) の
// 場合は aHash が退化するため、status / match_rate には影響させず警告
// メッセージのリスト (warnings) を返す (非一様な通常のペアでは空)。
// あわせて不一致セル数 (diffBits) を 0–256 の int で返す (aHash は 16x16 =
// 256 セルのため画像サイズによらず固定)。一致率はこの 256 段階の離散値から
// 算出されるため、呼び出し側が "N of 256 blocks differ" のように数量として
// 報告できる (strict モードの差分ピクセル数 diffCount に対応する情報)。
func CalculateLayoutSimilarityWithDiff(imgA, imgB image.Image, generateDiff bool, ignoreRegions []Region) (float64, int, string, []string, []string, error) {
	// 0次元画像は意味のある比較ができないため明示的なエラーとする。
	if b := imgA.Bounds(); b.Dx() == 0 || b.Dy() == 0 {
		return 0, 0, "", nil, nil, fmt.Errorf("image A dimensions are zero (%dx%d); perceptual comparison requires non-zero image size", b.Dx(), b.Dy())
	}
	if b := imgB.Bounds(); b.Dx() == 0 || b.Dy() == 0 {
		return 0, 0, "", nil, nil, fmt.Errorf("image B dimensions are zero (%dx%d); perceptual comparison requires non-zero image size", b.Dx(), b.Dy())
	}

	// 除外領域 (ignore_region) を両画像とも白でマスクしてから比較する。
	// 両画像の寸法は異なり得るため、いずれかの画像で範囲外の領域 (完全には
	// マスクされない) を "x,y,w,h" 形式の文字列リストとして検出して返す。
	var outOfBounds []string
	if len(ignoreRegions) > 0 {
		var oobA, oobB []string
		imgA, oobA = maskRegions(imgA, ignoreRegions)
		imgB, oobB = maskRegions(imgB, ignoreRegions)
		outOfBounds = mergeOutOfBoundsRegions(oobA, oobB)
	}

	grayA := resizeTo16x16Gray(imgA)
	grayB := resizeTo16x16Gray(imgB)

	var sumA, sumB uint32
	for i := 0; i < 256; i++ {
		sumA += uint32(grayA[i])
		sumB += uint32(grayB[i])
	}
	avgA := byte(sumA / 256)
	avgB := byte(sumB / 256)

	// aHash は各画像自身の平均輝度で2値化するため、一様 (ベタ塗り) な画像では
	// 全セルが同一ビットになる (255>=255 も 0>=0 も true)。全面白 vs 全面黒の
	// ようなペアでも diffBits=0 → 一致率100% となり、撮影失敗・真っ黒スクショ等が
	// 無検証で合格する。status / match_rate は変えず、一様な画像を検出して
	// 警告として呼び出し側に通知する。
	var warnings []string
	if isUniformGray(grayA) {
		warnings = append(warnings, "degenerate aHash: image A is uniform; perceptual match may be unreliable")
	}
	if isUniformGray(grayB) {
		warnings = append(warnings, "degenerate aHash: image B is uniform; perceptual match may be unreliable")
	}

	const cellScale = 16 // each aHash cell rendered as 16x16 px → 256x256 image
	var diffImg *image.RGBA
	if generateDiff {
		diffImg = image.NewRGBA(image.Rect(0, 0, 16*cellScale, 16*cellScale))
	}

	diffBits := 0
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			i := y*16 + x
			bitA := grayA[i] >= avgA
			bitB := grayB[i] >= avgB
			diff := bitA != bitB
			if diff {
				diffBits++
			}

			if generateDiff {
				var clr color.Color
				if diff {
					clr = color.RGBA{255, 0, 0, 255} // red highlight for mismatched cells
				} else {
					v := grayA[i]
					clr = color.RGBA{v, v, v, 255} // grayscale from image A
				}
				rect := image.Rect(x*cellScale, y*cellScale, (x+1)*cellScale, (y+1)*cellScale)
				draw.Draw(diffImg, rect, &image.Uniform{clr}, image.Point{}, draw.Src)
			}
		}
	}

	var diffDataURI string
	if generateDiff {
		var buf bytes.Buffer
		if err := png.Encode(&buf, diffImg); err != nil {
			return 0, 0, "", nil, nil, fmt.Errorf("failed to encode diff PNG: %w", err)
		}
		diffDataURI = "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	}

	similarity := float64(256-diffBits) / 256.0 * 100.0
	return similarity, diffBits, diffDataURI, outOfBounds, warnings, nil
}

// maskRegions returns a copy of img with the given regions filled with white,
// along with the list of regions that do not intersect the image rectangle at
// all, formatted as "x,y,w,h" strings. 領域は描画先の画像範囲に合わせて自動的
// にクリップされるため一部だけ交差する領域はマスクされるが、全く交差しない
// 領域は何もマスクされず沈黙する。座標ミスに呼び出し側が気付けるよう、それら
// の領域を検出して返す (w/h が 0 の退化した領域も何もマスクしないため検出対象)。
func maskRegions(img image.Image, regions []Region) (image.Image, []string) {
	bounds := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), img, bounds.Min, draw.Src)
	white := &image.Uniform{color.RGBA{255, 255, 255, 255}}
	var outOfBounds []string
	for _, r := range regions {
		rect := image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H)
		// 画像矩形と全く交差しない領域は draw.Draw の自動クリップにより
		// 何もマスクされないため、警告対象として検出する。
		if rect.Intersect(dst.Bounds()).Empty() {
			outOfBounds = append(outOfBounds, fmt.Sprintf("%d,%d,%d,%d", r.X, r.Y, r.W, r.H))
			continue
		}
		draw.Draw(dst, rect, white, image.Point{}, draw.Src)
	}
	return dst, outOfBounds
}

// mergeOutOfBoundsRegions merges the out-of-bounds region lists detected on
// each image into one deduplicated list. RunPixelMatch では EnsureSameSize に
// より両画像の寸法が同一のため同じリストが渡り、重複除去により 1 つにまとまる。
// CalculateLayoutSimilarityWithDiff では両画像の寸法が異なり得るため、
// いずれかの画像で範囲外の領域 (完全にはマスクされない) を報告する。
func mergeOutOfBoundsRegions(lists ...[]string) []string {
	var merged []string
	seen := make(map[string]bool)
	for _, list := range lists {
		for _, s := range list {
			if seen[s] {
				continue
			}
			seen[s] = true
			merged = append(merged, s)
		}
	}
	return merged
}

// isUniformGray は 16x16 グレースケール配列の最小値と最大値が一致する
// (一様 = ベタ塗り) かどうかを判定する。aHash は各画像自身の平均輝度で
// 2値化するため、一様な画像は全セルが同一ビットになり (255>=255 も 0>=0 も
// true)、画像間で内容が全く異なっても diffBits=0 (一致率100%) になってしまう。
func isUniformGray(gray []byte) bool {
	minVal, maxVal := gray[0], gray[0]
	for _, v := range gray[1:] {
		if v < minVal {
			minVal = v
		}
		if v > maxVal {
			maxVal = v
		}
	}
	return minVal == maxVal
}

func resizeTo16x16Gray(img image.Image) []byte {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	gray := make([]byte, 256)

	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			startX := bounds.Min.X + (x*w)/16
			endX := bounds.Min.X + ((x+1)*w)/16
			startY := bounds.Min.Y + (y*h)/16
			endY := bounds.Min.Y + ((y+1)*h)/16

			if endX <= startX {
				endX = startX + 1
			}
			if endY <= startY {
				endY = startY + 1
			}

			var sumR, sumG, sumB uint32
			var count uint32

			for py := startY; py < endY; py++ {
				for px := startX; px < endX; px++ {
					// 透過ピクセルは白背景に合成してから輝度化する。RGBA() は
					// premultiplied 値を返すため透過部分は (0,0,0) になり、アルファを
					// 無視すると透過が「黒」として扱われてしまう。strict モード
					// (pixelmatch は白背景に合成して比較する) と同じ「白背景」前提に
					// 揃え、背景透過PNGと不透明スクショの組での誤不一致を防ぐ
					// (Issue #134)。premultiplied 値は r,g,b ≤ a が保証されるため
					// 0xffff-a を加えても桁あふれしない。
					r, g, b, a := img.At(px, py).RGBA()
					if a != 0xffff {
						r += 0xffff - a
						g += 0xffff - a
						b += 0xffff - a
					}
					sumR += r >> 8
					sumG += g >> 8
					sumB += b >> 8
					count++
				}
			}

			avgR := sumR / count
			avgG := sumG / count
			avgB := sumB / count

			yVal := uint32(0.299*float64(avgR) + 0.587*float64(avgG) + 0.114*float64(avgB))
			gray[y*16+x] = byte(yVal)
		}
	}
	return gray
}

// EnsureSameSize verifies that both images have identical dimensions and returns
// them unchanged. Size differences are reported as an error instead of being
// silently padded, so that strict pixel comparison never counts padding as matches.
func EnsureSameSize(imgA, imgB image.Image) (image.Image, image.Image, error) {
	boundsA := imgA.Bounds()
	boundsB := imgB.Bounds()
	wA, hA := boundsA.Dx(), boundsA.Dy()
	wB, hB := boundsB.Dx(), boundsB.Dy()

	if wA != wB || hA != hB {
		return nil, nil, fmt.Errorf("image size mismatch: image A is %dx%d, image B is %dx%d; strict comparison requires identical sizes", wA, hA, wB, hB)
	}
	return imgA, imgB, nil
}
