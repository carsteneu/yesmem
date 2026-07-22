import { expect, test } from "bun:test";
import { injectYesMemToolContext } from "./context";

test("injects per-call context without leaking between sessions", () => {
  const firstArgs = { query: "first" };
  const secondArgs = { query: "second" };

  injectYesMemToolContext(
    { tool: "yesmem_search", sessionID: "session-a" },
    { args: firstArgs },
    "/workspace/a",
  );
  injectYesMemToolContext(
    { tool: "yesmem_search", sessionID: "session-b" },
    { args: secondArgs },
    "/workspace/b",
  );

  expect(firstArgs).toEqual({
    query: "first",
    _session_id: "opencode:session-a",
    _source_agent: "opencode",
    _cwd: "/workspace/a",
  });
  expect(secondArgs).toEqual({
    query: "second",
    _session_id: "opencode:session-b",
    _source_agent: "opencode",
    _cwd: "/workspace/b",
  });
});

test("does not mutate foreign tools", () => {
  const args = { command: "pwd" };

  injectYesMemToolContext(
    { tool: "bash", sessionID: "session-a" },
    { args },
    "/workspace/a",
  );

  expect(args).toEqual({ command: "pwd" });
});
