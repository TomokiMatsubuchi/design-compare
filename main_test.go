package main

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io/ioutil"
	"os"
	"path/filepath"
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

func TestVRTUnifiedCompare(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "vrt-test-*")
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
	})

	// =================================================================
	// 3. strict モード (厳密ピクセル比較) のテスト
	// =================================================================
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
}
