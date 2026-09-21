package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"design-compare/comparator"

	"github.com/mark3labs/mcp-go/mcp"
)

// メモリ上で指定サイズのベタ塗り画像を生成するヘルパー
func generateSolidImage(w, h int, clr color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{clr}, image.Point{}, draw.Src)
	return img
}

// 画像の半分を指定色で塗りつぶす（明暗パターンを作る）ヘルパー
func generateSplitImage(w, h int, leftColor, rightColor color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// 左半分
	draw.Draw(img, image.Rect(0, 0, w/2, h), &image.Uniform{leftColor}, image.Point{}, draw.Src)
	// 右半分
	draw.Draw(img, image.Rect(w/2, 0, w, h), &image.Uniform{rightColor}, image.Point{}, draw.Src)
	return img
}

// 画像を一時保存してパスを返すヘルパー
func saveTempImage(t *testing.T, dir, filename string, img image.Image) string {
	path := filepath.Join(dir, filename)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("failed to create temp image file: %v", err)
	}
	defer f.Close()

	if err := png.Encode(f, img); err != nil {
		t.Fatalf("failed to encode image: %v", err)
	}
	return path
}

// 画像をPNG base64文字列に変換するヘルパー
func encodePNGBase64(t *testing.T, img image.Image) string {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("failed to encode image: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// MIME スタイルに 76 文字ごとに改行を入れ、行頭スペースも混ぜる。
func wrapBase64MIME(s string) string {
	const lineLen = 76
	var b strings.Builder
	for i := 0; i < len(s); i += lineLen {
		end := i + lineLen
		if end > len(s) {
			end = len(s)
		}
		if i > 0 {
			b.WriteString("\n ")
		}
		b.WriteString(s[i:end])
	}
	return b.String()
}

// perceptual モードの応答に含まれる差分画像が base64 data URI であることを検証する
func assertDiffDataURI(t *testing.T, result map[string]interface{}) {
	t.Helper()
	diffImage, ok := result["diff_image"].(string)
	if !ok || !strings.HasPrefix(diffImage, "data:image/png;base64,") {
		t.Errorf("Expected diff_image as PNG data URI, got %v", result["diff_image"])
	}
}

// diff_image_content=true のとき content が Text + Image(png) になり、
// image の payload が JSON の diff_image data URI と一致することを検証する。
func assertDiffImageContent(t *testing.T, res *mcp.CallToolResult, result map[string]interface{}) {
	t.Helper()
	if len(res.Content) != 2 {
		t.Fatalf("Expected 2 content blocks (text + image), got %d", len(res.Content))
	}
	if _, ok := res.Content[0].(mcp.TextContent); !ok {
		t.Fatalf("Expected TextContent as content[0], got %T", res.Content[0])
	}
	img, ok := res.Content[1].(mcp.ImageContent)
	if !ok {
		t.Fatalf("Expected ImageContent as content[1], got %T", res.Content[1])
	}
	if img.MIMEType != "image/png" {
		t.Errorf("Expected MIMEType image/png, got %q", img.MIMEType)
	}
	diffImage, _ := result["diff_image"].(string)
	payload, ok := pngPayloadFromDiffImage(diffImage)
	if !ok {
		t.Fatalf("Expected PNG data URI in JSON diff_image, got %v", result["diff_image"])
	}
	if img.Data != payload {
		t.Errorf("Image content data does not match JSON diff_image payload")
	}
}

// pathA (左右分割) vs pathD (上下分割) の perceptual 不一致セル: 右上・左下の 128 セル。
func assertPerceptualPathDDiffCells(t *testing.T, result map[string]interface{}) {
	t.Helper()
	raw, ok := result["diff_cells"].([]interface{})
	if !ok {
		t.Fatalf("Expected diff_cells array, got %v", result["diff_cells"])
	}
	if len(raw) != 128 {
		t.Fatalf("Expected 128 diff_cells, got %d", len(raw))
	}
	idx := 0
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			if (x < 8) == (y < 8) {
				continue
			}
			cell, ok := raw[idx].(map[string]interface{})
			if !ok {
				t.Fatalf("diff_cells[%d] is %T, want object", idx, raw[idx])
			}
			if cell["grid_x"] != float64(x) || cell["grid_y"] != float64(y) {
				t.Errorf("diff_cells[%d]={grid_x:%v,grid_y:%v}, want {%d,%d}", idx, cell["grid_x"], cell["grid_y"], x, y)
				return
			}
			idx++
		}
	}
}

// /tmp 内の差分PNG一時ファイル（perceptual-diff-*.png）の数を数える
func countDiffTempFiles() (int, error) {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "perceptual-diff-*.png"))
	if err != nil {
		return 0, err
	}
	return len(matches), nil
}

