// skill_nudge.ts — Staged skill suggestion on user messages
// Stage 1: Local substring match against YAML trigger arrays (fast, deterministic, on critical path)
// Stage 2: DeepSeek V4 Flash fallback via direct API (semantic match) — NEVER on critical path:
//          results are LRU-cached by (catalogHash, messageHash) and precomputed in the background
//          so a later identical message reuses them. The transform hook always returns promptly.
// Injects nudge via experimental.chat.messages.transform

import { appendFileSync } from "node:fs";
import { resolveGuardConfig } from "./rule_guard";
import { isSkillInstalled } from "./skill_whitelist";

const LOG_FILE = `${process.env.HOME}/.claude/yesmem/logs/plugin.log`;
const PID = process.pid;
function dbgLog(tag: string, msg: string, inst?: string) {
  try {
    appendFileSync(LOG_FILE, `[${new Date().toISOString()}] ${tag}[pid${PID}${inst ? ":" + inst : ""}] ${msg}\n`);
  } catch {}
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

// --- LRU result cache: key = `${catalogHash}:${messageHash}` → matchedSkill | null (no match) ---
const RESULT_CACHE_MAX = 128;
const resultCache = new Map<string, string | null>();
const llmInFlight = new Set<string>();

function cacheGet(key: string): string | null | undefined {
  return resultCache.get(key); // undefined = miss, null = cached "no match", string = skill
}
function cacheSet(key: string, value: string | null) {
  resultCache.set(key, value);
  if (resultCache.size > RESULT_CACHE_MAX) {
    const oldest = resultCache.keys().next().value;
    if (oldest !== undefined) resultCache.delete(oldest);
  }
}

// --- Guard config cache: resolveGuardConfig re-reads 3 files from disk; don't redo per request ---
let cfgCache: { cfg: { model: string; apiUrl: string; apiKey: string; npm: string }; ts: number } | null = null;
const CFG_CACHE_TTL_MS = 300_000;
async function getGuardConfig() {
  if (cfgCache && Date.now() - cfgCache.ts < CFG_CACHE_TTL_MS) return cfgCache.cfg;
  const cfg = await resolveGuardConfig();
  cfgCache = { cfg, ts: Date.now() };
  return cfg;
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

// Stage 2: DeepSeek V4 Flash evaluation — semantic match when local fails.
// Only ever called off the critical path (background precompute). Uses cached guard config.
async function llmMatch(userMsg: string, catalogText: string): Promise<string | null> {
  const cfg = await getGuardConfig();
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

// Fire-and-forget semantic match: populates the result cache for later identical messages.
// Deduplicates concurrent calls for the same key. Never awaited by the hook.
function precomputeLLMMatch(key: string, userText: string, catalogText: string) {
  if (llmInFlight.has(key)) return;
  llmInFlight.add(key);
  llmMatch(userText, catalogText)
    .then((skill) => { cacheSet(key, skill); dbgLog("skill_nudge", `cached ${key}=${skill ?? "NONE"}`); })
    .catch(() => {})
    .finally(() => llmInFlight.delete(key));
}

export function skillNudgeHook() {
  const rulesPath = new URL('./RULES.md', import.meta.url).pathname;
  const instId = hashStr(rulesPath + Date.now() + Math.random().toString(36)).slice(0, 6);
  let parsedCatalog: SkillEntry[] | null = null;
  let catalogMtime = 0;
  let catalogText = "";
  let catalogHash = "";
  let lastMsgHash = "";

  return {
    "experimental.chat.messages.transform": async (_input: any, output: any) => {
      try {
        // 1. Find the last user message
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

        // Hash cache: handle each distinct message at most once per hook instance.
        const hash = hashStr(userText);
        if (hash === lastMsgHash) return;
        lastMsgHash = hash;

        // 2. Load and parse Skill Catalog (mtime-checked, reloads on change); cache text + hash
        try {
          const stat = await Bun.file(rulesPath).stat();
          if (parsedCatalog === null || stat.mtimeMs !== catalogMtime) {
            const content = await Bun.file(rulesPath).text();
            parsedCatalog = parseSkillCatalog(content);
            catalogMtime = stat.mtimeMs;
            catalogText = parsedCatalog.map(e =>
              `- ${e.skill} (${e.priority}): ${e.triggers.join(", ")}`
            ).join("\n");
            catalogHash = hashStr(catalogText);
            dbgLog("skill_nudge", `Loaded ${parsedCatalog.length} skills`, instId);
          }
        } catch {}
        if (!parsedCatalog || parsedCatalog.length === 0) return;

        // 3. Stage 1: Local substring match (fast, on critical path)
        let matchedSkill = localMatch(userText, parsedCatalog);

        // 4. Stage 2: LLM fallback — cache hit applies synchronously; cache miss precomputes in background.
        //    The remote call is deliberately NOT awaited by the transform, so the first model call is never blocked.
        if (!matchedSkill) {
          const key = `${catalogHash}:${hash}`;
          if (cacheGet(key) !== undefined) {
            matchedSkill = cacheGet(key); // string skill, or null (cached "no match")
          } else {
            precomputeLLMMatch(key, userText, catalogText);
          }
        }

        // 5. Prepend nudge to user message
        if (matchedSkill) {
          if (!isSkillInstalled(matchedSkill)) {
            dbgLog("skill_nudge", `SKIP ghost skill: ${matchedSkill}`, instId);
            return;
          }
          for (let i = msgs.length - 1; i >= 0; i--) {
            const m = msgs[i];
            const info = m?.info || m;
            const role = info?.role || m?.role;
            if (role !== "user") continue;
            const parts = m?.parts || info?.parts || [];
            for (const p of parts) {
              if (p?.type === "text" && p?.text) {
                p.text = `🧠 MANDATORY CHECK — activate ${matchedSkill} (use Skill tool)\n\n` + p.text;
                dbgLog("skill_nudge", `NUDGED: ${matchedSkill}`, instId);
                return;
              }
            }
            break;
          }
        }
      } catch (e: any) {
        dbgLog("skill_nudge", `ERR: ${e.message}`, instId);
      }
    },
  };
}
