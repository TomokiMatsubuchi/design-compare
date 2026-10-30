package comparator

import (
	"encoding/json"
	"fmt"
	"strings"
)

// iPad ビューポートの既定サイズ（CSS px）。
//
// なぜ Figma 比較とは別にこのチェックが必要か（Issue #271）:
// CompareLayoutTrees は「Figma と同じキャンバス幅で撮影した Web レイアウト」を
// 前提としたピクセルパーフェクト比較であり、デスクトップ幅で一致していても、
// タブレット幅でのみ発生する横はみ出しや親要素からの逸脱は検出できない。
// 縦向き (768x1024) と横向き (1024x768) は幅が違うため、片方だけ合格しても
// もう片方で崩れることがある。このチェックは Figma データを一切必要とせず、
// Web 側のノード座標とビューポートサイズだけでレイアウト崩れを判定できる。
const (
	ViewportPresetIPadPortrait  = "ipad_portrait"
	ViewportPresetIPadLandscape = "ipad_landscape"

	DefaultIPadViewportWidth  = 768 // iPad ポートレートの CSS px（クラシック / Chrome iPad Mini）
	DefaultIPadViewportHeight = 1024

	DefaultIPadLandscapeViewportWidth  = 1024
	DefaultIPadLandscapeViewportHeight = 768
)

// layoutSlopPx は getBoundingClientRect 由来のサブピクセル丸めを許容する。
// 0.5px 程度のはみ出しは描画ノイズであり、1.0px を超えたものだけを崩れとみなす。
const layoutSlopPx = 1.0

// LayoutIssue は検出した 1 件の崩れ。Type は viewport_overflow_x または parent_overflow。
type LayoutIssue struct {
	Type     string `json:"type"`
	Selector string `json:"selector"`
	Detail   string `json:"detail"`
}

type LayoutIntegrityResult struct {
	Status         string        `json:"status"` // success | mismatch | skipped
	ViewportWidth  float64       `json:"viewport_width"`
	ViewportHeight float64       `json:"viewport_height"`
	CheckedNodes   int           `json:"checked_nodes"`
	IssueCount     int           `json:"issue_count"`
	Issues         []LayoutIssue `json:"issues,omitempty"`
	Details        []string      `json:"details"`
	IgnoredCount   int           `json:"ignored_count"`
	// IgnoredNodes は ignore_nodes / ignore_region で除外したノードの識別子
	//（web:<selector>）。件数は IgnoredCount と一致する。プレフィックス除外の
	// 過剰一致検証用（layout_tree と同方針）。
	IgnoredNodes           []string `json:"ignored_nodes,omitempty"`
	UnmatchedIgnores       []string `json:"unmatched_ignores,omitempty"`
	UnmatchedIgnoreRegions []string `json:"unmatched_ignore_regions,omitempty"`
	// UnresolvedParentRefs は parent が空でないのに元リスト上のどの selector にも
	// 一致しない参照。合否は変えず、layout_tree と同形式（Web: '…'）で通知する。
	UnresolvedParentRefs []string `json:"unresolved_parent_refs,omitempty"`
}

// ViewportSizeForPreset は既知プリセットの CSS px サイズを返す。
// 空文字は ipad_portrait (768x1024) として扱う。未知プリセットは
// "unknown viewport_preset" を含み ipad_portrait / ipad_landscape を列挙する。
func ViewportSizeForPreset(preset string) (w, h float64, err error) {
	switch strings.TrimSpace(preset) {
	case "", ViewportPresetIPadPortrait:
		return DefaultIPadViewportWidth, DefaultIPadViewportHeight, nil
	case ViewportPresetIPadLandscape:
		return DefaultIPadLandscapeViewportWidth, DefaultIPadLandscapeViewportHeight, nil
	default:
		return 0, 0, fmt.Errorf("unknown viewport_preset: %s (valid presets: %s, %s)",
			preset, ViewportPresetIPadPortrait, ViewportPresetIPadLandscape)
	}
}

