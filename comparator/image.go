package comparator

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"math"
	"sort"

	"github.com/orisano/pixelmatch"
)

// Region は画像比較時に除外（マスク）する矩形領域を表す（ピクセル座標）。
type Region struct {
	X int
	Y int
	W int
	H int
}

// AHashGridSize は perceptual (aHash) 比較のグリッド一辺のセル数。
// AHashBlocks はセル総数。比較ループ・平均輝度・一致率と、main.go の
// total_blocks / details 応答はここを単一の情報源とする。
const (
	AHashGridSize = 16
	AHashBlocks   = AHashGridSize * AHashGridSize
)

// DiffCell は aHash グリッド上の不一致セル（0–AHashGridSize-1、行優先）。
// perceptual 応答の diff_cells として機械可読な位置を返す。セル (grid_x, grid_y)
// は差分画像の対応するセル矩形にマップされる。
type DiffCell struct {
	GridX int `json:"grid_x"`
	GridY int `json:"grid_y"`
}

// DiffRegion は strict 応答の diff_regions として返す差分の bounding box。
// pixelmatch 差分画像上の赤ピクセル (既定 diffColor) を 4 近傍連結成分に
// 分割した各領域のピクセル座標と差分ピクセル数。アンチエイリアス除外の黄は含めない。
type DiffRegion struct {
	X          int `json:"x"`
	Y          int `json:"y"`
	W          int `json:"w"`
	H          int `json:"h"`
	DiffPixels int `json:"diff_pixels"`
}

// maxDiffRegions は応答に含める連結成分の上限（差分ピクセル数の多い順）。
const maxDiffRegions = 10

// pixelmatch 既定の差分色 (color.RGBA{R: 255} → 不透明赤として描画される)。
func isPixelmatchDiffColor(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r == 0xffff && g == 0 && b == 0
}

// maxImageDimension は比較可能な画像の幅・高さの上限 (8192 = 8K スクショ相当)。
// 巨大または圧縮爆弾的な PNG は image.Decode だけでも数百MB〜数GB を確保し、
// さらに maskRegions の RGBA コピーや pixelmatch の差分画像でメモリ確保が数倍に
// 増幅される。長時間稼働する MCP サーバープロセスが OOM で落ちるのを防ぐため、
// 上限を超えた画像は修復可能な明示的エラーとして弾く (Issue #158)。
const maxImageDimension = 8192

// UnsupportedImageFormatHint は image.Decode 失敗エラーに付ける対応フォーマットの
// ヒント (Issue #122 の指定文面)。strict (RunPixelMatch) と perceptual (main.go)
// の両モードで同じ文面を使うため exported の共有定数とし、文面修正はこの
// 1 箇所で済むようにする。
const UnsupportedImageFormatHint = "(supported: PNG, JPEG, GIF; WebP/SVG are not supported)"

// decodeImageError は image.Decode の失敗エラーに "failed to decode <画像>" の
// コンテキストを付けて返す。対応外の画像形式 (image.ErrFormat) のときだけ
// UnsupportedImageFormatHint を付ける。PNG/JPEG/GIF だが破損・途中切れの
// ファイル (例: unexpected EOF) では「WebP/SVG は非対応」と読める文面が
// 原因を形式違いだと誤認させるため、ヒントは付けない (Issue #122)。
func decodeImageError(what string, err error) error {
	if errors.Is(err, image.ErrFormat) {
		return fmt.Errorf("failed to decode %s: %w %s", what, err, UnsupportedImageFormatHint)
	}
	return fmt.Errorf("failed to decode %s: %w", what, err)
}

