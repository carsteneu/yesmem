import { appendFileSync } from "node:fs";

const LOG_FILE = `${process.env.HOME}/.claude/yesmem/logs/plugin.log`;

function dbgLog(tag: string, msg: string) {
  try { appendFileSync(LOG_FILE, `[${new Date().toISOString()}] ${tag} ${msg}\n`); } catch {}
}

const NUDGE = "🧠 MANDATORY! hybrid_search() before answering — facts over intuition.";

export function hsNudgeHook(): Record<string, any> {
  return {
    "experimental.chat.messages.transform": async (_input: any, output: any) => {
      try {
        const msgs = output?.messages || [];
        for (let i = msgs.length - 1; i >= 0; i--) {
          const m = msgs[i];
          const info = m?.info || m;
          const role = info?.role || m?.role;
          if (role !== "user") continue;

          const parts = m?.parts || info?.parts || [];
          for (const p of parts) {
            if (p?.type === "text" && p?.text) {
              p.text = NUDGE + "\n\n" + p.text;
              dbgLog("hs_nudge", `NUDGED`);
              return;
            }
          }
          break;
        }
      } catch (_) { /* best-effort */ }
    },
  };
}