func TestVRTUnifiedCompare(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "vrt-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// A: 200x200 左白・右黒
	imgA := generateSplitImage(200, 200, color.White, color.Black)
	pathA := saveTempImage(t, tmpDir, "imageA.png", imgA)

	// C: 200x200 左薄グレー・右濃グレー (微細な色/輝度差のみ)
	imgC := generateSplitImage(200, 200, color.RGBA{220, 220, 220, 255}, color.RGBA{30, 30, 30, 255})
	pathC := saveTempImage(t, tmpDir, "imageC.png", imgC)

	// D: 200x200 上白・下黒 (配置/レイアウト構造が異なる)
	imgD := image.NewRGBA(image.Rect(0, 0, 200, 200))
	draw.Draw(imgD, image.Rect(0, 0, 200, 100), &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(imgD, image.Rect(0, 100, 200, 200), &image.Uniform{color.Black}, image.Point{}, draw.Src)
	pathD := saveTempImage(t, tmpDir, "imageD.png", imgD)

	// E: 200x200 白地に左上100x100の黒矩形 (ignore_region テスト用の既知差分領域)
	imgE := image.NewRGBA(image.Rect(0, 0, 200, 200))
	draw.Draw(imgE, imgE.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(imgE, image.Rect(0, 0, 100, 100), &image.Uniform{color.Black}, image.Point{}, draw.Src)
	pathE := saveTempImage(t, tmpDir, "imageE.png", imgE)

	// F: 200x200 全面白 (Eとの差分は左上100x100のみ)
	imgF := generateSolidImage(200, 200, color.White)
	pathF := saveTempImage(t, tmpDir, "imageF.png", imgF)

	// =================================================================
	// 1. layout_tree モード (構造ツリー比較) のテスト
	// =================================================================
	t.Run("LayoutTree_Compare", func(t *testing.T) {
		figmaLayout := `[
			{"id": "1", "name": "header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"id": "2", "name": "logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "1"},
			{"id": "3", "name": "nav", "x": 600, "y": 10, "w": 380, "h": 80, "parent": "1"}
		]`

		// Web側: セレクタ名や親ノードの指定方法が少し違うが、相対位置はほぼ同じ
		webLayoutCorrect := `[
			{"selector": "#header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"selector": ".logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "#header"},
			{"selector": ".nav", "x": 600, "y": 10, "w": 380, "h": 80, "parent": "#header"}
		]`

		// Web側: navの位置が著しく左にズレているケース (不一致になるはず)
		webLayoutIncorrect := `[
			{"selector": "#header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"selector": ".logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "#header"},
			{"selector": ".nav", "x": 200, "y": 10, "w": 380, "h": 80, "parent": "#header"}
		]`

		// A: 一致するはずのケース
		reqMatch := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayoutCorrect,
					"threshold":    0.15,
				},
			},
		}
		resMatch, err := compareDesignHandler(context.Background(), reqMatch)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultMatch map[string]interface{}
		json.Unmarshal([]byte(resMatch.Content[0].(mcp.TextContent).Text), &resultMatch)
		if resultMatch["status"] != "success" || resultMatch["match_rate"] != "100.00%" {
			t.Errorf("Expected LayoutTree success and 100%% match, got status=%v, rate=%v", resultMatch["status"], resultMatch["match_rate"])
		}
		// 構造化されたノード数フィールドの検証
		if got := resultMatch["matched_nodes"]; got != float64(3) {
			t.Errorf("Expected matched_nodes=3, got %v", got)
		}
		if got := resultMatch["total_nodes"]; got != float64(3) {
			t.Errorf("Expected total_nodes=3, got %v", got)
		}

		// 数値一致率と実効パラメータの出力検証
		if got := resultMatch["match_rate_value"]; got != float64(100) {
			t.Errorf("Expected match_rate_value=100, got %v", got)
		}
		if got := resultMatch["effective_threshold"]; got != 0.15 {
			t.Errorf("Expected effective_threshold=0.15, got %v", got)
		}
		if got := resultMatch["pass_rate"]; got != float64(98) {
			t.Errorf("Expected pass_rate=98 (default), got %v", got)
		}
		if got := resultMatch["ignored_count"]; got != float64(0) {
			t.Errorf("Expected ignored_count=0, got %v", got)
		}
		if _, ok := resultMatch["zero_geometry_warning"]; ok {
			t.Errorf("Expected no zero_geometry_warning for valid w/h keys, got %v", resultMatch["zero_geometry_warning"])
		}
		if _, ok := resultMatch["unresolved_parent_refs"]; ok {
			t.Errorf("Expected no unresolved_parent_refs for valid parent ids, got %v", resultMatch["unresolved_parent_refs"])
		}
		if _, ok := resultMatch["mismatched_nodes"]; ok {
			t.Errorf("Expected no mismatched_nodes when all pairs match, got %v", resultMatch["mismatched_nodes"])
		}
		// 一致ペアが details に出力されることの検証
		detailsMatch, ok := resultMatch["details"].([]interface{})
		if !ok {
			t.Fatalf("Expected details array in result, got %v", resultMatch["details"])
		}
		foundPair := false
		for _, d := range detailsMatch {
			if s, ok := d.(string); ok && strings.Contains(s, "Matched: 'nav' ↔ '.nav'") {
				foundPair = true
				break
			}
		}
		if !foundPair {
			t.Errorf("Expected details to contain matched pair \"Matched: 'nav' ↔ '.nav'\", got %v", detailsMatch)
		}

		// B: 不一致になるはずのケース
		reqMismatch := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayoutIncorrect,
					"threshold":    0.15,
				},
			},
		}
		resMismatch, err := compareDesignHandler(context.Background(), reqMismatch)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultMismatch map[string]interface{}
		json.Unmarshal([]byte(resMismatch.Content[0].(mcp.TextContent).Text), &resultMismatch)
		if resultMismatch["status"] != "mismatch" {
			t.Errorf("Expected LayoutTree mismatch, got status=%v", resultMismatch["status"])
		}
		// nav が不一致で、header と logo の2ノードのみ一致
		if got := resultMismatch["matched_nodes"]; got != float64(2) {
			t.Errorf("Expected matched_nodes=2, got %v", got)
		}
		if got := resultMismatch["total_nodes"]; got != float64(3) {
			t.Errorf("Expected total_nodes=3, got %v", got)
		}
		mismatched, ok := resultMismatch["mismatched_nodes"].([]interface{})
		if !ok || len(mismatched) != 1 {
			t.Fatalf("Expected 1 mismatched_nodes entry, got %v", resultMismatch["mismatched_nodes"])
		}
		mn, ok := mismatched[0].(map[string]interface{})
		if !ok {
			t.Fatalf("Expected mismatched_nodes[0] object, got %v", mismatched[0])
		}
		if mn["figma_name"] != "nav" || mn["web_selector"] != ".nav" {
			t.Errorf("Expected figma_name=nav web_selector=.nav, got %v", mn)
		}
		dx, _ := mn["dx"].(float64)
		dy, _ := mn["dy"].(float64)
		dw, _ := mn["dw"].(float64)
		dh, _ := mn["dh"].(float64)
		diff, _ := mn["diff"].(float64)
		detailLine := fmt.Sprintf("geometric diff %.2f exceeds tolerance %.2f (dx: %.2f, dy: %.2f, dw: %.2f, dh: %.2f)",
			diff, 0.15, dx, dy, dw, dh)
		detailsMismatch, _ := resultMismatch["details"].([]interface{})
		foundGeom := false
		for _, d := range detailsMismatch {
			if s, ok := d.(string); ok && strings.Contains(s, detailLine) {
				foundGeom = true
				break
			}
		}
		if !foundGeom {
			t.Errorf("Expected details to contain %q (same numbers as mismatched_nodes), got %v", detailLine, detailsMismatch)
		}

		// C: 除外項目を指定して一致させるケース (Figma node名 "nav" または Web selector ".nav" を除外)
		reqIgnore := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayoutIncorrect,
					"threshold":    0.15,
					"ignore_nodes": "nav", // Figma node name "nav"
				},
			},
		}
		resIgnore, err := compareDesignHandler(context.Background(), reqIgnore)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultIgnore map[string]interface{}
		json.Unmarshal([]byte(resIgnore.Content[0].(mcp.TextContent).Text), &resultIgnore)
		if resultIgnore["status"] != "success" || resultIgnore["match_rate"] != "100.00%" {
			t.Errorf("Expected LayoutTree success and 100%% match after ignoring 'nav', got status=%v, rate=%v", resultIgnore["status"], resultIgnore["match_rate"])
		}
		// Figma "nav" と Web ".nav" の2ノードが除外されたことを報告する
		if got := resultIgnore["ignored_count"]; got != float64(2) {
			t.Errorf("Expected ignored_count=2 after ignoring 'nav', got %v", got)
		}
		if _, ok := resultIgnore["unmatched_ignores"]; ok {
			t.Errorf("Expected no unmatched_ignores for valid entry 'nav', got %v", resultIgnore["unmatched_ignores"])
		}

		// C2: Web selector ".nav" を除外して一致させるケース
		reqIgnoreWeb := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayoutIncorrect,
					"threshold":    0.15,
					"ignore_nodes": ".nav", // Web selector ".nav"
				},
			},
		}
		resIgnoreWeb, err := compareDesignHandler(context.Background(), reqIgnoreWeb)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultIgnoreWeb map[string]interface{}
		json.Unmarshal([]byte(resIgnoreWeb.Content[0].(mcp.TextContent).Text), &resultIgnoreWeb)
		if resultIgnoreWeb["status"] != "success" || resultIgnoreWeb["match_rate"] != "100.00%" {
			t.Errorf("Expected LayoutTree success and 100%% match after ignoring '.nav', got status=%v, rate=%v", resultIgnoreWeb["status"], resultIgnoreWeb["match_rate"])
		}
		if got := resultIgnoreWeb["ignored_count"]; got != float64(2) {
			t.Errorf("Expected ignored_count=2 after ignoring '.nav', got %v", got)
		}

		// C3: 有効なエントリと無効なエントリ（スペルミス）を混在させたケース。
		// 有効分は除外され、無効分は unmatched_ignores として報告される。
		reqIgnoreMixed := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayoutIncorrect,
					"threshold":    0.15,
					"ignore_nodes": "nav, .does_not_exist",
				},
			},
		}
		resIgnoreMixed, err := compareDesignHandler(context.Background(), reqIgnoreMixed)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultIgnoreMixed map[string]interface{}
		json.Unmarshal([]byte(resIgnoreMixed.Content[0].(mcp.TextContent).Text), &resultIgnoreMixed)
		if resultIgnoreMixed["status"] != "success" {
			t.Errorf("Expected success with mixed ignore entries, got status=%v, rate=%v", resultIgnoreMixed["status"], resultIgnoreMixed["match_rate"])
		}
		if got := resultIgnoreMixed["ignored_count"]; got != float64(2) {
			t.Errorf("Expected ignored_count=2 with mixed ignore entries, got %v", got)
		}
		unmatched, ok := resultIgnoreMixed["unmatched_ignores"].([]interface{})
		if !ok || len(unmatched) != 1 || unmatched[0] != ".does_not_exist" {
			t.Errorf("Expected unmatched_ignores=[.does_not_exist], got %v", resultIgnoreMixed["unmatched_ignores"])
		}

		// C4: どのノードにも一致しない ignore エントリのみ指定したケース。
		// 除外は1件も発生せず、エントリが unmatched_ignores で報告される。
		reqIgnoreNothing := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayoutIncorrect,
					"threshold":    0.15,
					"ignore_nodes": "nope",
				},
			},
		}
		resIgnoreNothing, err := compareDesignHandler(context.Background(), reqIgnoreNothing)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultIgnoreNothing map[string]interface{}
		json.Unmarshal([]byte(resIgnoreNothing.Content[0].(mcp.TextContent).Text), &resultIgnoreNothing)
		if resultIgnoreNothing["status"] != "mismatch" {
			t.Errorf("Expected mismatch when ignore entry matches nothing, got status=%v, rate=%v", resultIgnoreNothing["status"], resultIgnoreNothing["match_rate"])
		}
		if got := resultIgnoreNothing["ignored_count"]; got != float64(0) {
			t.Errorf("Expected ignored_count=0 when ignore entry matches nothing, got %v", got)
		}
		unmatchedNothing, ok := resultIgnoreNothing["unmatched_ignores"].([]interface{})
		if !ok || len(unmatchedNothing) != 1 || unmatchedNothing[0] != "nope" {
			t.Errorf("Expected unmatched_ignores=[nope], got %v", resultIgnoreNothing["unmatched_ignores"])
		}

		// C5: 末尾 '*' のプレフィックス一致エントリで除外するケース。
		// '.na*' は Web セレクタ ".nav"（raw prefix）と Figma ノード名 "nav"（clean prefix）の
		// 両方に一致し、命名規則に従うグループを列挙なしで除外できる。
		reqIgnorePrefix := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayoutIncorrect,
					"threshold":    0.15,
					"ignore_nodes": ".na*", // prefix match (ends with '*')
				},
			},
		}
		resIgnorePrefix, err := compareDesignHandler(context.Background(), reqIgnorePrefix)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultIgnorePrefix map[string]interface{}
		json.Unmarshal([]byte(resIgnorePrefix.Content[0].(mcp.TextContent).Text), &resultIgnorePrefix)
		if resultIgnorePrefix["status"] != "success" || resultIgnorePrefix["match_rate"] != "100.00%" {
			t.Errorf("Expected LayoutTree success and 100%% match after ignoring '.na*', got status=%v, rate=%v", resultIgnorePrefix["status"], resultIgnorePrefix["match_rate"])
		}
		// Figma "nav" と Web ".nav" の2ノードがプレフィックス一致で除外されたことを報告する
		if got := resultIgnorePrefix["ignored_count"]; got != float64(2) {
			t.Errorf("Expected ignored_count=2 after ignoring '.na*', got %v", got)
		}
		if _, ok := resultIgnorePrefix["unmatched_ignores"]; ok {
			t.Errorf("Expected no unmatched_ignores for matching prefix '.na*', got %v", resultIgnorePrefix["unmatched_ignores"])
		}
	})

	// D: pass_rate を下げて不一致ケースを成功に切り替えるテスト
	t.Run("LayoutTree_PassRate", func(t *testing.T) {
		figmaLayout := `[
			{"id": "1", "name": "header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"id": "2", "name": "logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "1"},
			{"id": "3", "name": "nav", "x": 600, "y": 10, "w": 380, "h": 80, "parent": "1"}
		]`

		// nav の位置がズレている (一致率 66.67%)
		webLayoutIncorrect := `[
			{"selector": "#header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"selector": ".logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "#header"},
			{"selector": ".nav", "x": 200, "y": 10, "w": 380, "h": 80, "parent": "#header"}
		]`

		// pass_rate を 50% に下げれば成功になるはず
		reqPass := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayoutIncorrect,
					"threshold":    0.15,
					"pass_rate":    50.0,
				},
			},
		}
		resPass, err := compareDesignHandler(context.Background(), reqPass)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultPass map[string]interface{}
		json.Unmarshal([]byte(resPass.Content[0].(mcp.TextContent).Text), &resultPass)
		if resultPass["status"] != "success" {
			t.Errorf("Expected success with pass_rate=50, got status=%v, rate=%v", resultPass["status"], resultPass["match_rate"])
		}

		// pass_rate を 70% に上げれば不一致になるはず
		reqFail := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayoutIncorrect,
					"threshold":    0.15,
					"pass_rate":    70.0,
				},
			},
		}
		resFail, err := compareDesignHandler(context.Background(), reqFail)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultFail map[string]interface{}
		json.Unmarshal([]byte(resFail.Content[0].(mcp.TextContent).Text), &resultFail)
		if resultFail["status"] != "mismatch" {
			t.Errorf("Expected mismatch with pass_rate=70, got status=%v, rate=%v", resultFail["status"], resultFail["match_rate"])
		}

		// 生値は 66.666...% で 66.67 未満だが、表示は 66.67%。判定も表示桁に合わせる (Issue #233)
		reqRounded := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayoutIncorrect,
					"threshold":    0.15,
					"pass_rate":    66.67,
				},
			},
		}
		resRounded, err := compareDesignHandler(context.Background(), reqRounded)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultRounded map[string]interface{}
		json.Unmarshal([]byte(resRounded.Content[0].(mcp.TextContent).Text), &resultRounded)
		if resultRounded["match_rate"] != "66.67%" {
			t.Errorf("Expected displayed match_rate=66.67%%, got %v", resultRounded["match_rate"])
		}
		if resultRounded["status"] != "success" {
			t.Errorf("Expected success when displayed match_rate equals pass_rate=66.67, got status=%v value=%v", resultRounded["status"], resultRounded["match_rate_value"])
		}
		if got, want := resultRounded["match_rate_value"], float64(2)/3*100; got != want {
			t.Errorf("Expected raw match_rate_value=%v, got %v", want, got)
		}
	})

	// =================================================================
	// 2.4.1. layout_tree モード: width/height キーによる零幾何の警告
	// =================================================================
	t.Run("LayoutTree_ZeroGeometryWarning", func(t *testing.T) {
		// w/h の代わりに width/height を使うと Unmarshal は成功するが幾何は全て 0。
		// status は従来どおり success のまま、zero_geometry_warning で誤用を知らせる。
		figmaWrongKeys := `[
			{"id": "1", "name": "header", "x": 0, "y": 0, "width": 1000, "height": 100},
			{"id": "2", "name": "logo", "x": 10, "y": 10, "width": 100, "height": 80, "parent": "1"}
		]`
		webWrongKeys := `[
			{"selector": "#header", "x": 0, "y": 0, "width": 1000, "height": 100},
			{"selector": ".logo", "x": 10, "y": 10, "width": 100, "height": 80, "parent": "#header"}
		]`
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaWrongKeys,
					"web_layout":   webWrongKeys,
					"threshold":    0.15,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "success" {
			t.Errorf("Expected status to remain success, got %v", result["status"])
		}
		if result["match_rate"] != "100.00%" {
			t.Errorf("Expected match_rate 100.00%% (all-zero geometry still matches), got %v", result["match_rate"])
		}
		got, ok := result["zero_geometry_warning"].(string)
		if !ok || !strings.Contains(got, `{"id","name","x","y","w","h","parent"}`) {
			t.Errorf("Expected zero_geometry_warning about layout JSON keys, got %v", result["zero_geometry_warning"])
		}
	})

	// =================================================================
	// 2.4.2. layout_tree モード: 解決できない parent 参照の通知
	// =================================================================
	t.Run("LayoutTree_UnresolvedParentRefs", func(t *testing.T) {
		figmaLayout := `[
			{"id": "1", "name": "header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"id": "2", "name": "logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "999"}
		]`
		webLayout := `[
			{"selector": "#header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"selector": ".logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": ".foo"}
		]`
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayout,
					"threshold":    0.15,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "success" {
			t.Errorf("Expected status to remain success, got %v", result["status"])
		}
		if result["match_rate"] != "100.00%" {
			t.Errorf("Expected match_rate 100.00%% with absolute-coordinate fallback, got %v", result["match_rate"])
		}
		refs, ok := result["unresolved_parent_refs"].([]interface{})
		if !ok || len(refs) != 2 || refs[0] != "Figma: '999'" || refs[1] != "Web: '.foo'" {
			t.Errorf("Expected unresolved_parent_refs=[Figma: '999' Web: '.foo'], got %v", result["unresolved_parent_refs"])
		}
	})

	// =================================================================
	// 2.5. layout_tree モード: 重複マッチ防止のテスト
	// =================================================================
	t.Run("LayoutTree_NoDuplicateMatch", func(t *testing.T) {
		// Figma側に3ノードあるが、Web側には実質2ノードしかない（childA と childB が同じ位置を指す）。
		// 従来は複数のFigmaノードが同一Webノードにマッチし一致率が水増しされていた。
		// 修正後は1対1対応が保証され、一致率が正しく下がるはず。
		figmaLayout := `[
			{"id": "1", "name": "container", "x": 0, "y": 0, "w": 500, "h": 500},
			{"id": "2", "name": "childA", "x": 10, "y": 10, "w": 480, "h": 480, "parent": "1"},
			{"id": "3", "name": "childB", "x": 10, "y": 10, "w": 480, "h": 480, "parent": "1"}
		]`

		// Web側: container と childA の2ノードのみ（childB に対応するノードがない）
		webLayoutDuplicate := `[
			{"selector": "#container", "x": 0, "y": 0, "w": 500, "h": 500},
			{"selector": ".childA", "x": 10, "y": 10, "w": 480, "h": 480, "parent": "#container"}
		]`

		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayoutDuplicate,
					"threshold":    0.15,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)

		// Figma 3ノード中、Webノードは2つしかないため、1ノードはマッチできない。
		// 重複マッチが防止されていれば一致率は 66.67% 以下になるはず。
		if result["match_rate"] == "100.00%" {
			t.Errorf("Expected match rate to be less than 100%% (duplicate match prevention), got %v", result["match_rate"])
		}
		if result["status"] != "mismatch" {
			t.Errorf("Expected mismatch status due to unmatched node, got status=%v, rate=%v", result["status"], result["match_rate"])
		}
	})

	// =================================================================
	// 2.6. layout_tree モード: Web側未マッチノードの報告テスト
	// =================================================================
	t.Run("LayoutTree_UnmatchedWebNode", func(t *testing.T) {
		// Figma側に2ノード、Web側に3ノード（余分な .banner がある）。
		// Figmaノードはすべてマッチするが、.banner はどのFigmaノードにも
		// 対応付けられないため、details に報告されるはず。
		figmaLayout := `[
			{"id": "1", "name": "header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"id": "2", "name": "logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "1"}
		]`

		webLayoutExtra := `[
			{"selector": "#header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"selector": ".logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "#header"},
			{"selector": ".banner", "x": 400, "y": 300, "w": 200, "h": 100, "parent": "#header"}
		]`

		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayoutExtra,
					"threshold":    0.15,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)

		// Figma側のノードはすべてマッチするため一致率は100%
		if result["status"] != "success" || result["match_rate"] != "100.00%" {
			t.Errorf("Expected success and 100%% match (all Figma nodes matched), got status=%v, rate=%v", result["status"], result["match_rate"])
		}

		// 余分な .banner が未マッチWebノードとして details に報告されるはず
		details, ok := result["details"].([]interface{})
		if !ok {
			t.Fatalf("Expected details array in result, got %v", result["details"])
		}
		found := false
		for _, d := range details {
			if s, ok := d.(string); ok && strings.Contains(s, ".banner") && strings.Contains(s, "did not match any Figma node") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected details to report unmatched Web node '.banner', got %v", details)
		}

		// 余分な Web ノードは構造化フィールド (extra_web_count / extra_web_nodes) でも
		// 報告されるはず（クライアントは文字列パースなしで対象を特定できる）
		if got := result["extra_web_count"]; got != float64(1) {
			t.Errorf("Expected extra_web_count=1, got %v", got)
		}
		extraNodes, hasExtra := result["extra_web_nodes"].([]interface{})
		if !hasExtra || len(extraNodes) != 1 || extraNodes[0] != ".banner" {
			t.Errorf("Expected extra_web_nodes=[\".banner\"], got %v", result["extra_web_nodes"])
		}
	})

	// =================================================================
	// 2.7. layout_tree モード: ignore_nodes で全件除外された場合の skipped
	// =================================================================
	t.Run("LayoutTree_AllIgnored_Skipped", func(t *testing.T) {
		figmaLayout := `[
			{"id": "1", "name": "header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"id": "2", "name": "logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "1"}
		]`
		webLayout := `[
			{"selector": "#header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"selector": ".logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "#header"}
		]`

		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayout,
					"ignore_nodes": "header, #header, logo, .logo",
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "skipped" {
			t.Errorf("Expected skipped status when all nodes are excluded by ignore_nodes, got status=%v", result["status"])
		}
		if got := result["ignored_count"]; got != float64(4) {
			t.Errorf("Expected ignored_count=4, got %v", got)
		}
		details, ok := result["details"].([]interface{})
		if !ok || len(details) == 0 {
			t.Fatalf("Expected details array, got %v", result["details"])
		}
		if s, ok := details[0].(string); !ok || !strings.Contains(s, "ignore_nodes") {
			t.Errorf("Expected details to mention ignore_nodes exclusion, got %v", details)
		}
	})

	// =================================================================
	// 2.8. layout_tree モード: 空入力時にどちら側が空かを明示
	// =================================================================
	t.Run("LayoutTree_EmptySideMessages", func(t *testing.T) {
		figmaOnly := `[{"id":"1","name":"a","x":0,"y":0,"w":100,"h":100}]`
		webOnly := `[{"selector":".a","x":0,"y":0,"w":100,"h":100}]`

		cases := []struct {
			figma   string
			web     string
			wantMsg string
		}{
			{"[]", webOnly, "Figma layout node data is empty"},
			{figmaOnly, "[]", "Web layout node data is empty"},
			{"[]", "[]", "Both Figma and Web layout node data are empty"},
		}
		for _, c := range cases {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":         "layout_tree",
						"figma_layout": c.figma,
						"web_layout":   c.web,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			var result map[string]interface{}
			json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
			if result["status"] != "mismatch" {
				t.Errorf("Expected mismatch for empty input (%q), got status=%v", c.wantMsg, result["status"])
			}
			details, ok := result["details"].([]interface{})
			if !ok || len(details) != 1 {
				t.Fatalf("Expected exactly 1 detail, got %v", result["details"])
			}
			if s, ok := details[0].(string); !ok || s != c.wantMsg {
				t.Errorf("Expected detail %q, got %v", c.wantMsg, details[0])
			}
		}
	})

	// =================================================================
	// 2.8b. layout_tree: Web 空 + Figma 側だけ ignore しても空側を誤報しない
	// =================================================================
	t.Run("LayoutTree_EmptyWebWithIgnoredFigma", func(t *testing.T) {
		figmaLayout := `[
			{"id": "1", "name": "header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"id": "2", "name": "logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "1"}
		]`
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   "[]",
					"ignore_nodes": "logo",
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "mismatch" {
			t.Errorf("Expected mismatch when Web layout is empty, got status=%v", result["status"])
		}
		if got := result["ignored_count"]; got != float64(1) {
			t.Errorf("Expected ignored_count=1, got %v", got)
		}
		details, ok := result["details"].([]interface{})
		if !ok || len(details) == 0 {
			t.Fatalf("Expected details array, got %v", result["details"])
		}
		s, _ := details[0].(string)
		if !strings.Contains(s, "Web layout node data is empty") {
			t.Errorf("Expected details to point at empty Web input, got %v", details)
		}
		if strings.Contains(s, "All Web nodes were excluded") {
			t.Errorf("Did not expect all-excluded skipped message for originally empty Web, got %v", details)
		}
	})

	// =================================================================
	// 2.9. layout_tree モード: サイズ0の親を持つ子ノードの比較
	// =================================================================
	t.Run("LayoutTree_ZeroSizeParent", func(t *testing.T) {
		figmaLayout := `[
			{"id": "1", "name": "collapsed", "x": 0, "y": 0, "w": 0, "h": 0},
			{"id": "2", "name": "child", "x": 10, "y": 10, "w": 50, "h": 50, "parent": "1"}
		]`

		// 子の絶対座標が大きく異なるケース: サイズ0の親を持つ子は従来 (0,0,0,0) 同士で
		// 常に一致扱いになったが、絶対座標フォールバックにより不一致になる
		webFar := `[
			{"selector": "#collapsed", "x": 0, "y": 0, "w": 0, "h": 0},
			{"selector": ".child", "x": 500, "y": 400, "w": 50, "h": 50, "parent": "#collapsed"}
		]`
		reqFar := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webFar,
					"threshold":    0.15,
				},
			},
		}
		resFar, err := compareDesignHandler(context.Background(), reqFar)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultFar map[string]interface{}
		json.Unmarshal([]byte(resFar.Content[0].(mcp.TextContent).Text), &resultFar)
		if resultFar["status"] != "mismatch" {
			t.Errorf("Expected mismatch when child of zero-size parent is far away, got status=%v, rate=%v", resultFar["status"], resultFar["match_rate"])
		}

		// 子の絶対座標が同じケース: 一致扱いになる
		webSame := `[
			{"selector": "#collapsed", "x": 0, "y": 0, "w": 0, "h": 0},
			{"selector": ".child", "x": 10, "y": 10, "w": 50, "h": 50, "parent": "#collapsed"}
		]`
		reqSame := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webSame,
					"threshold":    0.15,
				},
			},
		}
		resSame, err := compareDesignHandler(context.Background(), reqSame)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultSame map[string]interface{}
		json.Unmarshal([]byte(resSame.Content[0].(mcp.TextContent).Text), &resultSame)
		if resultSame["status"] != "success" || resultSame["match_rate"] != "100.00%" {
			t.Errorf("Expected success and 100%% match for same absolute coords, got status=%v, rate=%v", resultSame["status"], resultSame["match_rate"])
		}
	})

	// =================================================================
	// 2.9.1. layout_tree モード: 片側だけ絶対座標モードの場合の対称な比較
	// =================================================================
	// 親なし（またはサイズ0の親）のノードは絶対座標モード、通常の親を持つノードは
	// 相対比率 (0-1) モードで比較される。従来はペアの片側だけ絶対座標モードの場合に
	// 絶対px値と比率を直接比較して常に不一致になっていたが、両側を絶対座標にそろえる
	// ことで対称化されることを検証する。
	t.Run("LayoutTree_MixedModeSymmetricCompare", func(t *testing.T) {
		// ケース1: Figma側の親参照がリスト内に存在しない（親なし扱い = 絶対座標モード）、
		// Web側は通常の親（相対比率モード）。絶対座標が同一なら一致する。
		// 修正前は (100,100,50,50) vs 比率 (0.2,0.2,0.1,0.1) の非対称比較で必ず不一致だった。
		figmaOrphanParent := `[
			{"id": "2", "name": "child", "x": 100, "y": 100, "w": 50, "h": 50, "parent": "1"}
		]`
		webNormalParent := `[
			{"selector": "#wrapper", "x": 0, "y": 0, "w": 500, "h": 500},
			{"selector": ".child", "x": 100, "y": 100, "w": 50, "h": 50, "parent": "#wrapper"}
		]`
		req1 := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaOrphanParent,
					"web_layout":   webNormalParent,
					"threshold":    0.15,
				},
			},
		}
		res1, err := compareDesignHandler(context.Background(), req1)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result1 map[string]interface{}
		json.Unmarshal([]byte(res1.Content[0].(mcp.TextContent).Text), &result1)
		if result1["status"] != "success" || result1["match_rate"] != "100.00%" {
			t.Errorf("Expected success and 100%% match for symmetric absolute compare, got status=%v, rate=%v", result1["status"], result1["match_rate"])
		}
		if got := result1["matched_nodes"]; got != float64(1) {
			t.Errorf("Expected matched_nodes=1, got %v", got)
		}
		if got := result1["absolute_mode_pairs"]; got != float64(1) {
			t.Errorf("Expected absolute_mode_pairs=1, got %v", got)
		}

		// ケース2: 同じ構成だが絶対座標が実際に異なる場合は不一致のまま
		// （対称化によって誤一致が生まれないことの保証）
		webShifted := `[
			{"selector": "#wrapper", "x": 0, "y": 0, "w": 500, "h": 500},
			{"selector": ".child", "x": 300, "y": 200, "w": 50, "h": 50, "parent": "#wrapper"}
		]`
		req2 := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaOrphanParent,
					"web_layout":   webShifted,
					"threshold":    0.15,
				},
			},
		}
		res2, err := compareDesignHandler(context.Background(), req2)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result2 map[string]interface{}
		json.Unmarshal([]byte(res2.Content[0].(mcp.TextContent).Text), &result2)
		if result2["status"] != "mismatch" {
			t.Errorf("Expected mismatch when absolute coords differ, got status=%v, rate=%v", result2["status"], result2["match_rate"])
		}
		if got := result2["matched_nodes"]; got != float64(0) {
			t.Errorf("Expected matched_nodes=0, got %v", got)
		}

		// ケース3: Figma側だけサイズ0の親（絶対座標モード）、Web側は通常の親。
		// 子の絶対座標が同じなら一致ペアが作られる（0x0のcollapsed自体は不一致のまま）。
		figmaZeroParent := `[
			{"id": "1", "name": "collapsed", "x": 0, "y": 0, "w": 0, "h": 0},
			{"id": "2", "name": "child", "x": 100, "y": 100, "w": 50, "h": 50, "parent": "1"}
		]`
		req3 := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaZeroParent,
					"web_layout":   webNormalParent,
					"threshold":    0.15,
				},
			},
		}
		res3, err := compareDesignHandler(context.Background(), req3)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result3 map[string]interface{}
		json.Unmarshal([]byte(res3.Content[0].(mcp.TextContent).Text), &result3)
		if got := result3["matched_nodes"]; got != float64(1) {
			t.Errorf("Expected matched_nodes=1 (child pair), got %v", got)
		}
		if got := result3["total_nodes"]; got != float64(2) {
			t.Errorf("Expected total_nodes=2, got %v", got)
		}
		if result3["match_rate"] != "50.00%" {
			t.Errorf("Expected match_rate=50.00%% (1 of 2), got %v", result3["match_rate"])
		}
		details3, ok := result3["details"].([]interface{})
		if !ok {
			t.Fatalf("Expected details array in result, got %v", result3["details"])
		}
		foundPair3 := false
		for _, d := range details3 {
			if s, ok := d.(string); ok && strings.Contains(s, "Matched: 'child' ↔ '.child'") {
				foundPair3 = true
				break
			}
		}
		if !foundPair3 {
			t.Errorf("Expected details to contain matched pair \"Matched: 'child' ↔ '.child'\", got %v", details3)
		}

		// ケース4: 逆向き - Figma側は通常の親（相対比率モード）、Web側だけサイズ0の親
		// （絶対座標モード）。子の絶対座標が同じなら一致ペアが作られる。
		figmaNormalParent := `[
			{"id": "1", "name": "frame", "x": 0, "y": 0, "w": 500, "h": 500},
			{"id": "2", "name": "child", "x": 100, "y": 100, "w": 50, "h": 50, "parent": "1"}
		]`
		webZeroParent := `[
			{"selector": "#collapsed", "x": 0, "y": 0, "w": 0, "h": 0},
			{"selector": ".child", "x": 100, "y": 100, "w": 50, "h": 50, "parent": "#collapsed"}
		]`
		req4 := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaNormalParent,
					"web_layout":   webZeroParent,
					"threshold":    0.15,
				},
			},
		}
		res4, err := compareDesignHandler(context.Background(), req4)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result4 map[string]interface{}
		json.Unmarshal([]byte(res4.Content[0].(mcp.TextContent).Text), &result4)
		if got := result4["matched_nodes"]; got != float64(1) {
			t.Errorf("Expected matched_nodes=1 (child pair), got %v", got)
		}
		if result4["match_rate"] != "50.00%" {
			t.Errorf("Expected match_rate=50.00%% (1 of 2), got %v", result4["match_rate"])
		}
		details4, ok := result4["details"].([]interface{})
		if !ok {
			t.Fatalf("Expected details array in result, got %v", result4["details"])
		}
		foundPair4 := false
		for _, d := range details4 {
			if s, ok := d.(string); ok && strings.Contains(s, "Matched: 'child' ↔ '.child'") {
				foundPair4 = true
				break
			}
		}
		if !foundPair4 {
			t.Errorf("Expected details to contain matched pair \"Matched: 'child' ↔ '.child'\", got %v", details4)
		}
	})

	// =================================================================
	// 2.10. layout_tree モード: 余分なWebノードの一致率反映 (count_extra_web)
	// =================================================================
	t.Run("LayoutTree_CountExtraWeb", func(t *testing.T) {
		figmaLayout := `[
			{"id": "1", "name": "header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"id": "2", "name": "logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "1"}
		]`
		webLayoutExtra := `[
			{"selector": "#header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"selector": ".logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "#header"},
			{"selector": ".banner", "x": 400, "y": 300, "w": 200, "h": 100, "parent": "#header"}
		]`

		// count_extra_web=true: 余分な .banner が分母に加算され 2/3=66.67% で mismatch
		reqOn := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":            "layout_tree",
					"figma_layout":    figmaLayout,
					"web_layout":      webLayoutExtra,
					"threshold":       0.15,
					"count_extra_web": true,
				},
			},
		}
		resOn, err := compareDesignHandler(context.Background(), reqOn)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultOn map[string]interface{}
		json.Unmarshal([]byte(resOn.Content[0].(mcp.TextContent).Text), &resultOn)
		if resultOn["status"] != "mismatch" || resultOn["match_rate"] != "66.67%" {
			t.Errorf("Expected mismatch with 66.67%% when count_extra_web=true, got status=%v, rate=%v", resultOn["status"], resultOn["match_rate"])
		}
		if got := resultOn["matched_nodes"]; got != float64(2) {
			t.Errorf("Expected matched_nodes=2, got %v", got)
		}
		if got := resultOn["total_nodes"]; got != float64(3) {
			t.Errorf("Expected total_nodes=3 (extra web node counted), got %v", got)
		}
		if got := resultOn["extra_web_count"]; got != float64(1) {
			t.Errorf("Expected extra_web_count=1, got %v", got)
		}
		if got := resultOn["count_extra_web"]; got != true {
			t.Errorf("Expected count_extra_web=true in response, got %v", got)
		}
		extraOn, hasExtraOn := resultOn["extra_web_nodes"].([]interface{})
		if !hasExtraOn || len(extraOn) != 1 || extraOn[0] != ".banner" {
			t.Errorf("Expected extra_web_nodes=[\".banner\"], got %v", resultOn["extra_web_nodes"])
		}

		// count_extra_web 未指定 (デフォルト false): 一致率には影響しない
		reqOff := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayoutExtra,
					"threshold":    0.15,
				},
			},
		}
		resOff, err := compareDesignHandler(context.Background(), reqOff)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultOff map[string]interface{}
		json.Unmarshal([]byte(resOff.Content[0].(mcp.TextContent).Text), &resultOff)
		if resultOff["status"] != "success" || resultOff["match_rate"] != "100.00%" {
			t.Errorf("Expected success with 100%% when count_extra_web is off, got status=%v, rate=%v", resultOff["status"], resultOff["match_rate"])
		}
		if got := resultOff["total_nodes"]; got != float64(2) {
			t.Errorf("Expected total_nodes=2 when count_extra_web is off, got %v", got)
		}

		// count_extra_web が false でも、余分な Web ノードの構造化レポートは返る
		// （分母への加算のみが本フラグで制御される）
		if got := resultOff["extra_web_count"]; got != float64(1) {
			t.Errorf("Expected extra_web_count=1 even when count_extra_web is off, got %v", got)
		}
		if got := resultOff["count_extra_web"]; got != false {
			t.Errorf("Expected count_extra_web=false in response when unset, got %v", got)
		}
		extraOff, hasExtraOff := resultOff["extra_web_nodes"].([]interface{})
		if !hasExtraOff || len(extraOff) != 1 || extraOff[0] != ".banner" {
			t.Errorf("Expected extra_web_nodes=[\".banner\"] even when count_extra_web is off, got %v", resultOff["extra_web_nodes"])
		}
	})

	// =================================================================
	// 2.10b. layout_tree モード: max_details で details を切り詰める
	// =================================================================
	t.Run("LayoutTree_MaxDetails", func(t *testing.T) {
		figmaLayout := `[
			{"id": "1", "name": "header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"id": "2", "name": "logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "1"},
			{"id": "3", "name": "nav", "x": 600, "y": 10, "w": 380, "h": 80, "parent": "1"}
		]`
		webLayout := `[
			{"selector": "#header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"selector": ".logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "#header"},
			{"selector": ".nav", "x": 600, "y": 10, "w": 380, "h": 80, "parent": "#header"}
		]`

		reqAll := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayout,
					"threshold":    0.15,
				},
			},
		}
		resAll, err := compareDesignHandler(context.Background(), reqAll)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultAll map[string]interface{}
		json.Unmarshal([]byte(resAll.Content[0].(mcp.TextContent).Text), &resultAll)
		detailsAll, ok := resultAll["details"].([]interface{})
		if !ok {
			t.Fatalf("Expected details array, got %v", resultAll["details"])
		}
		if len(detailsAll) != 5 {
			t.Fatalf("Expected 5 details when max_details is omitted (summary + abs-mode note + 3 pairs), got %d: %v", len(detailsAll), detailsAll)
		}

		reqCap := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayout,
					"threshold":    0.15,
					"max_details":  2.0,
				},
			},
		}
		resCap, err := compareDesignHandler(context.Background(), reqCap)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultCap map[string]interface{}
		json.Unmarshal([]byte(resCap.Content[0].(mcp.TextContent).Text), &resultCap)
		detailsCap, ok := resultCap["details"].([]interface{})
		if !ok {
			t.Fatalf("Expected details array, got %v", resultCap["details"])
		}
		if len(detailsCap) != 3 {
			t.Fatalf("Expected 2 kept details + omit line, got %d: %v", len(detailsCap), detailsCap)
		}
		summary, _ := detailsCap[0].(string)
		if !strings.Contains(summary, "Matched 3 out of 3") {
			t.Errorf("Expected summary line first, got %v", detailsCap[0])
		}
		omit, _ := detailsCap[len(detailsCap)-1].(string)
		if omit != "... and 3 more details omitted (max_details=2)" {
			t.Errorf("Expected omit line, got %q", omit)
		}
	})

	// =================================================================
	// 2.11. layout_tree モード: threshold=0 / pass_rate=0 は有効値
	// =================================================================
	t.Run("LayoutTree_ZeroThresholdAndPassRate", func(t *testing.T) {
		figmaLayout := `[
			{"id": "1", "name": "header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"id": "2", "name": "logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "1"},
			{"id": "3", "name": "nav", "x": 600, "y": 10, "w": 380, "h": 80, "parent": "1"}
		]`
		webLayoutCorrect := `[
			{"selector": "#header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"selector": ".logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "#header"},
			{"selector": ".nav", "x": 600, "y": 10, "w": 380, "h": 80, "parent": "#header"}
		]`
		webLayoutIncorrect := `[
			{"selector": "#header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"selector": ".logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "#header"},
			{"selector": ".nav", "x": 200, "y": 10, "w": 380, "h": 80, "parent": "#header"}
		]`

		// threshold=0 (完全一致要求) はデフォルト 0.15 へ上書きされず、正確に一致していれば成功
		reqZeroT := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayoutCorrect,
					"threshold":    0.0,
				},
			},
		}
		resZeroT, err := compareDesignHandler(context.Background(), reqZeroT)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultZeroT map[string]interface{}
		json.Unmarshal([]byte(resZeroT.Content[0].(mcp.TextContent).Text), &resultZeroT)
		if resultZeroT["status"] != "success" || resultZeroT["match_rate"] != "100.00%" {
			t.Errorf("Expected success with threshold=0 for exact match, got status=%v, rate=%v", resultZeroT["status"], resultZeroT["match_rate"])
		}

		// pass_rate=0 はデフォルト 98.0 へ上書きされず、66.67% でも成功扱いになる
		reqZeroP := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayoutIncorrect,
					"pass_rate":    0.0,
				},
			},
		}
		resZeroP, err := compareDesignHandler(context.Background(), reqZeroP)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultZeroP map[string]interface{}
		json.Unmarshal([]byte(resZeroP.Content[0].(mcp.TextContent).Text), &resultZeroP)
		if resultZeroP["status"] != "success" {
			t.Errorf("Expected success with pass_rate=0 despite 66.67%% match, got status=%v", resultZeroP["status"])
		}
	})

	// =================================================================
	// 2.12. layout_tree モード: レイアウトJSONのファイルパス指定
	// =================================================================
	t.Run("LayoutTree_LayoutFilePath", func(t *testing.T) {
		figmaLayout := `[
			{"id": "1", "name": "header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"id": "2", "name": "logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "1"}
		]`
		webLayout := `[
			{"selector": "#header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"selector": ".logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "#header"}
		]`
		figmaPath := filepath.Join(tmpDir, "figma-layout.json")
		if err := os.WriteFile(figmaPath, []byte(figmaLayout), 0o644); err != nil {
			t.Fatalf("failed to write figma layout file: %v", err)
		}
		webPath := filepath.Join(tmpDir, "web-layout.json")
		if err := os.WriteFile(webPath, []byte(webLayout), 0o644); err != nil {
			t.Fatalf("failed to write web layout file: %v", err)
		}

		// パス指定のみ: インライン指定と同等の結果になる
		reqPath := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":              "layout_tree",
					"figma_layout_path": figmaPath,
					"web_layout_path":   webPath,
				},
			},
		}
		resPath, err := compareDesignHandler(context.Background(), reqPath)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultPath map[string]interface{}
		json.Unmarshal([]byte(resPath.Content[0].(mcp.TextContent).Text), &resultPath)
		if resultPath["status"] != "success" || resultPath["match_rate"] != "100.00%" {
			t.Errorf("Expected success and 100%% match via layout file paths, got status=%v, rate=%v", resultPath["status"], resultPath["match_rate"])
		}

		// インラインとパスの同時指定はエラー
		reqBoth := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":              "layout_tree",
					"figma_layout":      figmaLayout,
					"figma_layout_path": figmaPath,
					"web_layout":        webLayout,
				},
			},
		}
		resBoth, err := compareDesignHandler(context.Background(), reqBoth)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if !resBoth.IsError {
			t.Errorf("Expected error when both figma_layout and figma_layout_path are specified, got content=%v", resBoth.Content[0].(mcp.TextContent).Text)
		}

		// figma側をどちらも指定しない場合はエラー
		reqNeither := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":       "layout_tree",
					"web_layout": webLayout,
				},
			},
		}
		resNeither, err := compareDesignHandler(context.Background(), reqNeither)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if !resNeither.IsError {
			t.Errorf("Expected error when neither figma_layout nor figma_layout_path is specified, got content=%v", resNeither.Content[0].(mcp.TextContent).Text)
		}
	})

	// =================================================================
	// 2.12b. layout_tree モード: UTF-8 BOM 付き JSON（インライン / パス）
	// =================================================================
	t.Run("LayoutTree_UTF8BOM", func(t *testing.T) {
		figmaLayout := `[{"id":"1","name":"header","x":0,"y":0,"w":1000,"h":100}]`
		webLayout := `[{"selector":"#header","x":0,"y":0,"w":1000,"h":100}]`
		bomFigma := "\uFEFF" + figmaLayout
		bomWeb := "\uFEFF" + webLayout

		reqInline := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": bomFigma,
					"web_layout":   bomWeb,
				},
			},
		}
		resInline, err := compareDesignHandler(context.Background(), reqInline)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if resInline.IsError {
			t.Fatalf("Expected BOM-prefixed inline JSON to parse, got error: %v", resInline.Content[0].(mcp.TextContent).Text)
		}
		var resultInline map[string]interface{}
		json.Unmarshal([]byte(resInline.Content[0].(mcp.TextContent).Text), &resultInline)
		if resultInline["status"] != "success" || resultInline["match_rate"] != "100.00%" {
			t.Errorf("Expected success and 100%% match with BOM-prefixed inline JSON, got status=%v, rate=%v", resultInline["status"], resultInline["match_rate"])
		}

		figmaPath := filepath.Join(tmpDir, "figma-layout-bom.json")
		if err := os.WriteFile(figmaPath, []byte(bomFigma), 0o644); err != nil {
			t.Fatalf("failed to write figma layout file: %v", err)
		}
		webPath := filepath.Join(tmpDir, "web-layout-bom.json")
		if err := os.WriteFile(webPath, []byte(bomWeb), 0o644); err != nil {
			t.Fatalf("failed to write web layout file: %v", err)
		}

		reqPath := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":              "layout_tree",
					"figma_layout_path": figmaPath,
					"web_layout_path":   webPath,
				},
			},
		}
		resPath, err := compareDesignHandler(context.Background(), reqPath)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if resPath.IsError {
			t.Fatalf("Expected BOM-prefixed layout files to parse, got error: %v", resPath.Content[0].(mcp.TextContent).Text)
		}
		var resultPath map[string]interface{}
		json.Unmarshal([]byte(resPath.Content[0].(mcp.TextContent).Text), &resultPath)
		if resultPath["status"] != "success" || resultPath["match_rate"] != "100.00%" {
			t.Errorf("Expected success and 100%% match via BOM-prefixed layout file paths, got status=%v, rate=%v", resultPath["status"], resultPath["match_rate"])
		}
	})

	// =================================================================
	// 2.12c. layout_tree モード: 配列内の JSON null は入力エラー
	// =================================================================
	t.Run("LayoutTree_NullArrayElement", func(t *testing.T) {
		validFigma := `[{"id":"1","name":"header","x":0,"y":0,"w":1000,"h":100}]`
		validWeb := `[{"selector":"#header","x":0,"y":0,"w":1000,"h":100}]`

		reqFigma := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": `[{"id":"1","name":"header","x":0,"y":0,"w":1000,"h":100},null]`,
					"web_layout":   validWeb,
				},
			},
		}
		resFigma, err := compareDesignHandler(context.Background(), reqFigma)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if !resFigma.IsError {
			t.Fatal("expected IsError for null element in figma_layout")
		}
		gotFigma := resFigma.Content[0].(mcp.TextContent).Text
		if !strings.Contains(gotFigma, "failed to parse Figma layout JSON: element at index 1 is null") {
			t.Errorf("got %q", gotFigma)
		}

		reqWeb := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": validFigma,
					"web_layout":   `[null]`,
				},
			},
		}
		resWeb, err := compareDesignHandler(context.Background(), reqWeb)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if !resWeb.IsError {
			t.Fatal("expected IsError for null element in web_layout")
		}
		gotWeb := resWeb.Content[0].(mcp.TextContent).Text
		if !strings.Contains(gotWeb, "failed to parse Web layout JSON: element at index 0 is null") {
			t.Errorf("got %q", gotWeb)
		}
	})

	// =================================================================
	// 2.12d. layout_tree モード: ネイティブ JSON 配列のインライン入力
	// =================================================================
	t.Run("LayoutTree_NativeJSONArray", func(t *testing.T) {
		figmaNative := []any{
			map[string]any{"id": "1", "name": "header", "x": 0, "y": 0, "w": 1000, "h": 100},
			map[string]any{"id": "2", "name": "logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "1"},
		}
		webNative := []any{
			map[string]any{"selector": "#header", "x": 0, "y": 0, "w": 1000, "h": 100},
			map[string]any{"selector": ".logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "#header"},
		}
		webString := `[{"selector":"#header","x":0,"y":0,"w":1000,"h":100},{"selector":".logo","x":10,"y":10,"w":100,"h":80,"parent":"#header"}]`

		reqNative := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaNative,
					"web_layout":   webNative,
				},
			},
		}
		resNative, err := compareDesignHandler(context.Background(), reqNative)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if resNative.IsError {
			t.Fatalf("Expected native JSON arrays to compare, got error: %v", resNative.Content[0].(mcp.TextContent).Text)
		}
		var resultNative map[string]interface{}
		json.Unmarshal([]byte(resNative.Content[0].(mcp.TextContent).Text), &resultNative)
		if resultNative["status"] != "success" || resultNative["match_rate"] != "100.00%" {
			t.Errorf("Expected success and 100%% match with native arrays, got status=%v, rate=%v", resultNative["status"], resultNative["match_rate"])
		}

		// 片側だけネイティブ配列、片側は従来の文字列でも成功する
		reqMixed := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaNative,
					"web_layout":   webString,
				},
			},
		}
		resMixed, err := compareDesignHandler(context.Background(), reqMixed)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if resMixed.IsError {
			t.Fatalf("Expected mixed string/array layout input to compare, got error: %v", resMixed.Content[0].(mcp.TextContent).Text)
		}
		var resultMixed map[string]interface{}
		json.Unmarshal([]byte(resMixed.Content[0].(mcp.TextContent).Text), &resultMixed)
		if resultMixed["status"] != "success" || resultMixed["match_rate"] != "100.00%" {
			t.Errorf("Expected success with mixed string/array input, got status=%v, rate=%v", resultMixed["status"], resultMixed["match_rate"])
		}

		reqBadType := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": 123,
					"web_layout":   webString,
				},
			},
		}
		resBadType, err := compareDesignHandler(context.Background(), reqBadType)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if !resBadType.IsError {
			t.Fatal("expected IsError when figma_layout is a number")
		}
		gotBad := resBadType.Content[0].(mcp.TextContent).Text
		if !strings.Contains(gotBad, "figma_layout must be a JSON-encoded string or a JSON array") {
			t.Errorf("got %q", gotBad)
		}
	})

	// =================================================================
	// 2.13. layout_tree モード: ignore_region による領域除外
	// =================================================================
	// 画像モードと同じ ignore_region を layout_tree でも受け付ける。
	// BoundingBox の中心点が領域内にあるノードは両側とも比較から除外され、
	// 除外数は ignored_count に加算される。全件除外時は ignore_nodes と同じ
	// skipped 判定になる。
	t.Run("LayoutTree_IgnoreRegion", func(t *testing.T) {
		figmaLayout := `[
			{"id": "1", "name": "header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"id": "2", "name": "banner", "x": 400, "y": 400, "w": 200, "h": 80}
		]`
		webLayout := `[
			{"selector": "#header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"selector": ".banner", "x": 650, "y": 420, "w": 200, "h": 80}
		]`

		// banner は Figma 側 (中心 500,440) と Web 側 (中心 750,460) で位置が大きく
		// 異なるため、除外が効かない場合は mismatch になる。
		cases := []struct {
			region        string
			wantStatus    string
			wantIgnored   float64
			wantMatchRate string
		}{
			// 両側の banner の中心点のみを含む領域 [400,900)x[400,500):
			// banner が両側から除外され、残った header 同士は一致する
			{"400,400,500,100", "success", 2, "100.00%"},
			// banner と重なるが中心点を含まない領域 [600,700)x[400,500):
			// 中心点ベースの判定のため除外されず mismatch のまま
			{"600,400,100,100", "mismatch", 0, "50.00%"},
			// 全ノードの中心点を含む領域: ignore_nodes と同じ skipped 判定になる
			{"0,0,2000,2000", "skipped", 4, "0.00%"},
		}

		for _, c := range cases {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":          "layout_tree",
						"figma_layout":  figmaLayout,
						"web_layout":    webLayout,
						"threshold":     0.15,
						"ignore_region": c.region,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if res.IsError {
				t.Fatalf("Expected no error for ignore_region=%q, got content=%v", c.region, res.Content[0].(mcp.TextContent).Text)
			}
			var result map[string]interface{}
			json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
			if result["status"] != c.wantStatus {
				t.Errorf("ignore_region=%q: expected status=%v, got %v", c.region, c.wantStatus, result["status"])
			}
			if got := result["ignored_count"]; got != c.wantIgnored {
				t.Errorf("ignore_region=%q: expected ignored_count=%v, got %v", c.region, c.wantIgnored, got)
			}
			if got := result["match_rate"]; got != c.wantMatchRate {
				t.Errorf("ignore_region=%q: expected match_rate=%v, got %v", c.region, c.wantMatchRate, got)
			}
			// 中心点に当たらない領域だけ unmatched_ignore_regions に出る（非空時のみ）。
			if c.region == "600,400,100,100" {
				gotRegions, ok := result["unmatched_ignore_regions"].([]interface{})
				if !ok || len(gotRegions) != 1 || gotRegions[0] != "600,400,100,100" {
					t.Errorf("ignore_region=%q: expected unmatched_ignore_regions=[600,400,100,100], got %v", c.region, result["unmatched_ignore_regions"])
				}
			} else if _, ok := result["unmatched_ignore_regions"]; ok {
				t.Errorf("ignore_region=%q: expected no unmatched_ignore_regions, got %v", c.region, result["unmatched_ignore_regions"])
			}
		}
	})

	// =================================================================
	// 1.5. layout_integrity モード (Issue #271: iPad 縦/横の崩れ検知)
	// =================================================================
	t.Run("LayoutIntegrity_Compare", func(t *testing.T) {
		inBounds := `[{"selector":"#page","x":0,"y":0,"w":768,"h":200}]`
		overflow := `[{"selector":"#wide","x":0,"y":0,"w":900,"h":100}]`
		parentOverflow := `[{"selector":"#card","x":0,"y":0,"w":400,"h":200},{"selector":"#child","x":0,"y":0,"w":500,"h":50,"parent":"#card"}]`
		tall := `[{"selector":"#tall","x":0,"y":0,"w":768,"h":2000}]`
		landscapeOverflow := `[{"selector":"#wide","x":0,"y":0,"w":1100,"h":100}]`

		t.Run("InBounds_DefaultPortrait", func(t *testing.T) {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":       "layout_integrity",
						"web_layout": inBounds,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if res.IsError {
				t.Fatalf("unexpected error: %s", res.Content[0].(mcp.TextContent).Text)
			}
			var result map[string]interface{}
			json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
			if result["status"] != "success" || result["mode"] != "layout_integrity" {
				t.Fatalf("got %#v", result)
			}
			if result["viewport_width"] != float64(768) || result["viewport_height"] != float64(1024) {
				t.Errorf("default viewport got %v x %v", result["viewport_width"], result["viewport_height"])
			}
			if result["issue_count"] != float64(0) {
				t.Errorf("issue_count=%v", result["issue_count"])
			}
			if _, ok := result["issues"]; ok {
				t.Errorf("issues should be omitted: %v", result["issues"])
			}
			if _, ok := result["match_rate"]; ok {
				t.Errorf("match_rate should not be present: %v", result["match_rate"])
			}
		})

		t.Run("ViewportOverflow_Portrait", func(t *testing.T) {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":       "layout_integrity",
						"web_layout": overflow,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			var result map[string]interface{}
			json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
			if result["status"] != "mismatch" {
				t.Fatalf("status=%v", result["status"])
			}
			issues, _ := result["issues"].([]interface{})
			if len(issues) < 1 {
				t.Fatalf("expected issues, got %#v", result)
			}
			issue := issues[0].(map[string]interface{})
			if issue["type"] != "viewport_overflow_x" || issue["selector"] != "#wide" {
				t.Errorf("issue=%v", issue)
			}
		})

		t.Run("Wide900_OK_OnLandscapePreset", func(t *testing.T) {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":            "layout_integrity",
						"web_layout":      overflow,
						"viewport_preset": "ipad_landscape",
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if res.IsError {
				t.Fatalf("unexpected error: %s", res.Content[0].(mcp.TextContent).Text)
			}
			var result map[string]interface{}
			json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
			if result["status"] != "success" {
				t.Fatalf("900px should fit landscape 1024, got %#v", result)
			}
			if result["viewport_width"] != float64(1024) || result["viewport_height"] != float64(768) {
				t.Errorf("landscape size got %v x %v", result["viewport_width"], result["viewport_height"])
			}
			if result["viewport_preset"] != "ipad_landscape" {
				t.Errorf("preset echo=%v", result["viewport_preset"])
			}
		})

		t.Run("Wide1100_Mismatch_Landscape", func(t *testing.T) {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":            "layout_integrity",
						"web_layout":      landscapeOverflow,
						"viewport_width":  1024.0,
						"viewport_height": 768.0,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			var result map[string]interface{}
			json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
			if result["status"] != "mismatch" {
				t.Fatalf("got %#v", result)
			}
		})

		t.Run("ParentOverflow", func(t *testing.T) {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":       "layout_integrity",
						"web_layout": parentOverflow,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			var result map[string]interface{}
			json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
			if result["status"] != "mismatch" {
				t.Fatalf("status=%v", result["status"])
			}
			issues, _ := result["issues"].([]interface{})
			foundParent := false
			for _, raw := range issues {
				issue := raw.(map[string]interface{})
				if issue["type"] == "parent_overflow" && issue["selector"] == "#child" {
					foundParent = true
				}
				if issue["type"] == "viewport_overflow_x" {
					t.Errorf("unexpected viewport overflow: %v", issue)
				}
			}
			if !foundParent {
				t.Fatalf("issues=%v", issues)
			}
		})

		t.Run("ViewportWidthZero", func(t *testing.T) {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":           "layout_integrity",
						"web_layout":     inBounds,
						"viewport_width": 0.0,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if !res.IsError {
				t.Fatal("expected error")
			}
			got := res.Content[0].(mcp.TextContent).Text
			if !strings.Contains(got, "viewport_width must be greater than 0") {
				t.Errorf("got %q", got)
			}
		})

		t.Run("MissingWebLayout", func(t *testing.T) {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode": "layout_integrity",
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if !res.IsError {
				t.Fatal("expected error")
			}
			got := res.Content[0].(mcp.TextContent).Text
			if !strings.Contains(got, "either web_layout or web_layout_path is required") {
				t.Errorf("got %q", got)
			}
		})

		t.Run("FigmaLayoutUnsupported", func(t *testing.T) {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":         "layout_integrity",
						"web_layout":   inBounds,
						"figma_layout": `[{"id":"1","name":"a","x":0,"y":0,"w":10,"h":10}]`,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if !res.IsError {
				t.Fatal("expected error")
			}
			got := res.Content[0].(mcp.TextContent).Text
			if !strings.Contains(got, "parameter 'figma_layout' is not supported in mode 'layout_integrity'") {
				t.Errorf("got %q", got)
			}
		})

		t.Run("UnknownPreset", func(t *testing.T) {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":            "layout_integrity",
						"web_layout":      inBounds,
						"viewport_preset": "iphone",
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if !res.IsError {
				t.Fatal("expected error")
			}
			got := res.Content[0].(mcp.TextContent).Text
			if !strings.Contains(got, "unknown viewport_preset") {
				t.Errorf("got %q", got)
			}
		})

		t.Run("TallPage_Success", func(t *testing.T) {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":       "layout_integrity",
						"web_layout": tall,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			var result map[string]interface{}
			json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
			if result["status"] != "success" || result["issue_count"] != float64(0) {
				t.Fatalf("got %#v", result)
			}
		})

		// width/height など w/h 以外のキー名で幾何が全て 0 の場合、「データが空」ではなく
		// 幾何欠落として区別されたメッセージが details に流れることを確認する。
		t.Run("ZeroGeometry_DistinctDetail", func(t *testing.T) {
			wrongKeys := `[{"selector":"#a","x":0,"y":0,"width":100,"height":50}]`
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":       "layout_integrity",
						"web_layout": wrongKeys,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			var result map[string]interface{}
			json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
			if result["status"] != "mismatch" {
				t.Fatalf("status=%v", result["status"])
			}
			details, _ := result["details"].([]interface{})
			if len(details) != 1 || !strings.Contains(details[0].(string), "no Web nodes have non-zero geometry") {
				t.Fatalf("details=%v", details)
			}
		})

		t.Run("UnmatchedIgnoreRegions_Response", func(t *testing.T) {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":          "layout_integrity",
						"web_layout":    inBounds,
						"ignore_region": "900,900,10,10",
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			var result map[string]interface{}
			json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
			if result["status"] != "success" {
				t.Fatalf("status=%v", result["status"])
			}
			got, _ := result["unmatched_ignore_regions"].([]interface{})
			if len(got) != 1 || got[0] != "900,900,10,10" {
				t.Fatalf("unmatched_ignore_regions=%v", got)
			}
		})
	})

	// =================================================================
	// 2. perceptual モード (知覚的画像比較) のテスト
	// =================================================================
	t.Run("Perceptual_Layout_Match", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathC, // 微小な輝度差
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "success" || result["match_rate"] != "100.00%" {
			t.Errorf("Expected perceptual layout success, got status=%v, rate=%v", result["status"], result["match_rate"])
		}
		if _, ok := result["diff_cells"]; ok {
			t.Errorf("Expected no diff_cells on full match, got %v", result["diff_cells"])
		}
		assertDiffDataURI(t, result)
		// 数値一致率フィールドの検証
		if got := result["match_rate_value"]; got != float64(100) {
			t.Errorf("Expected match_rate_value=100, got %v", got)
		}
		// 実効パラメータ min_match (デフォルト 98.0) が応答に含まれることの検証
		if got := result["min_match"]; got != float64(98) {
			t.Errorf("Expected min_match=98 (default), got %v", got)
		}
		// details は全モードで文字列配列に統一されている (perceptual は単一要素)
		details, ok := result["details"].([]interface{})
		if !ok {
			t.Fatalf("Expected details array in perceptual result, got %v", result["details"])
		}
		if len(details) != 1 {
			t.Errorf("Expected 1 detail entry in perceptual result, got %d: %v", len(details), details)
		}
	})

	t.Run("Perceptual_Layout_Mismatch", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathD, // 配置が異なる (左右 vs 上下)
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "mismatch" {
			t.Errorf("Expected perceptual layout mismatch, got status=%v", result["status"])
		}
		assertDiffDataURI(t, result)
		assertPerceptualPathDDiffCells(t, result)
	})

	// generate_diff=false で差分画像の生成を省略し、temp ファイルを作らない
	t.Run("Perceptual_GenerateDiff_False", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":          "perceptual",
					"image_path_a":  pathA,
					"image_path_b":  pathD,
					"generate_diff": false,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] == "" {
			t.Errorf("Expected a comparison result with generate_diff=false, got %v", result)
		}
		if diffImg, ok := result["diff_image"].(string); !ok || diffImg != "" {
			t.Errorf("Expected empty diff_image with generate_diff=false, got %v", result["diff_image"])
		}
		assertPerceptualPathDDiffCells(t, result)
	})

	// diff_on_mismatch=true: 成功時のみ diff_image を空にし、不一致では生成する
	t.Run("Perceptual_DiffOnMismatch", func(t *testing.T) {
		reqMatch := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":             "perceptual",
					"image_path_a":     pathA,
					"image_path_b":     pathC,
					"diff_on_mismatch": true,
				},
			},
		}
		resMatch, err := compareDesignHandler(context.Background(), reqMatch)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultMatch map[string]interface{}
		json.Unmarshal([]byte(resMatch.Content[0].(mcp.TextContent).Text), &resultMatch)
		if resultMatch["status"] != "success" {
			t.Fatalf("Expected perceptual success, got status=%v", resultMatch["status"])
		}
		if diffImg, ok := resultMatch["diff_image"].(string); !ok || diffImg != "" {
			t.Errorf("Expected empty diff_image on success with diff_on_mismatch=true, got %v", resultMatch["diff_image"])
		}

		reqMismatch := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":             "perceptual",
					"image_path_a":     pathA,
					"image_path_b":     pathD,
					"diff_on_mismatch": true,
				},
			},
		}
		resMismatch, err := compareDesignHandler(context.Background(), reqMismatch)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultMismatch map[string]interface{}
		json.Unmarshal([]byte(resMismatch.Content[0].(mcp.TextContent).Text), &resultMismatch)
		if resultMismatch["status"] != "mismatch" {
			t.Errorf("Expected perceptual mismatch, got status=%v", resultMismatch["status"])
		}
		assertDiffDataURI(t, resultMismatch)

		// generate_diff=false と併用すると不一致でも diff_image は空のまま
		reqBothFalse := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":             "perceptual",
					"image_path_a":     pathA,
					"image_path_b":     pathD,
					"generate_diff":    false,
					"diff_on_mismatch": true,
				},
			},
		}
		resBoth, err := compareDesignHandler(context.Background(), reqBothFalse)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultBoth map[string]interface{}
		json.Unmarshal([]byte(resBoth.Content[0].(mcp.TextContent).Text), &resultBoth)
		if resultBoth["status"] != "mismatch" {
			t.Errorf("Expected perceptual mismatch, got status=%v", resultBoth["status"])
		}
		if diffImg, ok := resultBoth["diff_image"].(string); !ok || diffImg != "" {
			t.Errorf("Expected empty diff_image with generate_diff=false even on mismatch, got %v", resultBoth["diff_image"])
		}
	})

	// diff_image_content=true: 差分 PNG を MCP image コンテンツでも返す (Issue #219)
	t.Run("Perceptual_DiffImageContent", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":               "perceptual",
					"image_path_a":       pathA,
					"image_path_b":       pathD,
					"diff_image_content": true,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if res.IsError {
			t.Fatalf("Expected success, got error %v", res.Content[0].(mcp.TextContent).Text)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "mismatch" {
			t.Fatalf("Expected perceptual mismatch, got status=%v", result["status"])
		}
		assertDiffDataURI(t, result)
		assertDiffImageContent(t, res, result)

		// 既定 (false) では content は JSON テキストのみ
		reqDefault := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathD,
				},
			},
		}
		resDefault, err := compareDesignHandler(context.Background(), reqDefault)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if len(resDefault.Content) != 1 {
			t.Errorf("Expected 1 content block by default, got %d", len(resDefault.Content))
		}

		// generate_diff=false では差分が無いので image ブロックを付けない
		reqNoDiff := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":               "perceptual",
					"image_path_a":       pathA,
					"image_path_b":       pathD,
					"generate_diff":      false,
					"diff_image_content": true,
				},
			},
		}
		resNoDiff, err := compareDesignHandler(context.Background(), reqNoDiff)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if len(resNoDiff.Content) != 1 {
			t.Errorf("Expected 1 content block when generate_diff=false, got %d", len(resNoDiff.Content))
		}
	})

	// ignore_region: 既知の差分領域をマスクして比較する
	t.Run("Perceptual_IgnoreRegion", func(t *testing.T) {
		// 指定なし: 左上の黒矩形 (pathE) と全面白 (pathF) は一致率75%で mismatch
		reqNoRegion := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathE,
					"image_path_b": pathF,
				},
			},
		}
		resNoRegion, err := compareDesignHandler(context.Background(), reqNoRegion)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultNoRegion map[string]interface{}
		json.Unmarshal([]byte(resNoRegion.Content[0].(mcp.TextContent).Text), &resultNoRegion)
		if resultNoRegion["status"] != "mismatch" {
			t.Errorf("Expected mismatch without ignore_region, got status=%v, rate=%v", resultNoRegion["status"], resultNoRegion["match_rate"])
		}
		if got := resultNoRegion["ignored_regions"]; got != float64(0) {
			t.Errorf("Expected ignored_regions=0 when ignore_region is omitted, got %v", got)
		}

		// 指定あり: 黒矩形領域をマスクすると完全一致で success
		reqRegion := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":          "perceptual",
					"image_path_a":  pathE,
					"image_path_b":  pathF,
					"ignore_region": "0,0,100,100",
				},
			},
		}
		resRegion, err := compareDesignHandler(context.Background(), reqRegion)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultRegion map[string]interface{}
		json.Unmarshal([]byte(resRegion.Content[0].(mcp.TextContent).Text), &resultRegion)
		if resultRegion["status"] != "success" || resultRegion["match_rate"] != "100.00%" {
			t.Errorf("Expected success and 100%% match with ignore_region, got status=%v, rate=%v", resultRegion["status"], resultRegion["match_rate"])
		}
		if got := resultRegion["ignored_regions"]; got != float64(1) {
			t.Errorf("Expected ignored_regions=1 with one ignore_region, got %v", got)
		}
		// 範囲内の ignore_region では警告フィールド (out_of_bounds_regions) は出ない
		if _, ok := resultRegion["out_of_bounds_regions"]; ok {
			t.Errorf("Expected no out_of_bounds_regions for in-bounds ignore_region, got %v", resultRegion["out_of_bounds_regions"])
		}

		// 空セグメントはパース時にスキップされ、有効領域数だけ ignored_regions に入る
		reqEmptySeg := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":          "perceptual",
					"image_path_a":  pathE,
					"image_path_b":  pathF,
					"ignore_region": "0,0,100,100;;",
				},
			},
		}
		resEmptySeg, err := compareDesignHandler(context.Background(), reqEmptySeg)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultEmptySeg map[string]interface{}
		json.Unmarshal([]byte(resEmptySeg.Content[0].(mcp.TextContent).Text), &resultEmptySeg)
		if got := resultEmptySeg["ignored_regions"]; got != float64(1) {
			t.Errorf("Expected ignored_regions=1 when empty segments are skipped, got %v", got)
		}

		// 画像範囲外の ignore_region は何もマスクされず差分が残るため、座標ミスが
		// 分かるよう out_of_bounds_regions として応答で警告される (Issue #128)
		reqOutOfBounds := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":          "perceptual",
					"image_path_a":  pathE,
					"image_path_b":  pathF,
					"ignore_region": "500,500,100,100",
				},
			},
		}
		resOutOfBounds, err := compareDesignHandler(context.Background(), reqOutOfBounds)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultOutOfBounds map[string]interface{}
		json.Unmarshal([]byte(resOutOfBounds.Content[0].(mcp.TextContent).Text), &resultOutOfBounds)
		// 200x200 画像に対する "500,500,100,100" は全く交差しないため差分が残る
		if resultOutOfBounds["status"] != "mismatch" {
			t.Errorf("Expected mismatch with out-of-bounds ignore_region (nothing masked), got status=%v", resultOutOfBounds["status"])
		}
		gotRegions, ok := resultOutOfBounds["out_of_bounds_regions"].([]interface{})
		if !ok || len(gotRegions) != 1 || gotRegions[0] != "500,500,100,100" {
			t.Errorf("Expected out_of_bounds_regions=[500,500,100,100], got %v", resultOutOfBounds["out_of_bounds_regions"])
		}
		if got := resultOutOfBounds["ignored_regions"]; got != float64(1) {
			t.Errorf("Expected ignored_regions=1 for out-of-bounds region, got %v", got)
		}
	})

	// perceptual で A/B のサイズが異なる場合、ignore_region は各画像の絶対ピクセルで
	// 適用されるため details に注記を出す (Issue #164)
	t.Run("Perceptual_IgnoreRegion_DifferentImageSizes", func(t *testing.T) {
		pathSmallWhite := saveTempImage(t, tmpDir, "imageSmallWhite.png", generateSolidImage(100, 100, color.White))
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":          "perceptual",
					"image_path_a":  pathE, // 200x200
					"image_path_b":  pathSmallWhite,
					"ignore_region": "0,0,100,100",
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if got := result["ignored_regions"]; got != float64(1) {
			t.Errorf("Expected ignored_regions=1, got %v", got)
		}
		details, ok := result["details"].([]interface{})
		if !ok || len(details) != 2 {
			t.Fatalf("Expected 2 detail entries when image sizes differ, got %v", result["details"])
		}
		note, ok := details[1].(string)
		if !ok || note != "note: image A is 200x200, image B is 100x100; ignore_region is applied in absolute pixels of each image" {
			t.Errorf("Expected size-mismatch ignore_region note, got %v", details[1])
		}
		if result["image_size_a"] != "200x200" || result["image_size_b"] != "100x100" {
			t.Errorf("Expected image_size_a=200x200 image_size_b=100x100, got a=%v b=%v", result["image_size_a"], result["image_size_b"])
		}
	})

	// diff_blocks / total_blocks: aHash の差分セル数を数量として応答へ含める
	// (strict の diff_pixels / total_pixels に対応。Issue #141)
	t.Run("Perceptual_DiffBlocks", func(t *testing.T) {
		// 指定なし: pathE (左上100x100の黒矩形) vs pathF (全面白) は
		// 16x16 グリッドの左上 8x8 = 64 セルが不一致 (一致率75%)
		reqNoRegion := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathE,
					"image_path_b": pathF,
				},
			},
		}
		resNoRegion, err := compareDesignHandler(context.Background(), reqNoRegion)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultNoRegion map[string]interface{}
		json.Unmarshal([]byte(resNoRegion.Content[0].(mcp.TextContent).Text), &resultNoRegion)

		totalBlocks, ok := resultNoRegion["total_blocks"].(float64)
		if !ok {
			t.Fatalf("Expected numeric total_blocks, got %T: %v", resultNoRegion["total_blocks"], resultNoRegion["total_blocks"])
		}
		if totalBlocks != 256 {
			t.Errorf("Expected total_blocks=256 (16x16 aHash grid), got %v", totalBlocks)
		}
		diffBlocks, ok := resultNoRegion["diff_blocks"].(float64)
		if !ok {
			t.Fatalf("Expected numeric diff_blocks, got %T: %v", resultNoRegion["diff_blocks"], resultNoRegion["diff_blocks"])
		}
		if diffBlocks != 64 {
			t.Errorf("Expected diff_blocks=64 (top-left 8x8 cells differ), got %v", diffBlocks)
		}
		// diff_blocks / total_blocks から算出される一致率と match_rate_value が整合すること
		if got, want := resultNoRegion["match_rate_value"], float64(256-64)/256*100; got != want {
			t.Errorf("Expected match_rate_value=%v consistent with diff_blocks/total_blocks, got %v", want, got)
		}
		// details は差分セル数を "N of 256 blocks differ" として含む (単一要素のまま)
		details, ok := resultNoRegion["details"].([]interface{})
		if !ok || len(details) != 1 {
			t.Fatalf("Expected 1 detail entry in perceptual result, got %v", resultNoRegion["details"])
		}
		if s, ok := details[0].(string); !ok || !strings.Contains(s, "64 of 256 blocks differ") {
			t.Errorf("Expected details to contain '64 of 256 blocks differ', got %v", details[0])
		}
		if resultNoRegion["image_size_a"] != "200x200" || resultNoRegion["image_size_b"] != "200x200" {
			t.Errorf("Expected image_size_a/b=200x200, got a=%v b=%v", resultNoRegion["image_size_a"], resultNoRegion["image_size_b"])
		}

		// ignore_region で既知の差分領域をマスクすると diff_blocks=0
		reqRegion := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":          "perceptual",
					"image_path_a":  pathE,
					"image_path_b":  pathF,
					"ignore_region": "0,0,100,100",
				},
			},
		}
		resRegion, err := compareDesignHandler(context.Background(), reqRegion)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultRegion map[string]interface{}
		json.Unmarshal([]byte(resRegion.Content[0].(mcp.TextContent).Text), &resultRegion)
		if got := resultRegion["diff_blocks"]; got != float64(0) {
			t.Errorf("Expected diff_blocks=0 with ignore_region, got %v", got)
		}
		if detailsRegion, ok := resultRegion["details"].([]interface{}); !ok || len(detailsRegion) != 1 {
			t.Fatalf("Expected 1 detail entry with ignore_region, got %v", resultRegion["details"])
		} else if str, ok := detailsRegion[0].(string); !ok || !strings.Contains(str, "0 of 256 blocks differ") {
			t.Errorf("Expected details to contain '0 of 256 blocks differ', got %v", detailsRegion[0])
		}
	})

	// 一様画像 (ベタ塗り) のペアは aHash が退化し、全面白 vs 全面黒でも一致率100%で
	// 合格してしまう。status / match_rate は変えず warnings で気付かせる (Issue #131)
	t.Run("Perceptual_UniformImage_Warnings", func(t *testing.T) {
		pathBlack := saveTempImage(t, tmpDir, "imageUniformBlack.png", generateSolidImage(200, 200, color.Black))

		// pathF (全面白) vs 全面黒: 挙動は success/100% のまま warnings が付く
		reqUniform := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathF,
					"image_path_b": pathBlack,
				},
			},
		}
		resUniform, err := compareDesignHandler(context.Background(), reqUniform)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultUniform map[string]interface{}
		json.Unmarshal([]byte(resUniform.Content[0].(mcp.TextContent).Text), &resultUniform)
		if resultUniform["status"] != "success" || resultUniform["match_rate"] != "100.00%" {
			t.Errorf("Expected success and 100%% match for uniform pair (behavior unchanged), got status=%v, rate=%v", resultUniform["status"], resultUniform["match_rate"])
		}
		gotWarnings, ok := resultUniform["warnings"].([]interface{})
		if !ok || len(gotWarnings) != 2 {
			t.Fatalf("Expected 2 warnings for all-white vs all-black, got %v", resultUniform["warnings"])
		}
		if gotWarnings[0] != "degenerate aHash: image A is uniform; perceptual match may be unreliable" {
			t.Errorf("Unexpected warning for image A: %v", gotWarnings[0])
		}
		if gotWarnings[1] != "degenerate aHash: image B is uniform; perceptual match may be unreliable" {
			t.Errorf("Unexpected warning for image B: %v", gotWarnings[1])
		}

		// 通常の明暗パターンを持つペアでは warnings フィールドは含まれない
		reqNormal := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathC,
				},
			},
		}
		resNormal, err := compareDesignHandler(context.Background(), reqNormal)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultNormal map[string]interface{}
		json.Unmarshal([]byte(resNormal.Content[0].(mcp.TextContent).Text), &resultNormal)
		if _, ok := resultNormal["warnings"]; ok {
			t.Errorf("Expected no warnings for non-uniform pair, got %v", resultNormal["warnings"])
		}
	})

	// strict の差分 0 合格は両画像が単色ベタ塗りでも success・100% になる。
	// status / match_rate は変えず warnings で空洞比較を通知する (Issue #227)
	t.Run("Strict_UniformImage_Warnings", func(t *testing.T) {
		const wantWarning = "degenerate comparison: both images are uniform; strict match may be vacuous (blank capture failure or over-broad ignore_region)"

		reqUniform := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "strict",
					"image_path_a": pathF,
					"image_path_b": pathF,
				},
			},
		}
		resUniform, err := compareDesignHandler(context.Background(), reqUniform)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultUniform map[string]interface{}
		json.Unmarshal([]byte(resUniform.Content[0].(mcp.TextContent).Text), &resultUniform)
		if resultUniform["status"] != "success" || resultUniform["match_rate"] != "100.00%" {
			t.Errorf("Expected success and 100%% match for all-white pair (behavior unchanged), got status=%v, rate=%v", resultUniform["status"], resultUniform["match_rate"])
		}
		gotWarnings, ok := resultUniform["warnings"].([]interface{})
		if !ok || len(gotWarnings) != 1 || gotWarnings[0] != wantWarning {
			t.Errorf("Expected warnings=[%q], got %v", wantWarning, resultUniform["warnings"])
		}

		reqNormal := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "strict",
					"image_path_a": pathA,
					"image_path_b": pathA,
				},
			},
		}
		resNormal, err := compareDesignHandler(context.Background(), reqNormal)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultNormal map[string]interface{}
		json.Unmarshal([]byte(resNormal.Content[0].(mcp.TextContent).Text), &resultNormal)
		if resultNormal["status"] != "success" {
			t.Errorf("Expected success for identical non-uniform pair, got status=%v", resultNormal["status"])
		}
		if _, ok := resultNormal["warnings"]; ok {
			t.Errorf("Expected no warnings for non-uniform pair, got %v", resultNormal["warnings"])
		}
	})

	// 背景透過PNG (Figma のフレーム書き出し等) の透過ピクセルを白背景に合成して
	// から輝度化することを検証する (Issue #134)。アルファを無視して透過部分を
	// 「黒」として扱うと、strict (pixelmatch は白背景に合成して比較) だけが通り、
	// perceptual だけが大差分の誤不一致になっていた。
	t.Run("Perceptual_TransparentBackground_WhiteComposite", func(t *testing.T) {
		// 全面透過 200x200 (全ピクセル alpha=0) vs 全面白 (pathF):
		// 白合成後は同じ全面白のため 100% 一致する (両画像とも一様のため
		// uniform 警告は発火するが合否には影響しない)。
		pathTransparent := saveTempImage(t, tmpDir, "imageTransparent.png", image.NewRGBA(image.Rect(0, 0, 200, 200)))

		reqFull := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathTransparent,
					"image_path_b": pathF,
				},
			},
		}
		resFull, err := compareDesignHandler(context.Background(), reqFull)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultFull map[string]interface{}
		json.Unmarshal([]byte(resFull.Content[0].(mcp.TextContent).Text), &resultFull)
		if resultFull["status"] != "success" || resultFull["match_rate"] != "100.00%" {
			t.Errorf("Expected full transparent vs full white to match 100%% after white compositing, got status=%v, rate=%v", resultFull["status"], resultFull["match_rate"])
		}

		// 透過背景 + 右半分に黒矩形 (Figma の背景透過書き出しを模擬) vs
		// pathA (白背景 + 同じ黒矩形): 透過部分が「黒」として扱われると透過側が
		// 一様な全面黒になり 50% の誤不一致になるが、白合成されれば 100% 一致する。
		imgTransparentContent := image.NewRGBA(image.Rect(0, 0, 200, 200))
		draw.Draw(imgTransparentContent, image.Rect(100, 0, 200, 200), &image.Uniform{color.Black}, image.Point{}, draw.Src)
		pathTransparentContent := saveTempImage(t, tmpDir, "imageTransparentContent.png", imgTransparentContent)

		reqContent := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathTransparentContent,
					"image_path_b": pathA,
				},
			},
		}
		resContent, err := compareDesignHandler(context.Background(), reqContent)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultContent map[string]interface{}
		json.Unmarshal([]byte(resContent.Content[0].(mcp.TextContent).Text), &resultContent)
		if resultContent["status"] != "success" || resultContent["match_rate"] != "100.00%" {
			t.Errorf("Expected transparent-bg content vs white-bg content to match 100%% after white compositing, got status=%v, rate=%v", resultContent["status"], resultContent["match_rate"])
		}
	})

	// 差分PNG一時ファイルが /tmp に蓄積しないことを確認する（Issue #32）。
	t.Run("Perceptual_NoDiffTempFiles", func(t *testing.T) {
		before, err := countDiffTempFiles()
		if err != nil {
			t.Fatalf("failed to list temp dir: %v", err)
		}

		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathD,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		assertDiffDataURI(t, result)

		after, err := countDiffTempFiles()
		if err != nil {
			t.Fatalf("failed to list temp dir: %v", err)
		}
		if after != before {
			t.Errorf("expected no new perceptual-diff-*.png files in %s, before=%d after=%d", os.TempDir(), before, after)
		}
	})

	t.Run("Perceptual_Threshold_Too_Low", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathC,
					"threshold":    0.1, // strict モードと同じ感覚で 0.1 を渡す誤用
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if !res.IsError {
			t.Errorf("Expected error result for perceptual threshold below 1.0, got content=%v", res.Content[0].(mcp.TextContent).Text)
		}
	})

	// min_match パラメータ (perceptual モード専用) のテスト
	t.Run("Perceptual_MinMatch_Success", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathC,
					"min_match":    98.0,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "success" {
			t.Errorf("Expected success with min_match=98.0, got status=%v", result["status"])
		}
		if v, ok := result["min_match"].(float64); !ok || v != 98.0 {
			t.Errorf("Expected min_match=98.0 in response, got %v", result["min_match"])
		}
	})

	t.Run("Perceptual_MinMatch_Mismatch", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathD,
					"min_match":    98.0,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "mismatch" {
			t.Errorf("Expected mismatch with min_match=98.0, got status=%v", result["status"])
		}
	})

	t.Run("Perceptual_MinMatch_DisplayRounding", func(t *testing.T) {
		// 16x16 の左右分割で 5 セルだけ反転すると一致率は 98.046875% → 表示 98.05%
		imgBase := generateSplitImage(16, 16, color.White, color.Black)
		imgFlip := generateSplitImage(16, 16, color.White, color.Black)
		rgba, ok := imgFlip.(*image.RGBA)
		if !ok {
			t.Fatalf("expected *image.RGBA from generateSplitImage")
		}
		for i := 0; i < 5; i++ {
			rgba.Set(i, 0, color.Black)
		}
		pathBase := saveTempImage(t, tmpDir, "perceptual-round-base.png", imgBase)
		pathFlip := saveTempImage(t, tmpDir, "perceptual-round-flip.png", imgFlip)

		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathBase,
					"image_path_b": pathFlip,
					"min_match":    98.05,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["diff_blocks"] != float64(5) {
			t.Fatalf("Expected diff_blocks=5, got %v", result["diff_blocks"])
		}
		wantRaw := float64(256-5) / 256 * 100
		if got := result["match_rate_value"]; got != wantRaw {
			t.Errorf("Expected raw match_rate_value=%v, got %v", wantRaw, got)
		}
		if result["match_rate"] != "98.05%" {
			t.Errorf("Expected displayed match_rate=98.05%%, got %v", result["match_rate"])
		}
		if result["status"] != "success" {
			t.Errorf("Expected success when displayed match_rate equals min_match=98.05, got status=%v", result["status"])
		}
	})

	t.Run("Perceptual_MinMatch_AcceptsLowValue", func(t *testing.T) {
		// min_match は 0.0-100.0 を許容する (threshold と異なり 1.0 未満でもエラーにしない)
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathD,
					"min_match":    0.5,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if res.IsError {
			t.Errorf("Expected no error for min_match=0.5, got content=%v", res.Content[0].(mcp.TextContent).Text)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		// 0.5% 基準なら不一致画像でも success になるはず
		if result["status"] != "success" {
			t.Errorf("Expected success with min_match=0.5, got status=%v", result["status"])
		}
		// 指定した min_match が実効値としてそのまま応答に echo されることの検証
		if got := result["min_match"]; got != float64(0.5) {
			t.Errorf("Expected min_match=0.5, got %v", got)
		}
	})

	t.Run("Perceptual_MinMatch_OutOfRange", func(t *testing.T) {
		for _, val := range []float64{-1.0, 101.0} {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":         "perceptual",
						"image_path_a": pathA,
						"image_path_b": pathC,
						"min_match":    val,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if !res.IsError {
				t.Errorf("Expected error for perceptual min_match=%.1f, got content=%v", val, res.Content[0].(mcp.TextContent).Text)
			}
		}
	})

	t.Run("Perceptual_MinMatch_PrefersOverThreshold", func(t *testing.T) {
		// min_match と threshold の同時指定は相互排他エラーにする
		// (threshold=0.1 は min_match 単独時に strict スケール混同として拒否される値)
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathD,
					"min_match":    98.0,
					"threshold":    0.1,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if !res.IsError {
			t.Errorf("Expected error when both min_match and threshold are specified, got content=%v", res.Content[0].(mcp.TextContent).Text)
		}
	})

	// perceptual 応答の min_match echo のテスト (Issue #129)
	// perceptual は常に閾値で合否判定するため、実効 min_match (threshold エイリアス
	// 解決後を含む) を常に応答へ含め、どの閾値で判定されたかを検証可能にする。
	t.Run("Perceptual_MinMatch_Echo", func(t *testing.T) {
		// 未指定時はデフォルトの 98.0 が応答される
		reqDefault := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathC,
				},
			},
		}
		resDefault, err := compareDesignHandler(context.Background(), reqDefault)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultDefault map[string]interface{}
		json.Unmarshal([]byte(resDefault.Content[0].(mcp.TextContent).Text), &resultDefault)
		if v, ok := resultDefault["min_match"].(float64); !ok || v != 98.0 {
			t.Errorf("Expected default min_match=98.0 in response, got %v", resultDefault["min_match"])
		}

		// 明示指定時は指定値が応答される
		reqExplicit := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathC,
					"min_match":    50.0,
				},
			},
		}
		resExplicit, err := compareDesignHandler(context.Background(), reqExplicit)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultExplicit map[string]interface{}
		json.Unmarshal([]byte(resExplicit.Content[0].(mcp.TextContent).Text), &resultExplicit)
		if v, ok := resultExplicit["min_match"].(float64); !ok || v != 50.0 {
			t.Errorf("Expected min_match=50.0 in response, got %v", resultExplicit["min_match"])
		}

		// threshold エイリアス使用時は解決後の値が応答される
		reqAlias := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathC,
					"threshold":    99.0,
				},
			},
		}
		resAlias, err := compareDesignHandler(context.Background(), reqAlias)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultAlias map[string]interface{}
		json.Unmarshal([]byte(resAlias.Content[0].(mcp.TextContent).Text), &resultAlias)
		if v, ok := resultAlias["min_match"].(float64); !ok || v != 99.0 {
			t.Errorf("Expected resolved min_match=99.0 in response for threshold alias, got %v", resultAlias["min_match"])
		}
	})

	// =================================================================
	// base64 入力のテスト (perceptual / strict モード)
	// =================================================================
	t.Run("Perceptual_Base64_Input", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":           "perceptual",
					"image_a_base64": encodePNGBase64(t, imgA),
					"image_b_base64": encodePNGBase64(t, imgC),
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "success" || result["match_rate"] != "100.00%" {
			t.Errorf("Expected perceptual base64 success, got status=%v, rate=%v", result["status"], result["match_rate"])
		}
	})

	// 改行・スペース入り base64 はパス入力と同じ比較結果になる (Issue #207)
	t.Run("Perceptual_Base64_Whitespace_Matches_Path", func(t *testing.T) {
		reqPath := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathC,
				},
			},
		}
		resPath, err := compareDesignHandler(context.Background(), reqPath)
		if err != nil {
			t.Fatalf("path handler failed: %v", err)
		}
		var resultPath map[string]interface{}
		if err := json.Unmarshal([]byte(resPath.Content[0].(mcp.TextContent).Text), &resultPath); err != nil {
			t.Fatalf("path result json: %v", err)
		}

		reqWrapped := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":           "perceptual",
					"image_a_base64": wrapBase64MIME(encodePNGBase64(t, imgA)),
					"image_b_base64": wrapBase64MIME(encodePNGBase64(t, imgC)),
				},
			},
		}
		resWrapped, err := compareDesignHandler(context.Background(), reqWrapped)
		if err != nil {
			t.Fatalf("wrapped base64 handler failed: %v", err)
		}
		var resultWrapped map[string]interface{}
		if err := json.Unmarshal([]byte(resWrapped.Content[0].(mcp.TextContent).Text), &resultWrapped); err != nil {
			t.Fatalf("wrapped result json: %v", err)
		}
		if resultWrapped["status"] != resultPath["status"] || resultWrapped["match_rate"] != resultPath["match_rate"] {
			t.Errorf("wrapped base64 status=%v rate=%v, want path status=%v rate=%v",
				resultWrapped["status"], resultWrapped["match_rate"], resultPath["status"], resultPath["match_rate"])
		}
	})

	t.Run("Strict_Base64_Input", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":           "strict",
					"image_a_base64": encodePNGBase64(t, imgA),
					"image_b_base64": encodePNGBase64(t, imgC),
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "mismatch" {
			t.Errorf("Expected strict base64 mismatch, got status=%v", result["status"])
		}
	})

	// 本ツールの diff_image が返す data URI 形式 ("data:image/png;base64,...") を
	// そのまま再入力できることを検証する (Issue #127: ラウンドトリップ)
	t.Run("Perceptual_Base64_DataURI_Input", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":           "perceptual",
					"image_a_base64": "data:image/png;base64," + encodePNGBase64(t, imgA),
					"image_b_base64": "data:image/png;base64," + encodePNGBase64(t, imgC),
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "success" || result["match_rate"] != "100.00%" {
			t.Errorf("Expected perceptual data URI base64 success, got status=%v, rate=%v, content=%v",
				result["status"], result["match_rate"], res.Content[0].(mcp.TextContent).Text)
		}
	})

	// プレーン base64 と data URI 形式を混在させて入力できることを検証する (Issue #150)
	t.Run("Strict_Base64_DataURI_Input", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":           "strict",
					"image_a_base64": encodePNGBase64(t, imgA),
					"image_b_base64": "data:image/png;base64," + encodePNGBase64(t, imgC),
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "mismatch" {
			t.Errorf("Expected strict data URI base64 mismatch, got status=%v", result["status"])
		}
	})

	t.Run("Perceptual_Base64_Path_Exclusive", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":           "perceptual",
					"image_path_a":   pathA,
					"image_a_base64": encodePNGBase64(t, imgA),
					"image_b_base64": encodePNGBase64(t, imgC),
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if !res.IsError {
			t.Errorf("Expected error when both path and base64 are specified, got content=%v", res.Content[0].(mcp.TextContent).Text)
		}
	})

	// =================================================================
	// 3. strict モード (厳密ピクセル比較) のテスト
	// =================================================================
	// =================================================================
	// 4. 範囲バリデーションのテスト
	// =================================================================
	t.Run("LayoutTree_Threshold_OutOfRange", func(t *testing.T) {
		figmaLayout := `[{"id":"1","name":"a","x":0,"y":0,"w":100,"h":100}]`
		webLayout := `[{"selector":".a","x":0,"y":0,"w":100,"h":100}]`

		for _, val := range []float64{-0.1, 1.5, 100.0} {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":         "layout_tree",
						"figma_layout": figmaLayout,
						"web_layout":   webLayout,
						"threshold":    val,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if !res.IsError {
				t.Errorf("Expected error for layout_tree threshold=%.1f, got content=%v", val, res.Content[0].(mcp.TextContent).Text)
			}
		}
	})

	t.Run("LayoutTree_PassRate_OutOfRange", func(t *testing.T) {
		figmaLayout := `[{"id":"1","name":"a","x":0,"y":0,"w":100,"h":100}]`
		webLayout := `[{"selector":".a","x":0,"y":0,"w":100,"h":100}]`

		for _, val := range []float64{-1.0, 101.0, 200.0} {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":         "layout_tree",
						"figma_layout": figmaLayout,
						"web_layout":   webLayout,
						"pass_rate":    val,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if !res.IsError {
				t.Errorf("Expected error for layout_tree pass_rate=%.1f, got content=%v", val, res.Content[0].(mcp.TextContent).Text)
			}
		}
	})

	t.Run("Perceptual_Threshold_Above100", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathC,
					"threshold":    150.0,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if !res.IsError {
			t.Errorf("Expected error for perceptual threshold=150.0, got content=%v", res.Content[0].(mcp.TextContent).Text)
		}
	})

	t.Run("Strict_Threshold_OutOfRange", func(t *testing.T) {
		for _, val := range []float64{-0.1, 1.5, 50.0} {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":         "strict",
						"image_path_a": pathA,
						"image_path_b": pathC,
						"threshold":    val,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if !res.IsError {
				t.Errorf("Expected error for strict threshold=%.1f, got content=%v", val, res.Content[0].(mcp.TextContent).Text)
			}
		}
	})

	t.Run("Strict_EffectiveThreshold_Echo", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "strict",
					"image_path_a": pathA,
					"image_path_b": pathC,
					"threshold":    0.25,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if v, ok := result["effective_threshold"].(float64); !ok || v != 0.25 {
			t.Errorf("Expected effective_threshold=0.25 in response, got %v", result["effective_threshold"])
		}
	})

	t.Run("StrictMode_MismatchColor", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "strict",
					"image_path_a": pathA,
					"image_path_b": pathC, // 色の差があるため不一致
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "mismatch" {
			t.Errorf("Expected strict mode mismatch, got status=%v", result["status"])
		}
		if diffImage, ok := result["diff_image"].(string); !ok || diffImage == "" {
			t.Errorf("Expected non-empty diff_image, got %v", result["diff_image"])
		}
		// details は全モードで文字列配列に統一されている (strict も配列で返す)
		details, ok := result["details"].([]interface{})
		if !ok {
			t.Fatalf("Expected details array in strict result, got %v", result["details"])
		}
		if len(details) != 1 {
			t.Errorf("Expected 1 detail entry in strict result, got %d: %v", len(details), details)
		}
		// 数値一致率が差分ピクセル数と整合することの検証
		totalPixels, _ := result["total_pixels"].(float64)
		diffPixels, _ := result["diff_pixels"].(float64)
		expectedRate := (totalPixels - diffPixels) / totalPixels * 100.0
		if v, ok := result["match_rate_value"].(float64); !ok || v != expectedRate {
			t.Errorf("Expected match_rate_value=%v (consistent with diff/total pixels), got %v", expectedRate, result["match_rate_value"])
		}
		// 色差許容は常に判定に使うため、未指定時もデフォルト 0.1 を echo する
		if v, ok := result["effective_threshold"].(float64); !ok || v != 0.1 {
			t.Errorf("Expected default effective_threshold=0.1 in response, got %v", result["effective_threshold"])
		}
		// 実効パラメータ max_diff_pixels も未指定時の既定値 0 が応答に含まれることの検証
		if got := result["max_diff_pixels"]; got != float64(0) {
			t.Errorf("Expected max_diff_pixels=0 (default), got %v", got)
		}
		if result["image_size"] != "200x200" {
			t.Errorf("Expected image_size=200x200, got %v", result["image_size"])
		}
	})

	// generate_diff=false で差分画像 (base64) を返さない
	t.Run("StrictMode_GenerateDiff_False", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":          "strict",
					"image_path_a":  pathA,
					"image_path_b":  pathC,
					"generate_diff": false,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "mismatch" {
			t.Errorf("Expected strict mode mismatch, got status=%v", result["status"])
		}
		if diffImage, ok := result["diff_image"].(string); !ok || diffImage != "" {
			t.Errorf("Expected empty diff_image with generate_diff=false, got %v", result["diff_image"])
		}
		if _, ok := result["diff_regions"]; ok {
			t.Errorf("Expected no diff_regions with generate_diff=false, got %v", result["diff_regions"])
		}
	})

	// Issue #187: pathE (左上 100x100 黒) vs pathF (全面白) の bounding box
	t.Run("StrictMode_DiffRegions", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "strict",
					"image_path_a": pathE,
					"image_path_b": pathF,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		raw, ok := result["diff_regions"].([]interface{})
		if !ok || len(raw) != 1 {
			t.Fatalf("Expected 1 diff_region for pathE vs pathF, got %v", result["diff_regions"])
		}
		region, ok := raw[0].(map[string]interface{})
		if !ok {
			t.Fatalf("Expected diff_regions[0] object, got %T", raw[0])
		}
		if region["x"] != float64(0) || region["y"] != float64(0) || region["w"] != float64(100) || region["h"] != float64(100) {
			t.Errorf("Expected top-left 100x100 region, got %v", region)
		}
		if region["diff_pixels"] != float64(10000) {
			t.Errorf("Expected diff_pixels=10000, got %v", region["diff_pixels"])
		}

		reqOff := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":          "strict",
					"image_path_a":  pathE,
					"image_path_b":  pathF,
					"generate_diff": false,
				},
			},
		}
		resOff, err := compareDesignHandler(context.Background(), reqOff)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultOff map[string]interface{}
		json.Unmarshal([]byte(resOff.Content[0].(mcp.TextContent).Text), &resultOff)
		if _, ok := resultOff["diff_regions"]; ok {
			t.Errorf("Expected no diff_regions with generate_diff=false, got %v", resultOff["diff_regions"])
		}
	})

	// diff_on_mismatch=true: 同一画像の success では空、色差 mismatch では生成する
	t.Run("StrictMode_DiffOnMismatch", func(t *testing.T) {
		reqMatch := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":             "strict",
					"image_path_a":     pathA,
					"image_path_b":     pathA,
					"diff_on_mismatch": true,
				},
			},
		}
		resMatch, err := compareDesignHandler(context.Background(), reqMatch)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultMatch map[string]interface{}
		json.Unmarshal([]byte(resMatch.Content[0].(mcp.TextContent).Text), &resultMatch)
		if resultMatch["status"] != "success" {
			t.Fatalf("Expected strict success for identical images, got status=%v", resultMatch["status"])
		}
		if diffImage, ok := resultMatch["diff_image"].(string); !ok || diffImage != "" {
			t.Errorf("Expected empty diff_image on success with diff_on_mismatch=true, got %v", resultMatch["diff_image"])
		}

		reqMismatch := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":             "strict",
					"image_path_a":     pathA,
					"image_path_b":     pathC,
					"diff_on_mismatch": true,
				},
			},
		}
		resMismatch, err := compareDesignHandler(context.Background(), reqMismatch)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultMismatch map[string]interface{}
		json.Unmarshal([]byte(resMismatch.Content[0].(mcp.TextContent).Text), &resultMismatch)
		if resultMismatch["status"] != "mismatch" {
			t.Errorf("Expected strict mismatch, got status=%v", resultMismatch["status"])
		}
		if diffImage, ok := resultMismatch["diff_image"].(string); !ok || diffImage == "" {
			t.Errorf("Expected non-empty diff_image on mismatch with diff_on_mismatch=true, got %v", resultMismatch["diff_image"])
		}

		reqBoth := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":             "strict",
					"image_path_a":     pathA,
					"image_path_b":     pathC,
					"generate_diff":    false,
					"diff_on_mismatch": true,
				},
			},
		}
		resBoth, err := compareDesignHandler(context.Background(), reqBoth)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultBoth map[string]interface{}
		json.Unmarshal([]byte(resBoth.Content[0].(mcp.TextContent).Text), &resultBoth)
		if resultBoth["status"] != "mismatch" {
			t.Errorf("Expected strict mismatch, got status=%v", resultBoth["status"])
		}
		if diffImage, ok := resultBoth["diff_image"].(string); !ok || diffImage != "" {
			t.Errorf("Expected empty diff_image with generate_diff=false even on mismatch, got %v", resultBoth["diff_image"])
		}
	})

	// diff_image_content=true: 差分 PNG を MCP image コンテンツでも返す (Issue #219)
	t.Run("Strict_DiffImageContent", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":               "strict",
					"image_path_a":       pathA,
					"image_path_b":       pathC,
					"diff_image_content": true,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if res.IsError {
			t.Fatalf("Expected success, got error %v", res.Content[0].(mcp.TextContent).Text)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "mismatch" {
			t.Fatalf("Expected strict mismatch, got status=%v", result["status"])
		}
		assertDiffImageContent(t, res, result)
	})

	// max_diff_pixels: 差分ピクセル数が許容値以下なら success と判定する
	t.Run("StrictMode_MaxDiffPixels", func(t *testing.T) {
		baseArgs := map[string]any{
			"mode":         "strict",
			"image_path_a": pathA,
			"image_path_b": pathC, // 色の差があるため不一致
		}

		// まずデフォルト (max_diff_pixels=0) で差分ピクセル数を取得する
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: baseArgs}}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "mismatch" {
			t.Errorf("Expected mismatch with default max_diff_pixels=0, got status=%v", result["status"])
		}
		diffPixels := int(result["diff_pixels"].(float64))
		if diffPixels <= 0 {
			t.Fatalf("Expected positive diff_pixels, got %d", diffPixels)
		}

		// diff_pixels ちょうどを許容すると success (diffPixels > maxDiffPixels のときだけ mismatch)
		argsOK := map[string]any{
			"mode": "strict", "image_path_a": pathA, "image_path_b": pathC,
			"max_diff_pixels": float64(diffPixels),
		}
		resOK, err := compareDesignHandler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: argsOK}})
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultOK map[string]interface{}
		json.Unmarshal([]byte(resOK.Content[0].(mcp.TextContent).Text), &resultOK)
		if resultOK["status"] != "success" {
			t.Errorf("Expected success with max_diff_pixels=%d, got status=%v", diffPixels, resultOK["status"])
		}
		// 指定した max_diff_pixels が実効値としてそのまま応答に echo されることの検証
		if got := resultOK["max_diff_pixels"]; got != float64(diffPixels) {
			t.Errorf("Expected max_diff_pixels=%d, got %v", diffPixels, got)
		}

		// 許容数を 1 でも下回ると mismatch のまま
		argsNG := map[string]any{
			"mode": "strict", "image_path_a": pathA, "image_path_b": pathC,
			"max_diff_pixels": float64(diffPixels - 1),
		}
		resNG, err := compareDesignHandler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: argsNG}})
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultNG map[string]interface{}
		json.Unmarshal([]byte(resNG.Content[0].(mcp.TextContent).Text), &resultNG)
		if resultNG["status"] != "mismatch" {
			t.Errorf("Expected mismatch with max_diff_pixels=%d, got status=%v", diffPixels-1, resultNG["status"])
		}
		if got := resultNG["max_diff_pixels"]; got != float64(diffPixels-1) {
			t.Errorf("Expected max_diff_pixels=%d, got %v", diffPixels-1, got)
		}
	})

	t.Run("StrictMode_MaxDiffPixels_Negative", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":            "strict",
					"image_path_a":    pathA,
					"image_path_b":    pathC,
					"max_diff_pixels": -1.0,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if !res.IsError {
			t.Errorf("Expected error for negative max_diff_pixels, got content=%v", res.Content[0].(mcp.TextContent).Text)
		}
	})

	// min_match: 一致率(%)で合否判定する (strict モード)。
	// max_diff_pixels は total_pixels が分からないと逆算できないため、
	// 「一致率○%以上で合格」を割合で直接指定できるようにする。
	t.Run("StrictMode_MinMatch", func(t *testing.T) {
		baseArgs := map[string]any{
			"mode":         "strict",
			"image_path_a": pathE, // 左上100x100の黒矩形 (pathF との比較で一致率75%)
			"image_path_b": pathF,
		}

		// まず素の状態 (max_diff_pixels=0) で一致率と差分ピクセル数を取得する
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: baseArgs}}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "mismatch" {
			t.Errorf("Expected mismatch with default max_diff_pixels=0, got status=%v", result["status"])
		}
		matchRate, ok := result["match_rate_value"].(float64)
		if !ok {
			t.Fatalf("Expected numeric match_rate_value, got %v", result["match_rate_value"])
		}
		if matchRate <= 0.0 || matchRate >= 100.0 {
			t.Fatalf("Expected partial match rate between 0 and 100, got %v", matchRate)
		}
		diffPixels := int(result["diff_pixels"].(float64))
		// min_match 未指定時は判定に使っていないため、応答に echo されない
		if _, ok := result["min_match"]; ok {
			t.Errorf("Expected no min_match echo when unspecified, got %v", result["min_match"])
		}

		// min_match が一致率未満なら success (max_diff_pixels も併せて緩める)
		argsOK := map[string]any{
			"mode": "strict", "image_path_a": pathE, "image_path_b": pathF,
			"max_diff_pixels": float64(diffPixels),
			"min_match":       matchRate - 1.0,
		}
		resOK, err := compareDesignHandler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: argsOK}})
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultOK map[string]interface{}
		json.Unmarshal([]byte(resOK.Content[0].(mcp.TextContent).Text), &resultOK)
		if resultOK["status"] != "success" {
			t.Errorf("Expected success with min_match=%.2f, got status=%v", matchRate-1.0, resultOK["status"])
		}
		// 指定時は実効値が応答に echo される
		if v, ok := resultOK["min_match"].(float64); !ok || v != matchRate-1.0 {
			t.Errorf("Expected min_match echo=%.2f, got %v", matchRate-1.0, resultOK["min_match"])
		}

		// min_match が一致率ちょうどなら success (matchRate < min_match のときだけ mismatch)
		argsEqual := map[string]any{
			"mode": "strict", "image_path_a": pathE, "image_path_b": pathF,
			"max_diff_pixels": float64(diffPixels),
			"min_match":       matchRate,
		}
		resEqual, err := compareDesignHandler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: argsEqual}})
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultEqual map[string]interface{}
		json.Unmarshal([]byte(resEqual.Content[0].(mcp.TextContent).Text), &resultEqual)
		if resultEqual["status"] != "success" {
			t.Errorf("Expected success with min_match equal to match rate (%.2f), got status=%v", matchRate, resultEqual["status"])
		}

		// min_match が一致率を超えていれば max_diff_pixels を満たしていても mismatch
		argsNG := map[string]any{
			"mode": "strict", "image_path_a": pathE, "image_path_b": pathF,
			"max_diff_pixels": float64(diffPixels),
			"min_match":       matchRate + 1.0,
		}
		resNG, err := compareDesignHandler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: argsNG}})
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultNG map[string]interface{}
		json.Unmarshal([]byte(resNG.Content[0].(mcp.TextContent).Text), &resultNG)
		if resultNG["status"] != "mismatch" {
			t.Errorf("Expected mismatch with min_match=%.2f, got status=%v", matchRate+1.0, resultNG["status"])
		}
		if v, ok := resultNG["min_match"].(float64); !ok || v != matchRate+1.0 {
			t.Errorf("Expected min_match echo=%.2f, got %v", matchRate+1.0, resultNG["min_match"])
		}

		// min_match を満たしていても max_diff_pixels (デフォルト 0) を超過すれば mismatch
		argsOnly := map[string]any{
			"mode": "strict", "image_path_a": pathE, "image_path_b": pathF,
			"min_match": matchRate - 1.0,
		}
		resOnly, err := compareDesignHandler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: argsOnly}})
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultOnly map[string]interface{}
		json.Unmarshal([]byte(resOnly.Content[0].(mcp.TextContent).Text), &resultOnly)
		if resultOnly["status"] != "mismatch" {
			t.Errorf("Expected mismatch when max_diff_pixels is exceeded even if min_match is met, got status=%v", resultOnly["status"])
		}
	})

	t.Run("StrictMode_MinMatch_DisplayRounding", func(t *testing.T) {
		// 200x100=20,000px・差分 401px → 一致率 97.995% → 表示 98.00%、生値は 98 未満
		imgWhite := generateSolidImage(200, 100, color.White)
		imgDiff := generateSolidImage(200, 100, color.White)
		rgba, ok := imgDiff.(*image.RGBA)
		if !ok {
			t.Fatalf("expected *image.RGBA from generateSolidImage")
		}
		for i := 0; i < 401; i++ {
			rgba.Set(i%200, i/200, color.Black)
		}
		pathWhite := saveTempImage(t, tmpDir, "strict-round-white.png", imgWhite)
		pathDiff := saveTempImage(t, tmpDir, "strict-round-diff.png", imgDiff)

		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":            "strict",
					"image_path_a":    pathWhite,
					"image_path_b":    pathDiff,
					"min_match":       98.0,
					"max_diff_pixels": 401.0,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["diff_pixels"] != float64(401) || result["total_pixels"] != float64(20000) {
			t.Fatalf("Expected 401/20000 diff pixels, got diff=%v total=%v", result["diff_pixels"], result["total_pixels"])
		}
		wantRaw := (20000.0 - 401.0) / 20000.0 * 100.0
		if got := result["match_rate_value"]; got != wantRaw {
			t.Errorf("Expected raw match_rate_value=%v, got %v", wantRaw, got)
		}
		if result["match_rate"] != "98.00%" {
			t.Errorf("Expected displayed match_rate=98.00%%, got %v", result["match_rate"])
		}
		if result["status"] != "success" {
			t.Errorf("Expected success when displayed match_rate equals min_match=98, got status=%v value=%v", result["status"], result["match_rate_value"])
		}
	})

	// min_match のみ指定し max_diff_pixels を省略すると既定 0 が判定を支配する。
	// status / match_rate は変えず、warnings で呼び出し側に気付かせる (Issue #206)
	t.Run("StrictMode_MinMatch_DefaultMaxDiffPixels_Warning", func(t *testing.T) {
		const wantWarning = "max_diff_pixels defaults to 0; any differing pixel causes mismatch regardless of min_match (set max_diff_pixels to allow some differences)"

		reqWarn := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "strict",
					"image_path_a": pathE,
					"image_path_b": pathF,
					"min_match":    50.0,
				},
			},
		}
		resWarn, err := compareDesignHandler(context.Background(), reqWarn)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultWarn map[string]interface{}
		json.Unmarshal([]byte(resWarn.Content[0].(mcp.TextContent).Text), &resultWarn)
		if resultWarn["status"] != "mismatch" {
			t.Errorf("Expected mismatch to be unchanged, got status=%v", resultWarn["status"])
		}
		gotWarnings, ok := resultWarn["warnings"].([]interface{})
		if !ok || len(gotWarnings) != 1 || gotWarnings[0] != wantWarning {
			t.Errorf("Expected warnings=[%q], got %v", wantWarning, resultWarn["warnings"])
		}

		reqBoth := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":            "strict",
					"image_path_a":    pathE,
					"image_path_b":    pathF,
					"min_match":       50.0,
					"max_diff_pixels": 100000.0,
				},
			},
		}
		resBoth, err := compareDesignHandler(context.Background(), reqBoth)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultBoth map[string]interface{}
		json.Unmarshal([]byte(resBoth.Content[0].(mcp.TextContent).Text), &resultBoth)
		if _, ok := resultBoth["warnings"]; ok {
			t.Errorf("Expected no warnings when max_diff_pixels is set, got %v", resultBoth["warnings"])
		}

		reqNone := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "strict",
					"image_path_a": pathE,
					"image_path_b": pathF,
				},
			},
		}
		resNone, err := compareDesignHandler(context.Background(), reqNone)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultNone map[string]interface{}
		json.Unmarshal([]byte(resNone.Content[0].(mcp.TextContent).Text), &resultNone)
		if _, ok := resultNone["warnings"]; ok {
			t.Errorf("Expected no warnings when min_match is omitted, got %v", resultNone["warnings"])
		}
	})

	// min_match の範囲バリデーション: 0.0–100.0 外の値はエラーになる
	t.Run("StrictMode_MinMatch_OutOfRange", func(t *testing.T) {
		for _, val := range []float64{-1.0, 101.0} {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":         "strict",
						"image_path_a": pathE,
						"image_path_b": pathF,
						"min_match":    val,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if !res.IsError {
				t.Errorf("Expected error for strict min_match=%.1f, got content=%v", val, res.Content[0].(mcp.TextContent).Text)
			}
		}
	})

	// include_aa: デフォルト/false は AA 境界を差分カウントから除外し、
	// true のときだけ pixelmatch.IncludeAntiAlias で差分が増える (Issue #239)。
	t.Run("StrictMode_IncludeAA", func(t *testing.T) {
		const w, h = 12, 12
		black := color.RGBA{0, 0, 0, 255}
		white := color.RGBA{255, 255, 255, 255}
		grayDark := color.RGBA{64, 64, 64, 255}
		grayLight := color.RGBA{192, 192, 192, 255}

		imgA := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(imgA, imgA.Bounds(), &image.Uniform{white}, image.Point{}, draw.Src)
		draw.Draw(imgA, image.Rect(0, 0, 5, h), &image.Uniform{black}, image.Point{}, draw.Src)
		draw.Draw(imgA, image.Rect(5, 0, 6, h), &image.Uniform{grayDark}, image.Point{}, draw.Src)

		imgB := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(imgB, imgB.Bounds(), &image.Uniform{white}, image.Point{}, draw.Src)
		draw.Draw(imgB, image.Rect(0, 0, 5, h), &image.Uniform{black}, image.Point{}, draw.Src)
		draw.Draw(imgB, image.Rect(5, 0, 6, h), &image.Uniform{grayLight}, image.Point{}, draw.Src)

		pathAA := saveTempImage(t, tmpDir, "imageAA_a.png", imgA)
		pathAB := saveTempImage(t, tmpDir, "imageAA_b.png", imgB)

		call := func(args map[string]any) map[string]interface{} {
			t.Helper()
			req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if res.IsError {
				t.Fatalf("unexpected error: %v", res.Content[0].(mcp.TextContent).Text)
			}
			var result map[string]interface{}
			json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
			return result
		}

		base := map[string]any{
			"mode":          "strict",
			"image_path_a":  pathAA,
			"image_path_b":  pathAB,
			"generate_diff": false,
		}
		omitted := call(base)
		withFalse := call(map[string]any{
			"mode": "strict", "image_path_a": pathAA, "image_path_b": pathAB,
			"generate_diff": false, "include_aa": false,
		})
		withTrue := call(map[string]any{
			"mode": "strict", "image_path_a": pathAA, "image_path_b": pathAB,
			"generate_diff": false, "include_aa": true,
		})

		omittedDiff := omitted["diff_pixels"].(float64)
		falseDiff := withFalse["diff_pixels"].(float64)
		trueDiff := withTrue["diff_pixels"].(float64)
		if omittedDiff != falseDiff {
			t.Errorf("unspecified include_aa should match include_aa=false: omitted=%v false=%v", omittedDiff, falseDiff)
		}
		if trueDiff <= omittedDiff {
			t.Errorf("include_aa=true should increase diff_pixels (omitted=%v, true=%v)", omittedDiff, trueDiff)
		}
	})

	// サイズの異なる画像ペアは白埋めで吸収せず、エラーとして明示的に報告する
	// (白埋め領域が一致として数えられ一致率が水増しされるのを防ぐ)。
	// エラーメッセージには対処ヒント (同一ビューポート・DPR で撮り直す /
	// perceptual モードへの代替) が含まれることも検証する (Issue #143)
	t.Run("StrictMode_SizeMismatch_Error", func(t *testing.T) {
		imgSmall := generateSolidImage(100, 100, color.White)
		pathSmall := saveTempImage(t, tmpDir, "imageSmall.png", imgSmall)

		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "strict",
					"image_path_a": pathA,     // 200x200
					"image_path_b": pathSmall, // 100x100
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if !res.IsError {
			t.Errorf("Expected error for strict mode with different image sizes, got content=%v", res.Content[0].(mcp.TextContent).Text)
		}
		msg := res.Content[0].(mcp.TextContent).Text
		if !strings.Contains(msg, "size mismatch") {
			t.Errorf("Expected size mismatch error message, got %v", msg)
		}
		// 対処ヒント: 同一ビューポートサイズ・DPR での撮り直しと、
		// サイズ違い画像に対する perceptual モードへの案内が含まれること (Issue #143)
		if !strings.Contains(msg, "same viewport size and device pixel ratio") {
			t.Errorf("Expected same viewport/DPR hint in size mismatch message, got %v", msg)
		}
		if !strings.Contains(msg, "perceptual mode") {
			t.Errorf("Expected perceptual mode hint in size mismatch message, got %v", msg)
		}
		// 意図的なサイズ違い (DPR 差など) への誘導には、perceptual モードが両画像を
		// 16x16 に縮小してマクロレイアウトを比較する旨の説明が含まれること (Issue #155)
		if !strings.Contains(msg, "downscaling both to 16x16") {
			t.Errorf("Expected 16x16 downscaling explanation in size mismatch message, got %v", msg)
		}
	})

	// =================================================================
	// 5. match_rate_value (数値フィールド) のテスト
	// =================================================================
	// 各モードの応答に、表示用 match_rate (文字列) に加えて 0〜100 の実数
	// match_rate_value が含まれ、pass_rate 等の閾値と直接大小比較できることを確認する。
	t.Run("MatchRateValue_Numeric", func(t *testing.T) {
		// layout_tree: nav の位置がズレている (一致率 66.67%)
		figmaLayout := `[
			{"id": "1", "name": "header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"id": "2", "name": "logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "1"},
			{"id": "3", "name": "nav", "x": 600, "y": 10, "w": 380, "h": 80, "parent": "1"}
		]`
		webLayoutNavShifted := `[
			{"selector": "#header", "x": 0, "y": 0, "w": 1000, "h": 100},
			{"selector": ".logo", "x": 10, "y": 10, "w": 100, "h": 80, "parent": "#header"},
			{"selector": ".nav", "x": 200, "y": 10, "w": 380, "h": 80, "parent": "#header"}
		]`

		// pass_rate=50 なら一致率 (66.67%) 以上なので success
		reqTreePass := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayoutNavShifted,
					"threshold":    0.15,
					"pass_rate":    50.0,
				},
			},
		}
		resTreePass, err := compareDesignHandler(context.Background(), reqTreePass)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultTreePass map[string]interface{}
		json.Unmarshal([]byte(resTreePass.Content[0].(mcp.TextContent).Text), &resultTreePass)
		if resultTreePass["status"] != "success" {
			t.Errorf("Expected layout_tree success with pass_rate=50, got status=%v", resultTreePass["status"])
		}
		treePassValue, ok := resultTreePass["match_rate_value"].(float64)
		if !ok {
			t.Fatalf("Expected numeric match_rate_value for layout_tree, got %T: %v", resultTreePass["match_rate_value"], resultTreePass["match_rate_value"])
		}
		if treePassValue < 0.0 || treePassValue > 100.0 {
			t.Errorf("Expected match_rate_value within 0.0-100.0, got %v", treePassValue)
		}
		// pass_rate (50.0) と直接大小比較でき、status と整合する
		if treePassValue < 50.0 {
			t.Errorf("Expected match_rate_value >= pass_rate=50.0 on success, got %v", treePassValue)
		}
		if _, ok := resultTreePass["match_rate"].(string); !ok {
			t.Errorf("Expected display match_rate to remain a string, got %T", resultTreePass["match_rate"])
		}

		// pass_rate=70 なら一致率 (66.67%) 未満なので mismatch
		reqTreeFail := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "layout_tree",
					"figma_layout": figmaLayout,
					"web_layout":   webLayoutNavShifted,
					"threshold":    0.15,
					"pass_rate":    70.0,
				},
			},
		}
		resTreeFail, err := compareDesignHandler(context.Background(), reqTreeFail)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultTreeFail map[string]interface{}
		json.Unmarshal([]byte(resTreeFail.Content[0].(mcp.TextContent).Text), &resultTreeFail)
		if resultTreeFail["status"] != "mismatch" {
			t.Errorf("Expected layout_tree mismatch with pass_rate=70, got status=%v", resultTreeFail["status"])
		}
		treeFailValue, ok := resultTreeFail["match_rate_value"].(float64)
		if !ok {
			t.Fatalf("Expected numeric match_rate_value for layout_tree, got %T: %v", resultTreeFail["match_rate_value"], resultTreeFail["match_rate_value"])
		}
		// pass_rate (70.0) と直接大小比較でき、status と整合する
		if treeFailValue >= 70.0 {
			t.Errorf("Expected match_rate_value < pass_rate=70.0 on mismatch, got %v", treeFailValue)
		}

		// perceptual: 微小な輝度差なら default min_match (98.0) 以上で success
		reqPerceptualPass := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathC,
				},
			},
		}
		resPerceptualPass, err := compareDesignHandler(context.Background(), reqPerceptualPass)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultPerceptualPass map[string]interface{}
		json.Unmarshal([]byte(resPerceptualPass.Content[0].(mcp.TextContent).Text), &resultPerceptualPass)
		if resultPerceptualPass["status"] != "success" {
			t.Errorf("Expected perceptual success with default min_match, got status=%v", resultPerceptualPass["status"])
		}
		perceptualPassValue, ok := resultPerceptualPass["match_rate_value"].(float64)
		if !ok {
			t.Fatalf("Expected numeric match_rate_value for perceptual, got %T: %v", resultPerceptualPass["match_rate_value"], resultPerceptualPass["match_rate_value"])
		}
		// min_match (デフォルト 98.0) と直接大小比較でき、status と整合する
		if perceptualPassValue < 98.0 {
			t.Errorf("Expected match_rate_value >= 98.0 (default min_match) on success, got %v", perceptualPassValue)
		}
		if _, ok := resultPerceptualPass["match_rate"].(string); !ok {
			t.Errorf("Expected display match_rate to remain a string, got %T", resultPerceptualPass["match_rate"])
		}

		// perceptual: 配置が異なる (左右 vs 上下) ため mismatch
		reqPerceptualFail := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathD, // 配置が異なる (左右 vs 上下)
				},
			},
		}
		resPerceptualFail, err := compareDesignHandler(context.Background(), reqPerceptualFail)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultPerceptualFail map[string]interface{}
		json.Unmarshal([]byte(resPerceptualFail.Content[0].(mcp.TextContent).Text), &resultPerceptualFail)
		if resultPerceptualFail["status"] != "mismatch" {
			t.Errorf("Expected perceptual mismatch, got status=%v", resultPerceptualFail["status"])
		}
		perceptualFailValue, ok := resultPerceptualFail["match_rate_value"].(float64)
		if !ok {
			t.Fatalf("Expected numeric match_rate_value for perceptual, got %T: %v", resultPerceptualFail["match_rate_value"], resultPerceptualFail["match_rate_value"])
		}
		// min_match (デフォルト 98.0) と直接大小比較でき、status と整合する
		if perceptualFailValue >= 98.0 {
			t.Errorf("Expected match_rate_value < 98.0 (default min_match) on mismatch, got %v", perceptualFailValue)
		}
		if _, ok := resultPerceptualFail["match_rate"].(string); !ok {
			t.Errorf("Expected display match_rate to remain a string, got %T", resultPerceptualFail["match_rate"])
		}

		// strict: 全画素に色差があるため diff_pixels > 0、一致率は 100% 未満
		reqStrict := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "strict",
					"image_path_a": pathA,
					"image_path_b": pathC, // 色の差があるため不一致
				},
			},
		}
		resStrict, err := compareDesignHandler(context.Background(), reqStrict)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultStrict map[string]interface{}
		json.Unmarshal([]byte(resStrict.Content[0].(mcp.TextContent).Text), &resultStrict)
		if resultStrict["status"] != "mismatch" {
			t.Errorf("Expected strict mismatch, got status=%v", resultStrict["status"])
		}
		strictValue, ok := resultStrict["match_rate_value"].(float64)
		if !ok {
			t.Fatalf("Expected numeric match_rate_value for strict, got %T: %v", resultStrict["match_rate_value"], resultStrict["match_rate_value"])
		}
		totalPixels, ok := resultStrict["total_pixels"].(float64)
		if !ok {
			t.Fatalf("Expected numeric total_pixels, got %T", resultStrict["total_pixels"])
		}
		diffPixels, ok := resultStrict["diff_pixels"].(float64)
		if !ok {
			t.Fatalf("Expected numeric diff_pixels, got %T", resultStrict["diff_pixels"])
		}
		if diffPixels <= 0 {
			t.Fatalf("Expected positive diff_pixels, got %v", diffPixels)
		}
		// total_pixels / diff_pixels から算出される実数値と一致すること (文字列をパースしていない)
		expectedStrict := (totalPixels - diffPixels) / totalPixels * 100.0
		if strictValue != expectedStrict {
			t.Errorf("Expected match_rate_value=%v from total/diff pixels, got %v", expectedStrict, strictValue)
		}
		if strictValue >= 100.0 {
			t.Errorf("Expected match_rate_value < 100.0 when diff_pixels > 0, got %v", strictValue)
		}
		if _, ok := resultStrict["match_rate"].(string); !ok {
			t.Errorf("Expected display match_rate to remain a string, got %T", resultStrict["match_rate"])
		}
		if resultStrict["image_size"] != "200x200" {
			t.Errorf("Expected image_size=200x200, got %v", resultStrict["image_size"])
		}
	})

	// ignore_region: 既知の差分領域をマスクして比較する
	t.Run("Strict_IgnoreRegion", func(t *testing.T) {
		// 指定なし: 左上100x100=10000pxの差分で mismatch
		reqNoRegion := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "strict",
					"image_path_a": pathE,
					"image_path_b": pathF,
				},
			},
		}
		resNoRegion, err := compareDesignHandler(context.Background(), reqNoRegion)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultNoRegion map[string]interface{}
		json.Unmarshal([]byte(resNoRegion.Content[0].(mcp.TextContent).Text), &resultNoRegion)
		if resultNoRegion["status"] != "mismatch" {
			t.Errorf("Expected mismatch without ignore_region, got status=%v", resultNoRegion["status"])
		}
		if got := resultNoRegion["diff_pixels"]; got == float64(0) {
			t.Errorf("Expected positive diff_pixels without ignore_region, got %v", got)
		}
		if got := resultNoRegion["ignored_regions"]; got != float64(0) {
			t.Errorf("Expected ignored_regions=0 when ignore_region is omitted, got %v", got)
		}

		// 指定あり: 差分領域をマスクすると diff_pixels=0 で success
		reqRegion := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":          "strict",
					"image_path_a":  pathE,
					"image_path_b":  pathF,
					"ignore_region": "0,0,100,100",
				},
			},
		}
		resRegion, err := compareDesignHandler(context.Background(), reqRegion)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultRegion map[string]interface{}
		json.Unmarshal([]byte(resRegion.Content[0].(mcp.TextContent).Text), &resultRegion)
		if resultRegion["status"] != "success" || resultRegion["match_rate"] != "100.00%" {
			t.Errorf("Expected success and 100%% match with ignore_region, got status=%v, rate=%v", resultRegion["status"], resultRegion["match_rate"])
		}
		if got := resultRegion["diff_pixels"]; got != float64(0) {
			t.Errorf("Expected diff_pixels=0 with ignore_region, got %v", got)
		}
		if got := resultRegion["ignored_regions"]; got != float64(1) {
			t.Errorf("Expected ignored_regions=1 with one ignore_region, got %v", got)
		}
		// 範囲内の ignore_region では警告フィールド (out_of_bounds_regions) は出ない
		if _, ok := resultRegion["out_of_bounds_regions"]; ok {
			t.Errorf("Expected no out_of_bounds_regions for in-bounds ignore_region, got %v", resultRegion["out_of_bounds_regions"])
		}

		// 画像範囲外の ignore_region は何もマスクされず差分が残るため、座標ミスが
		// 分かるよう out_of_bounds_regions として応答で警告される (Issue #128)
		reqOutOfBounds := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":          "strict",
					"image_path_a":  pathE,
					"image_path_b":  pathF,
					"ignore_region": "500,500,100,100",
				},
			},
		}
		resOutOfBounds, err := compareDesignHandler(context.Background(), reqOutOfBounds)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		var resultOutOfBounds map[string]interface{}
		json.Unmarshal([]byte(resOutOfBounds.Content[0].(mcp.TextContent).Text), &resultOutOfBounds)
		if resultOutOfBounds["status"] != "mismatch" {
			t.Errorf("Expected mismatch with out-of-bounds ignore_region (nothing masked), got status=%v", resultOutOfBounds["status"])
		}
		if got := resultOutOfBounds["diff_pixels"]; got == float64(0) {
			t.Errorf("Expected positive diff_pixels with out-of-bounds ignore_region (nothing masked), got %v", got)
		}
		gotRegions, ok := resultOutOfBounds["out_of_bounds_regions"].([]interface{})
		if !ok || len(gotRegions) != 1 || gotRegions[0] != "500,500,100,100" {
			t.Errorf("Expected out_of_bounds_regions=[500,500,100,100], got %v", resultOutOfBounds["out_of_bounds_regions"])
		}
		if got := resultOutOfBounds["ignored_regions"]; got != float64(1) {
			t.Errorf("Expected ignored_regions=1 for out-of-bounds region, got %v", got)
		}
	})

	// 不正な ignore_region 指定はエラーになる
	t.Run("IgnoreRegion_InvalidFormat", func(t *testing.T) {
		for _, region := range []string{
			"10,20,100",
			"a,b,c,d",
			"-1,0,10,10",
			"0,0,0,10",
			"10,20,100,50;bad",
			// x+w が int64 を超えると全面マスクの誤 pass になるため拒否する (Issue #203)
			"4611686018427387904,0,4611686018427387904,10",
			"2147483648,0,1,1",
		} {
			for _, mode := range []string{"perceptual", "strict"} {
				req := mcp.CallToolRequest{
					Params: mcp.CallToolParams{
						Arguments: map[string]any{
							"mode":          mode,
							"image_path_a":  pathE,
							"image_path_b":  pathF,
							"ignore_region": region,
						},
					},
				}
				res, err := compareDesignHandler(context.Background(), req)
				if err != nil {
					t.Fatalf("handler failed: %v", err)
				}
				if !res.IsError {
					t.Errorf("Expected error for ignore_region=%q in %s mode, got content=%v", region, mode, res.Content[0].(mcp.TextContent).Text)
				}
			}
		}
	})

	// 0次元画像は NaN や空応答にならず明示的なエラーになる
	t.Run("ZeroSizeImage_Error", func(t *testing.T) {
		var buf bytes.Buffer
		if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 0, 0))); err != nil {
			t.Skipf("cannot encode a 0x0 PNG on this Go version: %v", err)
		}
		pathZero := filepath.Join(tmpDir, "imageZero.png")
		if err := os.WriteFile(pathZero, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("failed to write zero-size image: %v", err)
		}
		for _, mode := range []string{"perceptual", "strict"} {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":         mode,
						"image_path_a": pathZero,
						"image_path_b": pathZero,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if !res.IsError {
				t.Errorf("Expected error for zero-size image in %s mode, got content=%v", mode, res.Content[0].(mcp.TextContent).Text)
			}
		}
	})

	// =================================================================
	// 6. モード非対応パラメータの検証テスト (Issue #116)
	// =================================================================
	// 各モードで効果を持たないパラメータ (従来は無警告で無視されていた) を
	// 指定した場合に明示的なエラーになることを表駆動で検証する。
	t.Run("ModeUnsupportedParams_Error", func(t *testing.T) {
		figmaLayout := `[{"id":"1","name":"a","x":0,"y":0,"w":100,"h":100}]`
		webLayout := `[{"selector":".a","x":0,"y":0,"w":100,"h":100}]`

		// 各モードで比較を実行できる最小限の有効引数
		validArgs := func(mode string) map[string]any {
			args := map[string]any{"mode": mode}
			switch mode {
			case "layout_tree":
				args["figma_layout"] = figmaLayout
				args["web_layout"] = webLayout
			case "layout_integrity":
				args["web_layout"] = `[{"selector":"#page","x":0,"y":0,"w":100,"h":50}]`
			default: // perceptual / strict
				args["image_path_a"] = pathA
				args["image_path_b"] = pathC
			}
			return args
		}

		// mode × param の組合せ。wantErr=true はモード非対応のため IsError、
		// false は対応パラメータなので比較が実行されてエラーにならない。
		// hint は min_match ↔ pass_rate の混同だけ付与する (Issue #171)。
		cases := []struct {
			mode    string
			param   string
			value   any
			wantErr bool
			hint    string
		}{
			// layout_tree: 画像入力・画像系モード専用パラメータは非対応
			{"layout_tree", "image_path_a", pathA, true, ""},
			{"layout_tree", "image_path_b", pathC, true, ""},
			{"layout_tree", "image_a_base64", "not-base64", true, ""},
			{"layout_tree", "image_b_base64", "not-base64", true, ""},
			{"layout_tree", "max_diff_pixels", 10.0, true, ""},
			{"layout_tree", "include_aa", true, true, ""},
			{"layout_tree", "generate_diff", false, true, ""},
			{"layout_tree", "diff_on_mismatch", true, true, ""},
			{"layout_tree", "diff_image_content", true, true, ""},
			{"layout_tree", "min_match", 90.0, true, " (use 'pass_rate' instead)"},
			// layout_tree: 対応パラメータはエラーにならない
			{"layout_tree", "ignore_nodes", "a", false, ""},
			{"layout_tree", "ignore_region", "0,0,10,10", false, ""},
			{"layout_tree", "count_extra_web", true, false, ""},
			{"layout_tree", "max_details", 2.0, false, ""},
			{"layout_tree", "pass_rate", 90.0, false, ""},
			{"layout_tree", "threshold", 0.15, false, ""},
			// perceptual: レイアウト入力・layout_tree 専用パラメータは非対応
			{"perceptual", "figma_layout", figmaLayout, true, ""},
			{"perceptual", "figma_layout_path", "/tmp/nonexistent.json", true, ""},
			{"perceptual", "web_layout", webLayout, true, ""},
			{"perceptual", "web_layout_path", "/tmp/nonexistent.json", true, ""},
			{"perceptual", "ignore_nodes", "nav", true, ""},
			{"perceptual", "count_extra_web", true, true, ""},
			{"perceptual", "max_details", 2.0, true, ""},
			{"perceptual", "pass_rate", 90.0, true, " (use 'min_match' instead)"},
			{"perceptual", "max_diff_pixels", 10.0, true, ""},
			{"perceptual", "include_aa", true, true, ""},
			// perceptual: 対応パラメータはエラーにならない
			{"perceptual", "min_match", 98.0, false, ""},
			{"perceptual", "threshold", 98.0, false, ""},
			{"perceptual", "ignore_region", "0,0,10,10", false, ""},
			{"perceptual", "generate_diff", false, false, ""},
			{"perceptual", "diff_on_mismatch", true, false, ""},
			{"perceptual", "diff_image_content", true, false, ""},
			// strict: レイアウト入力・layout_tree 専用パラメータは非対応
			{"strict", "figma_layout", figmaLayout, true, ""},
			{"strict", "figma_layout_path", "/tmp/nonexistent.json", true, ""},
			{"strict", "web_layout", webLayout, true, ""},
			{"strict", "web_layout_path", "/tmp/nonexistent.json", true, ""},
			{"strict", "ignore_nodes", "nav", true, ""},
			{"strict", "count_extra_web", true, true, ""},
			{"strict", "max_details", 2.0, true, ""},
			{"strict", "pass_rate", 90.0, true, " (use 'min_match' instead)"},
			// strict: 対応パラメータはエラーにならない
			{"strict", "max_diff_pixels", 100000.0, false, ""},
			{"strict", "include_aa", true, false, ""},
			{"strict", "min_match", 10.0, false, ""},
			{"strict", "threshold", 0.1, false, ""},
			{"strict", "ignore_region", "0,0,10,10", false, ""},
			{"strict", "generate_diff", false, false, ""},
			{"strict", "diff_on_mismatch", true, false, ""},
			{"strict", "diff_image_content", true, false, ""},
			// layout_integrity: Figma/画像/閾値系は非対応。viewport と web 除外は対応
			{"layout_integrity", "figma_layout", figmaLayout, true, ""},
			{"layout_integrity", "image_path_a", pathA, true, ""},
			{"layout_integrity", "min_match", 90.0, true, " (use 'pass_rate' instead)"},
			{"layout_integrity", "pass_rate", 90.0, true, " (use 'min_match' instead)"},
			{"layout_integrity", "threshold", 0.15, true, ""},
			{"layout_integrity", "max_diff_pixels", 10.0, true, ""},
			{"layout_integrity", "include_aa", true, true, ""},
			{"layout_integrity", "generate_diff", false, true, ""},
			{"layout_integrity", "diff_image_content", true, true, ""},
			{"layout_integrity", "count_extra_web", true, true, ""},
			{"layout_integrity", "max_details", 2.0, true, ""},
			{"layout_integrity", "ignore_nodes", "#page", false, ""},
			{"layout_integrity", "ignore_region", "0,0,10,10", false, ""},
			{"layout_integrity", "viewport_width", 768.0, false, ""},
			{"layout_integrity", "viewport_height", 1024.0, false, ""},
			{"layout_integrity", "viewport_preset", "ipad_landscape", false, ""},
			{"layout_tree", "viewport_width", 768.0, true, ""},
			{"perceptual", "viewport_width", 768.0, true, ""},
			{"strict", "viewport_width", 768.0, true, ""},
		}

		for _, c := range cases {
			t.Run(fmt.Sprintf("%s_with_%s", c.mode, c.param), func(t *testing.T) {
				args := validArgs(c.mode)
				args[c.param] = c.value
				req := mcp.CallToolRequest{
					Params: mcp.CallToolParams{Arguments: args},
				}
				res, err := compareDesignHandler(context.Background(), req)
				if err != nil {
					t.Fatalf("handler failed: %v", err)
				}
				if c.wantErr {
					if !res.IsError {
						t.Fatalf("Expected error for unsupported parameter %q in mode %q, got content=%v", c.param, c.mode, res.Content[0].(mcp.TextContent).Text)
					}
					got := res.Content[0].(mcp.TextContent).Text
					wantMsg := fmt.Sprintf("parameter '%s' is not supported in mode '%s'%s", c.param, c.mode, c.hint)
					if !strings.Contains(got, wantMsg) {
						t.Errorf("Expected error message containing %q, got %q", wantMsg, got)
					}
					if c.hint == "" && strings.Contains(got, "instead") {
						t.Errorf("Expected no alternative hint for parameter %q in mode %q, got %q", c.param, c.mode, got)
					}
				} else if res.IsError {
					t.Fatalf("Expected no error for supported parameter %q in mode %q, got content=%v", c.param, c.mode, res.Content[0].(mcp.TextContent).Text)
				}
			})
		}
	})

	// タイポ等の未知キーはサイレント無視せず IsError にする (Issue #194)。
	t.Run("UnknownParam_Error", func(t *testing.T) {
		figmaLayout := `[{"id":"1","name":"a","x":0,"y":0,"w":100,"h":100}]`
		webLayout := `[{"selector":".a","x":0,"y":0,"w":100,"h":100}]`
		cases := []struct {
			mode  string
			param string
			value any
		}{
			{"layout_tree", "passRate", 100.0},
			{"perceptual", "minmatch", 90.0},
			{"strict", "ignor_nodes", "nav"},
		}
		for _, c := range cases {
			t.Run(fmt.Sprintf("%s_with_%s", c.mode, c.param), func(t *testing.T) {
				args := map[string]any{"mode": c.mode}
				switch c.mode {
				case "layout_tree":
					args["figma_layout"] = figmaLayout
					args["web_layout"] = webLayout
				default:
					args["image_path_a"] = pathA
					args["image_path_b"] = pathC
				}
				args[c.param] = c.value
				req := mcp.CallToolRequest{
					Params: mcp.CallToolParams{Arguments: args},
				}
				res, err := compareDesignHandler(context.Background(), req)
				if err != nil {
					t.Fatalf("handler failed: %v", err)
				}
				if !res.IsError {
					t.Fatalf("Expected error for unknown parameter %q, got content=%v", c.param, res.Content[0].(mcp.TextContent).Text)
				}
				got := res.Content[0].(mcp.TextContent).Text
				want := fmt.Sprintf("parameter '%s' is not recognized; valid parameters are: %s", c.param, recognizedParamNames())
				if !strings.Contains(got, want) {
					t.Errorf("Expected error containing %q, got %q", want, got)
				}
			})
		}
	})

	// 複数の非対応パラメータを同時指定した場合も、アルファベット順で
	// 決定論的なエラーメッセージを返す (map の反復順に依存しない)。
	t.Run("MultipleUnsupportedParams_Deterministic", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathC,
					"pass_rate":    90.0,
					"ignore_nodes": "nav", // "ignore_nodes" < "pass_rate" (辞書順)
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if !res.IsError {
			t.Fatalf("Expected error for multiple unsupported params, got content=%v", res.Content[0].(mcp.TextContent).Text)
		}
		if got := res.Content[0].(mcp.TextContent).Text; !strings.Contains(got, "parameter 'ignore_nodes' is not supported in mode 'perceptual'") {
			t.Errorf("Expected deterministic error for 'ignore_nodes' (alphabetically first), got %q", got)
		}
	})

	// 非対応パラメータのエラーには、そのキーが有効なモードを固定順で列挙する
	// (未知モード時と同様、README を見ずに1回のリトライで自己修復できる)
	t.Run("UnsupportedParam_ListsSupportedModes", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "perceptual",
					"image_path_a": pathA,
					"image_path_b": pathC,
					"pass_rate":    90.0,
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if !res.IsError {
			t.Fatalf("Expected error for unsupported pass_rate in perceptual, got content=%v", res.Content[0].(mcp.TextContent).Text)
		}
		got := res.Content[0].(mcp.TextContent).Text
		if !strings.Contains(got, "supported in: layout_tree") {
			t.Errorf("Expected error to list supported modes for pass_rate, got %q", got)
		}
	})

	// 未知のモードはパラメータ検証より優先して "Unknown comparison mode" になる
	t.Run("UnknownMode_ParamsNotValidated", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode":         "bogus",
					"ignore_nodes": "nav",
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if !res.IsError {
			t.Fatalf("Expected error for unknown mode, got content=%v", res.Content[0].(mcp.TextContent).Text)
		}
		if got := res.Content[0].(mcp.TextContent).Text; !strings.Contains(got, "Unknown comparison mode") {
			t.Errorf("Expected unknown mode error, got %q", got)
		}
	})

	// 未知モードのエラーには有効モード名が列挙されている (タイポ時にREADME等を
	// 見ずに1回のリトライで自己修復できる)
	t.Run("UnknownMode_ListsValidModes", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{
					"mode": "typo",
				},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if !res.IsError {
			t.Fatalf("Expected error for unknown mode, got content=%v", res.Content[0].(mcp.TextContent).Text)
		}
		got := res.Content[0].(mcp.TextContent).Text
		for _, valid := range []string{"layout_tree", "perceptual", "strict", "layout_integrity"} {
			if !strings.Contains(got, valid) {
				t.Errorf("Expected unknown mode error to list valid mode %q, got %q", valid, got)
			}
		}
	})

	// mode 未指定のエラーにも有効モード名が列挙されている (未知モード時と同じ
	// 自己修復体験に揃え、呼び出し側のリトライ回数を減らす)
	t.Run("MissingMode_ListsValidModes", func(t *testing.T) {
		req := mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Arguments: map[string]any{},
			},
		}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if !res.IsError {
			t.Fatalf("Expected error for missing mode, got content=%v", res.Content[0].(mcp.TextContent).Text)
		}
		got := res.Content[0].(mcp.TextContent).Text
		if !strings.Contains(got, "mode parameter is required") {
			t.Errorf("Expected missing mode error to state mode is required, got %q", got)
		}
		for _, valid := range []string{"layout_tree", "perceptual", "strict", "layout_integrity"} {
			if !strings.Contains(got, valid) {
				t.Errorf("Expected missing mode error to list valid mode %q, got %q", valid, got)
			}
		}
	})

	// 画像デコード失敗時は対応フォーマットを列挙し、WebP/AVIF 等からの
	// 再書き出しを1回のリトライで選べるようにする (Issue #179)
	t.Run("ImageDecodeError_ListsSupportedFormats", func(t *testing.T) {
		txtPath := filepath.Join(tmpDir, "not-an-image.txt")
		if err := os.WriteFile(txtPath, []byte("this is not an image"), 0o644); err != nil {
			t.Fatalf("failed to write text fixture: %v", err)
		}
		// strict と perceptual の両モードで共有される対応フォーマットヒント (Issue #122)。
		const formatsHint = "(supported: PNG, JPEG, GIF, WebP; SVG and animated WebP are not supported)"

		t.Run("perceptual_txt", func(t *testing.T) {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":         "perceptual",
						"image_path_a": txtPath,
						"image_path_b": pathA,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if !res.IsError {
				t.Fatalf("Expected decode error for text image A, got content=%v", res.Content[0].(mcp.TextContent).Text)
			}
			got := res.Content[0].(mcp.TextContent).Text
			if !strings.Contains(got, "Failed to decode image A") {
				t.Errorf("Expected image A decode error, got %q", got)
			}
			if !strings.Contains(got, formatsHint) {
				t.Errorf("Expected supported-formats hint, got %q", got)
			}
		})

		t.Run("strict_txt", func(t *testing.T) {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":         "strict",
						"image_path_a": txtPath,
						"image_path_b": pathA,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if !res.IsError {
				t.Fatalf("Expected decode error for text design image, got content=%v", res.Content[0].(mcp.TextContent).Text)
			}
			got := res.Content[0].(mcp.TextContent).Text
			if !strings.Contains(got, "failed to decode design image") {
				t.Errorf("Expected design-image decode error, got %q", got)
			}
			if !strings.Contains(got, formatsHint) {
				t.Errorf("Expected supported-formats hint, got %q", got)
			}
		})

		t.Run("perceptual_empty_bytes", func(t *testing.T) {
			emptyPath := filepath.Join(tmpDir, "empty.bin")
			if err := os.WriteFile(emptyPath, nil, 0o644); err != nil {
				t.Fatalf("failed to write empty fixture: %v", err)
			}
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":         "perceptual",
						"image_path_a": pathA,
						"image_path_b": emptyPath,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if !res.IsError {
				t.Fatalf("Expected decode error for empty image B, got content=%v", res.Content[0].(mcp.TextContent).Text)
			}
			got := res.Content[0].(mcp.TextContent).Text
			if !strings.Contains(got, "Failed to decode image B") {
				t.Errorf("Expected image B decode error, got %q", got)
			}
			if !strings.Contains(got, formatsHint) {
				t.Errorf("Expected supported-formats hint, got %q", got)
			}
		})
	})
}

