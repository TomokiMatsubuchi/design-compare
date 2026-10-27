package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

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
// エラー時の本文は stdout の JSON ストリームを汚染しないよう stderr へ出す。
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
	if res.IsError {
		// stdout を JSON としてパースする呼び出し側に非 JSON テキストが混入するのを
		// 防ぐため、エラー本文は stderr へ出す (終了コード 1 で検知できる)。
		fmt.Fprintln(stderr, text)
		return exitError
	}
	fmt.Fprintln(stdout, text)
	return exitCodeFromResultJSON(text)
}

func parseCLIArgs(args []string, stderr io.Writer) (map[string]any, error) {
	fs := flag.NewFlagSet("design-compare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: design-compare --mode <layout_tree|perceptual|strict|layout_integrity> [options]\n")
		fmt.Fprintf(stderr, "       design-compare          (no args: start stdio MCP server)\n\n")
		fs.PrintDefaults()
	}

	mode := fs.String("mode", "", "Comparison mode: layout_tree, perceptual, strict, or layout_integrity")
	imageA := fs.String("image-a", "", "Path to reference image A (maps to image_path_a)")
	imageB := fs.String("image-b", "", "Path to target image B (maps to image_path_b)")
	figmaLayout := fs.String("figma-layout", "", "Inline Figma layout JSON (maps to figma_layout; mutually exclusive with --figma-layout-file)")
	webLayout := fs.String("web-layout", "", "Inline Web layout JSON (maps to web_layout; mutually exclusive with --web-layout-file)")
	figmaFile := fs.String("figma-layout-file", "", "Path to Figma layout JSON (maps to figma_layout_path)")
	webFile := fs.String("web-layout-file", "", "Path to Web layout JSON (maps to web_layout_path)")
	threshold := fs.Float64("threshold", 0, "Sensitivity threshold (mode-dependent; see compare_design)")
	minMatch := fs.Float64("min-match", 0, "Minimum match percentage for perceptual/strict (maps to min_match)")
	passRate := fs.Float64("pass-rate", 0, "Minimum match percentage for layout_tree (maps to pass_rate)")
	maxDiff := fs.Int("max-diff-pixels", 0, "Maximum differing pixels for strict (maps to max_diff_pixels)")
	includeAA := fs.Bool("include-aa", false, "Count anti-aliased pixels as diffs in strict mode (maps to include_aa)")
	ignoreRegion := fs.String("ignore-region", "", "Semicolon-separated x,y,w,h regions to ignore")
	generateDiff := fs.Bool("generate-diff", true, "Whether to generate a diff image (default true)")
	viewportPreset := fs.String("viewport-preset", "", "Viewport preset for layout_integrity: ipad_portrait or ipad_landscape (maps to viewport_preset)")
	viewportWidth := fs.Float64("viewport-width", 0, "CSS viewport width for layout_integrity (maps to viewport_width)")
	viewportHeight := fs.Float64("viewport-height", 0, "CSS viewport height for layout_integrity (maps to viewport_height)")
	ignoreNodes := fs.String("ignore-nodes", "", "Comma-separated node ids/names/selectors to exclude (maps to ignore_nodes)")
	countExtraWeb := fs.Bool("count-extra-web", false, "Count unmatched Web nodes in layout_tree match rate (maps to count_extra_web)")
	maxDetails := fs.Int("max-details", 0, "Max details lines for layout_tree and layout_integrity; 0 = unlimited (maps to max_details)")
	diffOnMismatch := fs.Bool("diff-on-mismatch", false, "Only generate diff image on mismatch in perceptual/strict (maps to diff_on_mismatch)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return nil, fmt.Errorf("unexpected argument(s): %s (boolean flags need the --flag=value form; every value must follow its flag)", strings.Join(rest, " "))
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
		case "figma-layout":
			toolArgs["figma_layout"] = *figmaLayout
		case "web-layout":
			toolArgs["web_layout"] = *webLayout
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
		case "include-aa":
			toolArgs["include_aa"] = *includeAA
		case "ignore-region":
			toolArgs["ignore_region"] = *ignoreRegion
		case "generate-diff":
			toolArgs["generate_diff"] = *generateDiff
		case "viewport-preset":
			toolArgs["viewport_preset"] = *viewportPreset
		case "viewport-width":
			toolArgs["viewport_width"] = *viewportWidth
		case "viewport-height":
			toolArgs["viewport_height"] = *viewportHeight
		case "ignore-nodes":
			toolArgs["ignore_nodes"] = *ignoreNodes
		case "count-extra-web":
			toolArgs["count_extra_web"] = *countExtraWeb
		case "max-details":
			toolArgs["max_details"] = *maxDetails
		case "diff-on-mismatch":
			toolArgs["diff_on_mismatch"] = *diffOnMismatch
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
