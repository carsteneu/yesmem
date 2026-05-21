import { YesMemRPC } from "./rpc";

const lastReminder = new Map<string, number>();

export function idleReminderHook(rpc: YesMemRPC): Record<string, any> {
  return {
    "message.updated": async (input) => {
      if (input.message?.role !== "user") return;

      const sessionId = input.session?.id;
      if (!sessionId) return;

      // Throttle: one check per session
      const last = lastReminder.get(sessionId) || 0;
      if (last > 0) return;
      lastReminder.set(sessionId, Date.now());

      try {
        const resp = await rpc.call("idle_tick", { session_id: sessionId });
        if (resp.ok && resp.result?.count >= 30) {
          const reminder = resp.result.reminder ||
            "Du hast ein Langzeitgedaechtnis (yesmem). Bei nicht-trivialen Aufgaben: ZUERST search(thema).";
          try {
            const client = (input as any).client;
            if (client?.session?.prompt) {
              await client.session.prompt({
                path: { id: sessionId },
                body: {
                  noReply: true,
                  parts: [{ type: "text", text: reminder }],
                },
              });
            }
          } catch (_) { /* best-effort prompt injection */ }
        }
      } catch (_) { /* daemon unreachable, skip */ }
    },
  };
}