// resolveImageInput が data URI 形式 ("data:<mime>;base64,<payload>") の
// base64 入力を受け付けること、および従来のプレーン base64 が引き続き
// 動作することを検証する (Issue #127)。
func TestParseIgnoreRegions_OverflowRejected(t *testing.T) {
	const overflow = "4611686018427387904,0,4611686018427387904,10"
	_, err := parseIgnoreRegions(overflow)
	if err == nil {
		t.Fatalf("expected error for overflowing ignore_region %q", overflow)
	}
	if !strings.Contains(err.Error(), "must be <=") {
		t.Errorf("expected max-value error, got %v", err)
	}

	_, err = parseIgnoreRegions("2147483647,0,1,1")
	if err != nil {
		t.Errorf("MaxInt32 should be accepted, got %v", err)
	}
}

func TestParseIgnoreRegions_FractionalValues(t *testing.T) {
	got, err := parseIgnoreRegions("327.5,30,100.5,40")
	if err != nil {
		t.Fatalf("expected fractional ignore_region to parse, got %v", err)
	}
	if len(got) != 1 || got[0].X != 328 || got[0].Y != 30 || got[0].W != 101 || got[0].H != 40 {
		t.Errorf("expected rounded region {328,30,101,40}, got %+v", got)
	}

	_, err = parseIgnoreRegions("a,b,c,d")
	if err == nil {
		t.Fatal("expected invalid ignore_region string to fail")
	}
	if !strings.Contains(err.Error(), "must be numbers") {
		t.Errorf("expected numbers error, got %v", err)
	}
}

