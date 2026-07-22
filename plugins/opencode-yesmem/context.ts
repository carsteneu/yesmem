export function injectYesMemToolContext(input: any, output: any, directory: string) {
  if (typeof input?.tool !== "string" || !input.tool.startsWith("yesmem_")) return;
  if (!output?.args || typeof output.args !== "object" || Array.isArray(output.args)) return;

  if (typeof input.sessionID === "string" && input.sessionID) {
    output.args._session_id = `opencode:${input.sessionID}`;
  }
  output.args._source_agent = "opencode";
  if (directory) output.args._cwd = directory;
}