// RunPixelMatch performs strict pixel-by-pixel VRT using pixelmatch. When
// generateDiff is false, the diff image is not rendered and an empty string
// is returned instead of its base64 data URI. ignoreRegions are masked with
// white on both images before comparison so their content is ignored.
// ignoreRegions のうち画像矩形と全く交差しない領域は draw.Draw の自動クリップ
// により何もマスクされないため、"x,y,w,h" 形式の文字列リストとして検出結果を
// 返す (layout_tree モードの unmatched_ignores と同様のフィードバック)。
// 成功時の 6 番目の戻り値は比較した画像の寸法 ("WxH")。EnsureSameSize 後の
// 同一サイズなので A/B を分けず image_size として応答へ echo できる。
// generateDiff が true かつ diffCount>0 のとき、7 番目に赤ピクセルの連結成分
// bounding box (最大 10 件) を返す。generateDiff が false なら nil。
func RunPixelMatch(imgABytes, imgBBytes []byte, threshold float64, generateDiff bool, ignoreRegions []Region) (float64, int, int, string, []string, string, []DiffRegion, error) {
	imgA, _, err := image.Decode(bytes.NewReader(imgABytes))
	if err != nil {
		return 0, 0, 0, "", nil, "", nil, decodeImageError("design image", err)
	}

	imgB, _, err := image.Decode(bytes.NewReader(imgBBytes))
	if err != nil {
		return 0, 0, 0, "", nil, "", nil, decodeImageError("web screenshot", err)
	}

	normA, normB, err := EnsureSameSize(imgA, imgB)
	if err != nil {
		return 0, 0, 0, "", nil, "", nil, err
	}

	bounds := normA.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	imageSize := fmt.Sprintf("%dx%d", w, h)
	// 0次元画像は totalPixels=0 となり一致率計算が0除算 (NaN) になるため、
	// 明示的なエラーとして報告する。
	if w == 0 || h == 0 {
		return 0, 0, 0, "", nil, imageSize, nil, fmt.Errorf("image dimensions are zero (%dx%d); strict comparison requires non-zero image size", w, h)
	}
	// 幅・高さのどちらかが上限を超えたら、maskRegions の RGBA コピーや
	// pixelmatch の差分画像による追加確保の前に修復可能なエラーとして弾く
	// (EnsureSameSize 済みのため両画像の寸法は同一)。
	if w > maxImageDimension || h > maxImageDimension {
		return 0, 0, 0, "", nil, imageSize, nil, fmt.Errorf("image is %dx%d; maximum supported dimension is %d, resize the images before comparison", w, h, maxImageDimension)
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
		opts = append(opts, pixelmatch.WriteTo(&diffImg))
	}

	diffCount, err := pixelmatch.MatchPixel(normA, normB, opts...)
	if err != nil {
		return 0, 0, 0, "", nil, imageSize, nil, fmt.Errorf("pixelmatch error: %w", err)
	}

	matchRate := float64(totalPixels-diffCount) / float64(totalPixels) * 100.0
	if !generateDiff {
		return matchRate, totalPixels, diffCount, "", outOfBounds, imageSize, nil, nil
	}

	// pixelmatch は差分描画用に内部で NewRGBA し WriteTo へ差し替えるが、同一画像の
	// fast path では差し替えずに return する。事前確保をやめたためその場合は nil。
	// 旧実装は空の RGBA をエンコードしていたので同等にする。
	if diffImg == nil {
		diffImg = image.NewRGBA(bounds)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, diffImg); err != nil {
		return 0, 0, 0, "", nil, imageSize, nil, fmt.Errorf("failed to encode diff PNG: %w", err)
	}
	diffDataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())

	// generate_diff=true かつ差分があるときだけ赤ピクセルの連結成分を返す。
	// 黄 (アンチエイリアス除外) は差分カウント対象外のため領域にも含めない。
	var regions []DiffRegion
	if diffCount > 0 && diffImg != nil {
		regions = collectDiffRegions(diffImg)
	}

	return matchRate, totalPixels, diffCount, diffDataURI, outOfBounds, imageSize, regions, nil
}

