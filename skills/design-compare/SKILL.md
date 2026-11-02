---
name: "design-compare"
description: "MANDATORY. This skill MUST be activated for any tasks involving implementing, modifying, or styling web pages/components based on Figma designs. It enforces verifying the visual and structural fidelity using `design-compare`."
---

# Design Compare (Layout and Visual Verification) Skill

This skill enforces verification of visual and structural fidelity by comparing Figma designs with Web implementations.

> [!IMPORTANT]
> **CRITICAL MANDATORY RULE:**
> You **MUST** run the verification workflow described below before declaring any Figma-to-Web implementation task complete. **Do NOT report task completion without executing `design-compare` and outputting its comparison results.**

## Prerequisites

Ensure the following MCP servers are configured:
1. **Figma Dev Mode MCP Server**: To download figma frames/nodes and retrieve layout metadata.
2. **chrome-devtools-mcp** (or **playwright-cli**): To capture web screenshots and extract DOM element geometry (Bounding Boxes).
3. **design-compare**: To perform layout-tree, perceptual, strict, or layout-integrity comparisons.

## Core Verification Workflow

When asked to verify how well a web page implementation matches a Figma design, execute the following multi-mcp workflow:

**Working directory for artifacts:** Create one unique directory per verification run (e.g. `/tmp/design-compare-<unique-suffix>/` where `<unique-suffix>` is a timestamp, PID, or random id). Save every capture under that directory and reuse the **same filenames** across Steps 1–3. Do **not** write fixed names under `/tmp/` itself — `compare_design` may run in parallel, and reusing `/tmp/figma.png` (etc.) can mix captures across calls or orientations.

Example layout for one run (`RUN=/tmp/design-compare-<unique-suffix>`):
- `$RUN/figma.png` — Figma node image
- `$RUN/web.png` — Web screenshot
- `$RUN/figma_layout.json` — Figma node list for `layout_tree` (Step 3)
- `$RUN/web_layout.json` — Web DOM boxes for `layout_tree` (Step 3)
- `$RUN/web-portrait.json` — DOM boxes at iPad portrait (Step 5)
- `$RUN/web-landscape.json` — DOM boxes at iPad landscape (Step 5)

### Step 1: Gather Figma Design Data
Use the **Figma MCP server** to fetch:
- Node image: Save to `$RUN/figma.png` (e.g. `/tmp/design-compare-20261029-a1b2/figma.png`).
- Node metadata: Extract the layout bounding boxes and structural details.

### Step 2: Gather Web Implementation Data
Use your browser automation MCP tools (chrome-devtools-mcp or playwright-cli):
- Capture page screenshot: Save to `$RUN/web.png` (e.g. `/tmp/design-compare-20261029-a1b2/web.png`).
- Extract DOM bounding boxes into a **WebNode** JSON array for `web_layout` / `web_layout_path`.

**WebNode schema** (same JSON for `layout_tree` and `layout_integrity`):

| Field | Required | Meaning |
| :--- | :--- | :--- |
| `selector` | yes | Element id used for matching and as the target of `parent` |
| `x` / `y` / `w` / `h` | yes | Bounding box in **absolute CSS px** (document / page coordinates shared by every node) |
| `parent` | no | Parent node's `selector` string (not a DOM parentNode walk result unless that string matches an entry's `selector`) |

Keys are `w` / `h` — **not** `width` / `height`. Coordinates must live in one shared absolute space; do **not** pass parent-relative offsets as `x` / `y` (the comparator derives parent ratios itself via `getWebRelativeCoords`). Do not mix scrolled and unscrolled samples in the same array.

**Copy-paste extraction snippet** (run in the page; add `window.scrollX` / `window.scrollY` to `getBoundingClientRect()`):

