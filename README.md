# design-compare MCP Server

`design-compare` は、Figmaのデザインモックと実装されたWebページの表示内容を、画像処理および幾何データ比較のプログラムアルゴリズムによって検証するMCP（Model Context Protocol）サーバーです。

従来のピクセル単位の厳密な比較（Pixel Perfect）だけでなく、文字の内容やフォント描画の違いを無視した**「レイアウトや要素の配置テンプレート（骨組み）の再現性」**を検証するための各種モードを提供します。

---

## 1. 3つの検証モード (`mode`)

本ツールは、検証の目的に応じて以下の3つの比較アルゴリズム（モード）を提供します。LLM等の非決定性（結果がブレる）をもたらす生成AI処理は一切使用しません。

| モード名 (`mode`) | 検証アプローチ | 比較対象となるデータ | 主な用途 |
| :--- | :--- | :--- | :--- |
| **`layout_tree`**<br>(構造的比較) | **DOM構造 vs Figma構造** | FigmaとWebそれぞれの要素の幾何位置 (Bounding Box JSON) | 親要素に対する相対的なX, Y, Width, Height比率を算出し、要素の並び順や階層が合っているかをデータレベルで比較します。文字や色の違いを完全に無視して**「配置テンプレート（木）」**を検証します。 |
| **`perceptual`**<br>(知覚的画像VRT) | **空間の明暗配置パターン** | 画像パス / base64 で指定した Figma の画像 vs Web スクショ | 画像を粗く縮小（16x16）してグレースケール化し、Average Hash（aHash）の明暗パターンとして比較します。文字内容やフォント・色の違いを無視し、**「見た目の大まかなレイアウト配置」**が合っているかを判定します。 |
| **`strict`**<br>(厳密画素VRT) | **画素単位のビジュアル比較** | 画像パス / base64 で指定した Figma の画像 vs Web スクショ | pixelmatch アルゴリズム（Go 実装: github.com/orisano/pixelmatch）を用い、画素単位で色の違いを厳密に比較します（アンチエイリアスの境界は自動除外）。色味や余白、線の太さなど、**微細なビジュアル差異の検知**に使用します。**両画像はピクセル寸法（縦横の画素数）が完全に一致している必要があります。** Retina 環境の DPR（device pixel ratio）差やフルページ撮影などでサイズが異なる場合は `image size mismatch` エラーになるため、同じビューポートサイズと DPR で両スクリーンショットを撮り直すか、サイズの異なる画像も比較できる `perceptual` モードを使用してください。 |

### 差分画像 (`diff_image`) の見方 (`perceptual` モード)

`perceptual` モードのレスポンス `diff_image` は、aHash比較の結果を可視化した **256x256ピクセルのブロック図**（base64 data URI）です。画像全体が 16x16 = 256 個のセルに区切られ、各セル（aHashの1ビットに対応）は 16x16ピクセルの正方形ブロックとして描画されます。

- **赤いセル**: その位置の明暗パターン（画像全体の平均輝度に対して明るいか暗いか）が image A（Figma側）と image B（Web側）で不一致であることを示します。レイアウトの骨組みがズレている箇所の目安になります。
- **赤以外のセル**: 明暗パターンが一致したセルで、image A 側の同じ位置の平均輝度をグレースケールで表示しています。
- `generate_diff` を `false` に指定した場合は差分画像を生成せず、`diff_image` は空文字列で返されます。
- `diff_on_mismatch` を `true` に指定した場合、判定が `success` なら `diff_image` は空文字列、`mismatch` なら差分画像が返されます。
- 不一致セルがある場合、応答に機械可読な `diff_cells`（例: `[{"grid_x": 3, "grid_y": 4}]`）が付きます。座標は 16x16 グリッドの 0–15 で、行優先の決定論的順序です。セル `(x, y)` は画像の `[x/16, (x+1)/16) × [y/16, (y+1)/16)` に対応します（256x256 の `diff_image` では 16x16 ピクセルのブロック）。`generate_diff=false` でも返します。

### 差分画像 (`diff_image`) の見方 (`strict` モード)

`strict` モードのレスポンス `diff_image` は、pixelmatch が生成した差分画像（base64 data URI）です。pixelmatch の既定の配色で描画されます（マゼンタ等の色は使われません）。

