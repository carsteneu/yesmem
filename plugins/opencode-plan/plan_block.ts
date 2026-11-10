// Rendering rules for a structured plan in the TUI sidebar.
//
// Kept out of the slot component on purpose: the cases that must not break
// (empty outline, unknown status, depth out of range, over-long text) are
// exactly the cases a running TUI is worst at reproducing, so they live here
// as pure functions with their own tests.

export type PlanItemStatus = "pending" | "in_progress" | "completed" | "cancelled"

export type PlanItem = {
  content?: unknown
  status?: unknown
  reason?: unknown
  depth?: unknown
}

export type Plan = {
  items?: unknown
}

export type PlanRow = {
  marker: string
  text: string
  reason: string
  status: PlanItemStatus
  depth: number
}

// The sidebar scrolls, so the plan does not have to fit: every row renders and
// the host's scrollbox takes the overflow. A cap here would hide steps that the
// plan is the only record of.
// A single step is shown truncated; the full text stays available through
// get_plan, and the sidebar is a glance, not a document.
export const PLAN_MAX_TEXT = 200

export const PLAN_MAX_REASON = 80

export const PLAN_SUMMARY_TEXT = 60

export function normalizeStatus(status: unknown): PlanItemStatus {
  switch (status) {
    case "pending":
    case "in_progress":
    case "completed":
    case "cancelled":
      return status
    default:
      // An unknown value is shown as an open step. Dropping the item would hide
      // work, and guessing "completed" would be a lie.
      return "pending"
  }
}

export function markerFor(status: PlanItemStatus): string {
  switch (status) {
    case "completed":
      return "[x]"
    case "in_progress":
      return "[>]"
    case "cancelled":
      return "[-]"
    default:
      return "[ ]"
  }
}

// depth is clamped to the two levels the model defines: the sidebar has no room
// for a value that came out of a hand-written file.
export function clampDepth(depth: unknown): number {
  const value = typeof depth === "number" && Number.isFinite(depth) ? Math.trunc(depth) : 0
  // Three levels is the contract: phase, step, detail. Deeper values are clamped
  // rather than dropped, so a plan written by an older or newer client still renders.
  return Math.min(2, Math.max(0, value))
}

export function flattenText(text: unknown, max: number): string {
  if (typeof text !== "string") return ""
  // Control characters become spaces rather than nothing, so "a\u0000b" does
  // not collapse into one word. The sidebar renders this straight into a text
  // node, and the plan text is model-written.
  const flat = text
    .replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029]/g, " ")
    .replace(/\s+/g, " ")
    .trim()
  if (flat.length <= max) return flat
  return flat.slice(0, max) + "…"
}

export function planItems(plan: unknown): PlanItem[] {
  if (!plan || typeof plan !== "object") return []
  const items = (plan as Plan).items
  if (!Array.isArray(items)) return []
  const out: PlanItem[] = []
  for (const item of items) {
    if (!item || typeof item !== "object") continue
    const content = typeof (item as PlanItem).content === "string" ? (item as PlanItem).content.trim() : ""
    if (content === "") continue
    out.push(item as PlanItem)
  }
  return out
}

export function planRows(plan: unknown): PlanRow[] {
  return planItems(plan).map((item) => {
    const status = normalizeStatus(item.status)
    return {
      marker: markerFor(status),
      text: flattenText(item.content, PLAN_MAX_TEXT),
      reason: flattenText(item.reason, PLAN_MAX_REASON),
      status,
      depth: clampDepth(item.depth),
    }
  })
}

// planSummary is the same one-liner the ledger injects: progress, the next
// step, and up to two further open steps. A cancelled step counts as neither
// done nor open, and not in the denominator either — otherwise "3/5 done" reads
// as two steps left when one was dropped. An unknown status counts as open.
export function planSummary(plan: unknown): string {
  const items = planItems(plan)
  if (items.length === 0) return ""
  let done = 0
  let active = 0
  const open: string[] = []
  for (const item of items) {
    const status = normalizeStatus(item.status)
    if (status === "cancelled") continue
    active++
    if (status === "completed") {
      done++
      continue
    }
    open.push(flattenText(item.content, PLAN_SUMMARY_TEXT))
  }
  if (active === 0) return ""
  let line = `${done}/${active} done`
  if (open.length === 0) return line
  line += `, next: ${open[0]}`
  const rest = open.slice(1, 3)
  if (rest.length > 0) line += `, open: ${rest.join(", ")}`
  return line
}
