// Run with: node --test .github/scripts/release-notes.test.mjs (CI runs this through `npm test`.)
//
// Pins what the release body claims: which section a subject lands in, that the
// subject is copied verbatim, that release bookkeeping commits are dropped, and
// that the compare link appears only when the workflow passes a tag and a repo.

import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import {
  RELEASE_COMMIT,
  TYPE_PREFIX,
  buildReleaseNotes,
  headingFor,
  readSubjects,
} from "./release-notes.mjs";

const repoDir = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
const script = join(repoDir, ".github", "scripts", "release-notes.mjs");

// Pasted from `git log --no-merges --pretty=format:%s v1.0.7..v1.0.8`.
const SUBJECTS_V1_0_8 = [
  "chore: release v1.0.8 [skip ci]",
  "fix #685: スキーマと modeParamSupport の同期をテストで保証する (#1530)",
  "fix #679: layout_integrity の max_details で issues も切り詰める (#1514)",
  "fix #671: layout_integrity の除外と幾何欠落の混在誤報を直す (#1497)",
  "fix #663: Pi拡張の figma_layout/web_layout でネイティブ JSON 配列を受け付ける (#1473)",
  "fix #659: 差分0時の diff_image を薄いグレースケールにする (#1472)",
  "fix #660: SKILL.md に差分画像と位置情報の使い方を追記 (#1471)",
  "fix #657: CLI に --version フラグを追加する (#1470)",
  "fix #647: perceptual の min_match 表示を小数2桁に揃える (#1449)",
  "fix #642: 不一致ペアの相手を余分要素として二重計上しない (#1434)",
  "fix #641: parent_overflow を辺ごとに複数報告する (#1428)",
];

// Real subjects from this repository's history, one per section decision.
const SUBJECTS_MIXED = [
  "feat: compare_design を Pi 拡張として npm 公開できるようにする",
  SUBJECTS_V1_0_8[1],
  "ci: restore the release change threshold to 10",
  "chat: session #2355",
  "Address review: symmetric coordinate-space comparison and path trust docs",
  "chore: release v1.0.8 [skip ci]",
];

test("v1.0.8 の本文は実コミット件名をそのまま並べる", () => {
  const notes = buildReleaseNotes({
    subjects: SUBJECTS_V1_0_8,
    previousTag: "v1.0.7",
    currentTag: "v1.0.8",
    repository: "TomokiMatsubuchi/design-compare",
  });
  assert.equal(
    notes,
    [
      "## 不具合修正",
      "",
      ...SUBJECTS_V1_0_8.slice(1).map((subject) => `- ${subject}`),
      "",
      "**Full Changelog**: https://github.com/TomokiMatsubuchi/design-compare/compare/v1.0.7...v1.0.8",
      "",
    ].join("\n"),
  );
});

test("feat は新機能、fix は不具合修正、それ以外はその他に入る", () => {
  const notes = buildReleaseNotes({
    subjects: SUBJECTS_MIXED,
    previousTag: "v1.0.8",
    currentTag: "v1.0.9",
    repository: "TomokiMatsubuchi/design-compare",
  });
  assert.equal(
    notes,
    [
      "## 新機能",
      "",
      "- feat: compare_design を Pi 拡張として npm 公開できるようにする",
      "",
      "## 不具合修正",
      "",
      `- ${SUBJECTS_V1_0_8[1]}`,
      "",
      "## その他",
      "",
      "- ci: restore the release change threshold to 10",
      "- chat: session #2355",
      "- Address review: symmetric coordinate-space comparison and path trust docs",
      "",
      "**Full Changelog**: https://github.com/TomokiMatsubuchi/design-compare/compare/v1.0.8...v1.0.9",
      "",
    ].join("\n"),
  );
});

test("件名の分類は type だけで決まり、件名は書き換えない", () => {
  assert.equal(headingFor("feat: x"), "## 新機能");
  assert.equal(headingFor("fix #123: x"), "## 不具合修正");
  assert.equal(headingFor("fix(scope)!: x"), "## 不具合修正");
  assert.equal(headingFor("docs: README を直す"), "## その他");
  assert.equal(headingFor("Address review: x"), "## その他");
  assert.equal(headingFor("chat: session #2355"), "## その他");
  assert.equal(headingFor("chore: release v1.0.8 [skip ci]"), null);
  assert.equal(headingFor("   "), null);
  // 件名の前後の空白だけ落とし、中身には触らない。
  assert.equal(headingFor("  fix #1: x  "), "## 不具合修正");
});

test("リリース用コミットだけを除外し、他の chore は残す", () => {
  assert.equal(RELEASE_COMMIT.test("chore: release v1.0.8 [skip ci]"), true);
  assert.equal(RELEASE_COMMIT.test("chore: release v10.20.30"), true);
  assert.equal(RELEASE_COMMIT.test("chore: release notes の書式を変える"), false);
  const notes = buildReleaseNotes({
    subjects: ["chore: release v1.0.8 [skip ci]", "chore: release notes の書式を変える"],
    currentTag: "v1.0.9",
  });
  assert.equal(notes, ["## その他", "", "- chore: release notes の書式を変える", ""].join("\n"));
});

