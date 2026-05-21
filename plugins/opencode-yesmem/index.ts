// yesmem opencode plugin (rule_guard: provider-resolution v2)
import { appendFileSync } from "node:fs";
import { codeNavHook } from "./code_nav";

let currentSessionID = "";

const LOG_FILE = `${process.env.HOME}/.claude/yesmem/logs/plugin.log`;
function dbgLog(tag: string, msg: string) {
  try { appendFileSync(LOG_FILE, `[${new Date().toISOString()}] ${tag} ${msg}\n`); } catch {}
}
import { failureLearnHook } from "./failure_learn";
import { autoResolveHook } from "./auto_resolve";
import { idleReminderHook } from "./idle_reminder";
import { hsNudgeHook } from "./hs_nudge";
import { ruleGuardHook } from "./rule_guard";
import { YesMemRPC } from "./rpc";

export const YesMemPlugin = async (ctx: any) => {
  const rpc = new YesMemRPC();
  const directory = ctx.directory || process.env.PWD || "";
          const V = 11; // bump to bust Bun module cache

          // v11: code_nav skips .gitignore/.gitattributes/.gitmodules (git metadata, not code)
  if (directory) {
    rpc.call("search_code_index", {
      pattern: "func",
      kind: "function",
      project: directory,
      limit: 1,
    }).catch(() => {});
  }

  const nv = codeNavHook(rpc, directory);
  const grd = ruleGuardHook(directory);
  const fl = failureLearnHook(rpc);
  const ar = autoResolveHook(rpc);
    const ir = idleReminderHook(rpc);
    const hs = hsNudgeHook();

  // Compose: both need tool.execute.before — code_nav blocks first, then rule_guard
  async function composedBefore(input: any, output: any) {
    try { await nv["tool.execute.before"]?.(input, output); } catch (e) { throw e; }
    try { await grd["tool.execute.before"]?.(input, output); } catch (e) { throw e; }
  }

  // Compose: all three need tool.execute.after — rule_guard injects, failure_learn tracks, auto_resolve resolves
  async function composedAfter(input: any, output: any) {
    try { await grd["tool.execute.after"]?.(input, output); } catch {}
    try { await fl["tool.execute.after"]?.(input, output); } catch {}
    try { await ar["tool.execute.after"]?.(input, output); } catch {}
  }

    // Compose: experimental.chat.messages.transform — rule_guard conversation capture + hs_nudge
    async function composedMessagesTransform(input: any, output: any) {
      try { await grd["experimental.chat.messages.transform"]?.(input, output); } catch {}
      try { await hs["experimental.chat.messages.transform"]?.(input, output); } catch {}
    }

    // Compose: message.updated — idle_reminder
    async function composedMessageUpdated(input: any) {
      try { await ir["message.updated"]?.(input); } catch {}
    }

    return {
      ...grd,              // chat.params, experimental.chat.messages.transform, etc.
      ...nv,               // any future code_nav hooks
      "experimental.chat.messages.transform": composedMessagesTransform,
      "tool.execute.before": composedBefore,
      "tool.execute.after": composedAfter,
      "message.updated": composedMessageUpdated,
      "shell.env": async (_input: any, output: any) => {
        output.env.YESMEM_SOCKET = `${process.env.HOME || "/home/chief"}/.claude/yesmem/daemon.sock`;
        output.env.YESMEM_SOURCE_AGENT = "opencode";
        // Inject current session ID for subprocesses (including MCP server)
        if (currentSessionID) {
          output.env.YESMEM_SESSION_ID = currentSessionID;
        }
        // Re-register PID on every shell env call — ensures daemon has
        // session→PID mapping even after daemon restart.
        const sid = `opencode:${currentSessionID || ""}`;
        if (sid !== "opencode:") {
          rpc.call("register_pid", {
            session_id: sid,
            pid: process.pid,
            source_agent: "opencode",
          }).catch(() => {});
        }
      },
      "session.created": async (input: any) => {
        try {
          const session = input.session;
          if (!session?.id) return;
          currentSessionID = session.id;
          process.env.YESMEM_SESSION_ID = session.id;
          const sid = `opencode:${session.id}`;
          const verbose = process.env.YESMEM_VERBOSE === "1";

        rpc.call("register_pid", {
          session_id: sid,
          pid: process.pid,
          source_agent: "opencode",
        }).catch(() => {});

        if (verbose) {
          console.log(`[yesmem] session.created: ${sid}, pid=${process.pid}`);
        }
      } catch (e: any) {
        if (process.env.YESMEM_VERBOSE === "1") {
          console.error("[yesmem] session.created failed:", e?.message || String(e));
        }
      }
    },
  };
};

export default YesMemPlugin;
