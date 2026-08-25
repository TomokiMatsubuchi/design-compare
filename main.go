package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
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
			mcp.Description("Path to reference image A (required for 'perceptual' and 'strict' modes)"),
		),
		mcp.WithString("image_path_b",
			mcp.Description("Path to target image B (required for 'perceptual' and 'strict' modes)"),
		),
		mcp.WithString("figma_layout",
			mcp.Description("JSON string representing Figma node list metadata (required for 'layout_tree' mode)"),
		),
		mcp.WithString("web_layout",
			mcp.Description("JSON string representing Web DOM node list layout (required for 'layout_tree' mode)"),
		),
		mcp.WithNumber("threshold",
			mcp.Description("Sensitivity threshold. For 'strict' mode, color diff tolerance (0.0 to 1.0, default 0.1). For 'perceptual' mode, minimum match % (default 98.0). For 'layout_tree', BoundingBox tolerance % (default 15.0)."),
		),
		mcp.WithString("ignore_nodes",
			mcp.Description("Comma-separated list of Figma Node IDs, Figma Node Names, or Web Selectors to ignore during comparison (for 'layout_tree' mode)."),
		),
		mcp.WithNumber("pass_rate",
			mcp.Description("Minimum match percentage (0.0 to 100.0) required to pass in 'layout_tree' mode. Default 98.0."),
		),
	)
	s.AddTool(compareDesignTool, compareDesignHandler)

	// stdio経由でMCPサーバーを起動
	log.Println("VRT Unified Compare MCP Server starting...")
	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("Server error: %v", err)
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
		passRate := request.GetFloat("pass_rate", 98.0) // デフォルト合格ライン 98%

		ignoreNodesStr := request.GetString("ignore_nodes", "")
		var ignoreList []string
		if ignoreNodesStr != "" {
			for _, s := range strings.Split(ignoreNodesStr, ", ") {
				// 最初は ", " でスプリットを試みるが、カンマのみの場合も考慮してカンマ単体で再度スプリット
				for _, part := range strings.Split(s, ",") {
					trimmed := strings.TrimSpace(part)
					if trimmed != "" {
						ignoreList = append(ignoreList, trimmed)
					}
				}
			}
		}

		treeResult, err := comparator.CompareLayoutTrees(figmaLayout, webLayout, tolerance, passRate, ignoreList)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Layout Tree comparison failed: %v", err)), nil
		}

		responseMap = map[string]interface{}{
			"status":     treeResult.Status,
			"mode":       "layout_tree",
			"match_rate": fmt.Sprintf("%.2f%%", treeResult.MatchRate),
			"details":    treeResult.Details,
		}

	case "perceptual":
		// =================================================================
		// 2. 知覚的画像比較（aHashによる大まかなテンプレート検証）
		// =================================================================
		imgPathA, err := request.RequireString("image_path_a")
		if err != nil {
			return mcp.NewToolResultError("image_path_a is required for perceptual mode"), nil
		}
		imgPathB, err := request.RequireString("image_path_b")
		if err != nil {
			return mcp.NewToolResultError("image_path_b is required for perceptual mode"), nil
		}

		minMatchRate := request.GetFloat("threshold", 98.0)

		fileA, err := os.Open(imgPathA)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to open image A: %v", err)), nil
		}
		defer fileA.Close()

		imgA, _, err := image.Decode(fileA)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to decode image A: %v", err)), nil
		}

		fileB, err := os.Open(imgPathB)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to open image B: %v", err)), nil
		}
		defer fileB.Close()

		imgB, _, err := image.Decode(fileB)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to decode image B: %v", err)), nil
		}

		matchRate := comparator.CalculateLayoutSimilarity(imgA, imgB)
		status := "success"
		if matchRate < minMatchRate {
			status = "mismatch"
		}

		responseMap = map[string]interface{}{
			"status":     status,
			"mode":       "perceptual",
			"match_rate": fmt.Sprintf("%.2f%%", matchRate),
			"details":    fmt.Sprintf("Template visual similarity. Minimum required: %.1f%%", minMatchRate),
		}

	case "strict":
		// =================================================================
		// 3. 厳密ピクセル比較（Pixelmatch VRT）
		// =================================================================
		imgPathA, err := request.RequireString("image_path_a")
		if err != nil {
			return mcp.NewToolResultError("image_path_a is required for strict mode"), nil
		}
		imgPathB, err := request.RequireString("image_path_b")
		if err != nil {
			return mcp.NewToolResultError("image_path_b is required for strict mode"), nil
		}

		threshold := request.GetFloat("threshold", 0.1)

		// B画像を読み込みバイト列に
		fileB, err := os.Open(imgPathB)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to open image B: %v", err)), nil
		}
		defer fileB.Close()

		imgBBytes, err := io.ReadAll(fileB)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to read image B: %v", err)), nil
		}

		matchRate, totalPixels, diffPixels, diffImagePath, err := comparator.RunPixelMatch(imgPathA, imgBBytes, threshold)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Pixelmatch VRT failed: %v", err)), nil
		}

		status := "success"
		if diffPixels > 0 {
			status = "mismatch"
		}

		responseMap = map[string]interface{}{
			"status":          status,
			"mode":            "strict",
			"match_rate":      fmt.Sprintf("%.2f%%", matchRate),
			"total_pixels":    totalPixels,
			"diff_pixels":     diffPixels,
			"diff_image_path": diffImagePath,
		}

	default:
		return mcp.NewToolResultError(fmt.Sprintf("Unknown comparison mode: %s", mode)), nil
	}

	responseJSON, _ := json.MarshalIndent(responseMap, "", "  ")
	return mcp.NewToolResultText(string(responseJSON)), nil
}
