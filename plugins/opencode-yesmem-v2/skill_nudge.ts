// skill_nudge — staged skill suggestion on user messages (v1: skill_nudge.ts).
//
// Stage 1: local substring match against the catalogue's trigger literals (fast, deterministic, on the
//          critical path).
// Stage 2: model fallback (semantic match) — NEVER on the critical path. It asks the daemon's guard_check
//          RPC with purpose "nudge", so the model, the chain and the answer cache are the same ones the
//          rule guard uses. Results are cached by (catalogue hash, message text) and precomputed in the
//          background, so the dispatch hook always returns promptly and a later identical message reuses
//          the answer.
//
// The nudge is prepended to the terminal user message. Skills that are not installed are dropped
// (skill_whitelist), the skill name must look like a skill name before it is injected, and a message that
// already carries the nudge is left alone.
//
// Disable entirely with YESMEM_SKILL_NUDGE=off (v1 had no switch for this one; the other two nudges do).

import { readFile, stat } from "node:fs/promises"
import { debugLog } from "./log"
import { findTerminalUserText, hashString, type HostMessage } from "./messages"
import type { YesMemRPC } from "./rpc"
import { isSkillInstalled } from "./skill_whitelist"

const NUDGE_MARKER = "🧠 MANDATORY CHECK — activate "
const RESULT_CACHE_MAX = 128
const MAX_CATALOG_BYTES = 256 * 1024
const MAX_CATALOG_ENTRIES = 200
const MAX_CATALOG_TEXT_CHARS = 32_000
const MAX_MESSAGE_CHARS = 2000
// The daemon may spend 15s on the model and retry once; this path is off the critical path, so it may wait.
const NUDGE_RPC_TIMEOUT_MS = 20_000

/**
 * A skill name reaches the model inside the user's message, so it may only look like a name. The whitelist
 * check is not enough: a directory name may legally contain a newline or a `<system-reminder>` frame
 * (YESMEM_SKILL_ROOTS can point anywhere).
 */
export function isValidSkillName(name: unknown): boolean {
  return typeof name === "string" && /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(name)
}

export interface SkillEntry {
  skill: string
  priority: string
  triggers: string[]
}

const resultCache = new Map<string, string | null>()
const inFlight = new Set<string>()

/**
 * Drops the per-process caches. The caches exist to avoid repeated provider calls within one server lifetime;
 * a fresh process (or a test that must not inherit another run's answers) starts empty.
 */
export function _resetCaches() {
  resultCache.clear()
  inFlight.clear()
}

function cacheSet(key: string, value: string | null) {
  resultCache.set(key, value)
  if (resultCache.size > RESULT_CACHE_MAX) {
    const oldest = resultCache.keys().next().value
    if (oldest !== undefined) resultCache.delete(oldest)
  }
}

/**
 * Log-safe label for a cache key. The key is `${catalogHash}\u0000${userMessage}`, so writing it into the
 * log wrote whole prompts into plugin-v2.log (YM-22). The label keeps the catalogue hash plus a short
 * message digest: the same recommendation stays recognisable, the text does not.
 */
export function cacheKeyLabel(key: string): string {
  const separator = key.indexOf("\u0000")
  if (separator < 0) return hashString(key)
  return `${key.substring(0, separator)}#${hashString(key.substring(separator + 1))}`
}

/** YAML strings of a trigger list, with escaped quotes handled. */
export function extractYamlStrings(text: string): string[] {
  const result: string[] = []
  let inString = false
  let current = ""
  for (let i = 0; i < text.length; i++) {
    const character = text[i]
    if (!inString) {
      if (character === '"') inString = true
      continue
    }
    if (character === "\\" && i + 1 < text.length && text[i + 1] === '"') {
      current += '"'
      i++
      continue
    }
    if (character === '"') {
      const trimmed = current.trim()
      if (trimmed) result.push(trimmed.toLowerCase())
      current = ""
      inString = false
      continue
    }
    current += character
  }
  const trimmed = current.trim()
  if (trimmed) result.push(trimmed.toLowerCase())
  return result
}

/** Parses the `## Skill Catalog` section: a YAML list of {id, skill, priority, triggers[]}. */
export function parseSkillCatalog(content: string): SkillEntry[] {
  const entries: SkillEntry[] = []
  const section = content.match(/## Skill Catalog\s*\n([\s\S]*?)(?=\n## |\n---\n|$)/)
  if (!section) return entries

  // Entries may be indented (the generated catalogue nests them under `rules:`) or flush.
  for (const block of section[1].split(/\n\s*- id:\s*/)) {
    if (!block || block.trim() === "") continue
    const skill = block.match(/skill:\s*"?([^"\n]+)"?/)
    if (!skill) continue
    const priority = block.match(/priority:\s*(\S+)/)
    const triggers: string[] = []
    const triggerSection = block.match(/triggers:\s*\[([\s\S]*?)\]/)
    if (triggerSection) triggers.push(...extractYamlStrings(triggerSection[1]))
    entries.push({
      skill: skill[1].trim(),
      priority: priority ? priority[1] : "MUST",
      triggers,
    })
  }

  // MUST first, as v1 did when it parsed the catalogue. Bounded: the catalogue is repo content.
  entries.sort((left, right) => {
    if (left.priority === right.priority) return 0
    return left.priority === "MUST" ? -1 : 1
  })
  return entries.slice(0, MAX_CATALOG_ENTRIES)
}

