import { $ } from "bun";
import type { RPCResponse } from "./types";

export class YesMemRPC {
  private socketPath: string;

  constructor(socketPath?: string) {
    this.socketPath = socketPath ||
      process.env.YESMEM_SOCKET ||
      `${process.env.HOME || "/home/" + (process.getuid?.() ?? "chief")}/.claude/yesmem/daemon.sock`;
  }

    async call(method: string, params?: Record<string, any>, opts?: { timeoutMs?: number }): Promise<RPCResponse> {
      const payload = JSON.stringify({ method, params: params || {} });
      // Optional per-call timeout bounds `-w`. Callers that must never block
      // (e.g. the live-ledger subscriber) pass a shorter bound, e.g. 2000ms.
      // Default stays the literal -w 5 (guarded by rpc_test regression checks).
      const timeoutSec = opts?.timeoutMs ? Math.max(1, Math.ceil(opts.timeoutMs / 1000)) : 0;
      try {
        // -N: half-close after stdin EOF → daemon's decoder.Decode returns EOF →
        // handleConn returns → conn.Close() → nc exits immediately.
        // -w: bounded fallback for pathological daemon hangs.
        // Without -N, nc waits the full -w timeout on every call because the
        // daemon keeps the connection open for multiple request/response cycles.
        const wFlag = timeoutSec ? `-w ${timeoutSec}` : "-w 5";
        const cmd = `echo ${$.escape(payload)} | nc -U -N ${wFlag} ${$.escape(this.socketPath)}`;
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
