// extract_skills.ts — Extract skill metadata from SKILL.md YAML frontmatter
// Generates YAML Skill Catalog rules for DECISIONS.md
// Usage: bun run scripts/extract_skills.ts

import { readdirSync, readFileSync } from "node:fs";
import { join, basename } from "node:path";

const HOME = process.env.HOME || "/home/chief";

const SKILL_DIRS = [
  `${HOME}/.claude/skills`,
  `${HOME}/.cache/opencode/packages/superpowers@git+https:/github.com/obra/superpowers.git/node_modules/superpowers/skills`,
];

interface SkillYaml {
  name: string;
  description: string;
  triggers: string[];
  priority: string;
}

function parseFrontmatter(content: string): Record<string, string> {
  const m = content.match(/^---\n([\s\S]*?)\n---/);
  if (!m) return {};
  const out: Record<string, string> = {};
  for (const line of m[1].split("\n")) {
    const kv = line.match(/^(\w+):\s*(.+)$/);
    if (kv) out[kv[1]] = kv[2].trim().replace(/^["']|["']$/g, "");
  }
  return out;
}

function extractTriggers(desc: string): string[] {
  const t: string[] = [];
  // Pattern: "Trigger on X, Y, Z" or "Trigger: X, Y, Z"
  const m = desc.match(/Trigger\s*(?:on|:)\s*(.+?)(?:\.\s|$)/i);
  if (m) {
    // Split by comma/and, clean quotes
    const raw = m[1].split(/, and |, oder |, or |, | und | or /);
    for (const r of raw) {
      let w = r.trim().replace(/^["']|["']$/g, "").replace(/ or when.*$/, "").replace(/^or /, "").trim();
      if (w && w.length < 80) t.push(w);
    }
  }
  return t;
}

function priorityFor(name: string): string {
  const mustSkills = [
    "brainstorming", "test-driven-development", "systematic-debugging",
    "verification-before-completion", "writing-plans", "yesmem-orientation",
    "yesmem-search", "yesmem-remember", "yesmem-planning", "using-superpowers",
  ];
  return mustSkills.includes(name) ? "MUST" : "SHOULD";
}

function main() {
  const skills: SkillYaml[] = [];
  const seen = new Set<string>();

  for (const dir of SKILL_DIRS) {
    let entries: string[] = [];
    try { entries = readdirSync(dir, { withFileTypes: true }).filter(e => e.isDirectory()).map(e => e.name); } catch { continue; }
    for (const entry of entries) {
      const f = join(dir, entry, "SKILL.md");
      try {
        const content = readFileSync(f, "utf-8");
        const fm = parseFrontmatter(content);
        const name = fm.name || entry;
        const desc = fm.description || "";
        if (!desc || seen.has(name)) continue;
        seen.add(name);
        skills.push({
          name,
          description: desc,
          triggers: extractTriggers(desc),
          priority: priorityFor(name),
        });
      } catch {}
    }
  }

  // Generate YAML Skill Catalog
  let id = 25;
  const lines: string[] = [];
  lines.push("## Skill Catalog");
  lines.push("# YAML format: skill, priority (MUST/SHOULD), triggers (exact keyword match from SKILL.md), rule text.");
  lines.push("rules:");

  for (const s of skills) {
    lines.push(`  - id: ${id}`);
    lines.push(`    skill: ${s.name}`);
    lines.push(`    priority: ${s.priority}`);
    const t = s.triggers.length > 0 ? s.triggers.map(x => `"${x}"`).join(", ") : "";
    lines.push(`    triggers: [${t}]`);
    lines.push(`    rule: "${s.description}"`);
    id++;
  }

  console.log(lines.join("\n"));
}

main();
