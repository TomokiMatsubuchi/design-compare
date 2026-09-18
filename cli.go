package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/mark3labs/mcp-go/mcp"
)

const (
	exitSuccess  = 0
	exitError    = 1
	exitMismatch = 2
)

// useCLI reports whether process arguments select one-shot CLI instead of stdio MCP.
func useCLI(osArgs []string) bool {
	return len(osArgs) > 1
}

// runCLI は compare_design を1回実行し、MCP ツールと同じ応答本文を stdout へ出す。
// 終了コード: success=0 / mismatch（および success 以外の比較結果）=2 / エラー=1。
func runCLI(args []string) int {
	return runCLIWithIO(args, os.Stdout, os.Stderr)
}

func runCLIWithIO(args []string, stdout, stderr io.Writer) int {
	toolArgs, err := parseCLIArgs(args, stderr)
	if err != nil {
		if err == flag.ErrHelp {
			return exitSuccess
		}
		fmt.Fprintln(stderr, err)
		return exitError
	}

	res, err := compareDesignHandler(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: toolArgs},
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}

	text := toolResultText(res)
	fmt.Fprintln(stdout, text)
	if res.IsError {
		return exitError
	}
	return exitCodeFromResultJSON(text)
}

func parseCLIArgs(args []string, stderr io.Writer) (map[string]any, error) {
	fs := flag.NewFlagSet("design-compare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: design-compare --mode <layout_tree|perceptual|strict> [options]\n")
		fmt.Fprintf(stderr, "       design-compare          (no args: start stdio MCP server)\n\n")
		fs.PrintDefaults()
	}

	mode := fs.String("mode", "", "Comparison mode: layout_tree, perceptual, or strict")
	imageA := fs.String("image-a", "", "Path to reference image A (maps to image_path_a)")
	imageB := fs.String("image-b", "", "Path to target image B (maps to image_path_b)")
	figmaFile := fs.String("figma-layout-file", "", "Path to Figma layout JSON (maps to figma_layout_path)")
	webFile := fs.String("web-layout-file", "", "Path to Web layout JSON (maps to web_layout_path)")
	threshold := fs.Float64("threshold", 0, "Sensitivity threshold (mode-dependent; see compare_design)")
	minMatch := fs.Float64("min-match", 0, "Minimum match percentage for perceptual/strict (maps to min_match)")
	passRate := fs.Float64("pass-rate", 0, "Minimum match percentage for layout_tree (maps to pass_rate)")
	maxDiff := fs.Int("max-diff-pixels", 0, "Maximum differing pixels for strict (maps to max_diff_pixels)")
	ignoreRegion := fs.String("ignore-region", "", "Semicolon-separated x,y,w,h regions to ignore")
	generateDiff := fs.Bool("generate-diff", true, "Whether to generate a diff image (default true)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	toolArgs := make(map[string]any)
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "mode":
			toolArgs["mode"] = *mode
		case "image-a":
			toolArgs["image_path_a"] = *imageA
		case "image-b":
			toolArgs["image_path_b"] = *imageB
		case "figma-layout-file":
			toolArgs["figma_layout_path"] = *figmaFile
		case "web-layout-file":
			toolArgs["web_layout_path"] = *webFile
		case "threshold":
			toolArgs["threshold"] = *threshold
		case "min-match":
			toolArgs["min_match"] = *minMatch
		case "pass-rate":
			toolArgs["pass_rate"] = *passRate
		case "max-diff-pixels":
			toolArgs["max_diff_pixels"] = *maxDiff
		case "ignore-region":
			toolArgs["ignore_region"] = *ignoreRegion
		case "generate-diff":
			toolArgs["generate_diff"] = *generateDiff
		}
	})
	return toolArgs, nil
}

func toolResultText(res *mcp.CallToolResult) string {
	if res == nil || len(res.Content) == 0 {
		return ""
	}
	if tc, ok := res.Content[0].(mcp.TextContent); ok {
		return tc.Text
	}
	return fmt.Sprintf("%v", res.Content[0])
}

func exitCodeFromResultJSON(text string) int {
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		return exitError
	}
	status, _ := m["status"].(string)
	if status == "success" {
		return exitSuccess
	}
	if status != "" {
		return exitMismatch
	}
	return exitError
}
