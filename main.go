package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"os"
	"strings"

	"design-compare/comparator"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	s := server.NewMCPServer("design-compare", "1.0.0")

	// compare_design ツール定義 (3つの決定論的検証モードをサポート。LLM等の非決定性AIは不使用)
	compareDesignTool := mcp.NewTool("compare_design",
		mcp.WithDescription("Compare designs against implementations using three deterministic modes: 'layout_tree' (structural data), 'perceptual' (macro-layout image match), or 'strict' (exact pixel match)."),
		mcp.WithString("mode",
			mcp.Required(),
			mcp.Description("Comparison mode: 'layout_tree' (DOM/Figma hierarchy comparison), 'perceptual' (aHash image template check), or 'strict' (pixelmatch VRT)"),
		),
		mcp.WithString("image_path_a",
			mcp.Description("Path to reference image A (required for 'perceptual' and 'strict' modes unless image_a_base64 is given; mutually exclusive with image_a_base64)"),
		),
		mcp.WithString("image_path_b",
			mcp.Description("Path to target image B (required for 'perceptual' and 'strict' modes unless image_b_base64 is given; mutually exclusive with image_b_base64)"),
		),
		mcp.WithString("image_a_base64",
			mcp.Description("Base64-encoded reference image A (for 'perceptual' and 'strict' modes; mutually exclusive with image_path_a)"),
		),
		mcp.WithString("image_b_base64",
			mcp.Description("Base64-encoded target image B (for 'perceptual' and 'strict' modes; mutually exclusive with image_path_b)"),
		),
		mcp.WithString("figma_layout",
			mcp.Description("JSON string representing Figma node list metadata (required for 'layout_tree' mode)"),
		),
		mcp.WithString("web_layout",
			mcp.Description("JSON string representing Web DOM node list layout (required for 'layout_tree' mode)"),
		),
		mcp.WithNumber("threshold",
			mcp.Description("Sensitivity threshold. For 'strict' mode, color diff tolerance (0.0 to 1.0, default 0.1). For 'layout_tree', BoundingBox tolerance (0.0 to 1.0, default 0.15). For backward compatibility, 'perceptual' mode also accepts this as a minimum match percentage (1.0 to 100.0, default 98.0); prefer 'min_match' instead to avoid confusion with the 0.0–1.0 tolerance scale."),
		),
		mcp.WithNumber("min_match",
			mcp.Description("Minimum match percentage (0.0 to 100.0) required to pass in 'perceptual' mode. Default 98.0. Use this instead of 'threshold' for perceptual mode, since 'threshold' uses a 0.0–1.0 scale in other modes."),
		),
		mcp.WithString("ignore_nodes",
			mcp.Description("Comma-separated list of Figma Node IDs, Figma Node Names, or Web Selectors to ignore during comparison (for 'layout_tree' mode)."),
		),
		mcp.WithNumber("pass_rate",
			mcp.Description("Minimum match percentage (0.0 to 100.0) required to pass in 'layout_tree' mode. Default 98.0."),
		),
		mcp.WithNumber("max_diff_pixels",
			mcp.Description("Maximum number of differing pixels allowed to still report success in 'strict' mode. Default 0 (any pixel difference causes mismatch). Useful to tolerate a few pixels of anti-aliasing or environment differences."),
		),
		mcp.WithBoolean("generate_diff",
			mcp.Description("Whether to generate a diff image (default true). When false, no diff image is produced and 'diff_image_path' (perceptual) / 'diff_image' (strict) are empty. Useful for long-running servers to avoid temp file growth."),
		),
	)
	s.AddTool(compareDesignTool, compareDesignHandler)

	// stdio経由でMCPサーバーを起動
	log.Println("VRT Unified Compare MCP Server starting...")
	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// resolveImageInput returns the raw bytes of a comparison image from either a
// local file path or a base64-encoded string (exactly one must be provided).
func resolveImageInput(pathValue, base64Value, pathParam, base64Param string) ([]byte, error) {
	switch {
	case pathValue != "" && base64Value != "":
		return nil, fmt.Errorf("only one of %s and %s can be specified", pathParam, base64Param)
	case base64Value != "":
		data, err := base64.StdEncoding.DecodeString(base64Value)
		if err != nil {
			return nil, fmt.Errorf("failed to decode %s: %w", base64Param, err)
		}
		return data, nil
	case pathValue != "":
		data, err := os.ReadFile(pathValue)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", pathParam, err)
		}
		return data, nil
	default:
		return nil, fmt.Errorf("either %s or %s is required", pathParam, base64Param)
	}
}