function localMatch(userMessage: string, catalog: SkillEntry[]): string | null {
  const lower = userMessage.toLowerCase()
  for (const entry of catalog) for (const trigger of entry.triggers) if (lower.includes(trigger)) return entry.skill
  return null
}

/**
 * Asks the daemon which skill fits the message (RPC guard_check, purpose "nudge"). The daemon owns the
 * model, the chain and the answer cache; this side owns the message and the rendering.
 *
 * Returns the matched skill, `null` for a definitive "no match" (cacheable) and `undefined` when the question
 * was not answered (not cacheable — a transient error must not poison the message forever). The distinction
 * is the core's own: a PASS carries a detail when nothing was judged (no credential, endpoint unreachable),
 * and carries none when the model answered PASS.
 */
async function modelMatch(
  rpc: YesMemRPC,
  directory: string,
  userMessage: string,
  sessionID?: string,
): Promise<string | null | undefined> {
  const response = await rpc.call(
    "guard_check",
    {
      // No call is judged here; the tool name only keeps the field from being empty.
      tool: "skill",
      tool_input: {},
      cwd: directory,
      session_id: sessionID || "",
      context: userMessage.substring(0, MAX_MESSAGE_CHARS),
      purpose: "nudge",
    },
    { timeoutMs: NUDGE_RPC_TIMEOUT_MS },
  )
  if (!response.ok) {
    debugLog("skill_nudge", `rpc failed: ${String(response.error || "no answer").substring(0, 200)}`)
    return undefined
  }
  const decision = (response.result ?? {}) as {
    decision?: string
    suggestion?: string
    detail?: string
    truncated?: boolean
  }
  if (decision.decision === "SUGGEST" && decision.suggestion) {
    // The prompt asks for the bare name. A reason behind a colon is cut off here rather than rejected by
    // isValidSkillName: the name is what reaches the message, the reason would only be noise.
    const name = String(decision.suggestion).split(":")[0].trim()
    return name || null
  }
  if (decision.decision === "PASS" && !decision.detail) return null
  debugLog(
    "skill_nudge",
    `no answer: ${decision.decision || "?"} ${String(decision.detail || "").substring(0, 120)}`.trim(),
  )
  return undefined
}

function precompute(
  rpc: YesMemRPC,
  directory: string,
  key: string,
  userMessage: string,
  sessionID: string | undefined,
  onTransient?: () => void,
) {
  if (inFlight.has(key)) return
  inFlight.add(key)
  modelMatch(rpc, directory, userMessage, sessionID)
    .then((skill) => {
      // Only a definitive answer is cached; a failed call stays open for the next sighting.
      if (skill === undefined) {
        debugLog("skill_nudge", `no answer for ${cacheKeyLabel(key)}, will retry`)
        onTransient?.()
        return
      }
      cacheSet(key, skill)
      debugLog("skill_nudge", `cached ${cacheKeyLabel(key)}=${skill ?? "NONE"}`)
    })
    .catch(() => {
      onTransient?.()
    })
    .finally(() => inFlight.delete(key))
}

/**
 * RULES.md candidates in priority order: an explicit override, then the project file the guard evaluates, then
 * the installation's standard rules (the only place a Skill Catalog is maintained since setup stopped writing one
 * into the project), then a copy shipped next to the plugin. The first readable file with a Skill Catalog wins —
 * the shipped copy is only a fallback, so the nudge and the guard cannot work with two different catalogues
 * (YM-88).
 */
export function catalogCandidates(sources: {
  override?: string
  pluginDirectory?: string
  projectDirectory?: string
  installationDirectory?: string
}): string[] {
  const asRulesFile = (directory: string) => `${directory.replace(/\/$/, "")}/RULES.md`
  // The override names a file; the directory sources are completed to their RULES.md.
  return [
    sources.override,
    sources.projectDirectory ? asRulesFile(sources.projectDirectory) : undefined,
    sources.installationDirectory ? asRulesFile(sources.installationDirectory) : undefined,
    sources.pluginDirectory ? asRulesFile(sources.pluginDirectory) : undefined,
  ].filter((candidate): candidate is string => Boolean(candidate))
}

export interface ContextEvent {
  readonly sessionID?: string
  messages?: HostMessage[]
}

/**
 * @param rpc the daemon client, which answers the semantic stage (purpose "nudge").
 * @param directory the project directory, which the daemon reads RULES.md from.
 * @param catalogPaths candidate `RULES.md` locations in priority order (env override, project directory,
 * plugin directory). The first readable one with a Skill Catalog wins for the local match.
 */
