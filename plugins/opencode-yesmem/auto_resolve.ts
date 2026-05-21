import { YesMemRPC } from "./rpc";

const LOG_FILE = `${process.env.HOME}/.claude/yesmem/logs/plugin.log`;
async function dbgLog(tag: string, msg: string) {
  try {
    const file = Bun.file(LOG_FILE);
    const existing = file.size > 0 ? await file.text() : "";
    await Bun.write(LOG_FILE, existing + `[${new Date().toISOString()}] ${tag} ${msg}\n`);
  } catch {}
}

export function autoResolveHook(rpc: YesMemRPC): Record<string, any> {
  return {
    "tool.execute.after": async (input, output) => {
      if (input.tool !== "bash") return;
      const command = ((output as any).args as any)?.command as string;
      if (!command?.startsWith("git commit")) return;

      const directory = (input.session as any)?.directory || "";
      const mMatch = command.match(/-m\s*["']([^"']+)["']/);
      const msg = mMatch?.[1] || "";
      if (!msg) return;

      try {
        const resp = await rpc.call("resolve_by_text", {
          text: msg,
          project: directory,
        });
        if (resp.ok && resp.result?.resolved > 0) {
          dbgLog("auto_resolve", `resolved via commit: ${msg.substring(0, 80)}`);
        }
      } catch (_) {
        // best-effort, silently ignore
      }
    },
  };
}
