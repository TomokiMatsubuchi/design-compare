package comparator

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
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
	MatchRate float64  `json:"match_rate"`
	Status    string   `json:"status"`
	Details   []string `json:"details"`
}

// CompareLayoutTrees performs structural layout comparison on element hierarchies
func CompareLayoutTrees(figmaJSON, webJSON string, tolerance float64) (*LayoutTreeResult, error) {
	if tolerance <= 0 {
		tolerance = 0.15 // デフォルト許容差 15%
	}

	var fNodes []FigmaNode
	if err := json.Unmarshal([]byte(figmaJSON), &fNodes); err != nil {
		return nil, fmt.Errorf("failed to parse Figma layout JSON: %w", err)
	}

	var wNodes []WebNode
	if err := json.Unmarshal([]byte(webJSON), &wNodes); err != nil {
		return nil, fmt.Errorf("failed to parse Web layout JSON: %w", err)
	}

	if len(fNodes) == 0 || len(wNodes) == 0 {
		return &LayoutTreeResult{
			MatchRate: 0,
			Status:    "mismatch",
			Details:   []string{"Empty layout node data provided"},
		}, nil
	}

	// 2. マッチング処理
	var matchedCount int
	var totalCompared int
	var details []string

	// 各Figmaノードと、Webの対応する要素を探して比較
	// 簡単のため、Figmaの各要素と、Web側で最も「幾何学的位置（相対位置）が近いもの」を対応付ける
	for _, fn := range fNodes {
		totalCompared++
		foundMatch := false

		// 親要素に対する相対サイズと相対座標を計算
		parentF := getFigmaParent(fn, fNodes)
		relX_f, relY_f, relW_f, relH_f := getFigmaRelativeCoords(fn, parentF)

		var bestMatchSelector string
		var minDiff float64 = math.MaxFloat64

		for _, wn := range wNodes {
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
				bestMatchSelector = wn.Selector
			}
		}

		// 許容誤差（tolerance）以内なら「テンプレートとして同じ位置・サイズで配置されている」と判定
		if minDiff <= tolerance {
			matchedCount++
			foundMatch = true
		} else {
			details = append(details, fmt.Sprintf("Figma Node '%s' (type config mismatch or position shifted) did not match closest Web element '%s' (diff: %.2f)", fn.Name, bestMatchSelector, minDiff))
		}

		_ = foundMatch
	}

	matchRate := (float64(matchedCount) / float64(totalCompared)) * 100.0
	status := "success"
	if matchRate < 90.0 { // 合格ライン 90%
		status = "mismatch"
	}

	// 構造化ディテール作成
	summaryDetail := fmt.Sprintf("Matched %d out of %d layout nodes.", matchedCount, totalCompared)
	details = append([]string{summaryDetail}, details...)

	return &LayoutTreeResult{
		MatchRate: matchRate,
		Status:    status,
		Details:   details,
	}, nil
}

func groupFigmaByParent(nodes []FigmaNode) map[string][]FigmaNode {
	g := make(map[string][]FigmaNode)
	for _, n := range nodes {
		g[n.Parent] = append(g[n.Parent], n)
	}
	// ソートして順序を決定論的に
	for k := range g {
		sort.Slice(g[k], func(i, j int) bool {
			if g[k][i].Y == g[k][j].Y {
				return g[k][i].X < g[k][j].X
			}
			return g[k][i].Y < g[k][j].Y
		})
	}
	return g
}

func groupWebByParent(nodes []WebNode) map[string][]WebNode {
	g := make(map[string][]WebNode)
	for _, n := range nodes {
		g[n.Parent] = append(g[n.Parent], n)
	}
	for k := range g {
		sort.Slice(g[k], func(i, j int) bool {
			if g[k][i].Y == g[k][j].Y {
				return g[k][i].X < g[k][j].X
			}
			return g[k][i].Y < g[k][j].Y
		})
	}
	return g
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
		return 0, 0, 0, 0
	}
	// 親に対する相対比率（比率を統一することで、レスポンシブなサイズ違いを吸収）
	return (n.X - parent.X) / parent.W, (n.Y - parent.Y) / parent.H, n.W / parent.W, n.H / parent.H
}

func getWebRelativeCoords(n WebNode, parent *WebNode) (x, y, w, h float64) {
	if parent == nil {
		return n.X, n.Y, n.W, n.H
	}
	if parent.W == 0 || parent.H == 0 {
		return 0, 0, 0, 0
	}
	return (n.X - parent.X) / parent.W, (n.Y - parent.Y) / parent.H, n.W / parent.W, n.H / parent.H
}