export function skillNudge(rpc: YesMemRPC, directory: string, catalogPaths: readonly string[]) {
  let catalog: SkillEntry[] | null = null
  let catalogText = ""
  let catalogHash = ""
  let loadedPath: string | null = null
  let loadedMtime = -1
  // A project without any catalogue would otherwise be re-read on every turn.
  let noCatalogUntil = 0
  const NO_CATALOG_TTL_MS = 30000
  // Keyed on catalogue + text: a catalogue change must not wedge the message that was pending before it.
  let lastPrecomputed: string | null = null

  async function loadCatalog() {
    if (Date.now() < noCatalogUntil) return
    const read = async (candidate: string): Promise<boolean> => {
      const info = await stat(candidate)
      // The catalogue is repo content and this runs on the request path.
      if (info.size > MAX_CATALOG_BYTES) {
        debugLog("skill_nudge", `catalog too large (${info.size} bytes), ignored: ${candidate}`)
        return false
      }
      const parsed = parseSkillCatalog(await readFile(candidate, "utf8"))
      // A rule file without a catalogue section carries nothing to match against; the next candidate
      // (the project file, or the copy shipped with the plugin) is still worth reading.
      if (parsed.length === 0) {
        debugLog("skill_nudge", `no skill catalog in ${candidate}`)
        return false
      }
      catalog = parsed
      catalogText = parsed
        .map((entry) => `- ${entry.skill} (${entry.priority}): ${entry.triggers.join(", ")}`)
        .join("\n")
        .substring(0, MAX_CATALOG_TEXT_CHARS)
      catalogHash = hashString(catalogText)
      loadedPath = candidate
      loadedMtime = info.mtimeMs
      debugLog("skill_nudge", `loaded ${parsed.length} skills from ${candidate}`)
      return true
    }

    // Re-stat the file that was loaded first: a different candidate with a coincidentally equal mtime must
    // not shadow a changed catalogue.
    if (loadedPath) {
      try {
        const info = await stat(loadedPath)
        if (info.mtimeMs === loadedMtime) return
        if (await read(loadedPath)) return
        loadedPath = null
      } catch {
        loadedPath = null
      }
    }
    // A run that finds no catalogue must leave no stale one behind: otherwise a removed file keeps nudging
    // from the previous content and the result cache stays keyed on an outdated hash.
    catalog = null
    catalogText = ""
    catalogHash = ""
    for (const candidate of catalogPaths) {
      try {
        if (await read(candidate)) {
          noCatalogUntil = 0
          return
        }
      } catch {}
    }
    noCatalogUntil = Date.now() + NO_CATALOG_TTL_MS
  }

  async function context(event: ContextEvent) {
    try {
      if (process.env.YESMEM_SKILL_NUDGE === "off") return
      const hit = findTerminalUserText(event.messages)
      if (!hit) return
      // The guard's own directive carries the same phrase; never stack nudges.
      if (hit.text.includes("MANDATORY CHECK — activate ")) return

      await loadCatalog()
      if (!catalog || catalog.length === 0) return

      let matched = localMatch(hit.text, catalog)
      if (!matched) {
        // The key holds the full text: a 32-bit hash would hand one message another message's answer.
        const key = `${catalogHash}\u0000${hit.text}`
        const cached = resultCache.get(key)
        if (cached === undefined) {
          // Cache miss: start the background match once per (catalogue, text); never nudge on the first sighting.
          const pending = `${catalogHash}\u0000${hit.text}`
          if (pending !== lastPrecomputed) {
            lastPrecomputed = pending
            precompute(rpc, directory, key, hit.text, event.sessionID, () => {
              if (lastPrecomputed === pending) lastPrecomputed = null
            })
          }
          return
        }
        matched = cached
      }
      if (!matched) return
      // The answer must come from the catalogue this adapter matched against. Stage 1 reads the candidates
      // above (an explicit override first), stage 2 asks the daemon, which reads the project's RULES.md: with
      // an override set the two lists can differ, and the override is the authoritative one. Without this a
      // name the override excluded could still be injected.
      if (!catalog.some((entry) => entry.skill === matched)) {
        debugLog("skill_nudge", `skip skill outside the catalogue: ${JSON.stringify(matched).substring(0, 80)}`)
        return
      }
      if (!isValidSkillName(matched)) {
        debugLog("skill_nudge", `reject malformed skill name: ${JSON.stringify(matched).substring(0, 80)}`)
        return
      }
      if (!isSkillInstalled(matched)) {
        debugLog("skill_nudge", `skip ghost skill: ${matched}`)
        return
      }

      hit.part.text = `${NUDGE_MARKER}${matched} (use Skill tool)\n\n${hit.part.text ?? ""}`
      debugLog("skill_nudge", `nudged: ${matched}`)
    } catch (error: any) {
      debugLog("skill_nudge", `ERR: ${error?.message || String(error)}`)
    }
  }

  return { context }
}
