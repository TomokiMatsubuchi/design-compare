package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"log"
	"os"
	"sort"
	"strconv"
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
		mcp.WithTitleAnnotation("Compare Designs"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithString("mode",
			mcp.Required(),
			mcp.Enum("layout_tree", "perceptual", "strict"),
			mcp.Description("Comparison mode: 'layout_tree' (DOM/Figma hierarchy comparison), 'perceptual' (aHash image template check), or 'strict' (pixelmatch VRT). 'strict' requires both images to have identical pixel dimensions"),
		),
		mcp.WithString("image_path_a",
			mcp.Description("Path to reference image A (required for 'perceptual' and 'strict' modes unless image_a_base64 is given; mutually exclusive with image_a_base64). Note: files are read from the server's local filesystem with the server process's privileges, so only pass paths from trusted callers"),
		),
		mcp.WithString("image_path_b",
			mcp.Description("Path to target image B (required for 'perceptual' and 'strict' modes unless image_b_base64 is given; mutually exclusive with image_b_base64). Note: files are read from the server's local filesystem with the server process's privileges, so only pass paths from trusted callers"),
		),
		mcp.WithString("image_a_base64",
			mcp.Description("Base64-encoded reference image A (for 'perceptual' and 'strict' modes; mutually exclusive with image_path_a). Also accepts a data URI form ('data:<mime>;base64,...'), as returned by screenshot tools or this tool's diff_image; the prefix is stripped before decoding"),
		),
		mcp.WithString("image_b_base64",
			mcp.Description("Base64-encoded target image B (for 'perceptual' and 'strict' modes; mutually exclusive with image_path_b). Also accepts a data URI form ('data:<mime>;base64,...'), as returned by screenshot tools or this tool's diff_image; the prefix is stripped before decoding"),
		),
		mcp.WithString("figma_layout",
			mcp.Description("JSON string representing Figma node list metadata (required for 'layout_tree' mode unless figma_layout_path is given; mutually exclusive with figma_layout_path). e.g. [{\"id\":\"1\",\"name\":\"card\",\"x\":0,\"y\":0,\"w\":400,\"h\":300},{\"id\":\"2\",\"name\":\"button\",\"x\":100,\"y\":100,\"w\":200,\"h\":50,\"parent\":\"1\"}]"),
		),
		mcp.WithString("web_layout",
			mcp.Description("JSON string representing Web DOM node list layout (required for 'layout_tree' mode unless web_layout_path is given; mutually exclusive with web_layout_path). e.g. [{\"selector\":\"#card\",\"x\":0,\"y\":0,\"w\":400,\"h\":300},{\"selector\":\"#card button.primary\",\"x\":100,\"y\":100,\"w\":200,\"h\":50,\"parent\":\"#card\"}]"),
		),
		mcp.WithString("figma_layout_path",
			mcp.Description("Path to a JSON file containing the Figma node list metadata (alternative to figma_layout for 'layout_tree' mode; mutually exclusive with figma_layout). The file content is a JSON array like [{\"id\":\"1\",\"name\":\"card\",\"x\":0,\"y\":0,\"w\":400,\"h\":300},{\"id\":\"2\",\"name\":\"button\",\"x\":100,\"y\":100,\"w\":200,\"h\":50,\"parent\":\"1\"}]. Note: files are read from the server's local filesystem with the server process's privileges, so only pass paths from trusted callers"),
		),
		mcp.WithString("web_layout_path",
			mcp.Description("Path to a JSON file containing the Web DOM node list layout (alternative to web_layout for 'layout_tree' mode; mutually exclusive with web_layout). The file content is a JSON array like [{\"selector\":\"#card\",\"x\":0,\"y\":0,\"w\":400,\"h\":300},{\"selector\":\"#card button.primary\",\"x\":100,\"y\":100,\"w\":200,\"h\":50,\"parent\":\"#card\"}]. Note: files are read from the server's local filesystem with the server process's privileges, so only pass paths from trusted callers"),
		),
		mcp.WithString("ignore_nodes",
			mcp.Description("Comma-separated list of Figma Node IDs, Figma Node Names, or Web Selectors to ignore during comparison (for 'layout_tree' mode). An entry ending with '*' matches by prefix (e.g. '.ad-*' matches '.ad-banner', 'Icon/*' matches 'Icon/Home'), so naming-convention groups can be excluded without enumerating every element; a prefix entry that matches no node is reported in 'unmatched_ignores'."),
		),
		mcp.WithString("ignore_region",
			mcp.Description("Semicolon-separated rectangular regions to ignore, each region formatted as 'x,y,w,h' in pixels (e.g. '10,20,100,50;200,300,80,60'). In 'perceptual' and 'strict' modes, both images are masked with white in these regions before comparison; the number of parsed regions is always reported as 'ignored_regions' (empty segments are skipped). Regions that do not intersect the image at all mask nothing and are reported in the 'out_of_bounds_regions' response field so coordinate mistakes are noticeable. When the two images differ in size in 'perceptual' mode, the same x,y,w,h is applied in absolute pixels of each image and a note is added to 'details'. In 'layout_tree' mode, nodes whose bounding-box center lies inside a region are excluded from both sides and counted in 'ignored_count'. Useful to exclude dynamic content (dates, ads, banners) that always differs."),
		),
		mcp.WithBoolean("count_extra_web",
			mcp.Description("For 'layout_tree' mode: when true, Web nodes that did not match any Figma node (extra implementation elements) are counted in the match rate denominator, lowering the match rate. Default false (extra elements are always reported in the 'extra_web_count' / 'extra_web_nodes' response fields and in 'details', regardless of this flag)."),
		),
		mcp.WithNumber("threshold",
			mcp.Description("Sensitivity threshold. For 'strict' mode, color diff tolerance (0.0 to 1.0, default 0.1). For 'layout_tree', BoundingBox tolerance (0.0 to 1.0, default 0.15). For backward compatibility, 'perceptual' mode also accepts this as a minimum match percentage (1.0 to 100.0, default 98.0) when 'min_match' is omitted; specifying both in perceptual mode is an error. Prefer 'min_match' to avoid confusion with the 0.0–1.0 tolerance scale."),
		),
		mcp.WithNumber("min_match",
			mcp.Description("Minimum match percentage (0.0 to 100.0) required to pass. For 'perceptual' mode: default 98.0; use this instead of 'threshold' (mutually exclusive with 'threshold' in perceptual mode), since 'threshold' uses a 0.0–1.0 scale in other modes. For 'strict' mode: optional with no default; when omitted, strict mode judges only by 'max_diff_pixels', and when specified the match rate must be at least this value (combinable with 'max_diff_pixels'; exceeding either causes mismatch)."),
		),
		mcp.WithNumber("pass_rate",
			mcp.Description("Minimum match percentage (0.0 to 100.0) required to pass in 'layout_tree' mode. Default 98.0."),
		),
		mcp.WithNumber("max_diff_pixels",
			mcp.Description("Maximum number of differing pixels allowed to still report success in 'strict' mode. Default 0 (any pixel difference causes mismatch). Useful to tolerate a few pixels of anti-aliasing or environment differences."),
		),
		mcp.WithBoolean("generate_diff",
			mcp.Description("Whether to generate a diff image (default true). When false, no diff image is produced and 'diff_image' is empty for 'perceptual' and 'strict' modes. In 'strict' mode, 'diff_regions' is also omitted because it is derived from the diff image. Useful to avoid large base64 payloads in responses."),
		),
		mcp.WithBoolean("diff_on_mismatch",
			mcp.Description("For 'perceptual' and 'strict' modes: when true, omit the diff image from the response if the comparison status is 'success' (default false). Combined with generate_diff (default true), this still generates a diff internally but replaces 'diff_image' with an empty string on success so matching calls do not return a large base64 payload. On mismatch the diff image is returned as usual. Ignored when generate_diff is false (diff_image is already empty)."),
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
// For base64 input, a data URI prefix ("data:<mime>;base64,...") such as the one
// returned by screenshot tools (e.g. chrome-devtools-mcp) or by this tool's own
// diff_image responses is stripped before decoding; a bare base64 string is
// accepted unchanged.
func resolveImageInput(pathValue, base64Value, pathParam, base64Param string) ([]byte, error) {
	switch {
	case pathValue != "" && base64Value != "":
		return nil, fmt.Errorf("only one of %s and %s can be specified", pathParam, base64Param)
	case base64Value != "":
		// data URI 形式 ("data:<mime>;base64,<payload>") の場合はプレフィックスを除去する。
		// スクリーンショットツール (e.g. chrome-devtools-mcp) や本ツールの diff_image が
		// この形式で返すため、そのまま再入力 (ラウンドトリップ) できる。
		payload := base64Value
		if strings.HasPrefix(payload, "data:") {
			if i := strings.Index(payload, ";base64,"); i >= 0 {
				payload = payload[i+len(";base64,"):]
			}
		}
		data, err := base64.StdEncoding.DecodeString(payload)
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

// resolveLayoutInput returns the layout JSON from either an inline JSON string or
// a local file path (exactly one must be provided, mirroring resolveImageInput).
func resolveLayoutInput(inlineValue, pathValue, inlineParam, pathParam string) (string, error) {
	switch {
	case inlineValue != "" && pathValue != "":
		return "", fmt.Errorf("only one of %s and %s can be specified", inlineParam, pathParam)
	case inlineValue != "":
		return inlineValue, nil
	case pathValue != "":
		data, err := os.ReadFile(pathValue)
		if err != nil {
			return "", fmt.Errorf("failed to read %s: %w", pathParam, err)
		}
		return string(data), nil
	default:
		return "", fmt.Errorf("either %s or %s is required", inlineParam, pathParam)
	}
}

// parseIgnoreRegions parses a semicolon-separated list of rectangular regions,
// each formatted as "x,y,w,h" (pixel coordinates from the image origin), e.g.
// "10,20,100,50;200,300,80,60". Returns nil for an empty input.
func parseIgnoreRegions(s string) ([]comparator.Region, error) {
	if s == "" {
		return nil, nil
	}
	var regions []comparator.Region
	for _, part := range strings.Split(s, ";") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		fields := strings.Split(trimmed, ",")
		if len(fields) != 4 {
			return nil, fmt.Errorf("ignore_region must be semicolon-separated regions of 'x,y,w,h' (got %q)", trimmed)
		}
		vals := make([]int, 4)
		for i, f := range fields {
			v, err := strconv.Atoi(strings.TrimSpace(f))
			if err != nil {
				return nil, fmt.Errorf("ignore_region values must be integers (got %q in %q)", strings.TrimSpace(f), trimmed)
			}
			vals[i] = v
		}
		x, y, w, h := vals[0], vals[1], vals[2], vals[3]
		if x < 0 || y < 0 || w <= 0 || h <= 0 {
			return nil, fmt.Errorf("ignore_region requires x,y >= 0 and w,h > 0 (got %q)", trimmed)
		}
		regions = append(regions, comparator.Region{X: x, Y: y, W: w, H: h})
	}
	if len(regions) == 0 {
		return nil, fmt.Errorf("ignore_region contained no valid regions")
	}
	return regions, nil
}

// modeParamSupport は compare_design の各パラメータが効果を持つモードの一覧
// (mode 自身は対象外)。モードで効果を持たないパラメータを警告なく無視すると
// 呼び出し側が「除外・合格ラインが効いているつもり」のまま判定結果を受け取り
// 誤った確信を得るため、validateModeParams でこの許可マップと照合する。
var modeParamSupport = map[string]map[string]bool{
	// 画像入力 (perceptual / strict)
	"image_path_a":   {"perceptual": true, "strict": true},
	"image_path_b":   {"perceptual": true, "strict": true},
	"image_a_base64": {"perceptual": true, "strict": true},
	"image_b_base64": {"perceptual": true, "strict": true},
	// レイアウト入力 (layout_tree)
	"figma_layout":      {"layout_tree": true},
	"figma_layout_path": {"layout_tree": true},
	"web_layout":        {"layout_tree": true},
	"web_layout_path":   {"layout_tree": true},
	// 比較条件 (モード固有)
	// threshold は全モードで有効 (perceptual では min_match の後方互換エイリアス)
	"threshold":        {"layout_tree": true, "perceptual": true, "strict": true},
	"min_match":        {"perceptual": true, "strict": true},
	"pass_rate":        {"layout_tree": true},
	"max_diff_pixels":  {"strict": true},
	"ignore_nodes":     {"layout_tree": true},
	"ignore_region":    {"layout_tree": true, "perceptual": true, "strict": true},
	"count_extra_web":  {"layout_tree": true},
	"generate_diff":    {"perceptual": true, "strict": true},
	"diff_on_mismatch": {"perceptual": true, "strict": true},
}

// modeParamAlternatives は、モード非対応パラメータのうち最頻出の混同ペアだけを
// 代替ヒントとして載せる。min_match (perceptual/strict) と pass_rate
// (layout_tree) はいずれも一致率%の合格ラインで、名前だけが違うため
// layout_tree への min_match / 画像モードへの pass_rate が自然に起きる。
// 未知モードが有効モードを列挙するのと同様、1 リトライで自己修復できるようにする。
var modeParamAlternatives = map[string]string{
	"min_match": "pass_rate",
	"pass_rate": "min_match",
}

// validateModeParams は、指定された引数の中に当該モードで効果を持たない
// (指定しても警告なく無視されるだけの) パラメータがないかを modeParamSupport
// と照合して検証する。非対応キーは無視せず "parameter 'X' is not supported in
// mode 'Y'" を返し、除外・合格ラインが効いていない判定結果を静かに受け取るのを
// 防ぐ。既知の代替があるキーは "(use 'Z' instead)" を追記し、続けて
// modeParamSupport から有効モードを固定順で "(supported in: ...)" に列挙する。
// mode 自身とここに列挙していない未知のキーは検証対象外とする (未知のモードは
// handler の switch で "Unknown comparison mode" としてエラーになる)。
func validateModeParams(args map[string]any, mode string) error {
	switch mode {
	case "layout_tree", "perceptual", "strict":
		// 既知のモードのみ検証する
	default:
		return nil
	}
	// map の反復順は不定のためキーをソートし、複数指定時のエラーも決定論的にする
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		supported, isToolParam := modeParamSupport[key]
		if !isToolParam || supported[mode] {
			continue
		}
		var validModes []string
		for _, m := range []string{"layout_tree", "perceptual", "strict"} {
			if supported[m] {
				validModes = append(validModes, m)
			}
		}
		msg := fmt.Sprintf("parameter '%s' is not supported in mode '%s'", key, mode)
		if alt, ok := modeParamAlternatives[key]; ok {
			msg += fmt.Sprintf(" (use '%s' instead)", alt)
		}
		msg += fmt.Sprintf(" (supported in: %s)", strings.Join(validModes, ", "))
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// floatArg は数値引数を読む。キー未指定なら def を返す。キーがあるのに
// 数値へ変換できない値 ("" / "abc" / null 等) は mcp-go の GetFloat のように
// デフォルトへ落とさずエラーにする。文字列の "98" 等は従来通り受け付ける。
func floatArg(request mcp.CallToolRequest, key string, def float64) (float64, error) {
	args := request.GetArguments()
	val, ok := args[key]
	if !ok {
		return def, nil
	}
	switch v := val.(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f, nil
		}
	}
	return 0, fmt.Errorf("argument '%s' must be a number", key)
}

// intArg は整数引数を読む。キー未指定なら def、変換不能ならエラー。
func intArg(request mcp.CallToolRequest, key string, def int) (int, error) {
	args := request.GetArguments()
	val, ok := args[key]
	if !ok {
		return def, nil
	}
	switch v := val.(type) {
	case int:
		return v, nil
	case float64:
		return int(v), nil
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return i, nil
		}
	}
	return 0, fmt.Errorf("argument '%s' must be an integer", key)
}

// boolArg は真偽引数を読む。キー未指定なら def、変換不能ならエラー。
// 文字列の "true"/"false" と、GetBool と同様の 0/1 数値も受け付ける。
func boolArg(request mcp.CallToolRequest, key string, def bool) (bool, error) {
	args := request.GetArguments()
	val, ok := args[key]
	if !ok {
		return def, nil
	}
	switch v := val.(type) {
	case bool:
		return v, nil
	case string:
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return b, nil
		}
	case int:
		return v != 0, nil
	case float64:
		return v != 0, nil
	}
	return false, fmt.Errorf("argument '%s' must be a boolean", key)
}

// perceptualTotalBlocks は perceptual (aHash) 比較のブロック (セル) 総数。
// aHash は画像を 16x16 = 256 セルに分割して比較するため画像サイズに依存せず
// 固定。strict モードの total_pixels に対応する数量情報として、応答の
// total_blocks と details の "N of M blocks differ" 表記に使う (Issue #141)。
const perceptualTotalBlocks = 256

// Handler: compare_design
func compareDesignHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	mode, err := request.RequireString("mode")
	if err != nil {
		return mcp.NewToolResultError("mode parameter is required (valid modes: layout_tree, perceptual, strict)"), nil
	}

	// モード非対応パラメータの検証: 当該モードで効果を持たないパラメータ
	// (例: perceptual への ignore_nodes、layout_tree への min_match) は
	// サイレントに無視せず、明示的にエラーとして返す。
	if err := validateModeParams(request.GetArguments(), mode); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	var responseMap map[string]interface{}

	switch mode {
	case "layout_tree":
		// =================================================================
		// 1. 構造的VRT（Layout Tree 比較）
		// =================================================================
		// layout JSON はインライン文字列またはファイルパスのどちらか一方で指定できる
		figmaLayout, err := resolveLayoutInput(
			request.GetString("figma_layout", ""), request.GetString("figma_layout_path", ""),
			"figma_layout", "figma_layout_path")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Layout tree mode input error: %v", err)), nil
		}
		webLayout, err := resolveLayoutInput(
			request.GetString("web_layout", ""), request.GetString("web_layout_path", ""),
			"web_layout", "web_layout_path")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Layout tree mode input error: %v", err)), nil
		}

		tolerance, err := floatArg(request, "threshold", 0.15) // デフォルト許容差 15%
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		passRate, err := floatArg(request, "pass_rate", 98.0) // デフォルト合格ライン 98%
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

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
		countExtraWeb, err := boolArg(request, "count_extra_web", false)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// 除外領域 (ignore_region) をパースする (形式は画像モードと共通)。
		// layout_tree では BoundingBox の中心点が領域内にあるノードを両側から除外する。
		ignoreRegions, err := parseIgnoreRegions(request.GetString("ignore_region", ""))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Layout tree mode input error: %v", err)), nil
		}

		treeResult, err := comparator.CompareLayoutTrees(figmaLayout, webLayout, tolerance, passRate, ignoreList, countExtraWeb, ignoreRegions)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Layout Tree comparison failed: %v", err)), nil
		}

		// 実効パラメータと除外件数を応答に含め、どの閾値で判定されたかを検証可能にする
		responseMap = map[string]interface{}{
			"status":              treeResult.Status,
			"mode":                "layout_tree",
			"match_rate":          fmt.Sprintf("%.2f%%", treeResult.MatchRate),
			"match_rate_value":    treeResult.MatchRate,
			"matched_nodes":       treeResult.MatchedNodes,
			"total_nodes":         treeResult.TotalNodes,
			"details":             treeResult.Details,
			"effective_threshold": tolerance,
			"pass_rate":           passRate,
			"ignored_count":       treeResult.IgnoredCount,
			"extra_web_count":     treeResult.ExtraWebCount,
		}
		// ignore_nodes 指定時に一致しなかったエントリ（スペルミス等）のフィードバックを返す
		if len(treeResult.UnmatchedIgnores) > 0 {
			responseMap["unmatched_ignores"] = treeResult.UnmatchedIgnores
		}
		// どの Figma ノードにもマッチしなかった余分な Web ノード（実装側の過剰要素）を
		// 文字列パースなしで扱えるよう、セレクタを構造化フィールドでも返す（0件時は省略）。
		if len(treeResult.ExtraWebNodes) > 0 {
			responseMap["extra_web_nodes"] = treeResult.ExtraWebNodes
		}
		// width/height などキー名の揺れで幾何が全て 0 になると一致率 100% になるため、
		// unmatched_ignores と同様に誤用検出のフィードバックを載せる（status は非破壊）。
		if treeResult.ZeroGeometryWarning != "" {
			responseMap["zero_geometry_warning"] = treeResult.ZeroGeometryWarning
		}
		// parent が空でないのに id / selector に解決できない参照（タイポ等）。
		// 比較は従来どおり親なし＝絶対座標へフォールバックし、status は非破壊。
		if len(treeResult.UnresolvedParentRefs) > 0 {
			responseMap["unresolved_parent_refs"] = treeResult.UnresolvedParentRefs
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

		// min_match を正規パラメータとし、threshold は後方互換エイリアスとして扱う。
		// threshold は layout_tree / strict では 0.0–1.0 の許容差だが、perceptual では
		// 一致率% (1.0–100.0) と意味が異なる。専用パラメータ min_match を使うことで
		// モード間の意味の不一致による誤用を防ぐ。同時指定すると threshold が黙って
		// 無視されるため、image_path / image_*_base64 と同様に相互排他エラーにする。
		args := request.GetArguments()
		_, hasMinMatch := args["min_match"]
		_, hasThreshold := args["threshold"]
		if hasMinMatch && hasThreshold {
			return mcp.NewToolResultError("only one of min_match and threshold can be specified"), nil
		}

		var minMatchRate float64
		if hasMinMatch {
			minMatchRate, err = floatArg(request, "min_match", 98.0)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if minMatchRate < 0.0 || minMatchRate > 100.0 {
				return mcp.NewToolResultError("min_match for perceptual mode must be between 0.0 and 100.0 (match percentage)."), nil
			}
		} else {
			// 後方互換: threshold を min_match のエイリアスとして受け付ける。
			// 1.0 未満は strict モードの 0.0–1.0 スケールとの混同を防ぐため拒否し、
			// 100 を超える値は到達不可能なため誤用として拒否する。
			minMatchRate, err = floatArg(request, "threshold", 98.0)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if hasThreshold && (minMatchRate < 1.0 || minMatchRate > 100.0) {
				return mcp.NewToolResultError(
					"threshold for perceptual mode must be 1.0–100.0 (match percentage). " +
						"A value below 1.0 is likely mistaken for the strict mode scale (0.0–1.0), " +
						"which would make nearly every comparison pass silently. " +
						"Prefer using the 'min_match' parameter for perceptual mode."), nil
			}
		}

		// 除外領域 (ignore_region) をパースする
		ignoreRegions, err := parseIgnoreRegions(request.GetString("ignore_region", ""))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Perceptual mode input error: %v", err)), nil
		}

		imgA, _, err := image.Decode(bytes.NewReader(imgABytes))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to decode image A: %v (supported formats: PNG, JPEG, GIF)", err)), nil
		}

		imgB, _, err := image.Decode(bytes.NewReader(imgBBytes))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to decode image B: %v (supported formats: PNG, JPEG, GIF)", err)), nil
		}

		boundsA := imgA.Bounds()
		boundsB := imgB.Bounds()

		// 0次元画像は意味のある比較ができないため明示的にエラーにする
		if boundsA.Dx() == 0 || boundsA.Dy() == 0 {
			return mcp.NewToolResultError(fmt.Sprintf("image A has zero dimensions (%dx%d); perceptual comparison requires non-zero image size", boundsA.Dx(), boundsA.Dy())), nil
		}
		if boundsB.Dx() == 0 || boundsB.Dy() == 0 {
			return mcp.NewToolResultError(fmt.Sprintf("image B has zero dimensions (%dx%d); perceptual comparison requires non-zero image size", boundsB.Dx(), boundsB.Dy())), nil
		}

		generateDiff, err := boolArg(request, "generate_diff", true)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		matchRate, diffBlocks, diffImage, outOfBounds, warnings, diffCells, err := comparator.CalculateLayoutSimilarityWithDiff(imgA, imgB, generateDiff, ignoreRegions)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Perceptual comparison failed: %v", err)), nil
		}
		status := "success"
		if matchRate < minMatchRate {
			status = "mismatch"
		}

		// 差分画像は base64 data URI で返す（strict モードの diff_image と同じ形式）。
		// 一時ファイルを書き出さないため /tmp への蓄積が発生しない。
		// 実効 min_match も常に応答に含め、どの閾値で合否判定されたかを検証可能にする
		// (layout_tree の effective_threshold / strict の min_match echo と同様。
		// perceptual は常に閾値で判定するため、threshold エイリアス解決後の値を含む)。
		// aHash の不一致セル数 (diff_blocks) と総数 (total_blocks) を strict の
		// diff_pixels / total_pixels と同様に数量として応答へ含める。aHash は
		// 256 段階の離散値のため、一致率だけよりも差分セル数の方が min_match の
		// 調整や差分の解釈が容易になる (Issue #141)。
		details := []string{fmt.Sprintf("Template visual similarity. Minimum required: %.1f%%. %d of %d blocks differ.", minMatchRate, diffBlocks, perceptualTotalBlocks)}
		// サイズが異なる画像では同じ x,y,w,h が各画像の絶対ピクセルとして
		// マスクされるため、割合的に別領域になることを呼び出し側へ伝える。
		if boundsA.Dx() != boundsB.Dx() || boundsA.Dy() != boundsB.Dy() {
			details = append(details, fmt.Sprintf("note: image A is %dx%d, image B is %dx%d; ignore_region is applied in absolute pixels of each image", boundsA.Dx(), boundsA.Dy(), boundsB.Dx(), boundsB.Dy()))
		}
		responseMap = map[string]interface{}{
			"status":           status,
			"mode":             "perceptual",
			"match_rate":       fmt.Sprintf("%.2f%%", matchRate),
			"match_rate_value": matchRate,
			"min_match":        minMatchRate,
			"total_blocks":     perceptualTotalBlocks,
			"diff_blocks":      diffBlocks,
			"image_size_a":     fmt.Sprintf("%dx%d", boundsA.Dx(), boundsA.Dy()),
			"image_size_b":     fmt.Sprintf("%dx%d", boundsB.Dx(), boundsB.Dy()),
			"details":          details,
			"diff_image":       diffImage,
			"ignored_regions":  len(ignoreRegions),
		}
		// ignore_region のうち画像矩形と全く交差しない領域は何もマスクされず
		// 座標ミスの可能性が高いため、layout_tree の unmatched_ignores と同様に
		// 非空時のみ応答へ含めて呼び出し側に通知する。
		if len(outOfBounds) > 0 {
			responseMap["out_of_bounds_regions"] = outOfBounds
		}
		// aHash 不一致セルの 16x16 座標。generate_diff=false でも返す
		// (画像ペイロードなしで修正箇所を特定するため。Issue #186)。
		if len(diffCells) > 0 {
			responseMap["diff_cells"] = diffCells
		}
		// 一様画像 (ベタ塗り) は aHash が退化するため比較として情報を持たず、
		// 全面白 vs 全面黒でも一致率100% pass が誤った安心感を与える。status /
		// match_rate は変えず、非空時のみ warnings として応答に通知する。
		if len(warnings) > 0 {
			responseMap["warnings"] = warnings
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

		threshold, err := floatArg(request, "threshold", 0.1)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		maxDiffPixels, err := intArg(request, "max_diff_pixels", 0)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// min_match: 一致率(%)による合格ライン (0.0–100.0)。strict ではデフォルト値を
		// 持たず、未指定なら判定に使わない (max_diff_pixels のみで判定する従来挙動)。
		// total_pixels は比較してみないと分からないため、呼び出し側が max_diff_pixels を
		// 逆算できない課題を、perceptual の min_match / layout_tree の pass_rate と同じ
		// 割合ベースの閾値で解消する。
		args := request.GetArguments()
		_, hasMinMatch := args["min_match"]
		var minMatchRate float64
		if hasMinMatch {
			minMatchRate, err = floatArg(request, "min_match", 0.0)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if minMatchRate < 0.0 || minMatchRate > 100.0 {
				return mcp.NewToolResultError("min_match for strict mode must be between 0.0 and 100.0 (match percentage)."), nil
			}
		}

		// 範囲バリデーション: threshold は 0.0–1.0、max_diff_pixels は 0 以上
		if args != nil {
			if _, ok := args["threshold"]; ok && (threshold < 0.0 || threshold > 1.0) {
				return mcp.NewToolResultError("threshold for strict mode must be between 0.0 and 1.0 (color diff tolerance)."), nil
			}
			if _, ok := args["max_diff_pixels"]; ok && maxDiffPixels < 0 {
				return mcp.NewToolResultError("max_diff_pixels for strict mode must be 0 or greater (maximum allowed number of differing pixels)."), nil
			}
		}

		// 除外領域 (ignore_region) をパースする
		ignoreRegions, err := parseIgnoreRegions(request.GetString("ignore_region", ""))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Strict mode input error: %v", err)), nil
		}

		generateDiff, err := boolArg(request, "generate_diff", true)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		matchRate, totalPixels, diffPixels, diffImage, outOfBounds, imageSize, diffRegions, err := comparator.RunPixelMatch(imgABytes, imgBBytes, threshold, generateDiff, ignoreRegions)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Pixelmatch VRT failed: %v", err)), nil
		}

		// 合否判定: max_diff_pixels (差分ピクセル数) と min_match (一致率%) は併用可能。
		// どちらかでも超過すれば mismatch とする (min_match は指定時のみ有効)。
		status := "success"
		if diffPixels > maxDiffPixels {
			status = "mismatch"
		}
		if hasMinMatch && matchRate < minMatchRate {
			status = "mismatch"
		}

		details := fmt.Sprintf("Strict pixel comparison. %d of %d pixels differ (max allowed: %d).", diffPixels, totalPixels, maxDiffPixels)
		if hasMinMatch {
			details += fmt.Sprintf(" Match rate %.2f%% must be at least %.2f%%.", matchRate, minMatchRate)
		}

		responseMap = map[string]interface{}{
			"status":              status,
			"mode":                "strict",
			"match_rate":          fmt.Sprintf("%.2f%%", matchRate),
			"match_rate_value":    matchRate,
			"total_pixels":        totalPixels,
			"diff_pixels":         diffPixels,
			"image_size":          imageSize,
			"effective_threshold": threshold,
			"details":             []string{details},
			"diff_image":          diffImage,
			"ignored_regions":     len(ignoreRegions),
		}
		// 色差許容 (threshold) は常に判定に使うため、layout_tree の effective_threshold
		// と同様に既定値適用時も含めて応答へ echo する。min_match は指定時のみ判定に
		// 使うため、未指定なら含めない。
		if hasMinMatch {
			responseMap["min_match"] = minMatchRate
		}
		// ignore_region のうち画像矩形と全く交差しない領域は何もマスクされず
		// 座標ミスの可能性が高いため、非空時のみ応答へ含めて通知する
		// (perceptual モードや layout_tree の unmatched_ignores と同様)。
		if len(outOfBounds) > 0 {
			responseMap["out_of_bounds_regions"] = outOfBounds
		}
		// 赤ピクセルの bounding box。generate_diff=false では差分画像が無く算出しない
		// (Issue #187)。差分ピクセル数の多い順に最大 10 件。
		if len(diffRegions) > 0 {
			responseMap["diff_regions"] = diffRegions
		}

	default:
		// 有効モードを列挙し、タイポ時にREADME等を見ずに1回のリトライで自己修復できるようにする
		return mcp.NewToolResultError(fmt.Sprintf("Unknown comparison mode: %s (valid modes: layout_tree, perceptual, strict)", mode)), nil
	}

	// 成功時は差分画像が不要な呼び出し向け: 合否確定後に diff_image だけ空にする。
	// comparator は変更せず、generate_diff の既存挙動 (未指定時は生成) を保つ。
	if request.GetBool("diff_on_mismatch", false) {
		if status, _ := responseMap["status"].(string); status == "success" {
			if _, ok := responseMap["diff_image"]; ok {
				responseMap["diff_image"] = ""
			}
		}
	}

	responseJSON, err := json.MarshalIndent(responseMap, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to marshal comparison result: %v", err)), nil
	}
	return mcp.NewToolResultText(string(responseJSON)), nil
}
