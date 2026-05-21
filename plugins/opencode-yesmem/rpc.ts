import { $ } from "bun";
import type { RPCResponse } from "./types";

export class YesMemRPC {
  private socketPath: string;

  constructor(socketPath?: string) {
    this.socketPath = socketPath ||
      process.env.YESMEM_SOCKET ||
      `${process.env.HOME || "/home/" + (process.getuid?.() ?? "chief")}/.claude/yesmem/daemon.sock`;
  }

  async call(method: string, params?: Record<string, any>): Promise<RPCResponse> {
    const payload = JSON.stringify({ method, params: params || {} });
    try {
      const cmd = `echo ${$.escape(payload)} | nc -U -w 20 ${$.escape(this.socketPath)}`;
      const result = await $`sh -c ${cmd}`.quiet();
      if (result.exitCode !== 0) {
        return { ok: false, error: `nc exit ${result.exitCode}: ${result.stderr}` };
      }
      const text = result.stdout.toString().trim();
      if (!text) return { ok: false, error: "empty response" };

      // Daemon returns raw JSON — {result: ...} on success, {error: "..."} on error.
      // Parse and normalize to RPCResponse format.
      // Unwrap the daemon's "result" wrapper so callers get clean data.
      try {
        const parsed = JSON.parse(text);
        if (parsed.error) {
          return { ok: false, error: parsed.error };
        }
        // Daemon wraps successful responses in {result: ...}, unwrap it.
        const inner = parsed.result !== undefined ? parsed.result : parsed;
        return { ok: true, result: inner };
      } catch {
        return { ok: false, error: `parse error: ${text.substring(0, 200)}` };
      }
    } catch (e: any) {
      return { ok: false, error: `rpc error: ${e.message}` };
    }
  }
}