```javascript
// nodes: [{ selector: '.frame' }, { selector: '.card', parent: '.frame' }, ...]
function extractWebLayout(nodes) {
  return nodes.map(({ selector, parent }) => {
    const el = document.querySelector(selector);
    if (!el) throw new Error(`missing: ${selector}`);
    const r = el.getBoundingClientRect();
    const node = {
      selector,
      x: r.left + window.scrollX,
      y: r.top + window.scrollY,
      w: r.width,
      h: r.height,
    };
    if (parent) node.parent = parent;
    return node;
  });
}
JSON.stringify(extractWebLayout([
  { selector: '.frame' },
  { selector: '.card', parent: '.frame' },
]));
```

Pass the resulting array as `web_layout` (or write it to a file and use `web_layout_path`). Reuse the same shape for Step 5 `layout_integrity` (recapture after resizing the viewport).

### Step 3: Run the Comparison
Call the `compare_design` tool from **design-compare** using the appropriate mode:

*   **Mode: `layout_tree` (Recommended for pure structural/template validation)**
    *   Use when you want to verify if components are placed in the correct hierarchy and alignment without worrying about precise text changes, fonts, or minor coloring.
    *   Parameters: `mode: "layout_tree"`, plus either `figma_layout: "[...]"` **or** `figma_layout_path: "/tmp/design-compare-<unique-suffix>/figma_layout.json"`, and either `web_layout: "[...]"` **or** `web_layout_path: "/tmp/design-compare-<unique-suffix>/web_layout.json"`.
    *   `figma_layout` and `figma_layout_path` are mutually exclusive (provide exactly one); same for `web_layout` and `web_layout_path`.
    *   For pages with many nodes, write the browser/Figma tool output to files under `$RUN` and pass `*_path` instead of inlining large JSON (avoids token bloat and escaping mistakes).
*   **Mode: `perceptual` (Recommended for visual template verification)**
    *   Use when comparing images, but you want to ignore minor sub-pixel rendering quirks, text content differences, or anti-aliasing details. It scales down and checks macro-layout blocks.
    *   Parameters: `mode: "perceptual"`, `image_path_a: "/tmp/design-compare-<unique-suffix>/figma.png"`, `image_path_b: "/tmp/design-compare-<unique-suffix>/web.png"`
*   **Mode: `strict` (Recommended for visual regression / detail verification)**
    *   Use when checking exact visual styles (color matches, border widths, exact layouts) using Pixelmatch.
    *   Parameters: `mode: "strict"`, `image_path_a: "/tmp/design-compare-<unique-suffix>/figma.png"`, `image_path_b: "/tmp/design-compare-<unique-suffix>/web.png"`

### Step 4: Verify Interaction States (Scroll & Button Interactions)
Static rendering alone is not enough. You **MUST** also confirm that the UI/UX does not break (no layout collapse, overlap, clipping, or overflow) after user interactions such as scrolling and pressing buttons.

For each meaningful interactive state, re-run the capture → compare cycle:
1. **Trigger the interaction** with your browser tools (e.g. `chrome-devtools-mcp` / `playwright-cli`): scroll the page/containers to top, middle, and bottom, and click buttons or controls that change the view (modals, dropdowns, accordions, tabs, drawers, sticky headers, etc.).
2. **Re-capture** the resulting screenshot and DOM bounding boxes for that post-interaction state.
3. **Re-compare** against the matching Figma state using `compare_design`:
    *   Use `layout_tree` to confirm elements keep their correct hierarchy and alignment (nothing overlaps, shifts unexpectedly, or escapes its container) after the interaction.
    *   Use `perceptual` or `strict` when a Figma mock exists for that specific state (e.g. an "open modal" or "scrolled" frame) to verify the visuals still match.
4. **Watch for breakage signals**: sudden large drops in `match_rate`, nodes appearing far outside the parent's bounding box, or diff regions concentrated around the elements you just interacted with usually indicate a broken interaction state.

If any interaction state fails the threshold, fix the CSS/HTML (e.g. overflow handling, z-index, sticky/fixed positioning, responsive breakpoints) and re-run this step until every state passes.

### Step 5: Verify iPad / Tablet Layout Integrity (Web DOM only — no Figma)

