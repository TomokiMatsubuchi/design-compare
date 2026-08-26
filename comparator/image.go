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
	"os"

	"github.com/orisano/pixelmatch"
)

// RunPixelMatch performs strict pixel-by-pixel VRT using pixelmatch
func RunPixelMatch(imgABytes, imgBBytes []byte, threshold float64) (float64, int, int, string, error) {
	imgA, _, err := image.Decode(bytes.NewReader(imgABytes))
	if err != nil {
		return 0, 0, 0, "", fmt.Errorf("failed to decode design image: %w", err)
	}

	imgB, _, err := image.Decode(bytes.NewReader(imgBBytes))
	if err != nil {
		return 0, 0, 0, "", fmt.Errorf("failed to decode web screenshot: %w", err)
	}

	normA, normB := EnsureSameSize(imgA, imgB)
	bounds := normA.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	totalPixels := w * h

	var diffImg image.Image = image.NewRGBA(bounds)

	diffCount, err := pixelmatch.MatchPixel(normA, normB,
		pixelmatch.Threshold(threshold),
		pixelmatch.WriteTo(&diffImg),
		pixelmatch.IncludeAntiAlias,
	)
	if err != nil {
		return 0, 0, 0, "", fmt.Errorf("pixelmatch error: %w", err)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, diffImg); err != nil {
		return 0, 0, 0, "", fmt.Errorf("failed to encode diff PNG: %w", err)
	}
	diffDataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())

	matchRate := float64(totalPixels-diffCount) / float64(totalPixels) * 100.0
	return matchRate, totalPixels, diffCount, diffDataURI, nil
}

// CalculateLayoutSimilarity calculates structural template matching using aHash (16x16)
func CalculateLayoutSimilarity(imgA, imgB image.Image) float64 {
	rate, _, _ := CalculateLayoutSimilarityWithDiff(imgA, imgB)
	return rate
}

// CalculateLayoutSimilarityWithDiff calculates aHash (16x16) similarity and writes a
// diff-visualization PNG to a temp file. Each cell is rendered as a 16x16 pixel block
// (256x256 total): matching cells show the grayscale value from image A; mismatching
// cells are highlighted in red. Returns the match rate, diff file path, and error.
func CalculateLayoutSimilarityWithDiff(imgA, imgB image.Image) (float64, string, error) {
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
	diffImg := image.NewRGBA(image.Rect(0, 0, 16*cellScale, 16*cellScale))

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

	tmpDir := os.TempDir()
	diffFile, err := os.CreateTemp(tmpDir, "perceptual-diff-*.png")
	if err != nil {
		return 0, "", fmt.Errorf("failed to create diff file: %w", err)
	}
	defer diffFile.Close()

	if err := png.Encode(diffFile, diffImg); err != nil {
		return 0, "", fmt.Errorf("failed to encode diff PNG: %w", err)
	}

	similarity := float64(256-diffBits) / 256.0 * 100.0
	return similarity, diffFile.Name(), nil
}

func resizeTo16x16Gray(img image.Image) []byte {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	gray := make([]byte, 256)

	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			startX := bounds.Min.X + (x * w) / 16
			endX := bounds.Min.X + ((x + 1) * w) / 16
			startY := bounds.Min.Y + (y * h) / 16
			endY := bounds.Min.Y + ((y + 1) * h) / 16

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

func EnsureSameSize(imgA, imgB image.Image) (image.Image, image.Image) {
	boundsA := imgA.Bounds()
	boundsB := imgB.Bounds()
	wA, hA := boundsA.Dx(), boundsA.Dy()
	wB, hB := boundsB.Dx(), boundsB.Dy()

	if wA == wB && hA == hB {
		return imgA, imgB
	}

	maxW := wA
	if wB > maxW {
		maxW = wB
	}
	maxH := hA
	if hB > maxH {
		maxH = hB
	}

	newA := image.NewRGBA(image.Rect(0, 0, maxW, maxH))
	draw.Draw(newA, newA.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(newA, boundsA, imgA, boundsA.Min, draw.Over)

	newB := image.NewRGBA(image.Rect(0, 0, maxW, maxH))
	draw.Draw(newB, newB.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(newB, boundsB, imgB, boundsB.Min, draw.Over)

	return newA, newB
}
