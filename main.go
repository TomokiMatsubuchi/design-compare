package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"log"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"design-compare/comparator"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	// 引数付き起動は MCP ハンドシェイクなしのワンショット比較。
	// 引数なしは従来どおり stdio MCP サーバー（既存クライアント互換）。
	if useCLI(os.Args) {
		os.Exit(runCLI(os.Args[1:]))
	}

	s := newDesignCompareMCPServer()

	// stdio経由でMCPサーバーを起動
	log.Println("design-compare MCP server starting...")
	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// mcpInstructions は initialize 応答の instructions に載せ、ツール説明だけを
// 読むクライアントが strict でサイズ不一致エラーを起こしにくいようにする。
const mcpInstructions = `layout_tree compares DOM/coordinate structure (Figma vs Web).
perceptual is a coarse layout image comparison that still works when image sizes differ.
strict is a pixel comparison that requires identical pixel dimensions; do not use it when sizes differ (use perceptual).
layout_integrity checks Web DOM overflow at a CSS viewport without Figma (default iPad portrait 768x1024).
The default pass line is 98% (pass_rate for layout_tree, min_match for perceptual).`

// newDesignCompareMCPServer は compare_design ツール付きの MCP サーバーを作る。
func newDesignCompareMCPServer() *server.MCPServer {
	s := server.NewMCPServer("design-compare", "1.0.0",
		server.WithInstructions(mcpInstructions),
	)

	// compare_design ツール定義 (4つの決定論的検証モードをサポート。LLM等の非決定性AIは不使用)
	compareDesignTool := mcp.NewTool("compare_design",
		mcp.WithDescription("Compare designs against implementations using four deterministic modes: 'layout_tree' (structural data), 'perceptual' (macro-layout image match), 'strict' (exact pixel match), or 'layout_integrity' (web layout breakage at a CSS viewport without Figma; default iPad portrait 768x1024, landscape via viewport_preset=ipad_landscape)."),
		mcp.WithTitleAnnotation("Compare Designs"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithString("mode",
			mcp.Required(),
			mcp.Enum("layout_tree", "perceptual", "strict", "layout_integrity"),
			mcp.Description("Comparison mode: 'layout_tree' (DOM/Figma hierarchy comparison), 'perceptual' (aHash image template check), 'strict' (pixelmatch VRT; both images must have identical pixel dimensions), or 'layout_integrity' (detect horizontal viewport overflow and parent overflow from Web bounding boxes only; default iPad portrait 768x1024 CSS px)"),
		),
		mcp.WithString("image_path_a",
			mcp.Description("Path to reference image A (required for 'perceptual' and 'strict' modes unless image_a_base64 is given; mutually exclusive with image_a_base64). Supported formats: PNG / JPEG / GIF / WebP (still images; animated WebP is not supported). Note: files are read from the server's local filesystem with the server process's privileges, so only pass paths from trusted callers"),
		),
		mcp.WithString("image_path_b",
			mcp.Description("Path to target image B (required for 'perceptual' and 'strict' modes unless image_b_base64 is given; mutually exclusive with image_b_base64). Supported formats: PNG / JPEG / GIF / WebP (still images; animated WebP is not supported). Note: files are read from the server's local filesystem with the server process's privileges, so only pass paths from trusted callers"),
		),
		mcp.WithString("image_a_base64",
			mcp.Description("Base64-encoded reference image A (for 'perceptual' and 'strict' modes; mutually exclusive with image_path_a). Supported formats: PNG / JPEG / GIF / WebP (still images; animated WebP is not supported). Also accepts a data URI form ('data:<mime>;base64,...'), as returned by screenshot tools or this tool's diff_image; the prefix is stripped before decoding. ASCII whitespace (newlines, spaces, tabs) in the base64 payload is ignored so MIME-wrapped copies decode"),
		),
		mcp.WithString("image_b_base64",
			mcp.Description("Base64-encoded target image B (for 'perceptual' and 'strict' modes; mutually exclusive with image_path_b). Supported formats: PNG / JPEG / GIF / WebP (still images; animated WebP is not supported). Also accepts a data URI form ('data:<mime>;base64,...'), as returned by screenshot tools or this tool's diff_image; the prefix is stripped before decoding. ASCII whitespace (newlines, spaces, tabs) in the base64 payload is ignored so MIME-wrapped copies decode"),
		),
		mcp.WithString("figma_layout",
			mcp.Description("JSON string representing Figma node list metadata (required for 'layout_tree' mode unless figma_layout_path is given; mutually exclusive with figma_layout_path). In layout_tree, a native JSON array/object is also accepted and marshaled to the same string form. e.g. [{\"id\":\"1\",\"name\":\"card\",\"x\":0,\"y\":0,\"w\":400,\"h\":300},{\"id\":\"2\",\"name\":\"button\",\"x\":100,\"y\":100,\"w\":200,\"h\":50,\"parent\":\"1\"}]"),
		),
		mcp.WithString("web_layout",
			mcp.Description("JSON string representing Web DOM node list layout (required for 'layout_tree' and 'layout_integrity' modes unless web_layout_path is given; mutually exclusive with web_layout_path). In layout_tree and layout_integrity, a native JSON array/object is also accepted and marshaled to the same string form. e.g. [{\"selector\":\"#card\",\"x\":0,\"y\":0,\"w\":400,\"h\":300},{\"selector\":\"#card button.primary\",\"x\":100,\"y\":100,\"w\":200,\"h\":50,\"parent\":\"#card\"}]"),
		),
		mcp.WithString("figma_layout_path",
			mcp.Description("Path to a JSON file containing the Figma node list metadata (alternative to figma_layout for 'layout_tree' mode; mutually exclusive with figma_layout). The file content is a JSON array like [{\"id\":\"1\",\"name\":\"card\",\"x\":0,\"y\":0,\"w\":400,\"h\":300},{\"id\":\"2\",\"name\":\"button\",\"x\":100,\"y\":100,\"w\":200,\"h\":50,\"parent\":\"1\"}]. Note: files are read from the server's local filesystem with the server process's privileges, so only pass paths from trusted callers"),
		),
		mcp.WithString("web_layout_path",
			mcp.Description("Path to a JSON file containing the Web DOM node list layout (alternative to web_layout for 'layout_tree' and 'layout_integrity' modes; mutually exclusive with web_layout). The file content is a JSON array like [{\"selector\":\"#card\",\"x\":0,\"y\":0,\"w\":400,\"h\":300},{\"selector\":\"#card button.primary\",\"x\":100,\"y\":100,\"w\":200,\"h\":50,\"parent\":\"#card\"}]. Note: files are read from the server's local filesystem with the server process's privileges, so only pass paths from trusted callers"),
		),
		mcp.WithString("viewport_preset",
			mcp.Description("Named CSS viewport for 'layout_integrity' only: 'ipad_portrait' (768x1024, default) or 'ipad_landscape' (1024x768); passing it in another mode is rejected as an unsupported parameter. Optional viewport_width / viewport_height override the preset size (must be greater than 0)"),
			mcp.Enum("ipad_portrait", "ipad_landscape"),
		),
		mcp.WithNumber("viewport_width",
			mcp.Description("CSS pixel viewport width for 'layout_integrity' only (default 768, iPad portrait); passing it in another mode is rejected as an unsupported parameter. Must be greater than 0. Combined with viewport_preset, an explicit value overrides the preset width"),
		),
		mcp.WithNumber("viewport_height",
			mcp.Description("CSS pixel viewport height for 'layout_integrity' only (default 1024, iPad portrait); passing it in another mode is rejected as an unsupported parameter. Must be greater than 0. Combined with viewport_preset, an explicit value overrides the preset height"),
		),
		mcp.WithString("ignore_nodes",
			mcp.Description("Comma-separated list of Figma Node IDs, Figma Node Names, or Web Selectors to ignore (for 'layout_tree' and 'layout_integrity' modes). In 'layout_integrity' only Web selectors apply. An entry ending with '*' matches by prefix (e.g. '.ad-*' matches '.ad-banner', 'Icon/*' matches 'Icon/Home'), so naming-convention groups can be excluded without enumerating every element; a prefix entry that matches no node is reported in 'unmatched_ignores'."),
		),
		mcp.WithString("ignore_region",
			mcp.Description("Semicolon-separated rectangular regions to ignore, each region formatted as 'x,y,w,h' in pixels (e.g. '10,20,100,50;200,300,80,60'). In 'perceptual' and 'strict' modes, both images are masked with white in these regions before comparison; the number of parsed regions is always reported as 'ignored_regions' (empty segments are skipped). Regions that do not intersect the image at all mask nothing and are reported in the 'out_of_bounds_regions' response field so coordinate mistakes are noticeable. When the two images differ in size in 'perceptual' mode, the same x,y,w,h is applied in absolute pixels of each image (a note is added to 'details', and 'warnings' notes that the same region may mask different areas). In 'layout_tree' and 'layout_integrity' modes, nodes whose bounding-box center lies inside a region are excluded (from both sides in layout_tree; Web nodes in layout_integrity) and counted in 'ignored_count'; regions that contain no node center exclude nothing and are reported in 'unmatched_ignore_regions'. In layout_tree the same region coordinates are applied to both Figma and Web (align coordinates when the Figma canvas is offset). Useful to exclude dynamic content (dates, ads, banners) that always differs."),
		),
		mcp.WithBoolean("count_extra_web",
			mcp.Description("For 'layout_tree' mode: when true, Web nodes that did not match any Figma node (extra implementation elements) are counted in the match rate denominator, lowering the match rate. Default false (extra elements are always reported in the 'extra_web_count' / 'extra_web_nodes' response fields and in 'details', regardless of this flag)."),
		),
		mcp.WithNumber("max_details",
			mcp.Min(0),
			mcp.Description("For 'layout_tree' mode: maximum number of 'details' lines to return (default 0 = unlimited). When greater than 0, the summary line is kept first, remaining details are truncated to this count, and a final '... and N more details omitted (max_details=M)' line is appended. Useful to keep large layout comparisons from flooding the MCP client context."),
		),
		mcp.WithNumber("threshold",
			mcp.Description("Sensitivity threshold. For 'strict' mode, color diff tolerance (0.0 to 1.0, default 0.1). For 'layout_tree', BoundingBox tolerance (0.0 to 1.0, default 0.15). For backward compatibility, 'perceptual' mode also accepts this as a minimum match percentage (1.0 to 100.0, default 98.0) when 'min_match' is omitted; specifying both in perceptual mode is an error. Prefer 'min_match' to avoid confusion with the 0.0–1.0 tolerance scale."),
		),
		mcp.WithNumber("min_match",
			mcp.Min(0),
			mcp.Max(100),
			mcp.Description("Minimum match percentage (0.0 to 100.0) required to pass. For 'perceptual' mode: default 98.0; use this instead of 'threshold' (mutually exclusive with 'threshold' in perceptual mode), since 'threshold' uses a 0.0–1.0 scale in other modes. For 'strict' mode: optional with no default; when omitted, strict mode judges only by 'max_diff_pixels', and when specified the match rate must be at least this value (combinable with 'max_diff_pixels'; exceeding either causes mismatch)."),
		),
		mcp.WithNumber("pass_rate",
			mcp.Min(0),
			mcp.Max(100),
			mcp.Description("Minimum match percentage (0.0 to 100.0) required to pass in 'layout_tree' mode. Default 98.0."),
		),
		mcp.WithNumber("max_diff_pixels",
			mcp.Min(0),
			mcp.Description("Maximum number of differing pixels allowed to still report success in 'strict' mode. Default 0 (any pixel difference causes mismatch). Useful to tolerate a few pixels of anti-aliasing or environment differences."),
		),
		mcp.WithBoolean("include_aa",
			mcp.Description("For 'strict' mode: when true, count anti-aliased boundary pixels as diffs (pixelmatch IncludeAntiAlias). Default false keeps the current behavior of excluding AA edge pixels from the diff count. Use true to detect text-rendering and other AA-boundary differences that the default would hide."),
		),
		mcp.WithBoolean("generate_diff",
			mcp.Description("Whether to generate a diff image (default true). When false, no diff image is produced and 'diff_image' is empty for 'perceptual' and 'strict' modes. In 'strict' mode, 'diff_regions' is also omitted because it is derived from the diff image. Useful to avoid large base64 payloads in responses."),
		),
		mcp.WithBoolean("diff_on_mismatch",
			mcp.Description("For 'perceptual' and 'strict' modes: when true, omit the diff image from the response if the comparison status is 'success' (default false). Combined with generate_diff (default true), this still generates a diff internally but replaces 'diff_image' with an empty string on success so matching calls do not return a large base64 payload. On mismatch the diff image is returned as usual. Ignored when generate_diff is false (diff_image is already empty)."),
		),
		mcp.WithBoolean("diff_image_content",
			mcp.Description("For 'perceptual' and 'strict' modes: when true, also return the generated diff image as an MCP image content block (MIME type image/png) after the JSON text result (default false). Existing clients that only read content[0] keep working. The extra block is added only when generate_diff produced a non-empty 'diff_image' (so generate_diff=false, or diff_on_mismatch=true on success, still return JSON alone)."),
		),
	)
	s.AddTool(compareDesignTool, compareDesignHandler)
	return s
}

// resolveImageInput returns the raw bytes of a comparison image from either a
// local file path or a base64-encoded string (exactly one must be provided).
// For base64 input, a data URI prefix ("data:<mime>;base64,...") such as the one
// returned by screenshot tools (e.g. chrome-devtools-mcp) or by this tool's own
// diff_image responses is stripped before decoding; a bare base64 string is
// accepted unchanged. ASCII whitespace in the payload (newlines, spaces, tabs)
// is removed so MIME-style wrapped copies still decode.
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
		// ログ等からコピーした MIME 折り返し (改行・スペース) を除去してからデコードする。
		payload = strings.Join(strings.Fields(payload), "")
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

// decodeImageErrorMessage は perceptual モードの image.Decode 失敗メッセージを
// 組み立てる。対応外の画像形式 (image.ErrFormat) のときだけ、strict モード
// (comparator.RunPixelMatch) と共通の対応フォーマットヒント
// (comparator.UnsupportedImageFormatHint) を付ける。PNG/JPEG/GIF/WebP だが破損・
// 途中切れのファイル (例: unexpected EOF) では「SVG and animated WebP are not
// supported」と読める文面が原因を形式違いだと誤認させるため、ヒントは付けない
// (Issue #122)。
func decodeImageErrorMessage(what string, err error) string {
	if errors.Is(err, image.ErrFormat) {
		return fmt.Sprintf("Failed to decode %s: %v %s", what, err, comparator.UnsupportedImageFormatHint)
	}
	return fmt.Sprintf("Failed to decode %s: %v", what, err)
}

// inlineLayoutJSON は layout_tree / layout_integrity のインライン layout 引数を文字列にする。
// MCP クライアントが JSON 配列・オブジェクトを文字列化せず渡した場合は
// Marshal して従来の文字列入力と同じ経路へ載せる。未指定は空文字。
// 文字列・配列・オブジェクト以外（数値・真偽・null 等）は明示エラー。
func inlineLayoutJSON(args map[string]any, key string) (string, error) {
	if args == nil {
		return "", nil
	}
	val, ok := args[key]
	if !ok {
		return "", nil
	}
	switch v := val.(type) {
	case string:
		return v, nil
	case []any, map[string]any:
		b, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("%s must be a JSON-encoded string or a JSON array", key)
		}
		return string(b), nil
	default:
		return "", fmt.Errorf("%s must be a JSON-encoded string or a JSON array", key)
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
			fv, err := strconv.ParseFloat(strings.TrimSpace(f), 64)
			if err != nil || math.IsNaN(fv) || math.IsInf(fv, 0) {
				return nil, fmt.Errorf("ignore_region values must be numbers (got %q in %q)", strings.TrimSpace(f), trimmed)
			}
			rounded := math.Round(fv)
			if rounded > math.MaxInt32 {
				return nil, fmt.Errorf("ignore_region values must be <= %d (got %q)", int32(math.MaxInt32), trimmed)
			}
			if rounded < math.MinInt32 {
				return nil, fmt.Errorf("ignore_region requires x,y >= 0 and w,h > 0 (got %q)", trimmed)
			}
			vals[i] = int(rounded)
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
	// レイアウト入力 (layout_tree / layout_integrity)。figma_* は layout_tree 専用
	"figma_layout":      {"layout_tree": true},
	"figma_layout_path": {"layout_tree": true},
	"web_layout":        {"layout_tree": true, "layout_integrity": true},
	"web_layout_path":   {"layout_tree": true, "layout_integrity": true},
	// 比較条件 (モード固有)
	// threshold は Figma 比較モード向け (layout_integrity は崩れ有無の検査で閾値なし)
	"threshold":          {"layout_tree": true, "perceptual": true, "strict": true},
	"min_match":          {"perceptual": true, "strict": true},
	"pass_rate":          {"layout_tree": true},
	"max_diff_pixels":    {"strict": true},
	"include_aa":         {"strict": true},
	"ignore_nodes":       {"layout_tree": true, "layout_integrity": true},
	"ignore_region":      {"layout_tree": true, "perceptual": true, "strict": true, "layout_integrity": true},
	"count_extra_web":    {"layout_tree": true},
	"max_details":        {"layout_tree": true},
	"generate_diff":      {"perceptual": true, "strict": true},
	"diff_on_mismatch":   {"perceptual": true, "strict": true},
	"diff_image_content": {"perceptual": true, "strict": true},
	"viewport_preset":    {"layout_integrity": true},
	"viewport_width":     {"layout_integrity": true},
	"viewport_height":    {"layout_integrity": true},
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

// recognizedParamNames は modeParamSupport のキーをソートして結合する。
// 未知パラメータのエラーに載せ、タイポ時に有効名へ自己修復できるようにする。
func recognizedParamNames() string {
	names := make([]string, 0, len(modeParamSupport))
	for k := range modeParamSupport {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// validateModeParams は、指定された引数の中に当該モードで効果を持たない
// (指定しても警告なく無視されるだけの) パラメータがないかを modeParamSupport
// と照合して検証する。非対応キーは無視せず "parameter 'X' is not supported in
// mode 'Y'" を返し、除外・合格ラインが効いていない判定結果を静かに受け取るのを
// 防ぐ。既知の代替があるキーは "(use 'Z' instead)" を追記し、続けて
// modeParamSupport から有効モードを固定順で "(supported in: ...)" に列挙する。
// mode 自身は検証対象外 (未知のモードは handler の switch で
// "Unknown comparison mode" としてエラーになる)。modeParamSupport に無い
// 未知キー (タイポ) は "parameter 'X' is not recognized" とし、サイレント無視しない。
func validateModeParams(args map[string]any, mode string) error {
	switch mode {
	case "layout_tree", "perceptual", "strict", "layout_integrity":
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
		if key == "mode" {
			continue
		}
		supported, isToolParam := modeParamSupport[key]
		if !isToolParam {
			return fmt.Errorf("parameter '%s' is not recognized; valid parameters are: %s", key, recognizedParamNames())
		}
		if supported[mode] {
			continue
		}
		var validModes []string
		for _, m := range []string{"layout_tree", "perceptual", "strict", "layout_integrity"} {
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

// Handler: compare_design
func compareDesignHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	mode, err := request.RequireString("mode")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid mode parameter: %v", err)), nil
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
		// layout JSON はインライン（文字列またはネイティブ配列/オブジェクト）
		// またはファイルパスのどちらか一方で指定できる
		args := request.GetArguments()
		figmaInline, err := inlineLayoutJSON(args, "figma_layout")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Layout tree mode input error: %v", err)), nil
		}
		figmaLayout, err := resolveLayoutInput(
			figmaInline, request.GetString("figma_layout_path", ""),
			"figma_layout", "figma_layout_path")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Layout tree mode input error: %v", err)), nil
		}
		webInline, err := inlineLayoutJSON(args, "web_layout")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Layout tree mode input error: %v", err)), nil
		}
		webLayout, err := resolveLayoutInput(
			webInline, request.GetString("web_layout_path", ""),
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
		maxDetails, err := intArg(request, "max_details", 0)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if maxDetails < 0 {
			return mcp.NewToolResultError("max_details for layout_tree mode must be >= 0 (0 = unlimited)."), nil
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
		treeResult.Details = comparator.LimitLayoutTreeDetails(treeResult.Details, maxDetails)

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
			"count_extra_web":     countExtraWeb,
			"ignored_count":       treeResult.IgnoredCount,
			"extra_web_count":      treeResult.ExtraWebCount,
			"absolute_mode_pairs":  treeResult.AbsoluteModePairs,
		}
		// ignore_nodes / ignore_region で実際に除外したノード（過剰一致の検証用）。
		// unmatched_ignores / extra_web_nodes と同様、非空時のみ返す。
		if len(treeResult.IgnoredNodes) > 0 {
			responseMap["ignored_nodes"] = treeResult.IgnoredNodes
		}
		// ignore_nodes 指定時に一致しなかったエントリ（スペルミス等）のフィードバックを返す
		if len(treeResult.UnmatchedIgnores) > 0 {
			responseMap["unmatched_ignores"] = treeResult.UnmatchedIgnores
		}
		// ignore_region のうち両側どのノード中心とも重ならない領域（座標ミス等）。
		// unmatched_ignores / 画像モードの out_of_bounds_regions と同様、非空時のみ返す。
		if len(treeResult.UnmatchedIgnoreRegions) > 0 {
			responseMap["unmatched_ignore_regions"] = treeResult.UnmatchedIgnoreRegions
		}
		// どの Figma ノードにもマッチしなかった余分な Web ノード（実装側の過剰要素）を
		// 文字列パースなしで扱えるよう、セレクタを構造化フィールドでも返す（0件時は省略）。
		if len(treeResult.ExtraWebNodes) > 0 {
			responseMap["extra_web_nodes"] = treeResult.ExtraWebNodes
		}
		// tolerance 超過の不一致ペアを、details と同じ数値で構造化して返す（0件時は省略）。
		if len(treeResult.MismatchedNodes) > 0 {
			responseMap["mismatched_nodes"] = treeResult.MismatchedNodes
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

		if err := comparator.ValidateImageSizeLimit(imgABytes, "image A"); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		imgA, _, err := image.Decode(bytes.NewReader(imgABytes))
		if err != nil {
			return mcp.NewToolResultError(decodeImageErrorMessage("image A", err)), nil
		}

		if err := comparator.ValidateImageSizeLimit(imgBBytes, "image B"); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		imgB, _, err := image.Decode(bytes.NewReader(imgBBytes))
		if err != nil {
			return mcp.NewToolResultError(decodeImageErrorMessage("image B", err)), nil
		}

		boundsA := imgA.Bounds()
		boundsB := imgB.Bounds()

		generateDiff, err := boolArg(request, "generate_diff", true)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		matchRate, diffBlocks, diffImage, outOfBounds, warnings, diffCells, diffRegion, err := comparator.CalculateLayoutSimilarityWithDiff(imgA, imgB, generateDiff, ignoreRegions)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Perceptual comparison failed: %v", err)), nil
		}
		status := "success"
		if comparator.RoundMatchRateDisplay(matchRate) < minMatchRate {
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
		details := []string{fmt.Sprintf("Template visual similarity. Minimum required: %.1f%%. %d of %d blocks differ.", minMatchRate, diffBlocks, comparator.AHashBlocks)}
		if diffRegion != "" {
			details = append(details, fmt.Sprintf("Differing blocks cluster near (%s) on image A.", diffRegion))
		}
		// サイズが異なる画像では同じ x,y,w,h が各画像の絶対ピクセルとして
		// マスクされるため、割合的に別領域になることを呼び出し側へ伝える。
		sizeMismatch := boundsA.Dx() != boundsB.Dx() || boundsA.Dy() != boundsB.Dy()
		if sizeMismatch {
			details = append(details, fmt.Sprintf("note: image A is %dx%d, image B is %dx%d; ignore_region is applied in absolute pixels of each image", boundsA.Dx(), boundsA.Dy(), boundsB.Dx(), boundsB.Dy()))
		}
		// ignore_region があるときだけ warnings にも出す。サイズ差のみでは
		// マスク座標の取り違えは起きない (Issue #244)。
		if len(ignoreRegions) > 0 && sizeMismatch {
			warnings = append(warnings, fmt.Sprintf("ignore_region is applied to each image's own pixel coordinates; image sizes differ (A %dx%d, B %dx%d), so the same region may mask different areas", boundsA.Dx(), boundsA.Dy(), boundsB.Dx(), boundsB.Dy()))
		}
		responseMap = map[string]interface{}{
			"status":           status,
			"mode":             "perceptual",
			"match_rate":       fmt.Sprintf("%.2f%%", matchRate),
			"match_rate_value": matchRate,
			"min_match":        minMatchRate,
			"total_blocks":     comparator.AHashBlocks,
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
		// 不一致セルの bounding box（画像 A のピクセル座標 "x,y,w,h"）。
		// テキストだけのクライアントでも差分の場所を絞り込める (Issue #253)。
		if diffRegion != "" {
			responseMap["diff_region"] = diffRegion
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
		includeAA, err := boolArg(request, "include_aa", false)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		matchRate, totalPixels, diffPixels, diffImage, outOfBounds, imageSize, diffRegions, warnings, err := comparator.RunPixelMatch(imgABytes, imgBBytes, threshold, generateDiff, includeAA, ignoreRegions)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Pixelmatch VRT failed: %v", err)), nil
		}

		// 合否判定: max_diff_pixels (差分ピクセル数) と min_match (一致率%) は併用可能。
		// どちらかでも超過すれば mismatch とする (min_match は指定時のみ有効)。
		status := "success"
		if diffPixels > maxDiffPixels {
			status = "mismatch"
		}
		if hasMinMatch && comparator.RoundMatchRateDisplay(matchRate) < minMatchRate {
			status = "mismatch"
		}

		details := fmt.Sprintf("Strict pixel comparison. %d of %d pixels differ (max allowed: %d).", diffPixels, totalPixels, maxDiffPixels)
		if hasMinMatch {
			details += fmt.Sprintf(" Match rate %.2f%% must be at least %.2f%%.", matchRate, minMatchRate)
		}

		// 実効パラメータ (threshold / max_diff_pixels) を応答に含め、どの閾値で判定されたかを
		// 検証可能にする (layout_tree の effective_threshold / pass_rate と同じ方針)。
		responseMap = map[string]interface{}{
			"status":              status,
			"mode":                "strict",
			"match_rate":          fmt.Sprintf("%.2f%%", matchRate),
			"match_rate_value":    matchRate,
			"total_pixels":        totalPixels,
			"diff_pixels":         diffPixels,
			"image_size":          imageSize,
			"effective_threshold": threshold,
			"max_diff_pixels":     maxDiffPixels,
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
		// min_match だけ指定して max_diff_pixels を省略すると既定 0 が判定を支配し、
		// 差分が 1px でも mismatch になる。status / match_rate は変えず、
		// perceptual の一様画像 warnings と同様に応答へ通知する (Issue #206)。
		// 両画像が単色ベタ塗りで diff_pixels=0 の空洞比較も同じフィールドへ載せる
		// (Issue #227)。非空時のみ含めるのは perceptual と同じ。
		var respWarnings []string
		if hasMinMatch {
			if _, hasMaxDiffPixels := args["max_diff_pixels"]; !hasMaxDiffPixels {
				respWarnings = append(respWarnings, "max_diff_pixels defaults to 0; any differing pixel causes mismatch regardless of min_match (set max_diff_pixels to allow some differences)")
			}
		}
		respWarnings = append(respWarnings, warnings...)
		if len(respWarnings) > 0 {
			responseMap["warnings"] = respWarnings
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

	case "layout_integrity":
		// =================================================================
		// 4. Webレイアウト崩れ検証（Figma不要のビューポート整合性チェック）
		// =================================================================
		// Web 側レイアウト JSON のノード座標だけで、指定 CSS ビューポートでの
		// 横はみ出し (viewport_overflow_x) と親要素からの逸脱 (parent_overflow) を
		// 検出する。縦向き 768x1024 と横向き 1024x768 は幅が違うため、viewport_preset
		// で切り替えて両方検査する。figma_layout / figma_layout_path は受け付けない
		// (modeParamSupport で layout_tree 専用のまま)。

		// layout_tree と同じ経路で、web_layout にネイティブ JSON 配列/オブジェクトが
		// 渡されても Marshal して文字列入力と同じ扱いにする。request.GetString だと
		// 非文字列は空文字に落ち、web_layout を指定済みなのに "either web_layout or
		// web_layout_path is required" という誤解を招くエラーになるため
		// (文字列・配列・オブジェクト以外の型は inlineLayoutJSON が専用エラーを返す)。
		args := request.GetArguments()
		webInline, err := inlineLayoutJSON(args, "web_layout")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Layout integrity mode input error: %v", err)), nil
		}
		webLayout, err := resolveLayoutInput(
			webInline, request.GetString("web_layout_path", ""),
			"web_layout", "web_layout_path")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Layout integrity mode input error: %v", err)), nil
		}

		preset := request.GetString("viewport_preset", "")
		viewportW, viewportH, err := comparator.ViewportSizeForPreset(preset)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if _, ok := args["viewport_width"]; ok {
			// 変換不能な値はサイレントに 0 へ落とさずエラーにする (floatArg, Issue #188)
			viewportW, err = floatArg(request, "viewport_width", viewportW)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if viewportW <= 0 {
				return mcp.NewToolResultError("viewport_width must be greater than 0"), nil
			}
		}
		if _, ok := args["viewport_height"]; ok {
			viewportH, err = floatArg(request, "viewport_height", viewportH)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if viewportH <= 0 {
				return mcp.NewToolResultError("viewport_height must be greater than 0"), nil
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
		ignoreRegions, err := parseIgnoreRegions(request.GetString("ignore_region", ""))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Layout integrity mode input error: %v", err)), nil
		}

		integrityResult, err := comparator.CheckLayoutIntegrity(webLayout, viewportW, viewportH, ignoreList, ignoreRegions)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Layout integrity check failed: %v", err)), nil
		}

		responseMap = map[string]interface{}{
			"status":          integrityResult.Status,
			"mode":            "layout_integrity",
			"viewport_width":  integrityResult.ViewportWidth,
			"viewport_height": integrityResult.ViewportHeight,
			"checked_nodes":   integrityResult.CheckedNodes,
			"issue_count":     integrityResult.IssueCount,
			"details":         integrityResult.Details,
			"ignored_count":   integrityResult.IgnoredCount,
		}
		if preset != "" {
			responseMap["viewport_preset"] = preset
		}
		if integrityResult.IssueCount > 0 {
			responseMap["issues"] = integrityResult.Issues
		}
		if len(integrityResult.UnmatchedIgnores) > 0 {
			responseMap["unmatched_ignores"] = integrityResult.UnmatchedIgnores
		}
		// ignore_region のうちどのノード中心とも重ならない領域（座標ミス等）。
		// layout_tree と同様、非空時のみ返す (Issue #208)。
		if len(integrityResult.UnmatchedIgnoreRegions) > 0 {
			responseMap["unmatched_ignore_regions"] = integrityResult.UnmatchedIgnoreRegions
		}

	default:
		// 有効モードを列挙し、タイポ時にREADME等を見ずに1回のリトライで自己修復できるようにする
		return mcp.NewToolResultError(fmt.Sprintf("Unknown comparison mode: %s (valid modes: layout_tree, perceptual, strict, layout_integrity)", mode)), nil
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
	jsonText := string(responseJSON)

	// MCP クライアントが差分 PNG をネイティブ描画できるよう、オプトイン時だけ
	// JSON テキストの後に image コンテンツを付ける。content[0] は従来どおり JSON。
	diffImageContent, err := boolArg(request, "diff_image_content", false)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if diffImageContent {
		if diffImage, _ := responseMap["diff_image"].(string); diffImage != "" {
			if payload, ok := pngPayloadFromDiffImage(diffImage); ok {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						mcp.NewTextContent(jsonText),
						mcp.NewImageContent(payload, "image/png"),
					},
					StructuredContent: responseMap,
				}, nil
			}
		}
	}
	return mcp.NewToolResultStructured(responseMap, string(responseJSON)), nil
}

// pngPayloadFromDiffImage は JSON の diff_image (PNG data URI) から
// MCP ImageContent 用の素の base64 を取り出す。空や非 PNG なら ok=false。
func pngPayloadFromDiffImage(diffImage string) (string, bool) {
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(diffImage, prefix) {
		return "", false
	}
	payload := diffImage[len(prefix):]
	if payload == "" {
		return "", false
	}
	return payload, true
}