A pixel-perfect match against Figma desktop/mobile frames does **NOT** prove that the layout survives tablet widths. You **MUST** additionally verify layout integrity at **both** iPad orientations:

Use **separate files** per orientation under the same `$RUN` directory (never overwrite one capture with the other — portrait and landscape checks may run in parallel):

1. **Portrait (default):** resize the browser to **768×1024 CSS px**, recapture **DOM bounding boxes only** (not Figma) into `$RUN/web-portrait.json`, then call `compare_design` with `mode: "layout_integrity"` and `web_layout_path: "/tmp/design-compare-<unique-suffix>/web-portrait.json"` (or inline `web_layout` from that capture).
2. **Landscape:** resize to **1024×768 CSS px**, recapture DOM boxes into `$RUN/web-landscape.json`, then call again with `viewport_preset: "ipad_landscape"` (or `viewport_width: 1024` and `viewport_height: 768`) and `web_layout_path: "/tmp/design-compare-<unique-suffix>/web-landscape.json"`. Do **not** reuse portrait coordinates or the portrait file for the landscape check.
3. Treat `viewport_overflow_x` and `parent_overflow` as failures. Vertical overflow of the viewport is expected page scroll and is not a failure.
4. **Confirm parent checks ran:** each response includes `parent_checked_nodes` (how many nodes had a resolvable `parent` and ran containment). If it is `0`, `parent_overflow` was **not** applied — do **not** treat `status: success` as proof that parent overflow passed. Recapture with correct `parent` selectors (matching another node's `selector` in the same JSON). Non-empty `unresolved_parent_refs` (same labels as `layout_tree`, e.g. `Web: '#card-typo'`) means a `parent` value did not resolve; fix the typo or missing parent node before claiming success.
5. Fix overflow / parent overflow, then re-run **both** orientations (each with its own file) until each returns `status: success` with no `unresolved_parent_refs`, and `parent_checked_nodes > 0` when the DOM has parent/child structure to verify. `mismatch` or `skipped` leaves verification incomplete.

Intentional horizontal scroll regions (carousels, `overflow-x: auto` tables, marquees, and similar) still fire `viewport_overflow_x` on their children when a child's box extends past the viewport width, even if the scroll container itself fits inside the viewport. Do **not** treat that as a CSS bug to "fix" — exclude those children with `ignore_nodes` (e.g. `.carousel-*`) or `ignore_region`, and confirm the exclusion matched via `unmatched_ignores` / `unmatched_ignore_regions` (empty when every ignore took effect).

### Step 6: Analyze Results and Fix Code
- Check the output `status` and, for Figma-compare modes, `match_rate`.
- In `layout_tree` mode, missing required keys (`id`/`name`/`selector`/`x`/`y`/`w`/`h`) or empty identifiers are input errors. If `zero_geometry_warning` is present despite valid keys, most nodes have explicit `w`/`h` of 0 — do not treat a 100% match as valid until the geometry is corrected.
- In `layout_integrity` mode, if `zero_geometry_warning` is present despite `status: success`, most nodes have explicit `w`/`h` of 0 and were excluded from checks (`checked_nodes` is smaller than the input) — do not treat success as verification complete until the geometry is corrected.
- In `layout_tree` mode, also check misuse-detection fields that do **not** change `status` / `match_rate`: if `unresolved_parent_refs` or `unmatched_ignores` is non-empty, the requested relative compare / exclusion did not take effect — fix the `parent` references and `ignore_nodes` spellings and re-run; do **not** treat verification as complete until they are empty. If `absolute_mode_pairs` > 0, those pairs were compared in raw px (`threshold` applies as px, not ratio), so a below-threshold `pass_rate` is not necessarily an implementation bug.
- If the response includes a non-empty `warnings` array (perceptual / strict only; omitted when empty), do **not** treat `status: success` as verification complete. Act on the warning content: recapture blank/uniform screenshots (all-white or all-black), fix viewport or aspect-ratio mismatches, or explicitly set `max_diff_pixels` when a `strict` call that specified only `min_match` returned `mismatch` because the default of 0 dominated the pass/fail decision (this warning is not emitted on success). See README §1 (`warnings`) for the full list.
- If discrepancies exist, inspect the output details and the `diff_image` field (perceptual / strict modes only; layout_tree results have no diff image — rely on `details` instead; when present it is a base64 PNG data URI returned in the response; no temporary file is written). For how the visualization is colored, see README §1. In `strict` mode, prefer `generate_diff=false` on iterative verify loops: `diff_image` stays empty while `diff_regions` still reports mismatch bounding boxes (same as `perceptual` returning `diff_cells` without a large base64 payload). Then correct the HTML/CSS code, and re-run the validation to ensure the layout matches the template.

## Guardrails & Best Practices

1. **Mandatory Execution**: You are strictly prohibited from submitting a final response or claiming success until you have successfully executed `compare_design` and confirmed the match rate meets the required threshold. For `perceptual` / `strict`, verification is also incomplete while `warnings` is non-empty — resolve those warnings (recapture, fix aspect/viewport, or — when `strict` returned `mismatch` with only `min_match` set and the default `max_diff_pixels` of 0 dominated — set `max_diff_pixels` explicitly) before claiming success.
2. **Match Threshold & Exclusions**: The passing criteria depend on the mode:
    *   `layout_tree` (judged by `pass_rate`) and `perceptual` (judged by `min_match`): the default passing threshold is **98.0%**. If the match rate is below 98.0%, you must analyze the diff details, fix the implementation, and run the comparison again until it passes.
    *   `strict` is **not** judged by match rate by default. Its default criterion is `max_diff_pixels` = **0** (zero differing pixels is required, so even a 99.9% match rate results in `mismatch` with a single differing pixel). To judge strict results by match rate, specify `min_match` together with an appropriate `max_diff_pixels` (the default of 0 allows no differing pixels at all, so a high match rate alone still yields `mismatch`).
    *   `layout_integrity` has no `match_rate`. It passes only when `issue_count` is 0 (`status: success`). Also confirm `parent_checked_nodes > 0` when parent/child structure should be verified — `0` means `parent_overflow` never ran. Non-empty `zero_geometry_warning` means most nodes were zero-geometry and excluded — do not treat that success as complete.
    *   Exclusions: the `ignore_nodes` parameter is effective in `layout_tree` and `layout_integrity` (e.g. dynamic placeholders or elements requested by the user). For `perceptual` / `strict` comparisons, use `ignore_region` instead. Non-empty `unmatched_ignores` or `unresolved_parent_refs` means verification is incomplete (typos / bad `parent` refs) even when `status` looks fine; `parent_checked_nodes=0` means parent overflow was not applied; `absolute_mode_pairs` > 0 means `threshold` is px for those pairs.
3. **Viewport Matching**: For Figma compare (`layout_tree` / `perceptual` / `strict`), the browser viewport must match the Figma mockup frame size. Image inputs for `perceptual` / `strict` are limited to **8192px** on each side; resize oversized captures (e.g. high-DPR Retina screenshots) before comparison. Additionally you **MUST** run `layout_integrity` at iPad portrait **and** landscape unless the user specifies other tablet sizes.
4. **Interaction Integrity**: Verification is not complete until you confirm the UI/UX stays intact **after** interactions. You **MUST** exercise scrolling (top/middle/bottom) and press interactive controls (buttons, tabs, modals, dropdowns, accordions, drawers), then re-run `compare_design` on the resulting states to prove nothing overlaps, clips, overflows, or collapses.
5. **Tablet Layout Integrity**: Verification is not complete until `layout_integrity` succeeds at both iPad portrait (768×1024) and landscape (1024×768), each with DOM captured at that viewport.
6. **Large Layout JSON via Files**: Prefer `figma_layout_path` / `web_layout_path` (files under `$RUN`) over inline `figma_layout` / `web_layout` when the node list is large. Do not pass both the inline parameter and its `*_path` counterpart for the same side.
