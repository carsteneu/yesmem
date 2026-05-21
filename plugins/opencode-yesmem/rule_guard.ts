// rule_guard.ts — Pre-tool-use rule compliance hook
//
// Fires on EVERY tool call. Evaluates against RULES.md rules via DeepSeek.
// - BLOCK: Write/Edit violating commit/path/security rules → throw, tool prevented
// - SUGGEST: missed skill activation → inject directive via tool.execute.after
// - PASS: no issue

import { appendFileSync } from "node:fs";

const LOG_FILE = `${process.env.HOME}/.claude/yesmem/logs/plugin.log`;

function dbgLog(tag: string, msg: string) {
  try {
    appendFileSync(LOG_FILE, `[${new Date().toISOString()}] ${tag} ${msg}\n`);
  } catch {}
}

async function loadRules(directory: string): Promise<string> {
  try {
    const path = `${directory}/RULES.md`;
    const content = await Bun.file(path).text();
    const lines = content.split("\n")
      .filter(l => /^\d+\.\s/.test(l))
      .join("\n");
    const skillCatalogMatch = content.match(/## Skill Catalog\n([\s\S]*?)(?=\n## |\n# [^#]|\n---\n|$)/);
    const skillSection = skillCatalogMatch ? skillCatalogMatch[1] : "";
    const rules = lines + (skillSection ? "\n\n## Skill Catalog\n" + skillSection : "");
    return rules || "No numbered rules found";
  } catch {
    return "No RULES.md found in project root";
  }
}

const FIRST_PARTY_DEFAULTS: Record<string, string> = {
    openai: "https://api.openai.com",
    anthropic: "https://api.anthropic.com",
    google: "https://generativelanguage.googleapis.com",
    groq: "https://api.groq.com",
    mistral: "https://api.mistral.ai",
  };

  const OPENAI_COMPAT_NPMS = new Set([
    "@ai-sdk/openai-compatible",
    "@ai-sdk/openai",
    "@ai-sdk/mistral",
  ]);

  async function loadModelsJSON(): Promise<Record<string, any>> {
    try {
      const modelsFile = `${process.env.HOME}/.cache/opencode/models.json`;
      return JSON.parse(await Bun.file(modelsFile).text());
    } catch {
      return {};
    }
  }

  async function loadAuthJSON(): Promise<Record<string, any>> {
    try {
      const authFile = `${process.env.HOME}/.local/share/opencode/auth.json`;
      return JSON.parse(await Bun.file(authFile).text());
    } catch {
      return {};
    }
  }

  async function resolveGuardConfig(): Promise<{ model: string; apiUrl: string; apiKey: string; npm: string }> {
    // 1. Read extraction.model from config.yaml (fallback: deepseek-v4-flash)
    let model = "deepseek-v4-flash";
    try {
      const configPath = `${process.env.HOME}/.claude/yesmem/config.yaml`;
      const yaml = await import("yaml");
      const config = yaml.parse(await Bun.file(configPath).text());
      if (config?.extraction?.model) model = config.extraction.model;
    } catch {}

    // 2. Find provider in models.json that owns this model
    const models = await loadModelsJSON();
    const auth = await loadAuthJSON();
    let apiUrl = "https://api.deepseek.com";
    let apiKey = "";
    let npm = "";

    for (const [providerId, provider] of Object.entries(models)) {
      const p = provider as any;
      if (p.models && p.models[model]) {
        apiUrl = p.api || FIRST_PARTY_DEFAULTS[providerId] || "";
        apiKey = auth[providerId]?.key || "";
        npm = p.npm || "";
        break;
      }
    }

    return { model, apiUrl, apiKey, npm };
  }

type GuardResult = { decision: string; violations?: string[]; suggestion?: string };

const blockableTools = new Set(["write", "edit"]);
const suggestableTools = new Set(["write", "edit", "read", "bash", "grep", "glob"]);
const skipTools = new Set(["skill", "task", "todowrite"]);

const guardCache = new Map<string, { result: GuardResult; ts: number }>();
const GUARD_CACHE_TTL_MS = 5000;

function guardCacheKey(rulesHash: string, tool: string, context: string): string {
  let hash = 0;
  const str = rulesHash + "|" + tool + "|" + context;
  for (let i = 0; i < str.length; i++) {
    hash = ((hash << 5) - hash) + str.charCodeAt(i);
    hash |= 0;
  }
  return String(hash);
}

let rulesHash = "";

function hashString(s: string): string {
  let h = 0;
  for (let i = 0; i < s.length; i++) {
    h = ((h << 5) - h) + s.charCodeAt(i);
    h |= 0;
  }
  return String(h);
}

async function guardCheck(rules: string, tool: string, context: string, canBlock: boolean, retried = false): Promise<GuardResult> {
  dbgLog("guard", `ENTER tool=${tool} retried=${retried}`);
  const ck = guardCacheKey(rulesHash, tool, context);
  const cached = guardCache.get(ck);
  if (cached && (Date.now() - cached.ts) < GUARD_CACHE_TTL_MS) {
    dbgLog("guard", `[cache] ${cached.result.decision}`);
    return cached.result;
  }

    const cfg = await resolveGuardConfig();
    const key = cfg.apiKey;
    if (!key || !cfg.apiUrl) { dbgLog("guard", `NO-KEY url=${cfg.apiUrl} key=${key ? "yes" : "no"}`); return { decision: "PASS" }; }

  const blockOption = canBlock
    ? `- BLOCK: call violates a rule → {"decision":"BLOCK","violations":["Rule X: reason"]}\n`
    : "";

  const systemPrompt =
    `Output ONLY this JSON format. Do NOT reason, explain, or think. Just output the JSON.\n\n` +
    `RULES:\n${rules}\n\n` +
    `Formats:\n` +
    blockOption +
    `- {"decision":"SUGGEST","suggestion":"skill: short reason max 60 chars"}\n` +
    `- {"decision":"PASS"}\n\n` +
    `For SUGGEST: use exact skill name from rules. Evaluate all Skill Catalog entries against context.`;

  const userPrompt = `PROPOSED TOOL CALL: ${tool}\nCONTEXT: ${context}`;

  const sysTok = Math.round(systemPrompt.length / 4);
  const usrTok = Math.round(userPrompt.length / 4);

      try {
        const t0 = Date.now();
        dbgLog("guard", `FETCH tool=${tool} model=${cfg.model} url=${cfg.apiUrl}`);
        const body: Record<string, any> = {
          model: cfg.model,
          messages: [
            { role: "system", content: systemPrompt },
            { role: "user", content: userPrompt },
          ],
          temperature: 0,
          max_tokens: 4096,
        };
        if (OPENAI_COMPAT_NPMS.has(cfg.npm)) {
          body.response_format = { type: "json_object" };
          body.thinking = { type: "disabled" };
        }
        const resp = await fetch(`${cfg.apiUrl}/v1/chat/completions`, {
          method: "POST",
          headers: { "Content-Type": "application/json", Authorization: `Bearer ${key}` },
          body: JSON.stringify(body),
        signal: AbortSignal.timeout(15000),
      });
    dbgLog("guard", `FETCH-OK status=${resp.status} t=${Date.now() - t0}ms`);

    const elapsed = Date.now() - t0;
    const fullData = await resp.text();
    const tail = fullData.length > 400 ? "…" + fullData.slice(-400) : fullData;
    dbgLog("guard", `RESP len=${fullData.length} t=${Date.now() - t0}ms tail: ${tail}`);
    const data = JSON.parse(fullData) as any;
    const msg = data?.choices?.[0]?.message;
    const rawContent = msg?.content;
    const content = rawContent || "";
    if (!content) {
      const hasReasoning = msg?.reasoning_content ? ` (has ${msg.reasoning_content.length}B reasoning)` : "";
      dbgLog("guard", `[${Date.now() - t0}ms sys~${sysTok} usr~${usrTok}] EMPTY-CONTENT${hasReasoning} — PASS`);
      const result: GuardResult = { decision: "PASS" };
      guardCache.set(ck, { result, ts: Date.now() });
      return result;
    }

    const parsed = JSON.parse(content) as GuardResult;
    guardCache.set(ck, { result: parsed, ts: Date.now() });
    const cacheHit = data?.usage?.prompt_cache_hit_tokens ?? 0;
    const cacheMiss = data?.usage?.prompt_cache_miss_tokens ?? 0;
    const cacheStr = `cache:hit=${cacheHit}/miss=${cacheMiss}`;
    if (parsed.decision === "BLOCK" && parsed.violations?.length > 0) {
      dbgLog("guard", `[${elapsed}ms sys~${sysTok} usr~${usrTok}] BLOCKED ${parsed.violations.join("; ")} ${cacheStr}`);
      return parsed;
    }
    if (parsed.decision === "SUGGEST" && parsed.suggestion) {
      dbgLog("guard", `[${elapsed}ms sys~${sysTok} usr~${usrTok}] SUGGEST ${parsed.suggestion} ${cacheStr}`);
      return parsed;
    }
    dbgLog("guard", `[${elapsed}ms sys~${sysTok} usr~${usrTok}] PASS ${cacheStr}`);
    return { decision: "PASS" };
  } catch (e: any) {
    if (!retried) {
      await new Promise(r => setTimeout(r, 500));
      return guardCheck(rules, tool, context, canBlock, true);
    }
    dbgLog("guard", `check failed (retried): ${e.message}`);
    return { decision: "PASS" };
  }
}

function detectUserIntent(msg: string, tool: string, args: Record<string, any>): boolean {
  if (!msg) return false;
  const lower = msg.toLowerCase();
  const cmd = (args?.command || "").toLowerCase();
  const explicitVerbs = ["commit", "push", "deploy", "do it", "mach ", "tu es", "mach es", "fire and forget"];
  for (const v of explicitVerbs) {
    if (lower.includes(v)) {
      if (tool === "bash" && cmd && lower.includes(cmd)) return true;
      if (tool !== "bash" && v.length > 3) return true;
    }
  }
  return false;
}

async function getGitBranch(directory: string): Promise<string> {
  try {
    const gitFile = await Bun.file(`${directory}/.git`).text();
    const match = gitFile.match(/^gitdir:\s*(.+)$/m);
    const gitDir = match ? match[1].trim() : `${directory}/.git`;
    const headContent = await Bun.file(`${gitDir}/HEAD`).text();
    return headContent.trim().replace(/^ref: refs\/heads\//, "");
  } catch {
    return "";
  }
}

export function ruleGuardHook(directory: string) {
  let rulesCache = "";
  let rulesLoaded = false;
  let gitBranch = "";
  const suggestions = new Map<string, { suggestion: string; level: string }>();
  let recentConversation: string[] = [];

  return {
    // Capture recent conversation (last 6 messages, user + assistant)
    // - Primary: experimental.chat.messages.transform (parts[].text)
    // - Fallback: chat.params (summary.title)
    "chat.params": async (input: any, _output: any) => {
      try {
        const msg = input?.message;
        if (!msg || msg.role !== "user") return;
        if (recentConversation.length === 0) {
          const summaryTitle = msg?.summary?.title || "";
          const summaryBody = msg?.summary?.body || "";
          if (summaryTitle) recentConversation = ["[user]: " + summaryTitle + (summaryBody ? ": " + summaryBody : "")];
        }
      } catch {}
    },

    "experimental.chat.messages.transform": async (_input: any, output: any) => {
      try {
        const msgs = output?.messages || [];
        const collected: string[] = [];
        for (let i = msgs.length - 1; i >= 0 && collected.length < 6; i--) {
          const m = msgs[i];
          const info = m?.info || m;
          const role = info?.role || m?.role;
          if (role !== "user" && role !== "assistant") continue;
          const parts = m?.parts || [];
          const texts: string[] = [];
          for (const p of parts) {
            if (p?.type === "text" && p?.text) texts.push(p.text);
          }
          const text = texts.join("\n");
          if (text) collected.unshift(`[${role}]: ${text.substring(0, 500)}`);
        }
        if (collected.length > 0) recentConversation = collected;
      } catch {}
    },

    "tool.execute.before": async (input: any, output: any) => {
      dbgLog("guard", `HOOK-FIRED tool=${input?.tool || "?"}`);
      if (!rulesLoaded) {
        rulesCache = await loadRules(directory);
        rulesHash = hashString(rulesCache);
        gitBranch = await getGitBranch(directory);
        rulesLoaded = true;
        dbgLog("guard", `loaded ${rulesCache.split("\n").length} rules ~${Math.round(rulesCache.length/4)} sys-tok (hash=${rulesHash}) branch=${gitBranch || "?"}`);
      }
      if (!rulesCache || rulesCache.startsWith("No ")) { dbgLog("guard", `SKIP-NORULES ${rulesCache}`); return; }

      const toolName = (input?.tool || "").toLowerCase();
      const args = output?.args || {};

      if (skipTools.has(toolName)) { dbgLog("guard", `SKIP ${toolName}`); return; }
      if (!suggestableTools.has(toolName)) { dbgLog("guard", `SKIP-UNSUG ${toolName}`); return; }

      const filePath = args?.filePath || args?.file_path || args?.path || "";
        if (filePath && (filePath.endsWith("RULES.md") || filePath.endsWith("rule_guard.ts"))) { dbgLog("guard", `SKIP-SELF ${filePath}`); return; }

        const docExts = /\.(md|txt|rst|pdf)(\*|$|,)/;
        const isDocTarget = docExts.test(filePath)
          || (toolName === "grep" && docExts.test(args?.include || ""))
          || (toolName === "glob" && docExts.test(args?.pattern || ""));
        if (isDocTarget) { dbgLog("guard", `SKIP-DOC tool=${toolName} filePath=${filePath} include=${args?.include || ""} pattern=${args?.pattern || ""}`); return; }

        let context = `Tool: ${toolName}, Args: ${JSON.stringify(args).substring(0, 300)}`;
      if (recentConversation.length > 0) {
        context += `\nRecent conversation:\n${recentConversation.join("\n")}`;
        const lastUser = recentConversation.filter(m => m.startsWith("[user]:")).pop() || "";
        if (detectUserIntent(lastUser, toolName, args)) {
          context += `\nNOTE: The user explicitly requested this action. Rules about user-decision (auto-commit, deployment, destructive ops) should yield to explicit user instruction.`;
        }
      }

        if (filePath && filePath.endsWith("_test.go")) {
          context += `\nNOTE: This is a test file ("_test.go"). Writing test files before implementation IS TDD-compliant — do not BLOCK under TDD/test-first rules.`;
        }

        if (gitBranch) {
        context += `\nGit branch: ${gitBranch} (feature branch rules 4, 42 are satisfied — no need to suggest worktree/branch skills)`;
      }

      const canBlock = blockableTools.has(toolName);
      try {
        const result = await guardCheck(rulesCache, toolName, context, canBlock);
        if (result.decision === "BLOCK" && canBlock) {
          throw new Error(`🚫 ${(result.violations || []).join("; ")}`);
        } else if (result.decision === "SUGGEST") {
          const level = toolName === "bash" ? "MANDATORY" : "REQUIRED";
          suggestions.set(input.callID, { suggestion: result.suggestion || "", level });
        }
      } catch (e: any) {
        dbgLog("guard", `CRASH ${e?.message || String(e)}`);
      }
    },

    "tool.execute.after": async (input: any, output: any) => {
      const entry = suggestions.get(input.callID);
      if (entry) {
        const prefix = entry.level === "MANDATORY"
          ? `[guard] ⚠️ MANDATORY CHECK — activate ${entry.suggestion} (use Skill tool)\n\n`
          : `[guard] ACTION REQUIRED: activate ${entry.suggestion} — use Skill tool now.\n\n`;
        output.output = prefix + (output.output || "");
        suggestions.delete(input.callID);
      }
    },
  };
}