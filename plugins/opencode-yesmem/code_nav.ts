import { YesMemRPC } from "./rpc";
import { appendFileSync } from "node:fs";

const LOG_FILE = `${process.env.HOME}/.claude/yesmem/logs/plugin.log`;

function dbgLog(tag: string, msg: string) {
  try {
    appendFileSync(LOG_FILE, `[${new Date().toISOString()}] ${tag} ${msg}\n`);
  } catch {}
}

const blockedCommands = ["grep", "cat", "head", "sed", "awk", "rg", "egrep", "fgrep", "find"];
const blockedTools = new Set(["read"]);
const dismissedSessions = new Map<string, number>();
const fileAttempts = new Map<string, {count: number, firstSeen: number}>();
const FILE_ATTEMPT_TTL_MS = 3600000; // 1h
const MAX_DISMISS = 5;

// Non-code basenames (no extension) that should never trigger code_nav blocking.
const NON_CODE_BASENAMES = new Set([
  "license", "license.txt", "license.md",
  "copying", "copying.txt", "copying.less",
  "notice", "notice.txt",
  "readme", "readme.txt", "readme.md",
  "changelog", "changelog.md", "changelog.txt",
  "authors", "contributors", "contributors.txt",
]);

// Basename prefixes: Dockerfile, Makefile — accept variants like
// Dockerfile.dev, Dockerfile.prod, GNUmakefile.
const NON_CODE_BASENAME_PREFIXES = ["dockerfile", "makefile", "gnu"];

// Non-code extensions that should never trigger code_nav blocking.
const NON_CODE_EXTENSIONS = new Set([
  ".txt", ".rst", ".md", ".log", ".lock", ".sum",
  ".license", ".notice",
]);

// isNonCodeFile returns true for files that are documentation, licensing, or
// build metadata — not source code. Exported for test coverage.
export function isNonCodeFile(rel: string): boolean {
  const lower = rel.toLowerCase();
  const base = lower.split("/").pop() || lower;
  // .git* metadata (existing whitelist)
  if (base.match(/^\.git/)) return true;
  // .md (existing whitelist — documentation)
  if (base.endsWith(".md")) return true;
  // Basename match: LICENSE, README, Dockerfile, Makefile, etc.
  if (NON_CODE_BASENAMES.has(base)) return true;
  // Prefix match: Dockerfile, Dockerfile.dev, Makefile, GNUmakefile
  for (const p of NON_CODE_BASENAME_PREFIXES) {
    if (base === p || base.startsWith(p + ".")) return true;
  }
  // Extension match: .txt, .rst, .log, .lock
  const dot = base.lastIndexOf(".");
  if (dot > 0 && NON_CODE_EXTENSIONS.has(base.slice(dot))) return true;
  return false;
}

function extractFileArgs(command: string): string[] {
  const parts = command.split(/\s+/);
  const files: string[] = [];
  for (let i = 1; i < parts.length; i++) {
    const arg = parts[i];
    if (arg.startsWith("-")) continue;
    if (arg.includes("*") || arg.includes("?")) continue;
    if (arg.includes("/") || arg.match(/\.(go|ts|js|py|rs|java|cpp|c|h|yaml|yml|toml|json|mod|sum)$/)) {
      files.push(arg);
    }
  }
  return files;
}

function relativePath(f: string, projectDir: string): string {
  if (!f.startsWith("/") || !projectDir) return f;
  const prefix = projectDir.endsWith("/") ? projectDir : projectDir + "/";
  if (f.startsWith(prefix)) return f.slice(prefix.length);
  return f;
}

  function isBlockedCommand(command: string): boolean {
    const cmd = command.split(/\s+/)[0].toLowerCase();
    return blockedCommands.includes(cmd);
  }

  function suggestTool(file: string, directory: string, project: string): string {
    const proj = project || directory.split("/").pop() || directory;
    return `get_file_symbols("${file}", "${proj}") for overview, or get_code_snippet(file="${file}", project="${proj}", start_line=x, end_line=y)`;
  }

  async function checkFileInGraph(file: string, directory: string, isDir: boolean, rpc: YesMemRPC): Promise<boolean> {
  if (isDir) {
    const fr = await rpc.call("get_file_index", {
      dir: file,
      project: directory,
    });
    return !!(fr.ok && fr.result?.text && !fr.result.text.includes("No source files found"));
    } else {
      const fr = await rpc.call("get_file_symbols", {
        file: file,
        project: directory,
      });
      // Mirror the get_file_index pattern: error string is "No symbols found in X"
      // which contains "symbol", so substring-include would false-positive.
      return !!(fr.ok && fr.result?.text && !fr.result.text.includes("No symbols found"));
    }
}