func TestIgnoreRegion_FractionalValuesInModes(t *testing.T) {
	tmpDir := t.TempDir()
	imgE := image.NewRGBA(image.Rect(0, 0, 200, 200))
	draw.Draw(imgE, imgE.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(imgE, image.Rect(0, 0, 100, 100), &image.Uniform{color.Black}, image.Point{}, draw.Src)
	pathE := saveTempImage(t, tmpDir, "imageE.png", imgE)
	pathF := saveTempImage(t, tmpDir, "imageF.png", generateSolidImage(200, 200, color.White))

	figmaLayout := `[{"id":"1","name":"header","x":0,"y":0,"w":1000,"h":100},{"id":"2","name":"banner","x":400,"y":400,"w":200,"h":80}]`
	webLayout := `[{"selector":"#header","x":0,"y":0,"w":1000,"h":100},{"selector":".banner","x":650,"y":420,"w":200,"h":80}]`

	successCases := []struct {
		mode string
		args map[string]any
	}{
		{
			mode: "layout_tree",
			args: map[string]any{
				"mode": "layout_tree", "figma_layout": figmaLayout, "web_layout": webLayout,
				"threshold": 0.15, "ignore_region": "399.6,399.6,500.4,100.4",
			},
		},
		{
			mode: "perceptual",
			args: map[string]any{
				"mode": "perceptual", "image_path_a": pathE, "image_path_b": pathF,
				"ignore_region": "0.4,0.4,99.6,99.6",
			},
		},
		{
			mode: "strict",
			args: map[string]any{
				"mode": "strict", "image_path_a": pathE, "image_path_b": pathF,
				"ignore_region": "0.4,0.4,99.6,99.6",
			},
		},
	}
	for _, c := range successCases {
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: c.args}}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: handler failed: %v", c.mode, err)
		}
		if res.IsError {
			t.Fatalf("%s: expected success with fractional ignore_region, got %v", c.mode, res.Content[0].(mcp.TextContent).Text)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "success" {
			t.Errorf("%s: expected status=success, got %v", c.mode, result["status"])
		}
	}

	invalidArgs := []map[string]any{
		{"mode": "layout_tree", "figma_layout": figmaLayout, "web_layout": webLayout, "ignore_region": "a,b,c,d"},
		{"mode": "perceptual", "image_path_a": pathE, "image_path_b": pathF, "ignore_region": "a,b,c,d"},
		{"mode": "strict", "image_path_a": pathE, "image_path_b": pathF, "ignore_region": "a,b,c,d"},
	}
	for _, args := range invalidArgs {
		mode := args["mode"].(string)
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}}
		res, err := compareDesignHandler(context.Background(), req)
		if err != nil {
			t.Fatalf("%s invalid: handler failed: %v", mode, err)
		}
		if !res.IsError {
			t.Errorf("%s: expected error for invalid ignore_region, got %v", mode, res.Content[0].(mcp.TextContent).Text)
		}
	}
}

