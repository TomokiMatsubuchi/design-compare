// Minimal stdio JSON-RPC client for the design-compare MCP server.
//
// Kept free of Pi and TypeBox imports so mcp-client.test.ts can exercise it
// directly, including the abort and timeout paths that a real comparison
// finishes too quickly to trigger.

import { spawn } from "node:child_process";

/** One MCP tools/call content block (text and/or image). */
export interface McpContent {
  type: string;
  text?: string;
  data?: string;
  mimeType?: string;
}

export interface McpCallResult {
  content?: McpContent[];
  isError?: boolean;
}

/** Pi AgentToolResult content shapes without importing pi-coding-agent. */
export type ToolResultContent =
  | { type: "text"; text: string }
  | { type: "image"; data: string; mimeType: string };

export interface ToolResult {
  content: ToolResultContent[];
  details: Record<string, unknown>;
}

/**
 * Map an MCP tools/call result onto Pi tool content.
 *
 * Text blocks are joined into a single leading text item (unchanged). Image
 * blocks are forwarded as native image content so diff_image_content works.
 * Errors stay text-only.
 */
export function toToolResult(result: McpCallResult): ToolResult {
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

  const content: ToolResultContent[] = [{ type: "text", text: text || "(empty result)" }];
  let hasImageContent = false;
  for (const item of result.content ?? []) {
    if (item.type !== "image" || typeof item.data !== "string" || item.data === "") continue;
    content.push({
      type: "image",
      data: item.data,
      mimeType: item.mimeType || "image/png",
    });
    hasImageContent = true;
  }

  return {
    content,
    details: hasImageContent ? { has_image_content: true } : {},
  };
}

interface JsonRpcMessage {
  id?: number;
  result?: McpCallResult;
  error?: { message?: string };
}

export interface CallOptions {
  /** Absolute path to the design-compare binary. */
  binaryPath: string;
  /** Arguments forwarded verbatim to the compare_design tool. */
  args: Record<string, unknown>;
  /** Working directory, so relative image paths resolve as the caller expects. */
  cwd: string;
  signal?: AbortSignal;
  timeoutMs: number;
}

const INIT_REQUEST = JSON.stringify({
  jsonrpc: "2.0",
  id: 1,
  method: "initialize",
  params: {
    protocolVersion: "2024-11-05",
    capabilities: {},
    clientInfo: { name: "pi-design-compare", version: "1.0.0" },
  },
});
const INITIALIZED_NOTIFICATION = JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" });

/**
 * Run one `compare_design` call against a freshly spawned server process.
 *
 * The server handles requests sequentially, so the handshake and the call are
 * written in one batch and the response for id 2 is awaited. The child is
 * always killed before this promise settles, including on abort and timeout.
 */
export function callCompareDesign(options: CallOptions): Promise<McpCallResult> {
  const { binaryPath, args, cwd, signal, timeoutMs } = options;

  return new Promise((fulfill, reject) => {
    if (signal?.aborted) return reject(new Error("compare_design was aborted"));

    const child = spawn(binaryPath, [], { cwd, stdio: ["pipe", "pipe", "pipe"] });

    let settled = false;
    let stdout = "";
    let stderr = "";

    const finish = (error: Error | undefined, value?: McpCallResult): void => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      signal?.removeEventListener("abort", onAbort);
      child.kill();
      if (error) reject(error);
      else fulfill(value as McpCallResult);
    };

    const onAbort = (): void => finish(new Error("compare_design was aborted"));
    const timer = setTimeout(() => finish(new Error(`compare_design timed out after ${timeoutMs}ms`)), timeoutMs);

    signal?.addEventListener("abort", onAbort, { once: true });

    // Logs go to stderr, so they never corrupt the stdout JSON-RPC stream.
    child.stderr.on("data", (chunk: Buffer) => {
      stderr += chunk.toString();
    });

    child.stdout.on("data", (chunk: Buffer) => {
      stdout += chunk.toString();
      let newlineIndex = stdout.indexOf("\n");
      while (newlineIndex !== -1) {
        const line = stdout.slice(0, newlineIndex).trim();
        stdout = stdout.slice(newlineIndex + 1);
        newlineIndex = stdout.indexOf("\n");
        if (line === "") continue;

        let message: JsonRpcMessage;
        try {
          message = JSON.parse(line) as JsonRpcMessage;
        } catch {
          continue; // Ignore anything that is not a JSON-RPC frame.
        }
        if (message.id !== 2) continue;
        if (message.error) return finish(new Error(message.error.message ?? "Unknown JSON-RPC error"));
        return finish(undefined, message.result ?? {});
      }
    });

    child.on("error", (error: Error) => finish(new Error(`Failed to run ${binaryPath}: ${error.message}`)));
    child.on("close", (code) =>
      finish(new Error(`design-compare exited with code ${code} before responding${stderr ? `: ${stderr.trim()}` : ""}`)),
    );
    // Early-exit stubs close stdin before the handshake write; ignore EPIPE so
    // the close handler can report the real exit status instead.
    child.stdin.on("error", () => {});

    const callRequest = JSON.stringify({
      jsonrpc: "2.0",
      id: 2,
      method: "tools/call",
      params: { name: "compare_design", arguments: args },
    });
    child.stdin.write(`${INIT_REQUEST}\n${INITIALIZED_NOTIFICATION}\n${callRequest}\n`);
  });
}