// CheckLayoutIntegrity は Web DOM の Bounding Box だけを見て、指定 CSS ビューポート
// での横はみ出しと親ボックスからの逸脱を検出する（Figma 不要）。
func CheckLayoutIntegrity(webJSON string, viewportW, viewportH float64, ignoreList []string, ignoreRegions []Region) (*LayoutIntegrityResult, error) {
	// layout_tree と同様、PowerShell 等が付ける UTF-8 BOM をパース前に除去する。
	webJSON = strings.TrimPrefix(webJSON, "\uFEFF")

	var wNodes []WebNode
	if err := json.Unmarshal([]byte(webJSON), &wNodes); err != nil {
		return nil, fmt.Errorf("failed to parse Web layout JSON: %w", err)
	}
	if viewportW <= 0 || viewportH <= 0 {
		return nil, fmt.Errorf("viewport_width and viewport_height must be greater than 0")
	}
	// BoundingBox の幅・高さは負になり得ない。負値は座標とサイズの取り違え等の
	// 入力ミスであり、checkable から無言で外すと未検証のまま success になる。
	// w/h=0 は collapsed ノードとして検査対象外にし、負値のみ拒否する（layout_tree と同方針）。
	for _, wn := range wNodes {
		if wn.W < 0 || wn.H < 0 {
			return nil, fmt.Errorf("Web node %q has negative width/height (w=%g, h=%g)", wn.Selector, wn.W, wn.H)
		}
	}

	// 親参照用インデックスは ignore フィルタ前の元リストから構築する。
	// 除外は報告対象から外すだけにし、子の parent_overflow 検査に必要な親座標は残す。
	// フィルタ後リストから作ると、除外された親を持つ子が親解決に失敗し
	// parent_overflow を無言でスキップする（layout_tree の親インデックスと同方針）。
	allWebNodes := wNodes

	var ignoredCount int
	var ignoredNodes []string
	var unmatchedIgnores []string
	if len(ignoreList) > 0 {
		ignoreMap := make(map[string]bool)
		var wildcardPrefixes []string
		for _, item := range ignoreList {
			if strings.HasSuffix(item, "*") {
				prefix := strings.TrimSuffix(item, "*")
				wildcardPrefixes = append(wildcardPrefixes, prefix, cleanNodeName(prefix))
				continue
			}
			ignoreMap[item] = true
			ignoreMap[cleanNodeName(item)] = true
		}

		nodeValues := make(map[string]bool)
		for _, wn := range wNodes {
			nodeValues[wn.Selector] = true
			nodeValues[cleanNodeName(wn.Selector)] = true
		}

		var filtered []WebNode
		for _, wn := range wNodes {
			if isNodeValueIgnored(wn.Selector, ignoreMap, wildcardPrefixes) {
				ignoredCount++
				ignoredNodes = append(ignoredNodes, ignoredWebID(wn))
				continue
			}
			filtered = append(filtered, wn)
		}
		wNodes = filtered

		for _, item := range ignoreList {
			if strings.HasSuffix(item, "*") {
				if !anyNodeValueHasPrefix(nodeValues, strings.TrimSuffix(item, "*")) {
					unmatchedIgnores = append(unmatchedIgnores, item)
				}
				continue
			}
			if !nodeValues[item] && !nodeValues[cleanNodeName(item)] {
				unmatchedIgnores = append(unmatchedIgnores, item)
			}
		}
	}

	// ignore_region のうちどのノード中心とも重ならない領域 (座標ミス等)。
	var unmatchedIgnoreRegions []string
	if len(ignoreRegions) > 0 {
		// 領域ごとのヒット数を数え、どのノード中心とも重ならない領域は座標ミス等の
		// 可能性が高いため UnmatchedIgnoreRegions に報告する (layout_tree の
		// unmatched_ignore_regions と同種。Issue #208)。
		regionHits := make([]int, len(ignoreRegions))
		var filtered []WebNode
		for _, wn := range wNodes {
			if boundingBoxCenterInRegions(wn.X, wn.Y, wn.W, wn.H, ignoreRegions, regionHits) {
				ignoredCount++
				ignoredNodes = append(ignoredNodes, ignoredWebID(wn))
				continue
			}
			filtered = append(filtered, wn)
		}
		wNodes = filtered
		for i, r := range ignoreRegions {
			if regionHits[i] == 0 {
				unmatchedIgnoreRegions = append(unmatchedIgnoreRegions, fmt.Sprintf("%d,%d,%d,%d", r.X, r.Y, r.W, r.H))
			}
		}
	}

	var checkable []WebNode
	for _, wn := range wNodes {
		if wn.W <= 0 || wn.H <= 0 {
			continue
		}
		checkable = append(checkable, wn)
	}

	// 解決不能な parent の検出用索引は ignore 前の元リスト全体から作る
	// （w/h=0 も含む）。bySelector（検査用）は幾何付きノードだけなので流用しない。
	// 走査対象は除外後の wNodes とし、除外済み親を誤って unresolved にしない
	// （layout_tree の collectUnresolvedParentRefs と同方針）。
	unresolvedParentRefs := collectUnresolvedParentRefs(nil, wNodes, nil, indexWebNodesBySelector(allWebNodes))

	result := &LayoutIntegrityResult{
		ViewportWidth:          viewportW,
		ViewportHeight:         viewportH,
		IgnoredCount:           ignoredCount,
		IgnoredNodes:           ignoredNodes,
		UnmatchedIgnores:       unmatchedIgnores,
		UnmatchedIgnoreRegions: unmatchedIgnoreRegions,
		UnresolvedParentRefs:   unresolvedParentRefs,
	}

	if len(checkable) == 0 {
		// 幾何 0 のノードが残っている場合は、ignore 件数があっても「全件除外」と
		// 誤報しない（layout_tree の #238 と同方針）。キー名ミス (width/height 等)
		// や非表示要素の計測が主因なので、ゼロ幾何の mismatch を先に返す。
		if len(wNodes) > 0 {
			result.Status = "mismatch"
			detail := fmt.Sprintf("no Web nodes have non-zero geometry (%d nodes parsed); check the layout JSON keys are {\"selector\",\"x\",\"y\",\"w\",\"h\",\"parent\"}", len(wNodes))
			if ignoredCount > 0 {
				detail += fmt.Sprintf(" (and %s by ignore_nodes / ignore_region)", ignoredNodesWerePhrase(ignoredCount))
			}
			result.Details = []string{detail}
			return result, nil
		}
		// ignore で全ノードが除外され検査対象が無くなった場合のみ skipped。
		if ignoredCount > 0 {
			result.Status = "skipped"
			result.Details = []string{fmt.Sprintf("All Web nodes were excluded by ignore_nodes / ignore_region (%d nodes ignored in total); no comparison pairs left", ignoredCount)}
			return result, nil
		}
		result.Status = "mismatch"
		result.Details = []string{"Web layout node data is empty"}
		return result, nil
	}

	bySelector := make(map[string]*WebNode, len(allWebNodes))
	for i := range allWebNodes {
		if allWebNodes[i].W <= 0 || allWebNodes[i].H <= 0 {
			continue
		}
		if _, ok := bySelector[allWebNodes[i].Selector]; !ok {
			bySelector[allWebNodes[i].Selector] = &allWebNodes[i]
		}
	}

	var issues []LayoutIssue
	for _, wn := range checkable {
		if issue, ok := viewportOverflowX(wn, viewportW); ok {
			issues = append(issues, issue)
		}
		if issue, ok := parentOverflow(wn, bySelector); ok {
			issues = append(issues, issue)
		}
	}

	result.CheckedNodes = len(checkable)
	result.IssueCount = len(issues)
	result.Issues = issues
	summary := fmt.Sprintf("Checked %d nodes at viewport %.0fx%.0f CSS px%s. Found %d layout issues.",
		len(checkable), viewportW, viewportH, viewportPresetNote(viewportW, viewportH), len(issues))
	details := []string{summary}
	for _, issue := range issues {
		details = append(details, issue.Detail)
	}
	result.Details = details
	if len(issues) == 0 {
		result.Status = "success"
	} else {
		result.Status = "mismatch"
	}
	return result, nil
}

