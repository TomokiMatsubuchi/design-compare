#!/usr/bin/env node
// Builds the GitHub release body from the commit subjects since the previous tag.
//
// Only the `<type>` prefix of a subject decides the section; the subject itself is
// never rewritten, so a release note says exactly what the commit says. Subjects that
// do not start with a known `<type>` prefix are listed verbatim under 「その他」instead
// of being guessed at.
//
// Run from the repository root:
//   node .github/scripts/release-notes.mjs --from v1.0.7 --to HEAD \
//     --current v1.0.8 --repository owner/repo --out release-notes.md

import { execFileSync } from "node:child_process";
import { writeFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

/** Release bookkeeping commits are not user-facing. Matches `chore: release v1.0.8 [skip ci]`. */
export const RELEASE_COMMIT = /^chore: release v\d+\.\d+\.\d+/;

/** `fix #123: ...`, `feat(scope): ...`, `ci: ...` -> the leading `type`. */
export const TYPE_PREFIX = /^([a-z]+)(?:\([^)]*\))?(?:\s+#\d+)?!?: /;

const OTHER_HEADING = "## その他";

/** Sections in output order; a type that is not listed here goes to 「その他」. */
const HEADING_BY_TYPE = new Map([
  ["feat", "## 新機能"],
  ["fix", "## 不具合修正"],
]);
const HEADINGS = ["## 新機能", "## 不具合修正", OTHER_HEADING];

const EMPTY_NOTES = "このリリースに含まれる変更はありません。\n";

/**
 * Section heading for one commit subject, or null when the commit is skipped.
 * Exported so the classification can be pinned per subject.
 */
export function headingFor(subject) {
  const trimmed = subject.trim();
  if (trimmed === "" || RELEASE_COMMIT.test(trimmed)) return null;
  const type = TYPE_PREFIX.exec(trimmed)?.[1];
  return HEADING_BY_TYPE.get(type) ?? OTHER_HEADING;
}

/**
 * Release body in Markdown.
 *
 * @param {object} options
 * @param {string[]} options.subjects commit subjects, oldest first
 * @param {string} [options.previousTag] tag the range starts at; link is omitted when empty
 * @param {string} options.currentTag tag being released
 * @param {string} [options.repository] `owner/repo`; link is omitted when empty
 * @returns {string} Markdown ending with a newline
 */
export function buildReleaseNotes({ subjects, previousTag = "", currentTag = "", repository = "" }) {
  const grouped = new Map(HEADINGS.map((heading) => [heading, []]));
  for (const subject of subjects) {
    const heading = headingFor(subject);
    if (heading === null) continue;
    grouped.get(heading).push(subject.trim());
  }

  const blocks = [];
  for (const heading of HEADINGS) {
    const items = grouped.get(heading);
    if (items.length === 0) continue;
    blocks.push([heading, "", ...items.map((item) => `- ${item}`)].join("\n"));
  }
  if (blocks.length === 0) return EMPTY_NOTES;
  if (previousTag !== "" && currentTag !== "" && repository !== "") {
    blocks.push(`**Full Changelog**: https://github.com/${repository}/compare/${previousTag}...${currentTag}`);
  }
  return `${blocks.join("\n\n")}\n`;
}

/** Commit subjects of `from..to` (`to` alone when `from` is empty), newest-first git order kept. */
export function readSubjects({ from = "", to = "HEAD" }) {
  const range = from === "" ? to : `${from}..${to}`;
  const output = execFileSync("git", ["log", "--no-merges", "--pretty=format:%s", range], {
    encoding: "utf8",
  });
  return output.split("\n").filter((line) => line !== "");
}

function parseArgs(argv) {
  const values = new Map();
  for (let index = 0; index < argv.length; index += 2) {
    const key = argv[index];
    const value = argv[index + 1];
    if (!key?.startsWith("--") || value === undefined) {
      throw new Error(`invalid arguments near ${key ?? "(end)"}; expected --name value pairs`);
    }
    values.set(key.slice(2), value);
  }
  return values;
}

function main(argv) {
  const args = parseArgs(argv);
  const currentTag = args.get("current") ?? "";
  if (currentTag === "") throw new Error("--current is required, e.g. --current v1.0.9");
  const from = args.get("from") ?? "";
  const notes = buildReleaseNotes({
    subjects: readSubjects({ from, to: args.get("to") ?? "HEAD" }),
    previousTag: from,
    currentTag,
    repository: args.get("repository") ?? process.env.GITHUB_REPOSITORY ?? "",
  });
  const out = args.get("out");
  if (out) {
    writeFileSync(out, notes);
  } else {
    process.stdout.write(notes);
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    main(process.argv.slice(2));
  } catch (error) {
    console.error(`release-notes: ${error.message}`);
    process.exit(1);
  }
}
