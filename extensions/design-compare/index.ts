// Pi extension that exposes this repository's `compare_design` MCP tool as a
// native Pi tool, so Pi can call it without separate MCP configuration.
//
// The Go binary stays the single source of truth for comparison behavior. The
// extension only owns the Pi tool schema and stdio JSON-RPC transport.

import { realpathSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import type { AgentToolResult, ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";
import { type McpCallResult, callCompareDesign } from "./mcp-client.ts";

// Pi may load an installed package through a symlink. Resolve the physical path
// before locating the repository root and its Go binary.
const extensionPath = realpathSync(fileURLToPath(import.meta.url));
const repoDir = resolve(dirname(extensionPath), "..", "..");
const binaryPath = join(repoDir, "design-compare");

const BUILD_TIMEOUT_MS = 180_000;
const CALL_TIMEOUT_MS = 120_000;

function toToolResult(result: McpCallResult): AgentToolResult<Record<string, unknown>> {
  const text = (result.content ?? [])
    .filter((item) => item.type === "text" && typeof item.text === "string")
    .map((item) => item.text as string)
    .join("\n")
    .trim();

  if (result.isError) {
    return {
      content: [{ type: "text", text: `Error: ${text || "compare_design failed"}` }],
      details: { error: "tool_error" },
    };
  }
  return {
    content: [{ type: "text", text: text || "(empty result)" }],
    details: {},
  };
}

export default async function designCompareExtension(pi: ExtensionAPI) {
  // Always invoke `go build`: Go's build cache keeps this cheap, while this
  // guarantees that an ignored but stale or platform-incompatible binary is
  // never used after the plugin source changes.
  const build = await pi.exec("go", ["build", "-o", "design-compare", "."], {
    cwd: repoDir,
    timeout: BUILD_TIMEOUT_MS,
  });
  if (build.code !== 0) {
    console.warn(
      `[design-compare] could not build ${binaryPath} (go build exited ${build.code}) — extension disabled\n${build.stderr.trim()}`,
    );
    return;
  }

  pi.registerTool({
    name: "compare_design",
    label: "Design Compare",
    description: [
      "Compare a Figma design against a web implementation using four deterministic modes:",
      "'layout_tree' (Figma vs DOM bounding-box hierarchy),",
      "'perceptual' (aHash macro-layout image match),",
      "'strict' (pixelmatch visual regression), or",
      "'layout_integrity' (Web DOM overflow at a CSS viewport without Figma; default iPad portrait 768x1024).",
      "Results include both formatted match_rate and numeric match_rate_value except in layout_integrity.",
    ].join(" "),
    promptSnippet: "Verify Figma-to-web fidelity by layout tree, perceptual, strict pixel, or iPad layout-integrity comparison",
    parameters: Type.Object({
      mode: Type.Union(
        [
          Type.Literal("layout_tree"),
          Type.Literal("perceptual"),
          Type.Literal("strict"),
          Type.Literal("layout_integrity"),
        ],
        { description: "Comparison mode" },
      ),
      image_path_a: Type.Optional(
        Type.String({
          description:
            "Trusted local path to reference image A for perceptual/strict; mutually exclusive with image_a_base64",
        }),
      ),
      image_path_b: Type.Optional(
        Type.String({
          description:
            "Trusted local path to target image B for perceptual/strict; mutually exclusive with image_b_base64",
        }),
      ),
      image_a_base64: Type.Optional(
        Type.String({
          description:
            "Raw base64-encoded reference image A for perceptual/strict; mutually exclusive with image_path_a",
        }),
      ),
      image_b_base64: Type.Optional(
        Type.String({
          description:
            "Raw base64-encoded target image B for perceptual/strict; mutually exclusive with image_path_b",
        }),
      ),
      figma_layout: Type.Optional(
        Type.String({
          description: "Inline JSON Figma node list for layout_tree; mutually exclusive with figma_layout_path",
        }),
      ),
      web_layout: Type.Optional(
        Type.String({
          description:
            "Inline JSON Web DOM node list for layout_tree and layout_integrity; mutually exclusive with web_layout_path",
        }),
      ),
      figma_layout_path: Type.Optional(
        Type.String({
          description:
            "Trusted local path to Figma layout JSON for layout_tree; mutually exclusive with figma_layout",
        }),
      ),
      web_layout_path: Type.Optional(
        Type.String({
          description:
            "Trusted local path to Web layout JSON for layout_tree and layout_integrity; mutually exclusive with web_layout",
        }),
      ),
      viewport_preset: Type.Optional(
        Type.Union([Type.Literal("ipad_portrait"), Type.Literal("ipad_landscape")], {
          description: "Named CSS viewport for layout_integrity; default is iPad portrait 768x1024",
        }),
      ),
      viewport_width: Type.Optional(
        Type.Number({
          description: "CSS pixel viewport width for layout_integrity; overrides viewport_preset width",
        }),
      ),
      viewport_height: Type.Optional(
        Type.Number({
          description: "CSS pixel viewport height for layout_integrity; overrides viewport_preset height",
        }),
      ),
      ignore_nodes: Type.Optional(
        Type.String({
          description:
            "Comma-separated Figma node IDs/names or Web selectors to ignore in layout_tree and layout_integrity",
        }),
      ),
      ignore_region: Type.Optional(
        Type.String({
          description:
            "Semicolon-separated x,y,w,h pixel regions to mask or exclude, e.g. '10,20,100,50;200,300,80,60'",
        }),
      ),
      count_extra_web: Type.Optional(
        Type.Boolean({
          description: "Whether unmatched extra Web nodes lower the layout_tree match rate; default false",
        }),
      ),
      max_details: Type.Optional(
        Type.Number({
          description: "Maximum layout_tree details lines to return; default 0 means unlimited",
        }),
      ),
      threshold: Type.Optional(
        Type.Number({
          description:
            "layout_tree tolerance 0.0-1.0 (default 0.15); strict color tolerance 0.0-1.0 (default 0.1); legacy perceptual minimum 1.0-100.0",
        }),
      ),
      min_match: Type.Optional(
        Type.Number({
          description: "Minimum perceptual or optional strict match percentage from 0.0 to 100.0",
        }),
      ),
      pass_rate: Type.Optional(
        Type.Number({
          description: "Minimum layout_tree match percentage from 0.0 to 100.0; default 98.0",
        }),
      ),
      max_diff_pixels: Type.Optional(
        Type.Number({
          description: "Maximum differing pixels allowed in strict mode; default 0",
        }),
      ),
      include_aa: Type.Optional(
        Type.Boolean({
          description: "Whether strict mode counts anti-aliased boundary pixels as diffs; default false",
        }),
      ),
      generate_diff: Type.Optional(
        Type.Boolean({
          description: "Whether perceptual/strict return a base64 PNG data URI in diff_image; default true",
        }),
      ),
      diff_on_mismatch: Type.Optional(
        Type.Boolean({
          description: "Whether perceptual/strict omit diff_image on success; default false",
        }),
      ),
      diff_image_content: Type.Optional(
        Type.Boolean({
          description: "Whether perceptual/strict also return the diff as an MCP image content block; default false",
        }),
      ),
    }),
    executionMode: "parallel",
    async execute(_toolCallId, params, signal, _onUpdate, ctx) {
      // Drop unset optionals so the Go server applies its own defaults.
      const args = Object.fromEntries(Object.entries(params).filter(([, value]) => value !== undefined));
      try {
        const result = await callCompareDesign({
          binaryPath,
          args,
          cwd: ctx.cwd,
          signal,
          timeoutMs: CALL_TIMEOUT_MS,
        });
        return toToolResult(result);
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        return {
          content: [{ type: "text", text: `Error: ${message}` }],
          details: { error: "execution_failed" },
        };
      }
    },
  });
}