// viewportPresetNote は、実効ビューポートが iPad プリセットと一致するときだけ
// プリセット名の補足を返す。viewport_width / viewport_height でプリセットと異なる
// サイズを指定した場合には、実際の検査サイズと食い違う「iPad 768x1024」等の注記を
// 出さない (応答の viewport_width / viewport_height が実効値なので補足は一致させる)。
func viewportPresetNote(viewportW, viewportH float64) string {
	if viewportW == DefaultIPadViewportWidth && viewportH == DefaultIPadViewportHeight {
		return " (iPad portrait 768x1024; use viewport_preset=ipad_landscape to check landscape 1024x768)"
	}
	if viewportW == DefaultIPadLandscapeViewportWidth && viewportH == DefaultIPadLandscapeViewportHeight {
		return " (iPad landscape 1024x768; use viewport_preset=ipad_portrait to check portrait 768x1024)"
	}
	return ""
}

func viewportOverflowX(wn WebNode, viewportW float64) (LayoutIssue, bool) {
	left := wn.X
	right := wn.X + wn.W
	switch {
	case left < -layoutSlopPx:
		detail := fmt.Sprintf("selector %q overflows the left edge of the viewport (x=%.2f, viewport_width=%.0f)", wn.Selector, left, viewportW)
		return LayoutIssue{Type: "viewport_overflow_x", Selector: wn.Selector, Detail: detail}, true
	case right > viewportW+layoutSlopPx:
		detail := fmt.Sprintf("selector %q overflows the right edge of the viewport (right=%.2f, viewport_width=%.0f)", wn.Selector, right, viewportW)
		return LayoutIssue{Type: "viewport_overflow_x", Selector: wn.Selector, Detail: detail}, true
	default:
		return LayoutIssue{}, false
	}
}

func parentOverflow(wn WebNode, bySelector map[string]*WebNode) (LayoutIssue, bool) {
	if wn.Parent == "" {
		return LayoutIssue{}, false
	}
	parent := bySelector[wn.Parent]
	if parent == nil {
		return LayoutIssue{}, false
	}
	pLeft, pTop := parent.X, parent.Y
	pRight, pBottom := parent.X+parent.W, parent.Y+parent.H
	cLeft, cTop := wn.X, wn.Y
	cRight, cBottom := wn.X+wn.W, wn.Y+wn.H

	var side string
	var childEdge, parentEdge float64
	switch {
	case cLeft < pLeft-layoutSlopPx:
		side, childEdge, parentEdge = "left", cLeft, pLeft
	case cRight > pRight+layoutSlopPx:
		side, childEdge, parentEdge = "right", cRight, pRight
	case cTop < pTop-layoutSlopPx:
		side, childEdge, parentEdge = "top", cTop, pTop
	case cBottom > pBottom+layoutSlopPx:
		side, childEdge, parentEdge = "bottom", cBottom, pBottom
	default:
		return LayoutIssue{}, false
	}
	detail := fmt.Sprintf("selector %q overflows its parent %q on the %s (child=%.2f, parent=%.2f)",
		wn.Selector, parent.Selector, side, childEdge, parentEdge)
	return LayoutIssue{Type: "parent_overflow", Selector: wn.Selector, Detail: detail}, true
}