// Handler: compare_design
func compareDesignHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	mode, err := request.RequireString("mode")
	if err != nil {
		return mcp.NewToolResultError("mode parameter is required"), nil
	}

	var responseMap map[string]interface{}

	switch mode {
	case "layout_tree":
		// =================================================================
		// 1. 構造的VRT（Layout Tree 比較）
		// =================================================================
		figmaLayout, err := request.RequireString("figma_layout")
		if err != nil {
			return mcp.NewToolResultError("figma_layout JSON string is required for layout_tree mode"), nil
		}
		webLayout, err := request.RequireString("web_layout")
		if err != nil {
			return mcp.NewToolResultError("web_layout JSON string is required for layout_tree mode"), nil
		}

		tolerance := request.GetFloat("threshold", 0.15) // デフォルト許容差 15%
		passRate := request.GetFloat("pass_rate", 98.0)  // デフォルト合格ライン 98%

		// 範囲バリデーション: threshold は 0.0–1.0、pass_rate は 0.0–100.0
		if args := request.GetArguments(); args != nil {
			if _, ok := args["threshold"]; ok && (tolerance < 0.0 || tolerance > 1.0) {
				return mcp.NewToolResultError("threshold for layout_tree mode must be between 0.0 and 1.0 (BoundingBox tolerance)."), nil
			}
			if _, ok := args["pass_rate"]; ok && (passRate < 0.0 || passRate > 100.0) {
				return mcp.NewToolResultError("pass_rate for layout_tree mode must be between 0.0 and 100.0."), nil
			}
		}

		ignoreNodesStr := request.GetString("ignore_nodes", "")
		var ignoreList []string
		if ignoreNodesStr != "" {
			for _, part := range strings.Split(ignoreNodesStr, ",") {
				trimmed := strings.TrimSpace(part)
				if trimmed != "" {
					ignoreList = append(ignoreList, trimmed)
				}
			}
		}

		treeResult, err := comparator.CompareLayoutTrees(figmaLayout, webLayout, tolerance, passRate, ignoreList)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Layout Tree comparison failed: %v", err)), nil
		}

		responseMap = map[string]interface{}{
			"status":        treeResult.Status,
			"mode":          "layout_tree",
			"match_rate":    fmt.Sprintf("%.2f%%", treeResult.MatchRate),
			"matched_nodes": treeResult.MatchedNodes,
			"total_nodes":   treeResult.TotalNodes,
			"details":       treeResult.Details,
		}
		// ignore_nodes 指定時のみ、適用結果のフィードバックを返す
		if len(ignoreList) > 0 {
			responseMap["ignored_count"] = treeResult.IgnoredCount
			if len(treeResult.UnmatchedIgnores) > 0 {
				responseMap["unmatched_ignores"] = treeResult.UnmatchedIgnores
			}
		}

	case "perceptual":
		// =================================================================
		// 2. 知覚的画像比較（aHashによる大まかなテンプレート検証）
		// =================================================================
		imgABytes, err := resolveImageInput(
			request.GetString("image_path_a", ""), request.GetString("image_a_base64", ""),
			"image_path_a", "image_a_base64")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Perceptual mode input error: %v", err)), nil
		}
		imgBBytes, err := resolveImageInput(
			request.GetString("image_path_b", ""), request.GetString("image_b_base64", ""),
			"image_path_b", "image_b_base64")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Perceptual mode input error: %v", err)), nil
		}

		// min_match を優先使用し、threshold は後方互換エイリアスとして扱う。
		// threshold は layout_tree / strict では 0.0–1.0 の許容差だが、perceptual では
		// 一致率% (1.0–100.0) と意味が異なる。専用パラメータ min_match を使うことで
		// モード間の意味の不一致による誤用を防ぐ。
		args := request.GetArguments()
		_, hasMinMatch := args["min_match"]
		_, hasThreshold := args["threshold"]

		var minMatchRate float64
		if hasMinMatch {
			minMatchRate = request.GetFloat("min_match", 98.0)
			if minMatchRate < 0.0 || minMatchRate > 100.0 {
				return mcp.NewToolResultError("min_match for perceptual mode must be between 0.0 and 100.0 (match percentage)."), nil
			}
		} else {
			// 後方互換: threshold を min_match のエイリアスとして受け付ける。
			// 1.0 未満は strict モードの 0.0–1.0 スケールとの混同を防ぐため拒否し、
			// 100 を超える値は到達不可能なため誤用として拒否する。
			minMatchRate = request.GetFloat("threshold", 98.0)
			if hasThreshold && (minMatchRate < 1.0 || minMatchRate > 100.0) {
				return mcp.NewToolResultError(
					"threshold for perceptual mode must be 1.0–100.0 (match percentage). " +
						"A value below 1.0 is likely mistaken for the strict mode scale (0.0–1.0), " +
						"which would make nearly every comparison pass silently. " +
						"Prefer using the 'min_match' parameter for perceptual mode."), nil
			}
		}

		imgA, _, err := image.Decode(bytes.NewReader(imgABytes))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to decode image A: %v", err)), nil
		}

		imgB, _, err := image.Decode(bytes.NewReader(imgBBytes))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to decode image B: %v", err)), nil
		}

		matchRate, diffImagePath, err := comparator.CalculateLayoutSimilarityWithDiff(imgA, imgB, request.GetBool("generate_diff", true))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Perceptual comparison failed: %v", err)), nil
		}
		status := "success"
		if matchRate < minMatchRate {
			status = "mismatch"
		}

		responseMap = map[string]interface{}{
			"status":          status,
			"mode":            "perceptual",
			"match_rate":      fmt.Sprintf("%.2f%%", matchRate),
			"details":         []string{fmt.Sprintf("Template visual similarity. Minimum required: %.1f%%", minMatchRate)},
			"diff_image_path": diffImagePath,
		}

	case "strict":
		// =================================================================
		// 3. 厳密ピクセル比較（Pixelmatch VRT）
		// =================================================================
		imgABytes, err := resolveImageInput(
			request.GetString("image_path_a", ""), request.GetString("image_a_base64", ""),
			"image_path_a", "image_a_base64")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Strict mode input error: %v", err)), nil
		}
		imgBBytes, err := resolveImageInput(
			request.GetString("image_path_b", ""), request.GetString("image_b_base64", ""),
			"image_path_b", "image_b_base64")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Strict mode input error: %v", err)), nil
		}

		threshold := request.GetFloat("threshold", 0.1)
		maxDiffPixels := request.GetInt("max_diff_pixels", 0)

		// 範囲バリデーション: threshold は 0.0–1.0、max_diff_pixels は 0 以上
		if args := request.GetArguments(); args != nil {
			if _, ok := args["threshold"]; ok && (threshold < 0.0 || threshold > 1.0) {
				return mcp.NewToolResultError("threshold for strict mode must be between 0.0 and 1.0 (color diff tolerance)."), nil
			}
			if _, ok := args["max_diff_pixels"]; ok && maxDiffPixels < 0 {
				return mcp.NewToolResultError("max_diff_pixels for strict mode must be 0 or greater (maximum allowed number of differing pixels)."), nil
			}
		}

		matchRate, totalPixels, diffPixels, diffImage, err := comparator.RunPixelMatch(imgABytes, imgBBytes, threshold, request.GetBool("generate_diff", true))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Pixelmatch VRT failed: %v", err)), nil
		}

		status := "success"
		if diffPixels > maxDiffPixels {
			status = "mismatch"
		}

		responseMap = map[string]interface{}{
			"status":       status,
			"mode":         "strict",
			"match_rate":   fmt.Sprintf("%.2f%%", matchRate),
			"total_pixels": totalPixels,
			"diff_pixels":  diffPixels,
			"details":      []string{fmt.Sprintf("Strict pixel comparison. %d of %d pixels differ (max allowed: %d).", diffPixels, totalPixels, maxDiffPixels)},
			"diff_image":   diffImage,
		}

	default:
		return mcp.NewToolResultError(fmt.Sprintf("Unknown comparison mode: %s", mode)), nil
	}

	responseJSON, _ := json.MarshalIndent(responseMap, "", "  ")
	return mcp.NewToolResultText(string(responseJSON)), nil
}
