// rule_guard — pre-tool rule compliance hook, the opencode side.
//
// The judgement is not made here. internal/guard owns the rules, the model, the prompt, the policy and the
// answer cache; the daemon serves that core as the RPC guard_check, and the Claude Code hook runs the same
// core in-process. This file owns what belongs to a harness event: which calls are worth asking about, the
// context the daemon cannot see (the recent conversation, the git branch, the test-file note), and the
// rendering of the verdict into a tool error or a tool-result prefix.
//
// Fail semantics, and the split is load-bearing: a transport failure or an unreachable daemon is PASS with a
// reason, because the guard must stay out of the way when it cannot answer. Only a request the model endpoint
// refused (UNAVAILABLE) refuses the file tools. Reversing that split blocks every write on every daemon
// restart, or passes a call the endpoint rejected (YM-410).
//
// What stays here by design: the 5s event cache, the UNAVAILABLE notice and the SUGGEST hand-off, because all
// three hang on opencode event ids that the core never sees; and the ghost-skill filter, which asks whether a
// skill is installed on this machine.

import { createHash } from "node:crypto"
import { readFile } from "node:fs/promises"
import { Error as ToolError } from "@opencode/plugin/promise/tool"
import { debugLog } from "./log"
import type { YesMemRPC } from "./rpc"
import { isSkillInstalled } from "./skill_whitelist"

/** The daemon's verdict, as internal/guard serialises it. */
export type GuardDecision = {
  decision: string
  violations?: string[]
  suggestion?: string
  detail?: string
  enforceable?: boolean
  model?: string
  truncated?: boolean
}

// Tools whose BLOCK the hook can enforce: `execute.before` returns the message for any tool and index.ts
// turns it into a Tool.Error, so the decision only needs to be offered. Shell belongs here because the
// Decision Policy names two BLOCK rules that can only ever fire on a command - 25 (destructive bash) and
// 32 (PR merge via CLI/API) - and both were judged and thrown away before (YM-466). Note the reach: the
// guard sees the whole command (the core describes a Bash call by its command) and the remaining arguments
// as a capped excerpt. What it cannot see at all is indirection (a script written by `write` and run by
// `shell`) and alternative spellings of the same act - that limit is the policy's, not the guard's.
export const blockableTools = new Set(["write", "edit", "patch", "shell"])
// Tools refused when the guard itself has no answer. Deliberately narrower than blockableTools: an
// unjudged write can still land, an unjudged `ls` cannot. Refusing a shell call would take the session's
// whole ability to recover with it (no git status, no ls to find out why), which is the false positive
// that locks every call - so a shell call runs and carries the YM-410 notice instead (YM-466).
// Invariant: every entry here must also be in blockableTools. An entry that is not would refuse calls the
// guard was never allowed to block, so an endpoint outage would enforce a rule that does not exist.
export const failClosedTools = new Set(["write", "edit", "patch"])
const suggestableTools = new Set(["write", "edit", "patch", "read", "shell", "grep", "glob"])
const skipTools = new Set(["skill", "subagent", "todowrite"])