func TestResolveImageInputBase64DataURI(t *testing.T) {
	payload := []byte("PNGDATA")
	plain := base64.StdEncoding.EncodeToString(payload)
	dataURI := "data:image/png;base64," + plain

	// プレーン base64 は従来通りデコードされる
	got, err := resolveImageInput("", plain, "image_path_a", "image_a_base64")
	if err != nil {
		t.Fatalf("plain base64 should decode: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("plain base64 decoded to %q, want %q", got, payload)
	}

	// MIME 折り返し (改行・スペース) 入りの base64 も同じバイト列になる (Issue #207)
	wrapped := plain[:len(plain)/2] + "\n " + plain[len(plain)/2:]
	got, err = resolveImageInput("", wrapped, "image_path_a", "image_a_base64")
	if err != nil {
		t.Fatalf("whitespace-wrapped base64 should decode: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("whitespace-wrapped base64 decoded to %q, want %q", got, payload)
	}

	// data URI はプレフィックスが除去されて同じバイト列になる
	got, err = resolveImageInput("", dataURI, "image_path_a", "image_a_base64")
	if err != nil {
		t.Fatalf("data URI base64 should decode: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("data URI decoded to %q, want %q", got, payload)
	}

	// "data:" プレフィックスがあるが ";base64," マーカーを欠く入力は
	// そのままデコードされるため illegal base64 エラーになる
	if _, err := resolveImageInput("", "data:image/png", "image_path_a", "image_a_base64"); err == nil {
		t.Error("expected error for data URI without ';base64,' marker, got nil")
	}

	// 不正な base64 は従来通りエラーになる
	if _, err := resolveImageInput("", "not-base64", "image_path_a", "image_a_base64"); err == nil {
		t.Error("expected error for invalid base64, got nil")
	}
}

func reqWithArgs(args map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}}
}

func TestTypedArgHelpers(t *testing.T) {
	t.Run("missing key uses default", func(t *testing.T) {
		req := reqWithArgs(map[string]any{})
		if v, err := floatArg(req, "threshold", 0.15); err != nil || v != 0.15 {
			t.Errorf("floatArg missing: got %v, %v", v, err)
		}
		if v, err := intArg(req, "max_diff_pixels", 0); err != nil || v != 0 {
			t.Errorf("intArg missing: got %v, %v", v, err)
		}
		if v, err := boolArg(req, "generate_diff", true); err != nil || v != true {
			t.Errorf("boolArg missing: got %v, %v", v, err)
		}
	})

	t.Run("numeric strings are accepted", func(t *testing.T) {
		req := reqWithArgs(map[string]any{
			"threshold":       " 98 ",
			"max_diff_pixels": "10",
			"generate_diff":   "true",
		})
		if v, err := floatArg(req, "threshold", 0); err != nil || v != 98 {
			t.Errorf("floatArg string: got %v, %v", v, err)
		}
		if v, err := intArg(req, "max_diff_pixels", 0); err != nil || v != 10 {
			t.Errorf("intArg string: got %v, %v", v, err)
		}
		if v, err := boolArg(req, "generate_diff", false); err != nil || v != true {
			t.Errorf("boolArg string: got %v, %v", v, err)
		}
	})

	t.Run("unconvertible values error", func(t *testing.T) {
		cases := []struct {
			key string
			val any
		}{
			{"threshold", ""},
			{"threshold", "abc"},
			{"threshold", nil},
			{"max_diff_pixels", "abc"},
			{"generate_diff", "abc"},
			{"count_extra_web", ""},
		}
		for _, tc := range cases {
			req := reqWithArgs(map[string]any{tc.key: tc.val})
			var err error
			switch tc.key {
			case "threshold":
				_, err = floatArg(req, tc.key, 0)
			case "max_diff_pixels":
				_, err = intArg(req, tc.key, 0)
			default:
				_, err = boolArg(req, tc.key, false)
			}
			if err == nil {
				t.Errorf("expected error for %s=%#v", tc.key, tc.val)
			}
		}
	})
}

func TestUnconvertibleNumericParams(t *testing.T) {
	figma := `[{"id":"1","name":"a","x":0,"y":0,"w":10,"h":10}]`
	web := `[{"selector":".a","x":0,"y":0,"w":10,"h":10}]`

	tmpDir := t.TempDir()
	img := generateSolidImage(8, 8, color.White)
	pathA := saveTempImage(t, tmpDir, "a.png", img)
	pathB := saveTempImage(t, tmpDir, "b.png", img)

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "threshold abc",
			args: map[string]any{"mode": "layout_tree", "figma_layout": figma, "web_layout": web, "threshold": "abc"},
			want: "argument 'threshold' must be a number",
		},
		{
			name: "pass_rate abc",
			args: map[string]any{"mode": "layout_tree", "figma_layout": figma, "web_layout": web, "pass_rate": "abc"},
			want: "argument 'pass_rate' must be a number",
		},
		{
			name: "min_match empty string",
			args: map[string]any{"mode": "strict", "image_path_a": pathA, "image_path_b": pathB, "min_match": ""},
			want: "argument 'min_match' must be a number",
		},
		{
			name: "max_diff_pixels abc",
			args: map[string]any{"mode": "strict", "image_path_a": pathA, "image_path_b": pathB, "max_diff_pixels": "abc"},
			want: "argument 'max_diff_pixels' must be an integer",
		},
		{
			name: "count_extra_web abc",
			args: map[string]any{"mode": "layout_tree", "figma_layout": figma, "web_layout": web, "count_extra_web": "abc"},
			want: "argument 'count_extra_web' must be a boolean",
		},
		{
			name: "generate_diff abc",
			args: map[string]any{"mode": "perceptual", "image_path_a": pathA, "image_path_b": pathB, "generate_diff": "abc"},
			want: "argument 'generate_diff' must be a boolean",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := compareDesignHandler(context.Background(), reqWithArgs(tc.args))
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if !res.IsError {
				t.Fatalf("Expected IsError for %s, got content=%v", tc.name, res.Content[0].(mcp.TextContent).Text)
			}
			got := res.Content[0].(mcp.TextContent).Text
			if !strings.Contains(got, tc.want) {
				t.Errorf("Expected %q, got %q", tc.want, got)
			}
		})
	}

	t.Run("numeric string still accepted", func(t *testing.T) {
		res, err := compareDesignHandler(context.Background(), reqWithArgs(map[string]any{
			"mode":         "layout_tree",
			"figma_layout": figma,
			"web_layout":   web,
			"pass_rate":    "98",
			"threshold":    "0.15",
		}))
		if err != nil {
			t.Fatalf("handler failed: %v", err)
		}
		if res.IsError {
			t.Fatalf("Expected success for numeric strings, got %v", res.Content[0].(mcp.TextContent).Text)
		}
	})
}

