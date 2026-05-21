import { YesMemRPC } from "./rpc";

const harmlessCommands = ["grep", "diff", "test", "[", "rg"];

function isHarmlessBashExit(command: string): boolean {
  const cmd = command.split(/\s+/)[0];
  return harmlessCommands.includes(cmd);
}

export function failureLearnHook(rpc: YesMemRPC): Record<string, any> {
  return {
    "tool.execute.after": async (input, output) => {
      const state = (output as any).state;
      if (!state || state.status !== "error") return;

      const tool = input.tool;
      const directory = (input.session as any)?.directory || "";

      if (tool === "bash") {
        const command = ((output as any).args as any)?.command as string;
        if (command && isHarmlessBashExit(command)) return;
      }

      const errorOutput = state.output || state.error || "";
      if (errorOutput.length < 10) return;

      // Phase 1: Learn
      const truncated = errorOutput.length > 200 ? errorOutput.substring(0, 200) : errorOutput;
      await rpc.call("remember", {
        text: `${tool} error: ${truncated}`,
        category: "gotcha",
        source: "hook_auto_learned",
        project: directory,
      });

      // Phase 2: Assist
      const searchResp = await rpc.call("hybrid_search", {
        query: `${tool} ${truncated.substring(0, 100)}`,
        project: directory,
        limit: 3,
      });
      if (searchResp.ok && searchResp.result?.results?.length > 0) {
        const snippets = searchResp.result.results
          .slice(0, 3)
          .map((r: any) => `- [${r.category}] ${r.content}`)
          .join("\n");
        (output as any).context = `[YesMem Assist] Similar known issues:\n${snippets}`;
      }
    },
  };
}
