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

### Step 1: Gather Figma Design Data
Use the **Figma MCP server** to fetch:
- Node image: Save to a local path (e.g. `/tmp/figma_design.png`).
- Node metadata: Extract the layout bounding boxes and structural details.

### Step 2: Gather Web Implementation Data
Use your browser automation MCP tools (chrome-devtools-mcp or playwright-cli):
- Capture page screenshot: Save to a local path (e.g. `/tmp/web_actual.png`).
- Extract DOM bounding boxes: Retrieve selectors, relative coordinate hierarchies, widths, and heights in a JSON format.

### Step 3: Run the Comparison
Call the `compare_design` tool from **design-compare** using the appropriate mode:

*   **Mode: `layout_tree` (Recommended for pure structural/template validation)**
    *   Use when you want to verify if components are placed in the correct hierarchy and alignment without worrying about precise text changes, fonts, or minor coloring.
    *   Parameters: `mode: "layout_tree"`, `figma_layout: "[...]"`, `web_layout: "[...]"`
*   **Mode: `perceptual` (Recommended for visual template verification)**
    *   Use when comparing images, but you want to ignore minor sub-pixel rendering quirks, text content differences, or anti-aliasing details. It scales down and checks macro-layout blocks.
    *   Parameters: `mode: "perceptual"`, `image_path_a: "/tmp/figma.png"`, `image_path_b: "/tmp/web.png"`
*   **Mode: `strict` (Recommended for visual regression / detail verification)**
    *   Use when checking exact visual styles (color matches, border widths, exact layouts) using Pixelmatch.
    *   Parameters: `mode: "strict"`, `image_path_a: "/tmp/figma.png"`, `image_path_b: "/tmp/web.png"`

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

1. **Portrait (default):** resize the browser to **768×1024 CSS px**, recapture **DOM bounding boxes only** (not Figma), then call `compare_design` with `mode: "layout_integrity"` and `web_layout` (or `web_layout_path`) from that capture.
2. **Landscape:** resize to **1024×768 CSS px**, recapture DOM boxes at that size, then call again with `viewport_preset: "ipad_landscape"` (or `viewport_width: 1024` and `viewport_height: 768`). Do **not** reuse portrait coordinates for the landscape check.
3. Treat `viewport_overflow_x` and `parent_overflow` as failures. Vertical overflow of the viewport is expected page scroll and is not a failure.
4. Fix overflow / parent overflow, then re-run **both** orientations until each returns `status: success`. `mismatch` or `skipped` leaves verification incomplete.

### Step 6: Analyze Results and Fix Code
- Check the output `status` and, for Figma-compare modes, `match_rate`.
- In `layout_tree` mode, if `zero_geometry_warning` is present, the layout JSON likely used wrong keys (`width`/`height` instead of `w`/`h`). Do not treat a 100% match as valid until the geometry keys are corrected.
- If discrepancies exist, inspect the output details and the `diff_image` field (a base64 PNG data URI returned in the response; no temporary file is written). For how the visualization is colored, see README §1. Then correct the HTML/CSS code, and re-run the validation to ensure the layout matches the template.

## Guardrails & Best Practices

1. **Mandatory Execution**: You are strictly prohibited from submitting a final response or claiming success until you have successfully executed `compare_design` and confirmed the match rate meets the required threshold.
2. **Match Threshold & Exclusions**: The passing criteria depend on the mode:
    *   `layout_tree` (judged by `pass_rate`) and `perceptual` (judged by `min_match`): the default passing threshold is **98.0%**. If the match rate is below 98.0%, you must analyze the diff details, fix the implementation, and run the comparison again until it passes.
    *   `strict` is **not** judged by match rate by default. Its default criterion is `max_diff_pixels` = **0** (zero differing pixels is required, so even a 99.9% match rate results in `mismatch` with a single differing pixel). To judge strict results by match rate, explicitly specify `min_match`; to tolerate a small number of differing pixels, explicitly specify `max_diff_pixels`.
    *   `layout_integrity` has no `match_rate`. It passes only when `issue_count` is 0 (`status: success`).
    *   Exclusions: the `ignore_nodes` parameter is effective in `layout_tree` and `layout_integrity` (e.g. dynamic placeholders or elements requested by the user). For `perceptual` / `strict` comparisons, use `ignore_region` instead.
3. **Viewport Matching**: For Figma compare (`layout_tree` / `perceptual` / `strict`), the browser viewport must match the Figma mockup frame size. Additionally you **MUST** run `layout_integrity` at iPad portrait **and** landscape unless the user specifies other tablet sizes.
4. **Interaction Integrity**: Verification is not complete until you confirm the UI/UX stays intact **after** interactions. You **MUST** exercise scrolling (top/middle/bottom) and press interactive controls (buttons, tabs, modals, dropdowns, accordions, drawers), then re-run `compare_design` on the resulting states to prove nothing overlaps, clips, overflows, or collapses.
5. **Tablet Layout Integrity**: Verification is not complete until `layout_integrity` succeeds at both iPad portrait (768×1024) and landscape (1024×768), each with DOM captured at that viewport.