// TestPerceptualDecodeErrorListsSupportedFormats verifies that perceptual-mode
// decode failures for unsupported image formats (e.g. SVG) include a hint
// about the supported formats (PNG, JPEG, GIF, WebP) in the error message, so callers
// can determine the corrective action (format conversion) without an extra
// round-trip (Issue #122).
func TestPerceptualDecodeErrorListsSupportedFormats(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "vrt-svg-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	svgPath := filepath.Join(tmpDir, "image.svg")
	if err := os.WriteFile(svgPath, []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" width="2" height="2"/>`), 0o644); err != nil {
		t.Fatalf("failed to write SVG file: %v", err)
	}
	pngPath := saveTempImage(t, tmpDir, "valid.png", generateSolidImage(10, 10, color.White))

	for _, c := range []struct {
		name         string
		pathA, pathB string
		wantMsg      string
	}{
		{"svg_as_image_A", svgPath, pngPath, "Failed to decode image A"},
		{"svg_as_image_B", pngPath, svgPath, "Failed to decode image B"},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":         "perceptual",
						"image_path_a": c.pathA,
						"image_path_b": c.pathB,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if !res.IsError {
				t.Fatalf("expected decode error, got content=%v", res.Content[0].(mcp.TextContent).Text)
			}
			got := res.Content[0].(mcp.TextContent).Text
			if !strings.Contains(got, c.wantMsg) {
				t.Errorf("expected error to contain %q, got %q", c.wantMsg, got)
			}
			if !strings.Contains(got, comparator.UnsupportedImageFormatHint) {
				t.Errorf("expected decode error to contain supported-format hint, got %q", got)
			}
		})
	}
}

