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

### 差分画像 (`diff_image`) の見方 (`perceptual` モード)

`perceptual` モードのレスポンス `diff_image` は、aHash比較の結果を可視化した **256x256ピクセルのブロック図**（base64 data URI）です。画像全体が 16x16 = 256 個のセルに区切られ、各セル（aHashの1ビットに対応）は 16x16ピクセルの正方形ブロックとして描画されます。

- **赤いセル**: その位置の明暗パターン（画像全体の平均輝度に対して明るいか暗いか）が image A（Figma側）と image B（Web側）で不一致であることを示します。レイアウトの骨組みがズレている箇所の目安になります。
- **赤以外のセル**: 明暗パターンが一致したセルで、image A 側の同じ位置の平均輝度をグレースケールで表示しています。
- `generate_diff` を `false` に指定した場合は差分画像を生成せず、`diff_image` は空文字列で返されます。

---

## 2. パラメータリファレンス (`compare_design`)

`compare_design` ツールの全パラメータを以下に示します。`mode`（必須）には `layout_tree` / `perceptual` / `strict` のいずれかを指定します（各モードの詳細は「1. 3つの検証モード (`mode`)」を参照）。

**注意:** モードごとに意味やスケールが異なるパラメータ（特に `threshold`）があります。また、当該モードでは効果を持たないパラメータ（例: `layout_tree` への `ignore_region`、`perceptual` への `pass_rate`）を指定すると、比較を実行せずに `parameter 'X' is not supported in mode 'Y'` のツール実行エラーが返ります。パラメータと有効モードの対応表は「8. 破壊的変更」を参照してください。

### 入力データ

| パラメータ | 型 | 対象モード | 説明 |
| :--- | :--- | :--- | :--- |
| `image_path_a` | string | `perceptual` / `strict` | 参照画像 A（Figma 側）のローカルファイルパス。`image_a_base64` と排他で、どちらか一方が必須。 |
| `image_path_b` | string | `perceptual` / `strict` | 比較対象画像 B（Web 側）のローカルファイルパス。`image_b_base64` と排他で、どちらか一方が必須。 |
| `image_a_base64` | string | `perceptual` / `strict` | 参照画像 A の base64 エンコード文字列。`image_path_a` と排他。 |
| `image_b_base64` | string | `perceptual` / `strict` | 比較対象画像 B の base64 エンコード文字列。`image_path_b` と排他。 |
| `figma_layout` | string | `layout_tree` | Figma ノードリストの JSON 文字列（インライン指定）。`figma_layout_path` と排他で、どちらか一方が必須。 |
| `figma_layout_path` | string | `layout_tree` | Figma ノードリスト JSON ファイルのローカルパス。`figma_layout` と排他。 |
| `web_layout` | string | `layout_tree` | Web DOM ノードリストの JSON 文字列（インライン指定）。`web_layout_path` と排他で、どちらか一方が必須。 |
| `web_layout_path` | string | `layout_tree` | Web DOM ノードリスト JSON ファイルのローカルパス。`web_layout` と排他。 |

### 比較条件（閾値・除外）

