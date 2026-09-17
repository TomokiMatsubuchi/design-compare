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
	Status           string        `json:"status"` // success | mismatch | skipped
	ViewportWidth    float64       `json:"viewport_width"`
	ViewportHeight   float64       `json:"viewport_height"`
	CheckedNodes     int           `json:"checked_nodes"`
	IssueCount       int           `json:"issue_count"`
	Issues           []LayoutIssue `json:"issues,omitempty"`
	Details          []string      `json:"details"`
	IgnoredCount     int           `json:"ignored_count"`
	UnmatchedIgnores []string      `json:"unmatched_ignores,omitempty"`
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
	var wNodes []WebNode
	if err := json.Unmarshal([]byte(webJSON), &wNodes); err != nil {
		return nil, fmt.Errorf("failed to parse Web layout JSON: %w", err)
	}
	if viewportW <= 0 || viewportH <= 0 {
		return nil, fmt.Errorf("viewport_width and viewport_height must be greater than 0")
	}

	var ignoredCount int
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

	if len(ignoreRegions) > 0 {
		var filtered []WebNode
		for _, wn := range wNodes {
			if boundingBoxCenterInRegions(wn.X, wn.Y, wn.W, wn.H, ignoreRegions) {
				ignoredCount++
				continue
			}
			filtered = append(filtered, wn)
		}
		wNodes = filtered
	}

	var checkable []WebNode
	for _, wn := range wNodes {
		if wn.W <= 0 || wn.H <= 0 {
			continue
		}
		checkable = append(checkable, wn)
	}

	result := &LayoutIntegrityResult{
		ViewportWidth:    viewportW,
		ViewportHeight:   viewportH,
		IgnoredCount:     ignoredCount,
		UnmatchedIgnores: unmatchedIgnores,
	}

	if len(checkable) == 0 {
		if ignoredCount > 0 {
			result.Status = "skipped"
			result.Details = []string{fmt.Sprintf("All Web nodes were excluded by ignore_nodes / ignore_region (%d nodes ignored in total); no comparison pairs left", ignoredCount)}
			return result, nil
		}
		result.Status = "mismatch"
		result.Details = []string{"Web layout node data is empty"}
		return result, nil
	}

	bySelector := make(map[string]*WebNode, len(checkable))
	for i := range checkable {
		if _, ok := bySelector[checkable[i].Selector]; !ok {
			bySelector[checkable[i].Selector] = &checkable[i]
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
	summary := fmt.Sprintf("Checked %d nodes at viewport %.0fx%.0f CSS px (iPad portrait 768x1024, landscape 1024x768). Found %d layout issues.",
		len(checkable), viewportW, viewportH, len(issues))
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
