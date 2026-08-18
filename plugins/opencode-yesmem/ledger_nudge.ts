// ledger_nudge.ts — Live-Ledger subscriber for the active plan.
// Appends the thread-scoped active plan as a compact ledger block to the
// terminal user message each turn via experimental.chat.messages.transform.
//
// Cache-safety doctrine (Learning #81269/#85490): a dynamic per-turn injection
// must NEVER mutate a message non-idempotently — that desyncs the prompt-cache
// prefix. This subscriber follows skill_nudge's proven pattern:
  //   - PATCH-ONCE: per distinct terminal user message exactly one patch, tracked
  //     by lastPatchedKey. The key is the message identity (info.id when present)
  //     so two DISTINCT turns with byte-identical text are not conflated — the new
  //     turn re-fetches a fresh ledger. A mid-turn set_plan/update_plan does NOT
  //     change the already-injected ledger — the new state pulls in only with the
  //     next distinct user message (plan-version-freeze).
//   - NEVER-BLOCK: the RPC fetch runs with a 2s timeout; on any failure the
//     subscriber silently skips injection. The transform is never awaited on a
//     slow remote call that would stall the first model call.
//   - DUMB RENDERER: the ledger is pass-through — it does not parse free-text
//     plans. Structure comes from the set_plan schema convention (Deliverable A).
//     A plan that isn't schema-shaped is still injected raw (a live ledger beats
//     none).
//
// Disable entirely with the env flag YESMEM_LEDGER=off.

import { appendFileSync } from "node:fs";
import type { YesMemRPC } from "./rpc";

const LOG_FILE = `${process.env.HOME}/.claude/yesmem/logs/plugin.log`;
const PID = process.pid;
function dbgLog(tag: string, msg: string) {
  try { appendFileSync(LOG_FILE, `[${new Date().toISOString()}] ${tag}[pid${PID}] ${msg}\n`); } catch {}
}

// Budget for the rendered ledger: max source lines before truncation.
const MAX_LEDGER_LINES = 40;
const HEADER = "[yesmem-ledger v1]";
const FOOTER = "[/yesmem-ledger]";
const MARKER = HEADER; // substring guard for idempotency

function hashStr(s: string): string {
  let h = 0;
  for (let i = 0; i < s.length; i++) {
    h = ((h << 5) - h) + s.charCodeAt(i);
    h |= 0;
  }
  return String(h);
}

// findTerminalUserText scans messages for the LAST genuine user turn and returns
// {text, part, msg} for the message+part to patch. Only role=user messages with a
// text part qualify; tool-result and assistant messages are skipped.
function findTerminalUserText(msgs: any[]): { text: string; part: any; msg: any } | null {
  for (let i = msgs.length - 1; i >= 0; i--) {
    const m = msgs[i];
    if (!m) continue;
    const info = m?.info || m;
    const role = info?.role || m?.role;
    if (role !== "user") continue;
    const parts = m?.parts || info?.parts || [];
    for (const p of parts) {
      if (p?.type === "text" && p?.text) return { text: p.text, part: p, msg: m };
    }
    // A user message with no text part is not a terminal human turn — keep scanning up.
  }
  return null;
}

// renderLedger builds the ledger block from the active plan content (pass-through).
// Returns null when there is nothing useful to show (no active plan / empty).
export function renderLedger(plan: string): string | null {
  if (!plan || !plan.trim()) return null;
  const lines = plan.split("\n");
  const truncated = lines.length > MAX_LEDGER_LINES;
  const body = (truncated ? lines.slice(0, MAX_LEDGER_LINES) : lines).map((l) => `  ${l}`).join("\n");
  const hint = truncated ? `\n  … (ledger truncated, ${lines.length - MAX_LEDGER_LINES} more lines)` : "";
  return `${HEADER}\n${body}${hint}\n${FOOTER}`;
}

// ledgerNudgeHook returns the transform subscriber. getThreadID resolves the
// current thread id (e.g. "opencode:<sessionID>"); pass a closure over the
// module-level currentSessionID from index.ts.
export function ledgerNudgeHook(rpc: YesMemRPC, getThreadID: () => string) {
  let lastPatchedKey = "";

  return {
    "experimental.chat.messages.transform": async (_input: any, output: any) => {
      try {
        if (process.env.YESMEM_LEDGER === "off") return;

        const msgs = output?.messages || [];
        if (msgs.length === 0) return;

        const hit = findTerminalUserText(msgs);
        if (!hit) return;

        // PATCH-ONCE: key on message identity when available so distinct turns with
        // byte-identical text still get a fresh ledger; fall back to content hash
        // for synthetic/non-id messages. Stable within a turn → mid-turn plan
        // changes stay frozen on the already-injected ledger.
        const messageId = hit.msg?.info?.id || hit.msg?.id || "";
        const key = messageId ? `id:${messageId}` : hashStr(hit.text);
        if (key === lastPatchedKey) return;
        // Belt-and-suspenders: some loops resend the patched message; never re-append.
        if (hit.text.includes(MARKER)) {
          lastPatchedKey = key;
          return;
        }

        const threadID = getThreadID();
        if (!threadID) return;

        const resp = await rpc.call("get_plan", { thread_id: threadID }, { timeoutMs: 2000 });
        if (!resp.ok || !resp.result) {
          dbgLog("ledger", `get_plan skip: ${resp.error || "empty"}`);
          lastPatchedKey = key; // don't retry this message on every transform
          return;
        }
        const r = resp.result;
        if (!r.exists || r.status !== "active") { lastPatchedKey = key; return; }
        const ledger = renderLedger(typeof r.plan === "string" ? r.plan : "");
        if (!ledger) { lastPatchedKey = key; return; }

        hit.part.text = hit.text + "\n\n" + ledger;
        lastPatchedKey = key;
        dbgLog("ledger", `APPENDED ledger (${ledger.length} chars)`);
      } catch (e: any) {
        dbgLog("ledger", `ERR: ${e?.message || String(e)}`);
      }
    },
  };
}
