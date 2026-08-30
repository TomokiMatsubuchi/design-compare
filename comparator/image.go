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
func RunPixelMatch(imgABytes, imgBBytes []byte, threshold float64, generateDiff bool, ignoreRegions []Region) (float64, int, int, string, error) {
	imgA, _, err := image.Decode(bytes.NewReader(imgABytes))
	if err != nil {
		return 0, 0, 0, "", fmt.Errorf("failed to decode design image: %w", err)
	}

	imgB, _, err := image.Decode(bytes.NewReader(imgBBytes))
	if err != nil {
		return 0, 0, 0, "", fmt.Errorf("failed to decode web screenshot: %w", err)
	}

	normA, normB, err := EnsureSameSize(imgA, imgB)
	if err != nil {
		return 0, 0, 0, "", err
	}

	bounds := normA.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	// 0次元画像は totalPixels=0 となり一致率計算が0除算 (NaN) になるため、
	// 明示的なエラーとして報告する。
	if w == 0 || h == 0 {
		return 0, 0, 0, "", fmt.Errorf("image dimensions are zero (%dx%d); strict comparison requires non-zero image size", w, h)
	}
	totalPixels := w * h

	// 除外領域 (ignore_region) を両画像とも白でマスクしてから比較する。
	if len(ignoreRegions) > 0 {
		normA = maskRegions(normA, ignoreRegions)
		normB = maskRegions(normB, ignoreRegions)
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
		return 0, 0, 0, "", fmt.Errorf("pixelmatch error: %w", err)
	}

	matchRate := float64(totalPixels-diffCount) / float64(totalPixels) * 100.0
	if !generateDiff {
		return matchRate, totalPixels, diffCount, "", nil
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, diffImg); err != nil {
		return 0, 0, 0, "", fmt.Errorf("failed to encode diff PNG: %w", err)
	}
	diffDataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())

	return matchRate, totalPixels, diffCount, diffDataURI, nil
}

// CalculateLayoutSimilarityWithDiff calculates aHash (16x16) similarity and, when
// generateDiff is true, renders a diff-visualization PNG (256x256) and returns it
// as a base64 data URI. Each cell is rendered as a 16x16 pixel block (256x256
// total): matching cells show the grayscale value from image A; mismatching
// cells are highlighted in red. ignoreRegions are masked with white on both
// images before hashing so their content is ignored. Returns the match rate and
// an empty string when generateDiff is false. 一時ファイルは作成しない。
func CalculateLayoutSimilarityWithDiff(imgA, imgB image.Image, generateDiff bool, ignoreRegions []Region) (float64, string, error) {
	// 0次元画像は意味のある比較ができないため明示的なエラーとする。
	if b := imgA.Bounds(); b.Dx() == 0 || b.Dy() == 0 {
		return 0, "", fmt.Errorf("image A dimensions are zero (%dx%d); perceptual comparison requires non-zero image size", b.Dx(), b.Dy())
	}
	if b := imgB.Bounds(); b.Dx() == 0 || b.Dy() == 0 {
		return 0, "", fmt.Errorf("image B dimensions are zero (%dx%d); perceptual comparison requires non-zero image size", b.Dx(), b.Dy())
	}

	if len(ignoreRegions) > 0 {
		imgA = maskRegions(imgA, ignoreRegions)
		imgB = maskRegions(imgB, ignoreRegions)
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
			return 0, "", fmt.Errorf("failed to encode diff PNG: %w", err)
		}
		diffDataURI = "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	}

	similarity := float64(256-diffBits) / 256.0 * 100.0
	return similarity, diffDataURI, nil
}

// maskRegions returns a copy of img with the given regions filled with white.
// 領域は描画先の画像範囲に合わせて自動的にクリップされる。
func maskRegions(img image.Image, regions []Region) image.Image {
	bounds := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), img, bounds.Min, draw.Src)
	white := &image.Uniform{color.RGBA{255, 255, 255, 255}}
	for _, r := range regions {
		rect := image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H)
		if !rect.Empty() {
			draw.Draw(dst, rect, white, image.Point{}, draw.Src)
		}
	}
	return dst
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
					r, g, b, _ := img.At(px, py).RGBA()
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
