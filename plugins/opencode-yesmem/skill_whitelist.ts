// skill_whitelist.ts — Filesystem-backed set of installed skill names.
// Used by skill_nudge and rule_guard to filter out suggestions for skills
// that exist in RULES.md catalog but are not actually installed.
//
// A "skill" is a directory containing SKILL.md. opencode/Claude Code discover
// skills by scanning known roots. We mirror that discovery here.

import { readdirSync, statSync } from "node:fs";
import { join } from "node:path";

const HOME = process.env.HOME || "/home/chief";

// Roots opencode/Claude Code scan for skills. Bundled skills that were never
// installed (e.g. yesmem-docs, yesmem-search) live only in the source repo
// and won't appear here — exactly the ghosts we want to filter out.
const SKILL_ROOTS = [
  `${HOME}/.claude/skills`,
  `${HOME}/.agents/skills`,
];

// Allow override/extensibility via env (colon-separated extra roots).
const extraRoots = (process.env.YESMEM_SKILL_ROOTS || "")
  .split(":")
  .filter(Boolean);
const ALL_ROOTS = [...SKILL_ROOTS, ...extraRoots];

let cachedNames: Set<string> | null = null;
let cacheKey = "";

function dirMtimeSum(): string {
  let key = "";
  for (const root of ALL_ROOTS) {
    try {
      const st = statSync(root);
      key += `${root}:${st.mtimeMs};`;
    } catch {
      key += `${root}:-;`;
    }
  }
  return key;
}

function scan(): Set<string> {
  const names = new Set<string>();
  for (const root of ALL_ROOTS) {
    let entries: string[] = [];
    try {
      entries = readdirSync(root);
    } catch {
      continue;
    }
    for (const entry of entries) {
      // Lowercased dir name = skill name (opencode convention).
      try {
        const skillMd = join(root, entry, "SKILL.md");
        statSync(skillMd);
        names.add(entry.toLowerCase());
      } catch {}
    }
  }
  return names;
}

export function loadInstalledSkills(): Set<string> {
  const key = dirMtimeSum();
  if (cachedNames && key === cacheKey) return cachedNames;
  cachedNames = scan();
  cacheKey = key;
  return cachedNames;
}

export function isSkillInstalled(name: string): boolean {
  if (!name) return false;
  return loadInstalledSkills().has(name.toLowerCase());
}

// For tests: force a rescan on next call.
export function _resetCacheForTest(): void {
  cachedNames = null;
  cacheKey = "";
}
