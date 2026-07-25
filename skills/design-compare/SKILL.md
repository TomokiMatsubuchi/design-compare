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
3. **design-compare**: To perform layout-tree, perceptual, or strict comparisons.

## Core Verification Workflow

When asked to verify how well a web page implementation matches a Figma design, execute the following multi-mcp workflow:

### Step 1: Gather Figma Design Data
Use the **Figma MCP server** to fetch:
- Node image: Save to a local path (e.g. `/tmp/figma_design.png`).
- Node metadata: Extract the layout bounding boxes and structural details.

### Step 2: Gather Web Implementation Data
Use Codex's browser tools to:
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

### Step 5: Analyze Results and Fix Code
- Check the output `status` and `match_rate`.
- If discrepancies exist, inspect the output details or the generated magenta diff image (for `strict` mode), correct the HTML/CSS code, and re-run the validation to ensure the layout matches the template.

## Guardrails & Best Practices

1. **Mandatory Execution**: You are strictly prohibited from submitting a final response or claiming success until you have successfully executed `compare_design` and confirmed the match rate meets the required threshold.
2. **Match Threshold & Exclusions**: The default passing threshold is **98.0%**. If the match rate is below 98.0%, you must analyze the diff details, fix the implementation, and run the comparison again until it passes. You can exclude specific elements (e.g., dynamic placeholders or elements requested by the user) using the `ignore_nodes` parameter to prevent false negatives.
3. **Viewport Matching**: Always ensure your browser viewport width/height configuration matches the Figma mockup frame size when gathering layout or screenshot data.
4. **Interaction Integrity**: Verification is not complete until you confirm the UI/UX stays intact **after** interactions. You **MUST** exercise scrolling (top/middle/bottom) and press interactive controls (buttons, tabs, modals, dropdowns, accordions, drawers), then re-run `compare_design` on the resulting states to prove nothing overlaps, clips, overflows, or collapses.
