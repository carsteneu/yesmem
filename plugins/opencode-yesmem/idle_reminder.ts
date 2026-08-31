// idle_reminder.ts — invisible yesmem usage reminder, every Nth real user turn.
// Serializes as a request-only PATCH on the terminal user message via
// experimental.chat.messages.transform (ledger_nudge PATCH-ONCE pattern) —
// the model sees the reminder, the opencode TUI shows nothing.
//
// Cadence is counted from the request's own message array: real user turns,
// synthetic-only user messages excluded. Deterministic and stateless —
// headless `opencode run` chains spawn a new process per turn, and the
// daemon-side idle counter resets on any RPC (global lastMCPCallTime).
//
// Cache-safety doctrine (Learning #81269/#85490): the patch touches only the
// terminal user message (the mutable tail); the prompt-cache prefix stays intact.
//
// Disable entirely with the env flag YESMEM_IDLE=off.

import { appendFileSync } from "node:fs";

const LOG_FILE = `${process.env.HOME}/.claude/yesmem/logs/plugin.log`;
const PID = process.pid;
function dbgLog(tag: string, msg: string) {
  try { appendFileSync(LOG_FILE, `[${new Date().toISOString()}] ${tag}[pid${PID}] ${msg}\n`); } catch {}
}

// CADENCE < 2 is pathological: the patched reminder re-sends with its own
// signature, so the marker check must catch it — keep the floor at 2 anyway.
const CADENCE = Math.max(2, Number(process.env.YESMEM_IDLE_CADENCE || 3));

const REMINDER_SIG = "[yesmem-idle]";
const FALLBACK =
  "Du hast ein Langzeitgedaechtnis (yesmem). Bei nicht-trivialen Aufgaben: ZUERST search(thema).";

const MARKER = REMINDER_SIG; // substring guard for idempotency

// findTerminalUserText — same scan as ledger_nudge: LAST genuine user turn with
// a text part. (Local copy: three lines beat a cross-file dependency.)
function findTerminalUserText(msgs: any[]): { text: string; part: any } | null {
  for (let i = msgs.length - 1; i >= 0; i--) {
    const m = msgs[i];
    if (!m) continue;
    const info = m?.info || m;
    if ((info?.role || m?.role) !== "user") continue;
    const parts = m?.parts || info?.parts || [];
    for (const p of parts) {
      if (p?.type === "text" && p?.text) return { text: p.text, part: p };
    }
    // a user message with no text part is not a terminal human turn — keep scanning
  }
  return null;
}

// countRealUserTurns — user messages with at least one non-synthetic text part.
// opencode injects environment context as synthetic-only user messages; those
// are not human turns and must not advance the cadence. Our own reminder patches
// ride INSIDE a real user message, so they neither add nor remove turns.
function countRealUserTurns(msgs: any[]): number {
  let n = 0;
  for (const m of msgs) {
    if (!m) continue;
    const info = m?.info || m;
    if ((info?.role || m?.role) !== "user") continue;
    const parts = m?.parts || info?.parts || [];
    const textParts = parts.filter((p: any) => p?.type === "text" && p?.text);
    if (parts.length > 0 && textParts.length > 0 && textParts.every((p: any) => p.synthetic === true)) continue;
    if (parts.length > 0 && textParts.length === 0 && parts.every((p: any) => p.synthetic === true)) continue;
    n++;
  }
  return n;
}

export function idleReminderHook() {
  return {
    "experimental.chat.messages.transform": async (_input: any, output: any) => {
      try {
        if (process.env.YESMEM_IDLE === "off") return;

        const msgs = output?.messages || [];
        if (msgs.length === 0) return;

        const userTurns = countRealUserTurns(msgs);
        if (userTurns === 0 || userTurns % CADENCE !== 0) return;

        const hit = findTerminalUserText(msgs);
        if (!hit) return;
        // belt-and-suspenders: some loops resend the patched message; never re-append
        if (hit.text.includes(REMINDER_SIG)) return;

        hit.part.text = `${hit.text}\n\n${REMINDER_SIG} ${FALLBACK}`;
        dbgLog("idle_reminder", `patched invisible reminder at user-turn ${userTurns}`);
      } catch (e: any) {
        dbgLog("idle_reminder", `ERR: ${e?.message || String(e)}`);
      }
    },
  };
}
