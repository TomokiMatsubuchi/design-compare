package comparator

import (
	"encoding/json"
	"fmt"
	"math"
)

type FigmaNode struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	W      float64 `json:"w"`
	H      float64 `json:"h"`
	Parent string  `json:"parent,omitempty"`
}

type WebNode struct {
	Selector string  `json:"selector"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	W        float64 `json:"w"`
	H        float64 `json:"h"`
	Parent   string  `json:"parent,omitempty"`
}

type LayoutTreeResult struct {
	MatchRate        float64  `json:"match_rate"`
	Status           string   `json:"status"`
	Details          []string `json:"details"`
	MatchedNodes     int      `json:"matched_nodes"`
	TotalNodes       int      `json:"total_nodes"`
	IgnoredCount     int      `json:"ignored_count"`
	UnmatchedIgnores []string `json:"unmatched_ignores,omitempty"`
}

// CompareLayoutTrees performs structural layout comparison on element hierarchies.
// countExtraWeb が true の場合、どの Figma ノードにもマッチしなかった Web ノード
// （実装側の余分な要素）を一致率の分母 (totalCompared) に加算する。
func CompareLayoutTrees(figmaJSON, webJSON string, tolerance float64, passRate float64, ignoreList []string, countExtraWeb bool) (*LayoutTreeResult, error) {
	if tolerance < 0 {
		tolerance = 0.15 // デフォルト許容差 15% (0 は有効値: 完全一致を要求)
	}
	if passRate < 0 {
		passRate = 98.0 // デフォルト合格ライン 98% (0 は有効値: 常に合格)
	}

	var fNodes []FigmaNode
	if err := json.Unmarshal([]byte(figmaJSON), &fNodes); err != nil {
		return nil, fmt.Errorf("failed to parse Figma layout JSON: %w", err)
	}

	var wNodes []WebNode
	if err := json.Unmarshal([]byte(webJSON), &wNodes); err != nil {
		return nil, fmt.Errorf("failed to parse Web layout JSON: %w", err)
	}

	// ignoreList に基づいてノードを除外し、適用結果（除外数・無効なエントリ）を集計する
	var ignoredCount int
	var unmatchedIgnores []string
	if len(ignoreList) > 0 {
		ignoreMap := make(map[string]bool)
		for _, item := range ignoreList {
			ignoreMap[item] = true
			ignoreMap[cleanNodeName(item)] = true
		}

		// ノード側の識別値（raw + clean）を集め、ignore エントリが実際に
		// どれかのノードに一致したかの判定に使う
		nodeValues := make(map[string]bool)
		for _, fn := range fNodes {
			nodeValues[fn.ID] = true
			nodeValues[fn.Name] = true
			nodeValues[cleanNodeName(fn.ID)] = true
			nodeValues[cleanNodeName(fn.Name)] = true
		}
		for _, wn := range wNodes {
			nodeValues[wn.Selector] = true
			nodeValues[cleanNodeName(wn.Selector)] = true
		}

		var filteredFNodes []FigmaNode
		for _, fn := range fNodes {
			if ignoreMap[fn.ID] || ignoreMap[fn.Name] || ignoreMap[cleanNodeName(fn.ID)] || ignoreMap[cleanNodeName(fn.Name)] {
				ignoredCount++
				continue
			}
			filteredFNodes = append(filteredFNodes, fn)
		}
		fNodes = filteredFNodes

		var filteredWNodes []WebNode
		for _, wn := range wNodes {
			if ignoreMap[wn.Selector] || ignoreMap[cleanNodeName(wn.Selector)] {
				ignoredCount++
				continue
			}
			filteredWNodes = append(filteredWNodes, wn)
		}
		wNodes = filteredWNodes

		// どのノードにも一致しなかった ignore エントリ（スペルミス等）を検出する
		for _, item := range ignoreList {
			if !nodeValues[item] && !nodeValues[cleanNodeName(item)] {
				unmatchedIgnores = append(unmatchedIgnores, item)
			}
		}
	}

	if len(fNodes) == 0 || len(wNodes) == 0 {
		// ignore_nodes により比較対象が全件除外された場合は、実装不一致と区別して
		// skipped ステータスで「比較できなかった」ことを明示する。
		if ignoredCount > 0 {
			side := "Figma and Web"
			switch {
			case len(fNodes) == 0 && len(wNodes) != 0:
				side = "Figma"
			case len(fNodes) != 0 && len(wNodes) == 0:
				side = "Web"
			}
			return &LayoutTreeResult{
				MatchRate:        0,
				Status:           "skipped",
				Details:          []string{fmt.Sprintf("All %s nodes were excluded by ignore_nodes (%d nodes ignored in total); no comparison pairs left", side, ignoredCount)},
				IgnoredCount:     ignoredCount,
				UnmatchedIgnores: unmatchedIgnores,
			}, nil
		}
		// どちら側の入力が空かを明示する。
		var emptyDetail string
		switch {
		case len(fNodes) == 0 && len(wNodes) == 0:
			emptyDetail = "Both Figma and Web layout node data are empty"
		case len(fNodes) == 0:
			emptyDetail = "Figma layout node data is empty"
		default:
			emptyDetail = "Web layout node data is empty"
		}
		return &LayoutTreeResult{
			MatchRate:        0,
			Status:           "mismatch",
			Details:          []string{emptyDetail},
			IgnoredCount:     ignoredCount,
			UnmatchedIgnores: unmatchedIgnores,
		}, nil
	}

	// 2. マッチング処理
	var matchedCount int
	var totalCompared int
	var matchedPairDetails []string
	var mismatchDetails []string
	var extraWebDetails []string

	// 使用済みWebノードを追跡し、1対1対応を保証する（重複マッチによる一致率水増しを防ぐ）
	usedWeb := make(map[int]bool)

	// 各Figmaノードと、Webの対応する要素を探して比較
	// 簡単のため、Figmaの各要素と、Web側で最も「幾何学的位置（相対位置）が近いもの」を対応付ける
	for _, fn := range fNodes {
		totalCompared++
		// 親要素に対する相対サイズと相対座標を計算
		parentF := getFigmaParent(fn, fNodes)
		relX_f, relY_f, relW_f, relH_f := getFigmaRelativeCoords(fn, parentF)

		var bestMatchSelector string
		var bestMatchIdx int = -1
		var minDiff float64 = math.MaxFloat64
		var bestDiffX, bestDiffY, bestDiffW, bestDiffH float64

		for wi, wn := range wNodes {
			// 使用済みのWebノードは候補から除外（1対1対応の保証）
			if usedWeb[wi] {
				continue
			}

			parentW := getWebParent(wn, wNodes)
			relX_w, relY_w, relW_w, relH_w := getWebRelativeCoords(wn, parentW)

			// 相対的な位置・サイズの差を計算 (L2距離)
			diffX := relX_f - relX_w
			diffY := relY_f - relY_w
			diffW := relW_f - relW_w
			diffH := relH_f - relH_w
			diff := math.Sqrt(diffX*diffX + diffY*diffY + diffW*diffW + diffH*diffH)

			if diff < minDiff {
				minDiff = diff
				bestDiffX, bestDiffY, bestDiffW, bestDiffH = diffX, diffY, diffW, diffH
				bestMatchSelector = wn.Selector
				bestMatchIdx = wi
			}
		}

		// 許容誤差（tolerance）以内なら「テンプレートとして同じ位置・サイズで配置されている」と判定
		if minDiff <= tolerance {
			matchedCount++
			// マッチしたWebノードを使用済みとしてマーク
			if bestMatchIdx >= 0 {
				usedWeb[bestMatchIdx] = true
			}
			// 一致したペア（Figmaノード名 ↔ Webセレクタ）を details に出力する。
			matchedPairDetails = append(matchedPairDetails, fmt.Sprintf("Matched: '%s' ↔ '%s'", fn.Name, bestMatchSelector))
		} else {
			mismatchDetails = append(mismatchDetails, fmt.Sprintf("Figma Node '%s' (type config mismatch or position shifted) did not match closest Web element '%s' (diff: %.2f, dx: %.2f, dy: %.2f, dw: %.2f, dh: %.2f)", fn.Name, bestMatchSelector, minDiff, bestDiffX, bestDiffY, bestDiffW, bestDiffH))
		}
	}

	// 1対1マッチング後に使用されなかったWebノード（実装側の余分な要素）を報告する
	for wi, wn := range wNodes {
		if !usedWeb[wi] {
			extraWebDetails = append(extraWebDetails, fmt.Sprintf("Web Node '%s' did not match any Figma node (extra element in implementation)", wn.Selector))
		}
	}

	// countExtraWeb=true の場合、未マッチのWebノード（実装側の余分な要素）を一致率の
	// 分母 (totalCompared) に加算し、余分な要素が一致率に反映されるようにする。
	if countExtraWeb {
		for wi := range wNodes {
			if !usedWeb[wi] {
				totalCompared++
			}
		}
	}

	matchRate := (float64(matchedCount) / float64(totalCompared)) * 100.0
	status := "success"
	if matchRate < passRate { // 合格ライン（パラメータ化）
		status = "mismatch"
	}

	// 構造化ディテール作成
	summaryDetail := fmt.Sprintf("Matched %d out of %d layout nodes.", matchedCount, totalCompared)

	details := []string{summaryDetail}
	details = append(details, matchedPairDetails...)
	details = append(details, mismatchDetails...)
	details = append(details, extraWebDetails...)

	return &LayoutTreeResult{
		MatchRate:        matchRate,
		Status:           status,
		Details:          details,
		MatchedNodes:     matchedCount,
		TotalNodes:       totalCompared,
		IgnoredCount:     ignoredCount,
		UnmatchedIgnores: unmatchedIgnores,
	}, nil
}

func getFigmaParent(node FigmaNode, list []FigmaNode) *FigmaNode {
	if node.Parent == "" {
		return nil
	}
	for _, n := range list {
		if n.ID == node.Parent {
			return &n
		}
	}
	return nil
}

func getWebParent(node WebNode, list []WebNode) *WebNode {
	if node.Parent == "" {
		return nil
	}
	for _, n := range list {
		if n.Selector == node.Parent {
			return &n
		}
	}
	return nil
}

func getFigmaRelativeCoords(n FigmaNode, parent *FigmaNode) (x, y, w, h float64) {
	if parent == nil {
		// 親が無い場合は絶対値をそのまま返す（または仮想的な全体キャンバスに対する比率）
		return n.X, n.Y, n.W, n.H
	}
	if parent.W == 0 || parent.H == 0 {
		// サイズ0の親に対する相対比率は定義できない（0除算相当）。
		// 従来は (0,0,0,0) を返すため両側が常に一致扱いになり一致率が水増しされていた。
		// 絶対座標にフォールバックして誤一致を防ぐ。
		return n.X, n.Y, n.W, n.H
	}
	// 親に対する相対比率（比率を統一することで、レスポンシブなサイズ違いを吸収）
	return (n.X - parent.X) / parent.W, (n.Y - parent.Y) / parent.H, n.W / parent.W, n.H / parent.H
}

func getWebRelativeCoords(n WebNode, parent *WebNode) (x, y, w, h float64) {
	if parent == nil {
		return n.X, n.Y, n.W, n.H
	}
	if parent.W == 0 || parent.H == 0 {
		// サイズ0の親に対する相対比率は定義できない（0除算相当）。
		// 絶対座標にフォールバックして誤一致を防ぐ。
		return n.X, n.Y, n.W, n.H
	}
	return (n.X - parent.X) / parent.W, (n.Y - parent.Y) / parent.H, n.W / parent.W, n.H / parent.H
}

func cleanNodeName(s string) string {
	if len(s) > 0 && (s[0] == '.' || s[0] == '#') {
		return s[1:]
	}
	return s
}