- **赤いピクセル**: 両画像で色差が `threshold` を超えた差分ピクセル（差分カウント `diff_pixels` の対象）。
- **黄色いピクセル**: アンチエイリアス境界由来と判定され、差分カウントから自動除外されたピクセル。
- **それ以外の領域**: 差分がなかった箇所で、image A（Figma側）の輝度をグレースケール化して白寄りに薄めた階調で表示されます。
- `generate_diff` を `false` に指定した場合は差分画像を生成せず、`diff_image` は空文字列で返されます。このとき算出対象の差分画像が存在しないため、`diff_regions` も応答に含まれません。
- `diff_on_mismatch` を `true` に指定した場合、判定が `success` なら `diff_image` は空文字列、`mismatch` なら差分画像が返されます。
- 差分ピクセルがある場合（`generate_diff` が true のとき）、応答に機械可読な `diff_regions`（例: `[{"x": 0, "y": 0, "w": 100, "h": 100, "diff_pixels": 10000}]`）が付きます。pixelmatch 差分画像の赤いピクセルを 4 近傍連結成分に分割し、bounding box と差分ピクセル数を算出します。差分ピクセル数の多い順に最大 10 件で、座標は画像のピクセル座標です。黄色いアンチエイリアス除外ピクセルは含めません。

### 一様画像（ベタ塗り）の警告 (`warnings`)

`perceptual` モードでは、aHash が各画像自身の平均輝度で明暗を2値化するため、一様（単色のベタ塗り）画像は全セルが同一ビットになります。その結果、全面白 vs 全面黒のようなペアでも一致率100%・`success` となり、撮影失敗（真っ黒スクショ等）が無検証で合格する恐れがあります。

image A / B のいずれかが一様と検出された場合、status / match_rate は従来どおり変えず、代わりに応答へ `warnings` フィールド（例: `["degenerate aHash: image A is uniform; perceptual match may be unreliable"]`）を付けて通知します。通常の明暗パターンを持つ画像ペアではこのフィールドは含まれません。

---

## 2. パラメータリファレンス (`compare_design`)

`compare_design` ツールの全パラメータを以下に示します。`mode`（必須）には `layout_tree` / `perceptual` / `strict` のいずれかを指定します（各モードの詳細は「1. 3つの検証モード (`mode`)」を参照）。なお `strict` では比較前に両画像のピクセル寸法（縦横の画素数）が完全に一致している必要があり、異なる場合は `image size mismatch` エラーになります（同一ビューポート・DPR で撮り直すか `perceptual` モードを使用）。

**注意:** モードごとに意味やスケールが異なるパラメータ（特に `threshold`）があります。また、当該モードでは効果を持たないパラメータ（例: `layout_tree` への `min_match`、`perceptual` への `pass_rate`）を指定すると、比較を実行せずに `parameter 'X' is not supported in mode 'Y'` のツール実行エラーが返ります。パラメータと有効モードの対応表は「8. 破壊的変更」を参照してください。

### 入力データ

| パラメータ | 型 | 対象モード | 説明 |
| :--- | :--- | :--- | :--- |
| `image_path_a` | string | `perceptual` / `strict` | 参照画像 A（Figma 側）のローカルファイルパス。`image_a_base64` と排他で、どちらか一方が必須。 |
| `image_path_b` | string | `perceptual` / `strict` | 比較対象画像 B（Web 側）のローカルファイルパス。`image_b_base64` と排他で、どちらか一方が必須。 |
| `image_a_base64` | string | `perceptual` / `strict` | 参照画像 A の base64 エンコード文字列。`data:image/png;base64,...` 形式の data URI も受け付け（`;base64,` までのプレフィックスは自動で除去）。`image_path_a` と排他。 |
| `image_b_base64` | string | `perceptual` / `strict` | 比較対象画像 B の base64 エンコード文字列。`data:image/png;base64,...` 形式の data URI も受け付け（`;base64,` までのプレフィックスは自動で除去）。`image_path_b` と排他。 |
| `figma_layout` | string | `layout_tree` | Figma ノードリストの JSON 文字列（インライン指定）。`figma_layout_path` と排他で、どちらか一方が必須。 |
| `figma_layout_path` | string | `layout_tree` | Figma ノードリスト JSON ファイルのローカルパス。`figma_layout` と排他。 |
| `web_layout` | string | `layout_tree` | Web DOM ノードリストの JSON 文字列（インライン指定）。`web_layout_path` と排他で、どちらか一方が必須。 |
| `web_layout_path` | string | `layout_tree` | Web DOM ノードリスト JSON ファイルのローカルパス。`web_layout` と排他。 |