// A read-only GitHub or git call writes nothing and notifies nobody, so no rule in a catalogue about
// posting, promising, or destroying work can be violated by it. On 28.10.2026 the model answered BLOCK on
// `gh pr view 58662` with "this session is heading toward posting/commenting/reopening on GitHub": it read
// a direction into the call and judged the goal instead of the command. A boundary whose position depends
// on how the goal is worded is not a boundary, so this one is a list.
const READ_ONLY_SHELL_STARTS = [
  /^gh\s+pr\s+(view|list|diff|checks|status)\b/,
  /^gh\s+issue\s+(view|list)\b/,
  /^gh\s+run\s+(view|list)\b/,
  /^gh\s+release\s+(view|list)\b/,
  /^gh\s+repo\s+(view|list)\b/,
  /^gh\s+branch\s+list\b/,
  /^gh\s+search\s+(repos|issues|prs|code)\b/,
  /^git\s+ls-remote\b/,
]
// Anything that chains, redirects or substitutes: `gh pr view 1; gh pr close 1` is two calls, and only the
// first is the one this list knows.
const SHELL_CHAINING = /[;&|<>`\n]|\$\(/
// `gh api` is a GET only while it carries neither a method nor a field: `-f key=value` makes it a POST. The
// short forms may be glued to their value (`-XPUT`, `-fquery=...`), which the previous separator-only pattern
// read as a read - a spelling that routed a write around the guard entirely.
const GH_API_WRITE = /(^|\s)(?:-[fFXd]\S*|--(?:method|field|raw-field|input)(?:\s|=|$))/

/** True only for a single command that provably reads. Everything else keeps going to the model. */
export function isReadOnlyShell(command: unknown): boolean {
  if (typeof command !== "string") return false
  const line = command.trim()
  if (!line || SHELL_CHAINING.test(line)) return false
  if (/^gh\s+api\b/.test(line)) return !GH_API_WRITE.test(line)
  return READ_ONLY_SHELL_STARTS.some((pattern) => pattern.test(line))
}

const EVENT_CACHE_TTL_MS = 5000
// The daemon's worst case, from its own bounds: a 15s model request (not retried on timeout) for each of two
// models in the Anthropic ladder, plus its 3s model-list fetch. The bound sits above that sum so a slow
// endpoint is a PASS with a reason from the core, not a truncated RPC the adapter has to report.
const GUARD_RPC_TIMEOUT_MS = 40000
// The same cap the core applies to the harness addendum: a longer context would be cut there anyway.
const MAX_CONTEXT_CHARS = 4000
const MAX_CONVERSATION_LINES = 6
const MAX_CONVERSATION_LINE_CHARS = 500
const MAX_LOG_EXCERPT_CHARS = 300
const MAX_VIOLATION_CHARS = 1000

/**
 * A capped one-line excerpt for a log line. The text may come from a peer and carry newlines, so it is
 * JSON-quoted: a raw body could otherwise forge a second log entry.
 */
function logExcerpt(text: string): string {
  const capped = text.length > MAX_LOG_EXCERPT_CHARS ? text.substring(0, MAX_LOG_EXCERPT_CHARS) + "…" : text
  return JSON.stringify(capped)
}

// Keyed on the tool, the head of its arguments and the context: the rules and the model belong to the
// daemon, which keeps its own 5s answer cache. This one saves the RPC itself for a call the guard has just
// judged. Keyed on text, not a hash: the map holds a handful of entries and a collision would hand one call
// another call's decision.
const eventCache = new Map<string, { decision: GuardDecision; ts: number }>()

/**
 * Test seam: forgets the module-level event cache so one test cannot inherit another's decision. Production
 * never calls it; the entries expire on their own.
 */
export function resetGuardState() {
  eventCache.clear()
}

/**
 * The event cache key: a digest over the whole call and its context.
 *
 * NOT a capped excerpt. The daemon judges the full arguments and its destructive pre-check runs behind the
 * RPC, so a key that only covers the first 300 characters hands a call the verdict of another call that
 * happens to start the same way - and skips the pre-check for the tail (a `git commit -m "<long>" && rm -rf /`
 * served from the cache of the same command without the tail). The old adapter could cap its key because it
 * capped its prompt too; this one may not.
 */
function eventKey(toolName: string, args: Record<string, any>, context: string, directory: string, sessionID: string): string {
  return createHash("sha256")
    // The project belongs in the key: the daemon loads RULES.md from it, so the same call in two projects
    // is two questions (the map is module-level, one instance per process today).
    .update(directory)
    .update("\u0000")
    // The session belongs in the key too: the deletion gate answers from a per-session refusal counter, so
    // the same command in a second session is a different question and may not inherit the first's PASS.
    .update(sessionID)
    .update("\u0000")
    .update(toolName)
    .update("\u0000")
    .update(JSON.stringify(args ?? {}))
    .update("\u0000")
    .update(context)
    .digest("hex")
}

export async function getGitBranch(directory: string): Promise<string> {
  try {
    const read = async (path: string) => readFile(path, "utf8").catch(() => "")
    // `.git` is a directory in a plain checkout and a `gitdir:` pointer file in a worktree. Reading it
    // as text fails on the directory, so the pointer decides only when it parses as one.
    const gitFile = await read(`${directory}/.git`)
    const match = gitFile.match(/^gitdir:\s*(.+)$/m)
    const gitDir = match ? match[1].trim() : `${directory}/.git`
    const head = await read(`${gitDir}/HEAD`)
    // Only a branch ref counts: a detached HEAD is a SHA, and calling that a feature branch would repeat
    // the false assurance YM-85 is about.
    const branch = /^ref:\s*refs\/heads\/(.+)$/.exec(head.trim())
    return branch ? branch[1] : ""
  } catch {
    return ""
  }
}

const defaultBranches = new Set(["main", "master"])

/**
 * The context line that tells the model a feature-branch rule is already satisfied. It belongs to a
 * feature branch only: on main the rule is exactly what the guard must keep suggesting (YM-85).
 */
export function featureBranchNote(branch: string): string {
  if (!branch || defaultBranches.has(branch)) return ""
  return `\nGit branch: ${branch} (feature branch rules are satisfied — no need to suggest worktree/branch skills)`
}

/**
 * One line for text a model produced. The prompt embeds RULES.md, so a violation string can carry text an
 * attacker steered; collapsing whitespace and control characters keeps it from forging structure (a second
 * "RULE:" block, a fake system line) in the tool error the agent reads. It is not a boundary against a
 * hostile repository, which can answer PASS just as well.
 */
function guardMessage(text: string): string {
  const collapsed = text
    .replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029]/g, " ")
    .replace(/\s+/g, " ")
    .trim()
  return collapsed.length > MAX_VIOLATION_CHARS ? collapsed.substring(0, MAX_VIOLATION_CHARS) + "…" : collapsed
}

function detectUserIntent(message: string, tool: string, args: Record<string, any>): boolean {
  if (!message) return false
  const lower = message.toLowerCase()
  const command = (args?.command || "").toLowerCase()
  for (const verb of ["commit", "push", "deploy", "do it", "mach ", "tu es", "mach es", "fire and forget"]) {
    if (!lower.includes(verb)) continue
    if (tool === "shell" && command && lower.includes(command)) return true
    if (tool !== "shell" && verb.length > 3) return true
  }
  return false
}

export interface ToolCallEvent {
  readonly tool?: string
  readonly sessionID?: string
  readonly id?: string
  input?: unknown
}

export interface ToolResultEvent {
  readonly tool?: string
  readonly id?: string
  readonly status?: string
  result?: any
  error?: any
}

export function ruleGuard(rpc: YesMemRPC, directory: string) {
  let gitBranch = ""
  let branchLoaded = false
  let recentConversation: string[] = []
  let lastModel = ""
  const suggestions = new Map<string, { suggestion: string; level: string }>()
  // Calls the guard could not judge: the reason travels with the tool result, because a tool that
  // cannot be blocked would otherwise pass without anyone noticing the guard was down (YM-410).
  const notices = new Map<string, string>()

  /** Read-only capture of the recent conversation for the guard context. */
  function capture(messages: ReadonlyArray<any>) {
    try {
      const collected: string[] = []
      for (let i = messages.length - 1; i >= 0 && collected.length < MAX_CONVERSATION_LINES; i--) {
        const message = messages[i]
        const role = message?.role
        if (role !== "user" && role !== "assistant") continue
        const parts = message?.content
        if (!Array.isArray(parts)) continue
        const text = parts
          .filter((part: any) => part?.type === "text" && part.text)
          .map((part: any) => part.text)
          .join("\n")
        if (text) collected.unshift(`[${role}]: ${text.substring(0, MAX_CONVERSATION_LINE_CHARS)}`)
      }
      if (collected.length > 0) recentConversation = collected
    } catch {}
  }

  /**
   * The harness's addendum: what the daemon has no view of. The call itself travels in `tool_input`, so it
   * is not repeated here.
   */
  function buildContext(toolName: string, args: Record<string, any>, filePath: string): string {
    const parts: string[] = []
    if (recentConversation.length > 0) {
      parts.push(`Recent conversation:\n${recentConversation.join("\n")}`)
      const lastUser = recentConversation.filter((line) => line.startsWith("[user]:")).pop() || ""
      if (detectUserIntent(lastUser, toolName, args)) {
        parts.push(
          `NOTE: The user explicitly requested this action. Rules about user-decision (auto-commit, deployment, destructive ops) should yield to explicit user instruction.`,
        )
      }
    }
    if (filePath && filePath.endsWith("_test.go")) {
      parts.push(
        `NOTE: This is a test file ("_test.go"). Writing test files before implementation IS TDD-compliant — do not BLOCK under TDD/test-first rules.`,
      )
    }
    const branchNote = featureBranchNote(gitBranch).trim()
    if (branchNote) parts.push(branchNote)
    return parts.join("\n").substring(0, MAX_CONTEXT_CHARS)
  }

  /**
   * Asks the daemon for the verdict. A failed RPC is PASS with a reason and is not cached: a daemon restart
   * must recover on the next call, and an unreachable daemon is not a judgement about the call.
   */
  async function judge(
    toolName: string,
    args: Record<string, any>,
    filePath: string,
    sessionID?: string,
  ): Promise<GuardDecision> {
    const now = Date.now()
    for (const [key, entry] of eventCache) if (now - entry.ts >= EVENT_CACHE_TTL_MS) eventCache.delete(key)
    const context = buildContext(toolName, args, filePath)
    const cacheKey = eventKey(toolName, args, context, directory, sessionID || "")
    // The verdict depends on the session (the deletion gate answers from a per-session refusal counter), so a
    // call without a session id is never answered from the cache.
    const cached = sessionID ? eventCache.get(cacheKey) : undefined
    if (cached) return cached.decision

    const started = Date.now()
    const response = await rpc.call(
      "guard_check",
      {
        tool: toolName,
        tool_input: args,
        cwd: directory,
        session_id: sessionID || "",
        context,
        purpose: "guard",
      },
      { timeoutMs: GUARD_RPC_TIMEOUT_MS },
    )
    const elapsed = Date.now() - started
    if (!response.ok) {
      debugLog("guard", `[${elapsed}ms] RPC-FAILED ${logExcerpt(response.error || "no answer")}`)
      return { decision: "PASS", detail: `guard check failed: ${response.error || "no answer"}` }
    }
    const decision = (response.result ?? {}) as GuardDecision
    if (sessionID) eventCache.set(cacheKey, { decision, ts: Date.now() })
    // The model is the daemon's business, but a silent switch between providers was invisible in the log
    // before, so the change is reported once (host and key are not).
    if (decision.model && decision.model !== lastModel) {
      lastModel = decision.model
      debugLog("guard", `RESOLVED model=${decision.model}`)
    }
    debugLog(
      "guard",
      `[${elapsed}ms] ${decision.decision}${decision.truncated ? " (context truncated)" : ""}${
        decision.detail ? ` ${logExcerpt(decision.detail)}` : ""
      }`,
    )
    return decision
  }

  async function before(event: ToolCallEvent): Promise<string | undefined> {
    try {
      const toolName = (event.tool || "").toString().toLowerCase()
      const args = (event.input ?? {}) as Record<string, any>
      if (skipTools.has(toolName)) return
      if (!suggestableTools.has(toolName)) return

      // Checked before the file-target skips below, because a shell call has no file target: the command
      // itself is the whole evidence.
      if (toolName === "shell" && isReadOnlyShell(args?.command)) {
        debugLog("guard", `SKIP-READONLY shell cmd=${logExcerpt(String(args?.command))}`)
        return
      }

      // A shell call has no file target: its arguments are a command, so a `path`-shaped field it happens
      // to carry must not excuse the call from the guard (the doc exemption and the two filename skips
      // below all key on this value).
      const filePath = toolName === "shell" ? "" : args?.filePath || args?.file_path || args?.path || ""
      if (filePath && (filePath.endsWith("RULES.md") || filePath.endsWith("rule_guard.ts"))) return

      const docExtensions = /\.(md|txt|rst|pdf)(\*|$|,)/
      const isDocTarget =
        docExtensions.test(filePath) ||
        (toolName === "grep" && docExtensions.test(args?.include || "")) ||
        (toolName === "glob" && docExtensions.test(args?.pattern || ""))
      if (isDocTarget) {
        debugLog("guard", `SKIP-DOC tool=${toolName} filePath=${filePath}`)
        return
      }

      if (!branchLoaded) {
        gitBranch = await getGitBranch(directory)
        branchLoaded = true
        debugLog("guard", `branch=${gitBranch || "?"}`)
      }

      const canBlock = blockableTools.has(toolName)
      const decision = await judge(toolName, args, filePath, event.sessionID)

      if (decision.decision === "UNAVAILABLE") {
        const detail = decision.detail || "no answer"
        if (!failClosedTools.has(toolName)) {
          if (event.id) notices.set(event.id, detail)
          return
        }
        return `Rule check unavailable (${detail}): this call was not judged.`
      }

      if (decision.decision === "BLOCK") {
        const violations = decision.violations || []
        // A BLOCK that reaches this adapter is enforceable: the core turns every block it may not enforce
        // into a SUGGEST, so the only reach decision left here is whether `execute.before` may refuse the
        // tool at all.
        if (!canBlock) {
          debugLog("guard", `DOWNGRADE block not enforceable tool=${toolName}`)
          if (event.id && violations.length > 0) {
            suggestions.set(event.id, { suggestion: violations.join("; "), level: "ADVISORY" })
          }
          return
        }
        // The text comes from a model whose prompt embeds repository content; it reaches the model again.
        return `Rule check blocked this call: ${guardMessage(violations.join("; "))}`
      }

      if (decision.decision === "SUGGEST") {
        // A downgraded block keeps its citations: that is an advisory note about rules, not a skill to
        // activate, and it must survive on a machine that lacks the skill named in the core's text.
        if (decision.violations?.length) {
          if (event.id) {
            suggestions.set(event.id, { suggestion: decision.violations.join("; "), level: "ADVISORY" })
          }
          return
        }
        const suggestion = decision.suggestion || ""
        // The policy prescribes "<skill-name>: <reason>" (RULES.md), and the live log showed every answer in
        // that shape dropped as a ghost skill: the check compared the whole string with a directory name
        // (733 SUGGEST, 733 SKIP ghost skill, none delivered). The prefix decides whether the skill exists;
        // the reason stays in the message.
        const skillName = suggestion.split(":", 1)[0].trim()
        if (suggestion && !isSkillInstalled(suggestion) && !isSkillInstalled(skillName)) {
          debugLog("guard", `SKIP ghost skill: ${suggestion}`)
          return
        }
        if (event.id) {
          suggestions.set(event.id, { suggestion, level: toolName === "shell" ? "MANDATORY" : "REQUIRED" })
        }
      }
      return
    } catch (error: any) {
      debugLog("guard", `CRASH ${error?.message || String(error)}`)
      return
    }
  }

  /** Prefixes the tool result (or its error) with a guard message. */
  function injectPrefix(event: ToolResultEvent, prefix: string) {
    if (event.status === "error" && event.error) {
      event.error = new ToolError({
        message: prefix + (event.error.message || ""),
        ...(event.error.error === undefined ? {} : { error: event.error.error }),
        ...(event.error.metadata === undefined ? {} : { metadata: event.error.metadata }),
      })
      return
    }
    if (!event.result) return
    const content = event.result.content
    if (Array.isArray(content)) event.result.content = [{ type: "text", text: prefix }, ...content]
    else event.result.content = prefix + (typeof content === "string" ? content : "")
  }

  /** Injects a pending guard message into the tool result (or its error). */
  function after(event: ToolResultEvent) {
    try {
      if (!event.id) return
      const notice = notices.get(event.id)
      if (notice) {
        notices.delete(event.id)
        injectPrefix(
          event,
          // Sanitised like every other text this file renders: the notice is built from the daemon's detail,
          // which is static today, and "static today" is not a property this line can check.
          `[guard] RULE GUARD UNAVAILABLE: ${guardMessage(notice)}. This call was not checked against RULES.md.\n\n`,
        )
      }
      const entry = suggestions.get(event.id)
      if (!entry) return
      suggestions.delete(event.id)
      // The suggestion is model output, and every branch below puts it in the tool result: collapse and cap
      // it, or a repository-steered answer forges structure in the agent's own context (YM-456 review).
      const text = guardMessage(entry.suggestion)
      const prefix =
        entry.level === "MANDATORY"
          ? `[guard] ⚠️ MANDATORY CHECK — activate ${text} (use Skill tool)\n\n`
          : entry.level === "ADVISORY"
            ? // A downgraded BLOCK: the policy reserves the rejection for an objectively detectable
              // violation, so the call stands and the model is told what to verify (YM-456).
              `[guard] SUGGEST — rule check did not block: ${text}\n\n`
            : `[guard] ACTION REQUIRED: activate ${text} — use Skill tool now.\n\n`

      injectPrefix(event, prefix)
    } catch (error: any) {
      debugLog("guard", `AFTER-CRASH ${error?.message || String(error)}`)
    }
  }

  return { before, after, capture }
}