// collectDiffRegions は差分画像の赤ピクセルを 4 近傍連結成分に分割し、
// bounding box とピクセル数を差分ピクセル数の多い順（同数なら y, x）に最大
// maxDiffRegions 件返す。座標は画像原点からのピクセル座標。
func collectDiffRegions(img image.Image) []DiffRegion {
	if img == nil {
		return nil
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return nil
	}

	visited := make([]bool, w*h)
	idx := func(x, y int) int { return (y-bounds.Min.Y)*w + (x - bounds.Min.X) }

	var regions []DiffRegion
	dx := [4]int{1, -1, 0, 0}
	dy := [4]int{0, 0, 1, -1}

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			i := idx(x, y)
			if visited[i] || !isPixelmatchDiffColor(img.At(x, y)) {
				continue
			}
			minX, minY, maxX, maxY := x, y, x, y
			count := 0
			queue := []image.Point{{X: x, Y: y}}
			visited[i] = true
			for len(queue) > 0 {
				p := queue[0]
				queue = queue[1:]
				count++
				if p.X < minX {
					minX = p.X
				}
				if p.Y < minY {
					minY = p.Y
				}
				if p.X > maxX {
					maxX = p.X
				}
				if p.Y > maxY {
					maxY = p.Y
				}
				for k := 0; k < 4; k++ {
					nx, ny := p.X+dx[k], p.Y+dy[k]
					if nx < bounds.Min.X || nx >= bounds.Max.X || ny < bounds.Min.Y || ny >= bounds.Max.Y {
						continue
					}
					ni := idx(nx, ny)
					if visited[ni] || !isPixelmatchDiffColor(img.At(nx, ny)) {
						continue
					}
					visited[ni] = true
					queue = append(queue, image.Point{X: nx, Y: ny})
				}
			}
			regions = append(regions, DiffRegion{
				X:          minX - bounds.Min.X,
				Y:          minY - bounds.Min.Y,
				W:          maxX - minX + 1,
				H:          maxY - minY + 1,
				DiffPixels: count,
			})
		}
	}

	sort.Slice(regions, func(i, j int) bool {
		if regions[i].DiffPixels != regions[j].DiffPixels {
			return regions[i].DiffPixels > regions[j].DiffPixels
		}
		if regions[i].Y != regions[j].Y {
			return regions[i].Y < regions[j].Y
		}
		return regions[i].X < regions[j].X
	})
	if len(regions) > maxDiffRegions {
		regions = regions[:maxDiffRegions]
	}
	if len(regions) == 0 {
		return nil
	}
	return regions
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
// 両画像のアスペクト比 (w/h) の max/min が aspectRatioMismatchThreshold を
// 超える場合も、16x16 への引き伸ばしで幾何が歪むため同様に warnings へ追加する
// (status / match_rate は変えない)。
// あわせて不一致セル数 (diffBits) を 0–256 の int で返す (aHash は 16x16 =
// 256 セルのため画像サイズによらず固定)。一致率はこの 256 段階の離散値から
// 算出されるため、呼び出し側が "N of 256 blocks differ" のように数量として
// 報告できる (strict モードの差分ピクセル数 diffCount に対応する情報)。
// 不一致セルの 16x16 グリッド座標は行優先・決定論的順序の DiffCell スライス
// として返し、generateDiff が false でも空でなければ呼び出し側が画像なしで
// 差分位置を特定できる。
func CalculateLayoutSimilarityWithDiff(imgA, imgB image.Image, generateDiff bool, ignoreRegions []Region) (float64, int, string, []string, []string, []DiffCell, error) {
	// 0次元画像は意味のある比較ができないため明示的なエラーとする。
	if b := imgA.Bounds(); b.Dx() == 0 || b.Dy() == 0 {
		return 0, 0, "", nil, nil, nil, fmt.Errorf("image A dimensions are zero (%dx%d); perceptual comparison requires non-zero image size", b.Dx(), b.Dy())
	}
	if b := imgB.Bounds(); b.Dx() == 0 || b.Dy() == 0 {
		return 0, 0, "", nil, nil, nil, fmt.Errorf("image B dimensions are zero (%dx%d); perceptual comparison requires non-zero image size", b.Dx(), b.Dy())
	}
	// 幅・高さのどちらかが上限を超えたら、maskRegions の RGBA コピーによる
	// 追加確保の前に修復可能なエラーとして弾く (OOM 防止、Issue #158)。
	if b := imgA.Bounds(); b.Dx() > maxImageDimension || b.Dy() > maxImageDimension {
		return 0, 0, "", nil, nil, nil, fmt.Errorf("image A is %dx%d; maximum supported dimension is %d, resize the images before comparison", b.Dx(), b.Dy(), maxImageDimension)
	}
	if b := imgB.Bounds(); b.Dx() > maxImageDimension || b.Dy() > maxImageDimension {
		return 0, 0, "", nil, nil, nil, fmt.Errorf("image B is %dx%d; maximum supported dimension is %d, resize the images before comparison", b.Dx(), b.Dy(), maxImageDimension)
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
	for i := 0; i < AHashBlocks; i++ {
		sumA += uint32(grayA[i])
		sumB += uint32(grayB[i])
	}
	avgA := byte(sumA / AHashBlocks)
	avgB := byte(sumB / AHashBlocks)

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
	if msg := aspectRatioMismatchWarning(imgA, imgB); msg != "" {
		warnings = append(warnings, msg)
	}

	const cellScale = 16 // each aHash cell rendered as 16x16 px → 256x256 image
	var diffImg *image.RGBA
	if generateDiff {
		diffImg = image.NewRGBA(image.Rect(0, 0, AHashGridSize*cellScale, AHashGridSize*cellScale))
	}

	diffBits := 0
	var diffCells []DiffCell
	for y := 0; y < AHashGridSize; y++ {
		for x := 0; x < AHashGridSize; x++ {
			i := y*AHashGridSize + x
			bitA := grayA[i] >= avgA
			bitB := grayB[i] >= avgB
			diff := bitA != bitB
			if diff {
				diffBits++
				diffCells = append(diffCells, DiffCell{GridX: x, GridY: y})
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
			return 0, 0, "", nil, nil, nil, fmt.Errorf("failed to encode diff PNG: %w", err)
		}
		diffDataURI = "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	}

	similarity := float64(AHashBlocks-diffBits) / float64(AHashBlocks) * 100.0
	return similarity, diffBits, diffDataURI, outOfBounds, warnings, diffCells, nil
}

// maskRegions returns a copy of img with the given regions filled with white,
// along with the list of regions that do not intersect the image rectangle at
// all, formatted as "x,y,w,h" strings. 画像矩形と全く交差しない領域は
// draw.Draw の自動クリップにより何もマスクされないため、警告対象として検出して
// 返す。零サイズ領域の入力レベル拒否は parseIgnoreRegions 側の役割である。
func maskRegions(img image.Image, regions []Region) (image.Image, []string) {
	bounds := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), img, bounds.Min, draw.Src)
	white := &image.Uniform{color.RGBA{255, 255, 255, 255}}
	var outOfBounds []string
	for _, r := range regions {
		rect, ok := regionRect(r)
		// 加算オーバーフローや画像矩形と全く交差しない領域はマスクしない。
		// 前者を image.Rect に渡すと正規化で全面マスクになり誤 pass するため。
		if !ok || rect.Intersect(dst.Bounds()).Empty() {
			outOfBounds = append(outOfBounds, fmt.Sprintf("%d,%d,%d,%d", r.X, r.Y, r.W, r.H))
			continue
		}
		draw.Draw(dst, rect, white, image.Point{}, draw.Src)
	}
	return dst, outOfBounds
}

// regionRect builds the ignore rectangle without overflowing x+w / y+h.
// Overflowing addition wraps to a negative Max, and image.Rect then
// normalizes into a huge covering rectangle that would mask the whole image.
func regionRect(r Region) (image.Rectangle, bool) {
	if r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 {
		return image.Rectangle{}, false
	}
	if r.X > math.MaxInt-r.W || r.Y > math.MaxInt-r.H {
		return image.Rectangle{}, false
	}
	return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H), true
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
// aspectRatioMismatchThreshold は perceptual 比較で警告するアスペクト比の
// 相対差 (大きい方 / 小さい方)。16x16 への独立リサイズは縦横比を捨てるため、
// この倍率を超えるペアは一致率が高くても幾何が全く異なる可能性がある。
const aspectRatioMismatchThreshold = 2.0

// aspectRatioMismatchWarning は両画像のアスペクト比 (幅/高さ) の比が
// aspectRatioMismatchThreshold を超えるとき警告文を返す。超えない場合は空文字。
func aspectRatioMismatchWarning(imgA, imgB image.Image) string {
	bA, bB := imgA.Bounds(), imgB.Bounds()
	wA, hA := bA.Dx(), bA.Dy()
	wB, hB := bB.Dx(), bB.Dy()
	aspectA := float64(wA) / float64(hA)
	aspectB := float64(wB) / float64(hB)
	minAspect, maxAspect := aspectA, aspectB
	if minAspect > maxAspect {
		minAspect, maxAspect = maxAspect, minAspect
	}
	if minAspect == 0 || maxAspect/minAspect <= aspectRatioMismatchThreshold {
		return ""
	}
	return fmt.Sprintf("aspect ratio mismatch: image A is %dx%d (aspect %.2f), image B is %dx%d (aspect %.2f); perceptual comparison stretches both to %dx%d", wA, hA, aspectA, wB, hB, aspectB, AHashGridSize, AHashGridSize)
}

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
	gray := make([]byte, AHashBlocks)

	for y := 0; y < AHashGridSize; y++ {
		for x := 0; x < AHashGridSize; x++ {
			startX := bounds.Min.X + (x*w)/AHashGridSize
			endX := bounds.Min.X + ((x+1)*w)/AHashGridSize
			startY := bounds.Min.Y + (y*h)/AHashGridSize
			endY := bounds.Min.Y + ((y+1)*h)/AHashGridSize

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
			gray[y*AHashGridSize+x] = byte(yVal)
		}
	}
	return gray
}