| パラメータ | 型 | 対象モード | 範囲 | デフォルト | 説明 |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `threshold` | number | `layout_tree` | 0.0–1.0 | 0.15 | BoundingBox の幾何差分（相対座標・相対サイズの L2 距離）に対する許容差。 |
| `threshold` | number | `perceptual` | 1.0–100.0 | 98.0 | 後方互換のため `min_match`（一致率%）のエイリアスとして受け付ける。1.0 未満は strict モードの 0.0–1.0 スケールとの混同を防ぐためエラーになる。**`min_match` の使用を推奨。** |
| `threshold` | number | `strict` | 0.0–1.0 | 0.1 | 色差の許容度（pixelmatch の color diff tolerance）。 |
| `min_match` | number | `perceptual` | 0.0–100.0 | 98.0 | 合格に必要な最低一致率（%）。 |
| `min_match` | number | `strict` | 0.0–100.0 | なし | 合格に必要な最低一致率（%）。未指定なら判定に使わず `max_diff_pixels` のみで判定する。指定時は `max_diff_pixels` と併用され、どちらか一方でも超過すると `mismatch`。 |
| `pass_rate` | number | `layout_tree` | 0.0–100.0 | 98.0 | 合格に必要な最低一致率（%）。 |
| `max_diff_pixels` | number | `strict` | 0 以上 | 0 | 許容される差分ピクセル数の上限。デフォルトの 0 は「1px でも差分があれば `mismatch`」を意味する。 |
| `ignore_nodes` | string | `layout_tree` | — | 空 | 比較から除外する Figma Node ID / Node Name / Web Selector のカンマ区切りリスト。どのノードにも一致しなかった除外エントリは `unmatched_ignores` として応答される。 |
| `ignore_region` | string | `perceptual` / `strict` | — | 空 | 比較前に両画像を白でマスクする矩形領域。`x,y,w,h`（px 単位、`x,y >= 0`・`w,h > 0`）をセミコロン区切りで列挙（例: `10,20,100,50;200,300,80,60`）。画像と全く交差しない領域は `out_of_bounds_regions` として応答される。 |
| `count_extra_web` | boolean | `layout_tree` | true / false | false | `true` の場合、どの Figma ノードにもマッチしなかった Web ノード（実装側の余分な要素）を一致率の分母に加算して一致率を下げる。 |
| `generate_diff` | boolean | `perceptual` / `strict` | true / false | true | `false` の場合は差分画像を生成せず、`diff_image` は空文字列で返される。 |

### `layout_tree` 入力 JSON スキーマ

`figma_layout` / `figma_layout_path` の内容は **FigmaNode オブジェクトの配列**、`web_layout` / `web_layout_path` の内容は **WebNode オブジェクトの配列** の JSON です。

**Figma 側（FigmaNode）:**

| フィールド | 型 | 必須 | 説明 |
| :--- | :--- | :--- | :--- |
| `id` | string | ○ | Figma ノード ID。`parent` の参照先、および `ignore_nodes` の除外対象としても使用される。 |
| `name` | string | ○ | Figma ノード名。details 出力、および `ignore_nodes` の除外対象としても使用される。 |
| `x` / `y` / `w` / `h` | number | ○ | ノードの BoundingBox（Figma キャンバス上の絶対座標とサイズ）。 |
| `parent` | string | — | 親ノードの `id`。省略時は親なしとして扱われる。 |

**Web 側（WebNode）:**

| フィールド | 型 | 必須 | 説明 |
| :--- | :--- | :--- | :--- |
| `selector` | string | ○ | 要素識別子（CSS セレクタ等）。`parent` の参照先、および `ignore_nodes` の除外対象としても使用される。 |
| `x` / `y` / `w` / `h` | number | ○ | 要素の BoundingBox（ページ上の絶対座標とサイズ）。 |
| `parent` | string | — | 親要素の `selector`。省略時は親なしとして扱われる。 |

最小例 — `figma_layout`（`figma_layout_path` で指定するファイルも同じ形式）:

```json
[
  {"id": "1", "name": "Card", "x": 0, "y": 0, "w": 400, "h": 300},
  {"id": "2", "name": "Button", "x": 100, "y": 100, "w": 200, "h": 50, "parent": "1"}
]
```

最小例 — `web_layout`（`web_layout_path` で指定するファイルも同じ形式）:

```json
[
  {"selector": "#card", "x": 0, "y": 0, "w": 400, "h": 300},
  {"selector": "#card button.primary", "x": 100, "y": 100, "w": 200, "h": 50, "parent": "#card"}
]
```

`parent` を持つノードは「親の BoundingBox に対する相対的な位置・サイズ（比率 0–1）」で比較され、レスポンシブなスケール差が吸収されます。親を持たないノード（および幅・高さが 0 の親を持つノード）は絶対座標のまま比較され、比較ペアの両側で座標空間は自動的に揃えられます。

---

## 3. 開発とビルド方法

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

## 4. 各種ツール（Codex / Claude）へのセットアップ手順

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

## 5. 全自動でのデザイン検証ワークフロー

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

## 6. 注意・制限事項 (環境による表示の揺らぎ)

比較アルゴリズム（ハッシュ計算や幾何判定）自体はプログラムとして決定論的ですが、以下の**プラットフォーム固有のレンダリング差（非決定的な要素）**により、同じWebコードであっても実行マシンによって比較結果に微細なブレが生じる場合があります。

