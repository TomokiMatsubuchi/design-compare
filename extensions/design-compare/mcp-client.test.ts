// Run with: node --test extensions/design-compare/mcp-client.test.ts
//
// Covers the failure paths a real comparison finishes too quickly to exercise:
// timeout, abort mid-flight, abort before start, and a server that exits
// without replying. Each case also asserts the child process is reaped.
//
// Stubs sleep only a few seconds on purpose: if the kill logic regresses, these
// tests must still terminate rather than wedge the runner.

import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { after, before, test } from "node:test";
import { fileURLToPath } from "node:url";
import { crc32, deflateSync } from "node:zlib";
import { callCompareDesign } from "./mcp-client.ts";

const repoDir = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
const realBinary = join(repoDir, "design-compare");

let workDir: string;
let hangingServer: string;
let silentExitServer: string;

/** Count live processes spawned from a given stub path. */
function liveStubs(path: string): number {
  try {
    return execFileSync("pgrep", ["-f", path], { encoding: "utf-8" }).trim().split("\n").filter(Boolean).length;
  } catch {
    return 0; // pgrep exits 1 when nothing matches.
  }
}

function writeStub(name: string, body: string): string {
  const path = join(workDir, name);
  writeFileSync(path, `#!/bin/sh\n${body}\n`);
  chmodSync(path, 0o755);
  return path;
}

function pngChunk(type: string, data: Buffer): Buffer {
  const body = Buffer.concat([Buffer.from(type, "ascii"), data]);
  const length = Buffer.alloc(4);
  length.writeUInt32BE(data.length);
  const checksum = Buffer.alloc(4);
  checksum.writeUInt32BE(crc32(body));
  return Buffer.concat([length, body, checksum]);
}

/** Write a truecolor PNG whose pixels come from `pixel(x, y)`. */
function writePng(name: string, w: number, h: number, pixel: (x: number, y: number) => number[]): string {
  const header = Buffer.alloc(13);
  header.writeUInt32BE(w, 0);
  header.writeUInt32BE(h, 4);
  header[8] = 8; // bit depth
  header[9] = 2; // color type: truecolor

  const rows: Buffer[] = [];
  for (let y = 0; y < h; y++) {
    const row = [Buffer.from([0])]; // filter type: none
    for (let x = 0; x < w; x++) row.push(Buffer.from(pixel(x, y)));
    rows.push(Buffer.concat(row));
  }

  const path = join(workDir, name);
  writeFileSync(
    path,
    Buffer.concat([
      Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
      pngChunk("IHDR", header),
      pngChunk("IDAT", deflateSync(Buffer.concat(rows))),
      pngChunk("IEND", Buffer.alloc(0)),
    ]),
  );
  return path;
}

function parseResult(result: { content?: { text?: string }[] }): Record<string, unknown> {
  return JSON.parse((result.content ?? []).map((c) => c.text ?? "").join(""));
}

async function compare(args: Record<string, unknown>) {
  return parseResult(await callCompareDesign({ binaryPath: realBinary, args, cwd: workDir, timeoutMs: 30_000 }));
}

// Solid red vs solid red (identical), solid blue (different color, same macro
// layout), and a top/bottom split (different macro layout).
let redA: string;
let redB: string;
let blue: string;
let split: string;

before(() => {
  workDir = mkdtempSync(join(tmpdir(), "dc-mcp-client-"));
  // Reads the request then never answers, emulating a wedged server.
  hangingServer = writeStub("hanging-server", "cat > /dev/null\nsleep 5");
  // Closes stdout immediately without producing a JSON-RPC reply.
  silentExitServer = writeStub("silent-exit-server", "echo 'boom' >&2\nexit 3");

  redA = writePng("red-a.png", 64, 64, () => [255, 0, 0]);
  redB = writePng("red-b.png", 64, 64, () => [255, 0, 0]);
  blue = writePng("blue.png", 64, 64, () => [0, 0, 255]);
  split = writePng("split.png", 64, 64, (_x, y) => (y < 32 ? [0, 0, 0] : [255, 255, 255]));
});

after(() => {
  rmSync(workDir, { recursive: true, force: true });
});

test("times out and kills the child when the server never replies", async () => {
  await assert.rejects(
    callCompareDesign({ binaryPath: hangingServer, args: { mode: "layout_tree" }, cwd: workDir, timeoutMs: 400 }),
    /timed out after 400ms/,
  );
  await new Promise((r) => setTimeout(r, 300));
  assert.equal(liveStubs(hangingServer), 0, "timed-out child must be killed");
});

test("aborts mid-flight and kills the child", async () => {
  const controller = new AbortController();
  const pending = callCompareDesign({
    binaryPath: hangingServer,
    args: { mode: "layout_tree" },
    cwd: workDir,
    signal: controller.signal,
    timeoutMs: 60_000,
  });

  setTimeout(() => controller.abort(), 200);
  await assert.rejects(pending, /was aborted/);

  await new Promise((r) => setTimeout(r, 300));
  assert.equal(liveStubs(hangingServer), 0, "aborted child must be killed");
});