// EnsureSameSize verifies that both images have identical dimensions and returns
// them unchanged. Size differences are reported as an error instead of being
// silently padded, so that strict pixel comparison never counts padding as matches.
// エラーメッセージには対処ヒント (同一ビューポートサイズ・DPR で両スクショを撮り
// 直す、またはサイズ違いが意図的 (例: デバイスピクセル比の差) な場合は 16x16 に
// 縮小してマクロレイアウトを比較する perceptual モードを使う) を含め、エージェン
// トが自己解決できるようにする (Retina 環境の DPR 差やフルページ撮影によるサイズ
// 違いは頻出の失敗のため) (Issue #155)。
func EnsureSameSize(imgA, imgB image.Image) (image.Image, image.Image, error) {
	boundsA := imgA.Bounds()
	boundsB := imgB.Bounds()
	wA, hA := boundsA.Dx(), boundsA.Dy()
	wB, hB := boundsB.Dx(), boundsB.Dy()

	if wA != wB || hA != hB {
		return nil, nil, fmt.Errorf("image size mismatch: image A is %dx%d, image B is %dx%d; strict comparison requires identical sizes; capture both screenshots at the same viewport size and device pixel ratio, or if the images intentionally differ in size (e.g. device pixel ratio) use the perceptual mode, which compares macro layout after downscaling both to %dx%d", wA, hA, wB, hB, AHashGridSize, AHashGridSize)
	}
	return imgA, imgB, nil
}