// TestPerceptualDecodeError_CorruptImageOmitsFormatHint verifies that a
// corrupted/truncated PNG (image.ErrFormat ではない失敗、例: unexpected EOF)
// does NOT get the unsupported-format hint in perceptual mode. PNG/JPEG/GIF/WebP だが
// 破損・途中切れのファイルで「SVG and animated WebP are not supported」と読める文面が付くと、原因を
// 形式違いだと誤認するためである (Issue #122)。
func TestPerceptualDecodeError_CorruptImageOmitsFormatHint(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "vrt-corrupt-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// PNG シグネチャは有効だが途中で切れたファイル → image.Decode は
	// "unexpected EOF" を返し image.ErrFormat ではない。
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, generateSolidImage(4, 4, color.White)); err != nil {
		t.Fatalf("failed to encode PNG fixture: %v", err)
	}
	corruptPath := filepath.Join(tmpDir, "corrupt.png")
	if err := os.WriteFile(corruptPath, pngBuf.Bytes()[:20], 0o644); err != nil {
		t.Fatalf("failed to write corrupted PNG: %v", err)
	}
	pngPath := saveTempImage(t, tmpDir, "valid.png", generateSolidImage(10, 10, color.White))

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"mode":         "perceptual",
				"image_path_a": corruptPath,
				"image_path_b": pngPath,
			},
		},
	}
	res, err := compareDesignHandler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler failed: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected decode error for corrupted PNG, got content=%v", res.Content[0].(mcp.TextContent).Text)
	}
	got := res.Content[0].(mcp.TextContent).Text
	if !strings.Contains(got, "Failed to decode image A") {
		t.Errorf("expected decode error prefix, got %q", got)
	}
	if strings.Contains(got, comparator.UnsupportedImageFormatHint) {
		t.Errorf("expected no unsupported-format hint for a corrupted image, got %q", got)
	}
}

