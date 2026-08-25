# design-compare MCP Server

`design-compare` は、Figmaのデザインモックと実装されたWebページの表示内容を、画像処理および幾何データ比較のプログラムアルゴリズムによって検証するMCP（Model Context Protocol）サーバーです。

従来のピクセル単位の厳密な比較（Pixel Perfect）だけでなく、文字の内容やフォント描画の違いを無視した**「レイアウトや要素の配置テンプレート（骨組み）の再現性」**を検証するための各種モードを提供します。

---

## 1. 3つの検証モード (`mode`)

本ツールは、検証の目的に応じて以下の3つの比較アルゴリズム（モード）を提供します。LLM等の非決定性（結果がブレる）をもたらす生成AI処理は一切使用しません。

| モード名 (`mode`) | 検証アプローチ | 比較対象となるデータ | 主な用途 |
| :--- | :--- | :--- | :--- |
| **`layout_tree`**<br>(構造的比較) | **DOM構造 vs Figma構造** | FigmaとWebそれぞれの要素の幾何位置 (Bounding Box JSON) | 親要素に対する相対的なX, Y, Width, Height比率を算出し、要素の並び順や階層が合っているかをデータレベルで比較します。文字や色の違いを完全に無視して**「配置テンプレート（木）」**を検証します。 |
| **`perceptual`**<br>(知覚的画像VRT) | **空間の明暗配置パターン** | Figmaの画像 vs Webスクショ of 画像パス | 画像を粗く縮小（16x16）してグレースケール化し、Average Hash（aHash）の明暗パターンとして比較します。文字内容やフォント・色の違いを無視し、**「見た目の大まかなレイアウト配置」**が合っているかを判定します。 |
| **`strict`**<br>(厳密画素VRT) | **画素単位のビジュアル比較** | Figmaの画像 vs Webスクショ of 画像パス | Mapboxの `pixelmatch` を用い、画素単位で色の違いを厳密に比較します（アンチエイリアスの境界は自動除外）。色味や余白、線の太さなど、**微細なビジュアル差異の検知**に使用します。 |

---

## 2. 開発とビルド方法

### 前提条件
- Go 1.26 以上

### ビルド手順
リポジトリルートで以下のコマンドを実行し、バイナリをコンパイルします。

```bash
go build -o design-compare
```

### 単体テストの実行
ブラウザの起動を必要としない超高速なメモリ内画像/ツリーデータ検証テストが実行できます。

```bash
go test -v ./...
```

---

## 3. 各種ツール（Codex / Claude）へのセットアップ手順

本サーバーは、Codexのローカルプラグインとして動作するほか、Claude DesktopやClaude Codeなどの一般的なMCPクライアントにインポートして使用することができます。

### A. Codex（Piプラグイン）としてインストールする場合
1. **`~/.codex/config.toml` にローカルマーケットプレイスを設定:**
   ```toml
   [marketplaces.design-compare-marketplace]
   source_type = "local"
   source = "/Users/username/workspace/design-compare" # リポジトリへの絶対パス
   ```
2. **Codex CLIでプラグインをインストール:**
   ```bash
   codex plugin add design-compare@design-compare-marketplace
   ```

### B. Claude Desktop に追加する場合
設定ファイル（Mac: `~/Library/Application Support/Claude/claude_desktop_config.json`）の `mcpServers` セクションに以下の設定を追記します。

```json
{
  "mcpServers": {
    "design-compare": {
      "command": "/Users/username/workspace/design-compare/design-compare"
    }
  }
}
```

### C. Claude Code (CLI) に追加する場合
以下のコマンドを実行して、MCPサーバーとして追加します。

```bash
claude mcp add design-compare "/Users/username/workspace/design-compare/design-compare"
```

---

## 4. 全自動でのデザイン検証ワークフロー

有効化されると、AIエージェントは自動的に他のツール（Figma MCPやChrome-DevTools MCP）と連携して、ログイン状態などを維持したまま全自動でVRT/構造検証を実行します。

### プロンプト例:
> 「Figmaのこのデザイン（Figma URL）と、ローカルの http://localhost:3000/dashboard を比較して。テンプレート（配置）が同じかどうかを検証したい」

### AIの自律的な処理フロー:
1. **デザインデータの取得:**
   AIが `figma` MCPを使ってデザイン画像やレイアウト座標（JSON）を取得。
2. **実装データの取得:**
   AIが `chrome-devtools` MCP等を使ってWebページを自動操作（必要なら自動ログイン）し、実装された要素のスクショやDOM座標（JSON）を取得。
3. **比較の実行:**
   AIが本ツールの `compare_design` を呼び出して比較を実行。
   * 例: `compare_design(mode="layout_tree", figma_layout="...", web_layout="...", pass_rate=95.0)`
4. **結果の分析とコード修正:**
   AIが一致率や差分を分析し、レイアウトがズレているCSSやHTMLを自動で修正・再検証します。

---

## 5. 注意・制限事項 (環境による表示の揺らぎ)

比較アルゴリズム（ハッシュ計算や幾何判定）自体はプログラムとして決定論的ですが、以下の**プラットフォーム固有のレンダリング差（非決定的な要素）**により、同じWebコードであっても実行マシンによって比較結果に微細なブレが生じる場合があります。

1. **OS/ブラウザによるフォントレンダリングの差:**
   フォントエンジン（MacのCoreText、LinuxのFreeTypeなど）の違いにより、文字のピクセル位置やアンチエイリアスの太さが微妙に変化します。
2. **ディスプレイ解像度 (DPI) の差:**
   Retinaディスプレイ環境と非Retina環境では、スクリーンショットの画素数や縮小処理時のブレンドピクセルが変化します。
3. **GPUハードウェアアクセラレーションの差:**
   ブラウザのGPUレンダリングによって、グラデーションや色の境界部分で数カラー値の微差が生じることがあります。
