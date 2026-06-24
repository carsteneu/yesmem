// skill_nudge.ts — Staged skill suggestion on user messages
// Stage 1: Local substring match against YAML trigger arrays (fast, deterministic)
// Stage 2: DeepSeek V4 Flash fallback via direct API (semantic match)
// Injects nudge via experimental.chat.messages.transform (same surface as hs_nudge.ts)

import { appendFileSync } from "node:fs";
import { resolveGuardConfig } from "./rule_guard";

const LOG_FILE = `${process.env.HOME}/.claude/yesmem/logs/plugin.log`;
function dbgLog(tag: string, msg: string) {
  try { appendFileSync(LOG_FILE, `[${new Date().toISOString()}] ${tag} ${msg}\n`); } catch {}
}

interface SkillEntry {
  skill: string;
  priority: string;
  triggers: string[];
}

function hashStr(s: string): string {
  let h = 0;
  for (let i = 0; i < s.length; i++) {
    h = ((h << 5) - h) + s.charCodeAt(i);
    h |= 0;
  }
  return String(h);
}

// Parse YAML strings with escape-sequence handling (e.g., \" → ")
function extractYamlStrings(text: string): string[] {
  const result: string[] = [];
  let inString = false;
  let current = "";
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (!inString) {
      if (c === '"') inString = true;
      continue;
    }
    if (c === "\\" && i + 1 < text.length && text[i + 1] === '"') {
      current += '"';
      i++; // skip the escaped quote
      continue;
    }
    if (c === '"') {
      const trimmed = current.trim();
      if (trimmed) result.push(trimmed.toLowerCase());
      current = "";
      inString = false;
      continue;
    }
    current += c;
  }
  const trimmed = current.trim();
  if (trimmed) result.push(trimmed.toLowerCase());
  return result;
}

// Parse YAML Skill Catalog from ## Skill Catalog section
// Structure: YAML list of {id, skill, priority, triggers[], rule}
function parseSkillCatalog(content: string): SkillEntry[] {
  const entries: SkillEntry[] = [];
  const sectionMatch = content.match(/## Skill Catalog\s*\n([\s\S]*?)(?=\n## |\n---\n|$)/);
  if (!sectionMatch) return entries;

  // Split on "- id:" at any indent level
  const blocks = sectionMatch[1].split(/\n\s+- id:\s*/);
  for (const block of blocks) {
    if (!block || block.trim() === "") continue;

    const skillMatch = block.match(/skill:\s*"?([^"\n]+)"?/);
    const priorityMatch = block.match(/priority:\s*(\S+)/);
    if (!skillMatch) continue;

    const triggers: string[] = [];
    const triggersSection = block.match(/triggers:\s*\[([\s\S]*?)\]/);
    if (triggersSection) {
      const strings = extractYamlStrings(triggersSection[1]);
      triggers.push(...strings);
    }

    const priority = priorityMatch ? priorityMatch[1] : "MUST";
    entries.push({ skill: skillMatch[1].trim(), priority, triggers });
  }

  // Sort once at parse time: MUST first, then by priority string
  entries.sort((a, b) => {
    if (a.priority !== b.priority) return a.priority === "MUST" ? -1 : 1;
    return 0;
  });
  return entries;
}

// Stage 1: Local substring match — check user message against trigger literals
// Catalog is already sorted by priority (MUST first) from parseSkillCatalog
function localMatch(userMsg: string, catalog: SkillEntry[]): string | null {
  const lower = userMsg.toLowerCase();
  for (const entry of catalog) {
    for (const trigger of entry.triggers) {
      if (lower.includes(trigger)) return entry.skill;
    }
  }
  return null;
}

