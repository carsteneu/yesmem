// idle_reminder_test.ts — invisible reminder patch, deterministic every-3-user-turns cadence
// Run from REPO ROOT: bun test plugins/opencode-yesmem/idle_reminder_test.ts
//
// Contract (invisible, mirrors ledger_nudge's PATCH-ONCE pattern):
//   - subscribes experimental.chat.messages.transform (request-only patch —
//     the TUI never shows a message)
//   - counts real user turns in the request array (synthetic parts excluded)
//   - patches the terminal user message with the reminder on every 3rd turn
//   - never throws, never double-patches the same turn

import { test, expect, describe } from "bun:test";
import { idleReminderHook } from "./idle_reminder";

const FALLBACK =
  "Du hast ein Langzeitgedaechtnis (yesmem). Bei nicht-trivialen Aufgaben: ZUERST search(thema).";
const SIG = "[yesmem-idle]";

function storedUserPrompt(sid: string, opts: { synthetic?: boolean; text?: string } = {}) {
  const id = `um_${Math.random().toString(36).slice(2, 9)}`;
  const part: any = { type: "text", id: `p_${id}`, sessionID: sid, messageID: id, text: opts.text ?? `prompt ${id}` };
  if (opts.synthetic) part.synthetic = true;
  return { info: { id, role: "user", sessionID: sid, agent: "build" }, parts: [part] };
}

function asstMsg(sid: string) {
  const id = `am_${Math.random().toString(36).slice(2, 9)}`;
  return { info: { id, role: "assistant", sessionID: sid }, parts: [{ type: "text", id, sessionID: sid, messageID: id, text: "answer" }] };
}

function patchedCount(output: { messages: any[] }): number {
  return output.messages.filter((m: any) => JSON.stringify(m).includes(SIG)).length;
}

function turns(sid: string, n: number) {
  return Array.from({ length: n }, () => storedUserPrompt(sid));
}

describe("idleReminderHook (invisible transform patch)", () => {
  test("patches the terminal user message on the 3rd turn, no extra message added", async () => {
    const sid = "s1";
    const output = { messages: turns(sid, 3) };
    const hook = idleReminderHook();
    await hook["experimental.chat.messages.transform"]({}, output);

    expect(output.messages.length).toBe(3);
    expect(JSON.stringify(output.messages[2]).includes(SIG)).toBe(true);
    expect(JSON.stringify(output.messages[2]).includes(FALLBACK)).toBe(true);
    expect(patchedCount(output)).toBe(1);
  });

  test("no patch on turns 1 and 2", async () => {
    const sid = "s2";
    const output = { messages: turns(sid, 2) };
    const hook = idleReminderHook();
    await hook["experimental.chat.messages.transform"]({}, output);
    expect(patchedCount(output)).toBe(0);
  });

  test("cadence repeats: turn 6 patches again (fresh transform array)", async () => {
    const sid = "s3";
    const hook = idleReminderHook();
    const output3 = { messages: turns(sid, 3) };
    await hook["experimental.chat.messages.transform"]({}, output3);
    expect(patchedCount(output3)).toBe(1);

    const output6 = { messages: [...turns(sid, 3), asstMsg(sid), ...turns(sid, 3)] };
    await hook["experimental.chat.messages.transform"]({}, output6);
    expect(patchedCount(output6)).toBe(1);
  });

  test("synthetic user parts do not count as turns", async () => {
    const sid = "s4";
    const output = { messages: [storedUserPrompt(sid), storedUserPrompt(sid, { synthetic: true }), storedUserPrompt(sid)] };
    const hook = idleReminderHook();
    await hook["experimental.chat.messages.transform"]({}, output);
    expect(patchedCount(output)).toBe(0); // 2 real turns → below cadence
  });

  test("assistant messages do not count", async () => {
    const sid = "s5";
    const output = { messages: [storedUserPrompt(sid), asstMsg(sid), storedUserPrompt(sid), asstMsg(sid), storedUserPrompt(sid)] };
    const hook = idleReminderHook();
    await hook["experimental.chat.messages.transform"]({}, output);
    expect(patchedCount(output)).toBe(1);
  });

  test("belt-and-suspenders: a terminal text already carrying SIG is not patched twice", async () => {
    const sid = "s6";
    const output = { messages: [storedUserPrompt(sid), storedUserPrompt(sid), storedUserPrompt(sid, { text: `${SIG} reminder` })] };
    const hook = idleReminderHook();
    await hook["experimental.chat.messages.transform"]({}, output);
    expect(patchedCount(output)).toBe(1); // only the pre-existing SIG text
    expect(JSON.stringify(output.messages[2]).includes(FALLBACK)).toBe(false);
  });

  test("transform hook never throws on malformed input", async () => {
    const hook = idleReminderHook();
    let threw = false;
    try {
      await hook["experimental.chat.messages.transform"]({}, {});
      await hook["experimental.chat.messages.transform"]({}, { messages: [] });
      await hook["experimental.chat.messages.transform"]({}, { messages: [undefined, {}] });
    } catch (_) { threw = true; }
    expect(threw).toBe(false);
  });
});