1. **OS/ブラウザによるフォントレンダリングの差:**
   フォントエンジン（MacのCoreText、LinuxのFreeTypeなど）の違いにより、文字のピクセル位置やアンチエイリアスの太さが微妙に変化します。
2. **ディスプレイ解像度 (DPI) の差:**
   Retinaディスプレイ環境と非Retina環境では、スクリーンショットの画素数や縮小処理時のブレンドピクセルが変化します。
3. **GPUハードウェアアクセラレーションの差:**
   ブラウザのGPUレンダリングによって、グラデーションや色の境界部分で数カラー値の微差が生じることがあります。

---

## 7. セキュリティ上の注意（信頼モデル）

本サーバーはローカル実行のMCPサーバーとして設計されています。以下のパラメータで指定された
ファイルパスはそのまま解決され、**サーバープロセスの権限で**ローカルファイルシステムから
読み込まれます（ディレクトリ制限やサンドボックス化は行われません）。

- `image_path_a` / `image_path_b`（`perceptual` / `strict` モード）
- `figma_layout_path` / `web_layout_path`（`layout_tree` モード）

そのため、信頼できない入力に由来するパスをこれらのパラメータに渡す構成（例: 不特定多数の
ユーザー入力をMCPクライアント経由で渡す、サーバーをネットワーク越しに公開する）では、
任意のローカルファイルを読み取られる可能性があります（パストラバーサル的な読み取り）。

安全に利用するための指針:

1. 本サーバーを呼び出すMCPクライアント（Claude Desktop / Claude Code / Codex など）は、
   ユーザー自身が設定した信頼できるものに限定してください。
2. 本サーバーを信頼できないネットワーク越しに公開しないでください。
3. 信頼できない入力を扱うシステムからファイルを渡す必要がある場合は、パスではなく
   内容を直接渡せる `image_a_base64` / `image_b_base64`（base64文字列）や
   `figma_layout` / `web_layout`（インラインJSON）を使用してください。

---

## 8. 破壊的変更 (Breaking Changes)

### `diff_image_path` → `diff_image` （フィールド名変更）

`perceptual` モードのレスポンスにおいて、差分画像のフィールド名を `diff_image_path` から `diff_image` に変更しました。`strict` モードと命名を統一し、いずれのモードでも base64 data URI 形式で返却するようになりました。

**影響:** 既存クライアントが `diff_image_path` を参照している場合、フィールド名の更新が必要です。

なお `generate_diff` を `false` に指定した場合は差分画像を生成せず、`diff_image` は空文字列で返されます。

### モード非対応パラメータの明示的エラー化

従来は、モードごとに効果を持たないパラメータ（例: `perceptual` / `strict` モードへの `ignore_nodes`、`layout_tree` モードへの `ignore_region`、`perceptual` モードへの `pass_rate`）を指定しても警告なく無視され、呼び出し側は「除外・合格ラインが効いているつもり」のまま判定結果を受け取る状態でした。

これを防ぐため、モードごとのパラメータ許可マップによる照合を導入しました。非対応モードでパラメータを指定すると、比較を実行せずに `parameter 'X' is not supported in mode 'Y'` のツール実行エラー (`IsError: true`) を返します。

各パラメータが有効なモード:

| パラメータ | 有効なモード |
| :--- | :--- |
| `image_path_a` / `image_path_b` / `image_a_base64` / `image_b_base64` | `perceptual`, `strict` |
| `figma_layout` / `figma_layout_path` / `web_layout` / `web_layout_path` | `layout_tree` |
| `ignore_nodes` / `count_extra_web` / `pass_rate` | `layout_tree` |
| `ignore_region` / `generate_diff` / `min_match` | `perceptual`, `strict` |
| `max_diff_pixels` | `strict` |
| `threshold` | `layout_tree`, `perceptual`, `strict` (`perceptual` では `min_match` の後方互換エイリアスとして 1.0–100.0 を受け付ける) |

**影響:** 既存クライアントがモード非対応のパラメータを渡していた場合、それらの呼び出しはエラーになります。該当パラメータを除外するか、対応するモードで指定し直してください。
