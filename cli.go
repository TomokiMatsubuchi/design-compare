package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// mcpParamToCLIFlag は compare_design の MCP パラメータ名と CLI フラグの対応表。
// ツールエラー本文が MCP 名のまま出るとき、stderr へ CLI 側の直し方を追記する。
// mode は英語エラー文 ("… mode …") に誤マッチしやすいため載せない。
var mcpParamToCLIFlag = map[string]string{
	"image_path_a":      "--image-a",
	"image_path_b":      "--image-b",
	"image_a_base64":    "--image-a-base64",
	"image_b_base64":    "--image-b-base64",
	"figma_layout":      "--figma-layout",
	"web_layout":        "--web-layout",
	"figma_layout_path": "--figma-layout-file",
	"web_layout_path":   "--web-layout-file",
	"threshold":         "--threshold",
	"min_match":         "--min-match",
	"pass_rate":         "--pass-rate",
	"max_diff_pixels":   "--max-diff-pixels",
	"include_aa":        "--include-aa",
	"ignore_region":     "--ignore-region",
	"generate_diff":     "--generate-diff",
	"viewport_preset":   "--viewport-preset",
	"viewport_width":    "--viewport-width",
	"viewport_height":   "--viewport-height",
	"ignore_nodes":      "--ignore-nodes",
	"count_extra_web":   "--count-extra-web",
	"max_details":       "--max-details",
	"diff_on_mismatch":  "--diff-on-mismatch",
}

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
	toolArgs, err := parseCLIArgs(args, stdout, stderr)
	if err != nil {
		if err == flag.ErrHelp {
			// 明示ヘルプの Usage は parseCLIArgs が stdout へ書き済み。
			return exitSuccess
		}
		// parseCLIArgs は flag の自動出力を Discard しているため、ここが
		// パース／検証エラー本文の唯一の stderr 出力点になる。
		fmt.Fprintln(stderr, err)
		return exitError
	}
	// 必須入力欠落を handler 到達前に CLI フラグ名で案内する。
	// MCP パラメータ名のまま出すと CLI では直し方が分からないため。
	mode, _ := toolArgs["mode"].(string)
	if err := validateCLIRequiredFlags(mode, toolArgs); err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}

	res, err := compareDesignHandler(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Arguments: toolArgs},
	})
	if err != nil {
		msg := err.Error()
		fmt.Fprintln(stderr, msg)
		writeCLIParamHints(stderr, msg)
		return exitError
	}

	text := toolResultText(res)
	if res.IsError {
		// stdout を JSON としてパースする呼び出し側に非 JSON テキストが混入するのを
		// 防ぐため、エラー本文は stderr へ出す (終了コード 1 で検知できる)。
		fmt.Fprintln(stderr, text)
		writeCLIParamHints(stderr, text)
		return exitError
	}
	fmt.Fprintln(stdout, text)
	return exitCodeFromResultJSON(text)
}

// cliStringArgPresent は parseCLIArgs が map に載せた非空文字列引数の有無を返す。
func cliStringArgPresent(args map[string]any, key string) bool {
	v, ok := args[key]
	if !ok {
		return false
	}
	s, ok := v.(string)
	return ok && s != ""
}

// validateCLIRequiredFlags はモードごとの必須入力を CLI フラグ名で案内する。
// MCP ツール側の "either image_path_a or …" 文面は変えず、CLI 経路だけを置き換える。
// 未知モードは handler の Unknown comparison mode 案内に委ねる。
func validateCLIRequiredFlags(mode string, args map[string]any) error {
	switch mode {
	case "perceptual", "strict":
		hasA := cliStringArgPresent(args, "image_path_a") || cliStringArgPresent(args, "image_a_base64")
		hasB := cliStringArgPresent(args, "image_path_b") || cliStringArgPresent(args, "image_b_base64")
		if !hasA || !hasB {
			return fmt.Errorf("%s mode requires --image-a and --image-b (or --image-a-base64 / --image-b-base64)", mode)
		}
	case "layout_tree":
		hasFigma := cliStringArgPresent(args, "figma_layout") || cliStringArgPresent(args, "figma_layout_path")
		hasWeb := cliStringArgPresent(args, "web_layout") || cliStringArgPresent(args, "web_layout_path")
		if !hasFigma || !hasWeb {
			return fmt.Errorf("layout_tree mode requires --figma-layout-file and --web-layout-file (or --figma-layout / --web-layout)")
		}
	case "layout_integrity":
		if !cliStringArgPresent(args, "web_layout") && !cliStringArgPresent(args, "web_layout_path") {
			return fmt.Errorf("layout_integrity mode requires --web-layout-file (or --web-layout)")
		}
	}
	return nil
}

