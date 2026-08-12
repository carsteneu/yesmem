// rpc_test.ts — verify RPC command construction (Bug 1)
// Run: bun test plugins/opencode-yesmem/rpc_test.ts
//
// We test the nc flag combination without actually opening a socket.
// The regression: nc -U -w 20 → nc -U -N -w 5.
// -N signals half-close after stdin EOF, so the daemon's decoder.Decode
// returns EOF, handleConn returns, conn.Close() runs, nc exits in ~7ms
// instead of waiting for the full -w timeout.

import { test, expect, describe } from "bun:test";

describe("YesMemRPC command construction (Bug 1 regression guard)", () => {
  test("nc command uses -N flag for half-close after stdin EOF", async () => {
    // Read rpc.ts source and verify the nc flag combination.
    const src = await Bun.file("./plugins/opencode-yesmem/rpc.ts").text();
    expect(src).toContain("-N");
    expect(src).toMatch(/nc\s+-U\s+-N/);
  });

  test("nc command keeps bounded -w timeout as fallback", async () => {
    const src = await Bun.file("./plugins/opencode-yesmem/rpc.ts").text();
    // Should have a -w flag with a small value (5 or less), not 20.
    expect(src).toMatch(/-w\s+\d+/);
    expect(src).not.toMatch(/-w\s+2\d/); // no values 20-29
    expect(src).not.toMatch(/-w\s+[3-9]\d/); // no values 30-99
  });

  test("nc command no longer uses the regressed -w 20", async () => {
    const src = await Bun.file("./plugins/opencode-yesmem/rpc.ts").text();
    expect(src).not.toContain("nc -U -w 20");
  });
});