test("type の接頭辞は issue 番号付き・scope 付き・破壊的変更の ! を許す", () => {
  assert.equal(TYPE_PREFIX.exec("fix #698: x")?.[1], "fix");
  assert.equal(TYPE_PREFIX.exec("feat(layout): x")?.[1], "feat");
  assert.equal(TYPE_PREFIX.exec("ci!: x")?.[1], "ci");
  // 空白が無い `fix:x` と、大文字始まりは type 付きとみなさない。
  assert.equal(TYPE_PREFIX.exec("fix:x"), null);
  assert.equal(TYPE_PREFIX.exec("Fix: x"), null);
});

test("空の節は見出しごと出さず、変更が無ければその旨を返す", () => {
  assert.equal(
    buildReleaseNotes({ subjects: ["fix: x"], currentTag: "v1.0.9" }),
    ["## 不具合修正", "", "- fix: x", ""].join("\n"),
  );
  assert.equal(
    buildReleaseNotes({ subjects: ["chore: release v1.0.8 [skip ci]"], currentTag: "v1.0.9" }),
    "このリリースに含まれる変更はありません。\n",
  );
  assert.equal(
    buildReleaseNotes({ subjects: [], previousTag: "v1.0.8", currentTag: "v1.0.9", repository: "o/r" }),
    "このリリースに含まれる変更はありません。\n",
  );
});

test("Full Changelog はタグとリポジトリが揃ったときだけ付く", () => {
  const notes = (options) => buildReleaseNotes({ subjects: ["fix: x"], ...options });
  assert.match(notes({ previousTag: "v1.0.8", currentTag: "v1.0.9", repository: "o/r" }), /Full Changelog/);
  assert.doesNotMatch(notes({ currentTag: "v1.0.9", repository: "o/r" }), /Full Changelog/);
  assert.doesNotMatch(notes({ previousTag: "v1.0.8", currentTag: "v1.0.9" }), /Full Changelog/);
  assert.doesNotMatch(notes({ previousTag: "v1.0.8", repository: "o/r" }), /Full Changelog/);
});

test("任意の件名でも各コミットは 1 回だけ、節は 1 回だけ現れる", () => {
  const types = ["feat", "fix", "ci", "chore", "docs", "", "Fix", "Address", "chat", "feat(layout)!"];
  const bodies = ["x", "スキーマと modeParamSupport の同期をテストで保証する (#1530)", "- 先頭がハイフン", "#123"];
  let seed = 20250101;
  const random = (limit) => {
    seed = (seed * 1103515245 + 12345) % 2147483648;
    return seed % limit;
  };
  for (let round = 0; round < 200; round++) {
    const subjects = [];
    for (let index = 0, count = random(8); index < count; index++) {
      const type = types[random(types.length)];
      const body = bodies[random(bodies.length)];
      subjects.push(type === "" ? body : `${type}: ${body}`);
    }
    const notes = buildReleaseNotes({
      subjects,
      previousTag: "v1.0.8",
      currentTag: "v1.0.9",
      repository: "o/r",
    });
    const lines = notes.split("\n");
    assert.equal(lines.at(-1), "");
    const headings = lines.filter((line) => line.startsWith("## "));
    assert.deepEqual(headings, [...new Set(headings)], "同じ見出しを 2 回出さない");
    const items = lines.filter((line) => line.startsWith("- "));
    const expected = subjects
      .map((subject) => subject.trim())
      .filter((subject) => headingFor(subject) !== null)
      .sort();
    assert.deepEqual([...items].sort(), expected.map((subject) => `- ${subject}`).sort());
    // 見出し・空行・箇条書き・Full Changelog・変更なしの一文以外は出さない。
    const emptyNotes = "このリリースに含まれる変更はありません。";
    for (const line of lines.slice(0, -1)) {
      assert.ok(
        line === "" ||
          line === emptyNotes ||
          line.startsWith("## ") ||
          line.startsWith("- ") ||
          line.startsWith("**Full Changelog**"),
        `unexpected line: ${line}`,
      );
    }
    assert.equal(
      buildReleaseNotes({ subjects, previousTag: "v1.0.8", currentTag: "v1.0.9", repository: "o/r" }),
      notes,
      "同じ入力からは同じ本文",
    );
  }
});

test("cli は --out に本文を書き、--current が無ければ失敗する", () => {
  const workDir = mkdtempSync(join(tmpdir(), "release-notes-"));
  try {
    const out = join(workDir, "release-notes.md");
    // HEAD..HEAD は空の範囲なので、git から読む経路だけを通して一覧を検証する。
    execFileSync("node", [script, "--from", "HEAD", "--to", "HEAD", "--current", "v9.9.9", "--out", out], {
      cwd: repoDir,
    });
    assert.equal(readFileSync(out, "utf8"), "このリリースに含まれる変更はありません。\n");
    assert.equal(readSubjects({ from: "HEAD", to: "HEAD" }).length, 0);
    assert.throws(
      () => execFileSync("node", [script, "--out", out], { cwd: repoDir, stdio: "pipe" }),
      /--current is required/,
    );
  } finally {
    rmSync(workDir, { recursive: true, force: true });
  }
});