export function codeNavHook(rpc: YesMemRPC, pluginDirectory: string): Record<string, any> {
  let projectIndexed = false;

  async function ensureIndexed(directory: string): Promise<boolean> {
    if (projectIndexed) return true;
    const r = await rpc.call("search_code_index", {
      pattern: "func",
      kind: "function",
      project: directory,
      limit: 1,
    });
    projectIndexed = !!(r.ok && r.result?.text?.includes("Found"));
    return projectIndexed;
  }

  return {
    "tool.execute.before": async (input: any, output: any) => {
      try {
        const tool = input.tool;
        dbgLog("code_nav", `HOOK tool=${tool}`);

        // --- BASH tool: grep/cat/find via shell ---
        if (tool === "bash") {
          const command = (output.args as any)?.command as string;
          if (!command || !isBlockedCommand(command)) return;

          const sessionId = input.session?.id || "";
          if (dismissedSessions.has(sessionId)) return;

          const files = extractFileArgs(command);
          if (files.length === 0) return;

          const directory = (input.session as any)?.directory || pluginDirectory || process.env.PWD || "";
          if (!await ensureIndexed(directory)) { dbgLog("code_nav", `SKIP-NOT-INDEXED ${tool}`); return; }

              let fileInGraph = false;
              for (const f of files) {
                const rel = relativePath(f, directory).replace(/\/+$/, "");
                const isDir = f.endsWith("/") || !f.includes(".");
                  // Non-code files (LICENSE, README, Dockerfile, .md, .txt, etc.)
                  // never benefit from code-tools — skip them entirely.
                  if (isNonCodeFile(rel)) continue;
                if (await checkFileInGraph(rel, directory, isDir, rpc)) {
                fileInGraph = true;
                break;
              }
            }
          if (!fileInGraph) return;

          // 2-strike with 1h TTL: first block, second allow
          const paths = files.map(f => relativePath(f, directory).replace(/\/+$/, "")).join(",");
          const entry = fileAttempts.get(paths);
          const now = Date.now();
          const attempt = entry && (now - entry.firstSeen) < FILE_ATTEMPT_TTL_MS ? entry.count : 0;
            if (attempt === 0) {
              fileAttempts.set(paths, {count: 1, firstSeen: now});
              const suggest = suggestTool(files[0] || "", directory, (input.session as any)?.directory || directory || "");
              throw new Error(`YesMem: BLOCKED shell nav on indexed file(s)\n  → ${suggest}\n  If the code tools don't find what you need, run this ${tool} again.`);
          }
          dbgLog("code_nav", `ALLOW-2ND ${tool} ${paths} (attempt=${attempt+1} age=${(now - entry!.firstSeen)/1000}s)`);
          fileAttempts.set(paths, {count: attempt + 1, firstSeen: entry?.firstSeen || now});
        }

          // --- Opencode read tool ---
          if (blockedTools.has(tool)) {
            const sessionId = input.session?.id || "";
            if (dismissedSessions.has(sessionId)) return;

            const directory = (input.session as any)?.directory || pluginDirectory || process.env.PWD || "";
            if (!await ensureIndexed(directory)) { dbgLog("code_nav", `SKIP-NOT-INDEXED ${tool}`); return; }

              const args = output.args || {};
              const target = (args.filePath || args.file_path || "") as string;
              if (!target) return;
              const rel = relativePath(target, directory).replace(/\/+$/, "");

              // Non-code files (LICENSE, README, Dockerfile, .md, .txt, etc.)
              // never benefit from code-tools — let them through.
              if (isNonCodeFile(rel)) return;

            if (!await checkFileInGraph(rel, directory, false, rpc)) return;

            const entry = fileAttempts.get(rel);
            const now = Date.now();
            const attempt = entry && (now - entry.firstSeen) < FILE_ATTEMPT_TTL_MS ? entry.count : 0;
              if (attempt === 0) {
                dbgLog("code_nav", `BLOCK-1ST ${tool} ${rel}`);
                fileAttempts.set(rel, {count: 1, firstSeen: now});
                const suggest = suggestTool(rel, directory, (input.session as any)?.directory || directory || "");
                throw new Error(`YesMem: BLOCKED ${tool} on indexed file\n  → ${suggest}\n  If the code tools don't find what you need, run this ${tool} again.`);
            }
            dbgLog("code_nav", `ALLOW-2ND ${tool} ${rel} (attempt=${attempt+1} age=${(now - entry!.firstSeen)/1000}s)`);
            fileAttempts.set(rel, {count: attempt + 1, firstSeen: entry?.firstSeen || now});
          }
      } catch (e: any) {
        if (e.message?.startsWith("YesMem:")) throw e;
        dbgLog("code_nav", e?.message || String(e));
      }
    },
  };
}