### 比較条件（閾値・除外）

| パラメータ | 型 | 対象モード | 範囲 | デフォルト | 説明 |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `threshold` | number | `layout_tree` | 0.0–1.0 | 0.15 | BoundingBox の幾何差分（相対座標・相対サイズの L2 距離）に対する許容差。 |
| `threshold` | number | `perceptual` | 1.0–100.0 | 98.0 | 後方互換のため `min_match`（一致率%）のエイリアスとして受け付ける。1.0 未満は strict モードの 0.0–1.0 スケールとの混同を防ぐためエラーになる。`min_match` との同時指定もエラー。**`min_match` の使用を推奨。** |
| `threshold` | number | `strict` | 0.0–1.0 | 0.1 | 色差の許容度（pixelmatch の color diff tolerance）。 |
| `min_match` | number | `perceptual` | 0.0–100.0 | 98.0 | 合格に必要な最低一致率（%）。実効値（`threshold` エイリアス解決後を含む）は応答の `min_match` として常に返される。 |
| `min_match` | number | `strict` | 0.0–100.0 | なし | 合格に必要な最低一致率（%）。未指定なら判定に使わず `max_diff_pixels` のみで判定する。指定時は `max_diff_pixels` と併用され、どちらか一方でも超過すると `mismatch`。 |
| `pass_rate` | number | `layout_tree` | 0.0–100.0 | 98.0 | 合格に必要な最低一致率（%）。 |
| `max_diff_pixels` | number | `strict` | 0 以上 | 0 | 許容される差分ピクセル数の上限。デフォルトの 0 は「1px でも差分があれば `mismatch`」を意味する。 |
| `ignore_nodes` | string | `layout_tree` | — | 空 | 比較から除外する Figma Node ID / Node Name / Web Selector のカンマ区切りリスト。末尾が `*` のエントリはプレフィックス一致（例: `.ad-*` は `.ad-banner` に一致）として扱われ、命名規則に従うグループを列挙なしで除外できる。どのノードにも一致しなかった除外エントリは `unmatched_ignores` として応答される（プレフィックスエントリは一致ノードが1つも無い場合のみ報告）。全ノードが除外されて比較ペアがなくなった場合は比較を実施せず、status は `skipped`（比較未実施）になる。 |
| `ignore_region` | string | 全モード | — | 空 | 除外する矩形領域。`x,y,w,h`（px 単位、`x,y >= 0`・`w,h > 0`）をセミコロン区切りで列挙（例: `10,20,100,50;200,300,80,60`）。`perceptual` / `strict` では比較前に両画像を白でマスクし、パースできた領域数は応答の `ignored_regions` に常に含まれる。画像と全く交差しない領域は `out_of_bounds_regions` として応答される。`perceptual` で両画像のサイズが異なる場合、同じ座標は各画像の絶対ピクセルとして適用され、`details` に注記が入る。`layout_tree` では BoundingBox の中心点が領域内にあるノードを両側から除外し、除外数は `ignored_count` に加算される（全件除外時は `skipped`）。 |
| `count_extra_web` | boolean | `layout_tree` | true / false | false | `true` の場合、どの Figma ノードにもマッチしなかった Web ノード（実装側の余分な要素）を一致率の分母に加算して一致率を下げる。 |
| `generate_diff` | boolean | `perceptual` / `strict` | true / false | true | `false` の場合は差分画像を生成せず、`diff_image` は空文字列で返される。`strict` の `diff_regions` も差分画像が無いため含まれない。 |
| `diff_on_mismatch` | boolean | `perceptual` / `strict` | true / false | false | `true` かつ判定が `success` の場合、応答の `diff_image` を空文字列にする（失敗時の分析用に差分画像は残しつつ、成功時のトークン消費を抑える）。`generate_diff=false` のときはもともと空。 |

### `layout_tree` 入力 JSON スキーマ

`figma_layout` / `figma_layout_path` の内容は **FigmaNode オブジェクトの配列**、`web_layout` / `web_layout_path` の内容は **WebNode オブジェクトの配列** の JSON です。

**Figma 側（FigmaNode）:**

| フィールド | 型 | 必須 | 説明 |
| :--- | :--- | :--- | :--- |
| `id` | string | ○ | Figma ノード ID。`parent` の参照先、および `ignore_nodes` の除外対象としても使用される。 |
| `name` | string | ○ | Figma ノード名。details 出力、および `ignore_nodes` の除外対象としても使用される。 |
| `x` / `y` / `w` / `h` | number | ○ | ノードの BoundingBox（Figma キャンバス上の絶対座標とサイズ）。 |
| `parent` | string | — | 親ノードの `id`。省略時は親なしとして扱われる。入力内のどの `id` にも一致しない場合は絶対座標比較へフォールバックし、応答に `unresolved_parent_refs` が付く。 |