// Stage 2: DeepSeek V4 Flash evaluation — semantic match when local fails
async function llmMatch(userMsg: string, catalogText: string): Promise<string | null> {
  const cfg = await resolveGuardConfig();
  if (!cfg || !cfg.apiKey) return null;

  const systemPrompt =
    `You evaluate user messages against a skill catalog. ` +
    `If the user's intent matches a skill, respond with ONLY the skill name. ` +
    `If no skill matches, respond with "NONE". ` +
    `No explanation. No formatting.\n\n## Skill Catalog\n${catalogText}`;

  const userPrompt = `User message: "${userMsg.substring(0, 2000)}"`;

  try {
    const resp = await fetch(`${cfg.apiUrl}/v1/chat/completions`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${cfg.apiKey}` },
      body: JSON.stringify({
        model: cfg.model,
        messages: [
          { role: "system", content: systemPrompt },
          { role: "user", content: userPrompt },
        ],
        temperature: 0,
        max_tokens: 64,
      }),
      signal: AbortSignal.timeout(10000),
    });
    if (!resp.ok) { dbgLog("skill_nudge", `LLM err ${resp.status}`); return null; }
    const data = await resp.json() as any;
    const content = data?.choices?.[0]?.message?.content || "";
    const trimmed = content.trim();
    if (trimmed && trimmed !== "NONE") { dbgLog("skill_nudge", `LLM: ${trimmed}`); return trimmed; }
    return null;
  } catch (e: any) { dbgLog("skill_nudge", `LLM err: ${e.message}`); return null; }
}

export function skillNudgeHook() {
  const rulesPath = new URL('./RULES.md', import.meta.url).pathname;
  let parsedCatalog: SkillEntry[] | null = null;
  let catalogMtime = 0;
  let lastMsgHash = "";

  return {
    "experimental.chat.messages.transform": async (_input: any, output: any) => {
      try {
        // 1. Find the last user message (same pattern as hs_nudge.ts)
        const msgs = output?.messages || [];
        let userText = "";
        for (let i = msgs.length - 1; i >= 0; i--) {
          const m = msgs[i];
          const info = m?.info || m;
          const role = info?.role || m?.role;
          if (role !== "user") continue;
          const parts = m?.parts || info?.parts || [];
          for (const p of parts) {
            if (p?.type === "text" && p?.text) { userText = p.text; break; }
          }
          break;
        }
        if (!userText) return;

        // Idempotency: skip if skill nudge already present
        if (userText.includes("MANDATORY CHECK — activate ")) return;

        // Hash cache: avoid redundant LLM calls for same user message
        const hash = hashStr(userText);
        if (hash === lastMsgHash) return;

        // 2. Load and parse Skill Catalog (mtime-checked, reloads on change)
        try {
          const stat = await Bun.file(rulesPath).stat();
          if (parsedCatalog === null || stat.mtimeMs !== catalogMtime) {
            const content = await Bun.file(rulesPath).text();
            parsedCatalog = parseSkillCatalog(content);
            catalogMtime = stat.mtimeMs;
            dbgLog("skill_nudge", `Loaded ${parsedCatalog.length} skills`);
          }
        } catch {}
        if (!parsedCatalog || parsedCatalog.length === 0) return;

        // 3. Stage 1: Local substring match
        let matchedSkill = localMatch(userText, parsedCatalog);

        // 4. Stage 2: LLM fallback (DeepSeek V4 Flash)
        let llmAttempted = false;
        if (!matchedSkill) {
          const catalogText = parsedCatalog.map(e =>
            `- ${e.skill} (${e.priority}): ${e.triggers.join(", ")}`
          ).join("\n");
          matchedSkill = await llmMatch(userText, catalogText);
          llmAttempted = true;
        }

        // 5. Cache hash only after success (retry on transient failure)
        if (matchedSkill || llmAttempted) {
          lastMsgHash = hash;
        }

        // 6. Prepend nudge to user message
        if (matchedSkill) {
          for (let i = msgs.length - 1; i >= 0; i--) {
            const m = msgs[i];
            const info = m?.info || m;
            const role = info?.role || m?.role;
            if (role !== "user") continue;
            const parts = m?.parts || info?.parts || [];
            for (const p of parts) {
              if (p?.type === "text" && p?.text) {
                p.text = `🧠 MANDATORY CHECK — activate ${matchedSkill} (use Skill tool)\n\n` + p.text;
                dbgLog("skill_nudge", `NUDGED: ${matchedSkill}`);
                return;
              }
            }
            break;
          }
        }
      } catch (e: any) {
        dbgLog("skill_nudge", `ERR: ${e.message}`);
      }
    },
  };
}
