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
		if diffPath, ok := result["diff_image_path"].(string); !ok || diffPath == "" {
			t.Errorf("Expected non-empty diff_image_path, got %v", result["diff_image_path"])
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
		if diffPath, ok := result["diff_image_path"].(string); !ok || diffPath == "" {
			t.Errorf("Expected non-empty diff_image_path, got %v", result["diff_image_path"])
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
}