func testdataWebP(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("testdata", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("missing WebP fixture %s: %v", path, err)
	}
	return path
}

func mustWebPChunk(t *testing.T, path, chunk string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Contains(b, []byte(chunk)) {
		t.Fatalf("%s: expected %s chunk, got header %q", path, chunk, b[:min(len(b), 16)])
	}
}

// TestCompareDesign_WebP_VP8AndVP8L は静止画 WebP (lossy VP8 / lossless VP8L) を
// perceptual と strict に渡し、同一ファイル同士の比較がデコード成功することを確認する
// (Issue #232)。
func TestCompareDesign_WebP_VP8AndVP8L(t *testing.T) {
	vp8 := testdataWebP(t, "webp-vp8.webp")
	vp8l := testdataWebP(t, "webp-vp8l.webp")
	mustWebPChunk(t, vp8, "VP8 ")
	mustWebPChunk(t, vp8l, "VP8L")

	for _, tc := range []struct {
		name, path, mode string
	}{
		{"vp8_perceptual", vp8, "perceptual"},
		{"vp8_strict", vp8, "strict"},
		{"vp8l_perceptual", vp8l, "perceptual"},
		{"vp8l_strict", vp8l, "strict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":         tc.mode,
						"image_path_a": tc.path,
						"image_path_b": tc.path,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if res.IsError {
				t.Fatalf("expected WebP decode success, got %s", res.Content[0].(mcp.TextContent).Text)
			}
			var result map[string]any
			if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result); err != nil {
				t.Fatalf("parse result: %v", err)
			}
			if result["status"] != "success" {
				t.Errorf("expected status=success for identical WebP, got %v", result["status"])
			}
		})
	}
}

// pngWithDeclaredSize は IHDR に width/height を宣言した最小 PNG（画素データなし）。
func pngWithDeclaredSize(width, height uint32) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], width)
	binary.BigEndian.PutUint32(ihdr[4:8], height)
	ihdr[8] = 8
	ihdr[9] = 2
	writePNGChunk(&buf, []byte("IHDR"), ihdr)
	writePNGChunk(&buf, []byte("IEND"), nil)
	return buf.Bytes()
}

func writePNGChunk(buf *bytes.Buffer, typ, data []byte) {
	var lenbuf [4]byte
	binary.BigEndian.PutUint32(lenbuf[:], uint32(len(data)))
	buf.Write(lenbuf[:])
	crc := crc32.NewIEEE()
	crc.Write(typ)
	crc.Write(data)
	buf.Write(typ)
	buf.Write(data)
	var crcbuf [4]byte
	binary.BigEndian.PutUint32(crcbuf[:], crc.Sum32())
	buf.Write(crcbuf[:])
}

// TestPerceptualPreDecodeSizeLimit verifies the perceptual handler rejects
// header-only PNGs whose IHDR exceeds the pre-decode limits (Issue #237).
func TestPerceptualPreDecodeSizeLimit(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "vrt-ihdr-limit-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validPath := saveTempImage(t, tmpDir, "valid.png", generateSolidImage(10, 10, color.White))
	hugePath := filepath.Join(tmpDir, "huge.png")
	if err := os.WriteFile(hugePath, pngWithDeclaredSize(30001, 1), 0o644); err != nil {
		t.Fatalf("failed to write IHDR-only PNG: %v", err)
	}
	pixelsPath := filepath.Join(tmpDir, "pixels.png")
	if err := os.WriteFile(pixelsPath, pngWithDeclaredSize(10000, 6000), 0o644); err != nil {
		t.Fatalf("failed to write pixel-limit PNG: %v", err)
	}

	for _, c := range []struct {
		name         string
		pathA, pathB string
		want         string
	}{
		{"oversized_image_A", hugePath, validPath, "image A is 30001x1 (30001 pixels); maximum is 30000px per side and 50000000 total pixels, resize or crop the images before comparison"},
		{"oversized_image_B", validPath, hugePath, "image B is 30001x1 (30001 pixels); maximum is 30000px per side and 50000000 total pixels, resize or crop the images before comparison"},
		{"over_total_pixels_A", pixelsPath, validPath, "image A is 10000x6000 (60000000 pixels); maximum is 30000px per side and 50000000 total pixels, resize or crop the images before comparison"},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := mcp.CallToolRequest{
				Params: mcp.CallToolParams{
					Arguments: map[string]any{
						"mode":         "perceptual",
						"image_path_a": c.pathA,
						"image_path_b": c.pathB,
					},
				},
			}
			res, err := compareDesignHandler(context.Background(), req)
			if err != nil {
				t.Fatalf("handler failed: %v", err)
			}
			if !res.IsError {
				t.Fatalf("expected size-limit error, got content=%v", res.Content[0].(mcp.TextContent).Text)
			}
			got := res.Content[0].(mcp.TextContent).Text
			if got != c.want {
				t.Errorf("error message: got %q want %q", got, c.want)
			}
		})
	}
}
