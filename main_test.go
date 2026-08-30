package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		if diffImg, ok := result["diff_image"].(string); !ok || !strings.HasPrefix(diffImg, "data:image/png;base64,") {
			t.Errorf("Expected base64 diff_image data URI, got %v", result["diff_image"])
		}
		// 数値一致率フィールドの検証
		if got := result["match_rate_value"]; got != float64(100) {
			t.Errorf("Expected match_rate_value=100, got %v", got)
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
		if diffImg, ok := result["diff_image"].(string); !ok || !strings.HasPrefix(diffImg, "data:image/png;base64,") {
			t.Errorf("Expected base64 diff_image data URI, got %v", result["diff_image"])
		}
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
	})

	// 差分画像は一時ファイルを作らず base64 data URI で返る (ファイルリーク解消)
	t.Run("Perceptual_NoTempFileLeak", func(t *testing.T) {
		before, _ := filepath.Glob(filepath.Join(os.TempDir(), "perceptual-diff-*.png"))
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
		if diffImg, ok := result["diff_image"].(string); !ok || !strings.HasPrefix(diffImg, "data:image/png;base64,") {
			t.Errorf("Expected base64 diff_image data URI, got %v", result["diff_image"])
		}
		after, _ := filepath.Glob(filepath.Join(os.TempDir(), "perceptual-diff-*.png"))
		if len(after) != len(before) {
			t.Errorf("Expected no new perceptual-diff-* temp files (before=%d, after=%d)", len(before), len(after))
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
		// min_match と threshold が両方指定された場合は min_match を優先する
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
		if res.IsError {
			t.Errorf("Expected no error when min_match overrides threshold, got content=%v", res.Content[0].(mcp.TextContent).Text)
		}
		var result map[string]interface{}
		json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &result)
		if result["status"] != "mismatch" {
			t.Errorf("Expected mismatch (min_match=98.0 should be used, not threshold=0.1), got status=%v", result["status"])
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

	// サイズの異なる画像ペアは白埋めで吸収せず、エラーとして明示的に報告する
	// (白埋め領域が一致として数えられ一致率が水増しされるのを防ぐ)
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
		if msg := res.Content[0].(mcp.TextContent).Text; !strings.Contains(msg, "size mismatch") {
			t.Errorf("Expected size mismatch error message, got %v", msg)
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
	})

	// 不正な ignore_region 指定はエラーになる
	t.Run("IgnoreRegion_InvalidFormat", func(t *testing.T) {
		for _, region := range []string{"10,20,100", "a,b,c,d", "-1,0,10,10", "0,0,0,10", "10,20,100,50;bad"} {
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
}