**Web 側（WebNode）:**

| フィールド | 型 | 必須 | 説明 |
| :--- | :--- | :--- | :--- |
| `selector` | string | ○ | 要素識別子（CSS セレクタ等）。`parent` の参照先、および `ignore_nodes` の除外対象としても使用される。 |
| `x` / `y` / `w` / `h` | number | ○ | 要素の BoundingBox（ページ上の絶対座標とサイズ）。 |
| `parent` | string | — | 親要素の `selector`。省略時は親なしとして扱われる。入力内のどの `selector` にも一致しない場合は絶対座標比較へフォールバックし、応答に `unresolved_parent_refs` が付く。 |

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

`parent` を持つノードは「親の BoundingBox に対する相対的な位置・サイズ（比率 0–1）」で比較され、レスポンシブなスケール差が吸収されます。親を持たないノード（および幅・高さが 0 の親を持つノード）は絶対座標のまま比較され、比較ペアの両側で座標空間は自動的に揃えられます。`parent` が空でないのに解決できない参照は `status` を変えず `unresolved_parent_refs`（例: `Figma: '999'` / `Web: '.foo'`）として応答されます（`unmatched_ignores` と同様の誤用検出）。

`width` / `height` など `w` / `h` 以外のキー名は Unmarshal 時に無視され、幾何値がすべて 0 のノードになります。Figma または Web のいずれかで過半数のノードが幅・高さともに 0 の場合、`status` は変えず応答に `zero_geometry_warning` を付けます（`unmatched_ignores` と同様の誤用検出）。

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

従来は、モードごとに効果を持たないパラメータ（例: `perceptual` / `strict` モードへの `ignore_nodes`、`layout_tree` モードへの `max_diff_pixels`、`perceptual` モードへの `pass_rate`）を指定しても警告なく無視され、呼び出し側は「除外・合格ラインが効いているつもり」のまま判定結果を受け取る状態でした。

これを防ぐため、モードごとのパラメータ許可マップによる照合を導入しました。非対応モードでパラメータを指定すると、比較を実行せずに `parameter 'X' is not supported in mode 'Y'` のツール実行エラー (`IsError: true`) を返します。

各パラメータが有効なモード:

| パラメータ | 有効なモード |
| :--- | :--- |
| `image_path_a` / `image_path_b` / `image_a_base64` / `image_b_base64` | `perceptual`, `strict` |
| `figma_layout` / `figma_layout_path` / `web_layout` / `web_layout_path` | `layout_tree` |
| `ignore_nodes` / `count_extra_web` / `pass_rate` | `layout_tree` |
| `ignore_region` | `layout_tree`, `perceptual`, `strict` |
| `generate_diff` / `diff_on_mismatch` / `min_match` | `perceptual`, `strict` |
| `max_diff_pixels` | `strict` |
| `threshold` | `layout_tree`, `perceptual`, `strict` (`perceptual` では `min_match` の後方互換エイリアスとして 1.0–100.0 を受け付ける。`min_match` との同時指定は不可) |

**影響:** 既存クライアントがモード非対応のパラメータを渡していた場合、それらの呼び出しはエラーになります。該当パラメータを除外するか、対応するモードで指定し直してください。

### perceptual モードの `min_match` と `threshold` の同時指定

従来は `perceptual` モードで `min_match` と `threshold` を同時に渡すと、`threshold` は範囲検証もされず黙って無視され、`min_match` のみで判定されていました。呼び出し側は `threshold` が効いているつもりで結果を受け取り、誤った確信を得る状態でした（例: `threshold=0.1` は `min_match` 未指定時には strict スケールとの混同としてエラーになる値でも、同時指定では無視されていました）。

これを防ぐため、`image_path_a` と `image_a_base64` と同様に相互排他とし、同時指定時は比較を実行せずに `only one of min_match and threshold can be specified` のツール実行エラー (`IsError: true`) を返します。`threshold` 単独指定の後方互換は維持されます。

**影響:** 既存クライアントが `perceptual` モードで両方を渡していた場合、どちらか一方に揃えてください。新規呼び出しでは `min_match` を使ってください。
