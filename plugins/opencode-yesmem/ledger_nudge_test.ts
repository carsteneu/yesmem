// ledger_nudge_test.ts — Live-Ledger subscriber behavior
// Run: bun test plugins/opencode-yesmem/ledger_nudge_test.ts
//
// Verifies the cache-safety contract (Learning #81269/#85490):
//   - PATCH-ONCE per terminal user message (lastPatchedHash)
//   - plan-version-freeze: a mid-turn plan change does NOT re-patch the message
//   - never-block: a failing/slow RPC causes a silent skip, not a throw
//   - dumb renderer emits the pass-through ledger block
//   - YESMEM_LEDGER=off disables the subscriber

import { test, expect, describe } from "bun:test";
import { ledgerNudgeHook, renderLedger } from "./ledger_nudge";

function stubRpc(result: any, opts?: { fail?: boolean; error?: string }) {
  const calls: { method: string; params: any; timeoutMs?: number }[] = [];
  const rpc = {
    call: async (method: string, params?: any, sopts?: any) => {
      calls.push({ method, params, timeoutMs: sopts?.timeoutMs });
      if (opts?.fail) return { ok: false, error: opts.error || "boom" };
      return { ok: true, result };
    },
  };
  return { rpc, calls };
}

function msgList(msgs: any[]) {
  return { messages: msgs };
}

function userMsg(text: string, parts?: any[]) {
  return { role: "user", parts: parts || [{ type: "text", text }] };
}

describe("renderLedger", () => {
  test("wraps plan content in ledger header/footer with header marker", () => {
    const out = renderLedger("Goal: x\nCore: y");
    expect(out).toContain("[yesmem-ledger v1]");
    expect(out).toContain("[/yesmem-ledger]");
    expect(out).toContain("  Goal: x");
    expect(out).toContain("  Core: y");
  });

  test("returns null for empty plan", () => {
    expect(renderLedger("")).toBeNull();
    expect(renderLedger("   ")).toBeNull();
  });

  test("truncates plans longer than the 40-line budget with a hint", () => {
    const long = Array.from({ length: 50 }, (_, i) => `line ${i}`).join("\n");
    const out = renderLedger(long)!;
    expect(out).not.toContain("line 45");
    expect(out).toContain("ledger truncated");
  });
});