// writeCLIParamHints はエラー本文に MCP パラメータ名が含まれるときだけ、
// 対応する CLI フラグを hint 行として追記する。CI のワンショット検証で
// MCP 名のままリトライして無駄打ちしないための案内。
func writeCLIParamHints(w io.Writer, msg string) {
	var params []string
	for param := range mcpParamToCLIFlag {
		if strings.Contains(msg, param) {
			params = append(params, param)
		}
	}
	if len(params) == 0 {
		return
	}
	sort.Strings(params)
	for _, param := range params {
		fmt.Fprintf(w, "hint: CLI flag is %s\n", mcpParamToCLIFlag[param])
	}
}

// writeUsage は CLI の使い方を w へ書く。明示ヘルプは stdout、パース／検証エラーは stderr。
func writeUsage(fs *flag.FlagSet, w io.Writer) {
	fmt.Fprintf(w, "Usage: design-compare --mode <layout_tree|perceptual|strict|layout_integrity> [options]\n")
	fmt.Fprintf(w, "       design-compare          (no args: start stdio MCP server)\n")
	fmt.Fprintf(w, "Positional arguments are not accepted; specify images with --image-a / --image-b.\n\n")
	prev := fs.Output()
	fs.SetOutput(w)
	fs.PrintDefaults()
	fs.SetOutput(prev)
}

func parseCLIArgs(args []string, stdout, stderr io.Writer) (map[string]any, error) {
	fs := flag.NewFlagSet("design-compare", flag.ContinueOnError)
	// flag は ContinueOnError でも解析失敗時にエラー行を Output へ書く。
	// runCLIWithIO でも同じ行を出すと二重になるため Discard し、エラー本文は
	// 呼び出し元に一本化する。fs.Usage は stderr 用（検証エラーなど）のままにする。
	fs.SetOutput(io.Discard)
	// fs.Usage は stderr 用のまま（--mode 未指定などの検証エラー向け）。
	usageStderr := func() { writeUsage(fs, stderr) }
	fs.Usage = usageStderr

	mode := fs.String("mode", "", "Comparison mode: layout_tree, perceptual, strict, or layout_integrity")
	imageA := fs.String("image-a", "", "Path to reference image A (maps to image_path_a)")
	imageB := fs.String("image-b", "", "Path to target image B (maps to image_path_b)")
	imageABase64 := fs.String("image-a-base64", "", "Base64 reference image A (maps to image_a_base64; mutually exclusive with --image-a)")
	imageBBase64 := fs.String("image-b-base64", "", "Base64 target image B (maps to image_b_base64; mutually exclusive with --image-b)")
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

	// Parse は -h/--help でも Usage を呼ぶ。明示ヘルプは stdout だけにしたいので
	// 自動 Usage を抑え、行き先を分岐する。
	fs.Usage = func() {}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			writeUsage(fs, stdout)
			return nil, err
		}
		writeUsage(fs, stderr)
		return nil, err
	}
	fs.Usage = usageStderr
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected non-flag argument(s): %s (positional arguments are not accepted; specify images with --image-a / --image-b; boolean flags need the --flag=value form)", strings.Join(fs.Args(), " "))
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
		case "image-a-base64":
			toolArgs["image_a_base64"] = *imageABase64
		case "image-b-base64":
			toolArgs["image_b_base64"] = *imageBBase64
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
	// --mode は CLI の必須入口。未指定のまま handler へ渡すと MCP 向けの
	// RequireString 文言だけが出て直し方が分からないため、ここで Usage と
	// 有効値を案内する (未知モード時の案内方針と揃える)。
	if _, ok := toolArgs["mode"]; !ok {
		fs.Usage()
		return nil, fmt.Errorf("--mode is required (valid modes: layout_tree, perceptual, strict, layout_integrity); see --help")
	}
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