test("rejects without spawning when the signal is already aborted", async () => {
  await assert.rejects(
    callCompareDesign({
      binaryPath: hangingServer,
      args: { mode: "layout_tree" },
      cwd: workDir,
      signal: AbortSignal.abort(),
      timeoutMs: 60_000,
    }),
    /was aborted/,
  );
  assert.equal(liveStubs(hangingServer), 0, "no process should be spawned");
});

test("surfaces stderr when the server exits before replying", async () => {
  await assert.rejects(
    callCompareDesign({ binaryPath: silentExitServer, args: { mode: "layout_tree" }, cwd: workDir, timeoutMs: 10_000 }),
    /exited with code 3.*boom/s,
  );
});

// The Go comparator reads `w`/`h` (not width/height) and matches Figma nodes to
// Web nodes by relative geometry, so a child under a shared root is needed for
// the comparison to be meaningful.
const figmaLayout = JSON.stringify([
  { id: "0", name: "root", x: 0, y: 0, w: 100, h: 100 },
  { id: "1", name: "hero", parent: "0", x: 0, y: 0, w: 50, h: 50 },
]);
const matchingWebLayout = JSON.stringify([
  { selector: "body", x: 0, y: 0, w: 100, h: 100 },
  { selector: ".hero", parent: "body", x: 0, y: 0, w: 50, h: 50 },
]);
const shiftedWebLayout = JSON.stringify([
  { selector: "body", x: 0, y: 0, w: 100, h: 100 },
  { selector: ".hero", parent: "body", x: 60, y: 70, w: 20, h: 10 },
]);

test("resolves a matching layout_tree comparison against the real binary", async () => {
  const result = await callCompareDesign({
    binaryPath: realBinary,
    args: { mode: "layout_tree", figma_layout: figmaLayout, web_layout: matchingWebLayout },
    cwd: workDir,
    timeoutMs: 30_000,
  });

  const text = (result.content ?? []).map((c) => c.text ?? "").join("");
  assert.match(text, /"status": "success"/);
  assert.match(text, /"match_rate": "100.00%"/);
});

test("reports a mismatch instead of silently passing", async () => {
  const result = await callCompareDesign({
    binaryPath: realBinary,
    args: { mode: "layout_tree", figma_layout: figmaLayout, web_layout: shiftedWebLayout },
    cwd: workDir,
    timeoutMs: 30_000,
  });

  const text = (result.content ?? []).map((c) => c.text ?? "").join("");
  assert.match(text, /"status": "mismatch"/);
  assert.match(text, /did not match closest Web element/);
});

test("strict: identical images pass with zero diff pixels", async () => {
  const out = await compare({ mode: "strict", image_path_a: redA, image_path_b: redB });
  assert.equal(out.status, "success");
  assert.equal(out.diff_pixels, 0);
  assert.equal(out.match_rate, "100.00%");
  assert.equal(out.match_rate_value, 100);
  assert.match(out.diff_image as string, /^data:image\/png;base64,/);
});

test("strict: a color change is reported as a mismatch", async () => {
  const out = await compare({ mode: "strict", image_path_a: redA, image_path_b: blue });
  assert.equal(out.status, "mismatch");
  assert.ok((out.diff_pixels as number) > 0, "diff pixels should be non-zero");
});

test("perceptual: identical images pass", async () => {
  const out = await compare({ mode: "perceptual", image_path_a: redA, image_path_b: redB });
  assert.equal(out.status, "success");
  assert.equal(out.match_rate, "100.00%");
  assert.equal(out.match_rate_value, 100);
  assert.match(out.diff_image as string, /^data:image\/png;base64,/);
});

test("perceptual: ignores a pure color change that keeps the macro layout", async () => {
  const out = await compare({ mode: "perceptual", image_path_a: redA, image_path_b: blue });
  assert.equal(out.status, "success");
});

test("perceptual: a different macro layout is reported as a mismatch", async () => {
  const out = await compare({ mode: "perceptual", image_path_a: redA, image_path_b: split });
  assert.equal(out.status, "mismatch");
});

test("strict: generate_diff=false omits the diff payload", async () => {
  const out = await compare({
    mode: "strict",
    image_path_a: redA,
    image_path_b: blue,
    generate_diff: false,
  });
  assert.equal(out.status, "mismatch");
  assert.equal(out.diff_image, "");
});

test("perceptual: rejects a strict-scale threshold instead of silently passing", async () => {
  const result = await callCompareDesign({
    binaryPath: realBinary,
    args: { mode: "perceptual", image_path_a: redA, image_path_b: split, threshold: 0.1 },
    cwd: workDir,
    timeoutMs: 30_000,
  });
  assert.equal(result.isError, true);
  const text = (result.content ?? []).map((c) => c.text ?? "").join("");
  assert.match(text, /threshold for perceptual mode must be 1\.0.100\.0/);
});