describe("ledgerNudgeHook transform", () => {
  test("appends ledger suffix to the terminal user message (PATCH-ONCE)", async () => {
    const plan = "Goal: g\nVerified:\n  - x — proof: go test → exit 0";
    const { rpc, calls } = stubRpc({ exists: true, status: "active", plan });
    const hook = ledgerNudgeHook(rpc as any, () => "opencode:ses_1");

    const m = userMsg("Do the thing");
    const output = msgList([m]);
    await hook["experimental.chat.messages.transform"]({}, output);

    expect(m.parts[0].text).toContain("Do the thing");
    expect(m.parts[0].text).toContain("[yesmem-ledger v1]");
    expect(calls[0].method).toBe("get_plan");
    expect(calls[0].params).toEqual({ thread_id: "opencode:ses_1" });
    expect(calls[0].timeoutMs).toBe(2000);
  });

  test("does NOT re-patch the same message on a repeated transform (PATCH-ONCE)", async () => {
    const { rpc, calls } = stubRpc({ exists: true, status: "active", plan: "Goal: g" });
    const hook = ledgerNudgeHook(rpc as any, () => "opencode:ses_1");

    const m = userMsg("hello");
    const output = msgList([m]);
    await hook["experimental.chat.messages.transform"]({}, msgList([m]));
    // Second call on the SAME message/instance — RPC should not fire again.
    await hook["experimental.chat.messages.transform"]({}, output);

    // Only one get_plan call; text appended once.
    expect(calls.filter((c) => c.method === "get_plan").length).toBe(1);
    expect(m.parts[0].text.match(/\[yesmem-ledger v1\]/g)!.length).toBe(1);
  });

  test("plan-version-freeze: a changed plan on the same message is NOT re-injected", async () => {
    let latest = "Goal: v1";
    const rpc = {
      call: async () => ({ ok: true, result: { exists: true, status: "active", plan: latest } }),
    };
    const hook = ledgerNudgeHook(rpc as any, () => "opencode:ses_1");

    const m = userMsg("run");
    await hook["experimental.chat.messages.transform"]({}, msgList([m]));
    const afterFirst = m.parts[0].text;

    // Simulate mid-turn update_plan: the plan changed, but the SAME user message.
    latest = "Goal: v2";
    await hook["experimental.chat.messages.transform"]({}, msgList([m]));

      expect(m.parts[0].text).toBe(afterFirst); // unchanged — frozen on the message
      expect(m.parts[0].text).toContain("Goal: v1");
    });

    test("re-patches a DISTINCT turn that repeats identical text (stale-ledger fix)", async () => {
      let planVersion = 0;
      const rpc = {
        call: async () => ({ ok: true, result: { exists: true, status: "active", plan: `Goal: v${planVersion}` } }),
      };
      const hook = ledgerNudgeHook(rpc as any, () => "opencode:ses_1");

      const m1 = { info: { id: "msg-1", role: "user" }, parts: [{ type: "text", text: "run" }] };
      await hook["experimental.chat.messages.transform"]({}, { messages: [m1] });
      expect(m1.parts[0].text).toContain("Goal: v0");

      // New turn, same bytes, DIFFERENT message id → a fresh ledger must be injected.
      planVersion = 1;
      const m2 = { info: { id: "msg-2", role: "user" }, parts: [{ type: "text", text: "run" }] };
      await hook["experimental.chat.messages.transform"]({}, { messages: [m2] });
      expect(m2.parts[0].text).toContain("Goal: v1");
    });

    test("skips silently when RPC fails (never-block)", async () => {
    const { rpc, calls } = stubRpc({}, { fail: true, error: "daemon down" });
    const hook = ledgerNudgeHook(rpc as any, () => "opencode:ses_1");
    const m = userMsg("hi");
    await hook["experimental.chat.messages.transform"]({}, msgList([m]));
    expect(m.parts[0].text).not.toContain("[yesmem-ledger v1]");
    expect(calls.length).toBe(1);
  });

  test("does not inject when no active plan exists", async () => {
    const { rpc } = stubRpc({ exists: false, status: "" });
    const hook = ledgerNudgeHook(rpc as any, () => "opencode:ses_1");
    const m = userMsg("hi");
    await hook["experimental.chat.messages.transform"]({}, msgList([m]));
    expect(m.parts[0].text).not.toContain("[yesmem-ledger v1]");
  });

  test("does not inject when plan status is not active", async () => {
    const { rpc } = stubRpc({ exists: true, status: "completed", plan: "Goal: done" });
    const hook = ledgerNudgeHook(rpc as any, () => "opencode:ses_1");
    const m = userMsg("hi");
    await hook["experimental.chat.messages.transform"]({}, msgList([m]));
    expect(m.parts[0].text).not.toContain("[yesmem-ledger v1]");
  });

  test("skips when thread id is unavailable", async () => {
    const { rpc, calls } = stubRpc({ exists: true, status: "active", plan: "Goal: g" });
    const hook = ledgerNudgeHook(rpc as any, () => "");
    const m = userMsg("hi");
    await hook["experimental.chat.messages.transform"]({}, msgList([m]));
    expect(calls.length).toBe(0);
  });

  test("ignores tool-result / assistant messages (terminal discrimination)", async () => {
    const { rpc, calls } = stubRpc({ exists: true, status: "active", plan: "Goal: g" });
    const hook = ledgerNudgeHook(rpc as any, () => "opencode:ses_1");
    const toolMsg = { role: "tool", parts: [{ type: "text", text: "tool result" }] };
    const asstMsg = { role: "assistant", parts: [{ type: "text", text: "thinking" }] };
    const realUser = userMsg("the real user turn");
    await hook["experimental.chat.messages.transform"]({}, msgList([toolMsg, asstMsg, realUser]));
    expect(realUser.parts[0].text).toContain("[yesmem-ledger v1]");
    expect(calls.length).toBe(1);
  });

  test("YESMEM_LEDGER=off disables the subscriber entirely", async () => {
    const old = process.env.YESMEM_LEDGER;
    process.env.YESMEM_LEDGER = "off";
    try {
      const { rpc, calls } = stubRpc({ exists: true, status: "active", plan: "Goal: g" });
      const hook = ledgerNudgeHook(rpc as any, () => "opencode:ses_1");
      const m = userMsg("hi");
      await hook["experimental.chat.messages.transform"]({}, msgList([m]));
      expect(calls.length).toBe(0);
      expect(m.parts[0].text).toBe("hi");
    } finally {
      if (old === undefined) delete process.env.YESMEM_LEDGER;
      else process.env.YESMEM_LEDGER = old;
    }
  });
});

// rpc.ts must forward the timeout so the never-block guarantee holds at the wire.
describe("rpc timeout plumbing (source guard)", () => {
  test("call() signature accepts an options object with timeoutMs", async () => {
    const src = await Bun.file("./plugins/opencode-yesmem/rpc.ts").text();
    expect(src).toMatch(/call\(method: string, params\??.*opts\??:/s);
    expect(src).toContain("timeoutMs");
  });
});
