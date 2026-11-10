import { describe, expect, test } from "bun:test"
import {
  PLAN_MAX_ROWS,
  clampDepth,
  markerFor,
  normalizeStatus,
  planRows,
  planSummary,
} from "./plan_block"

// The sidebar must survive whatever a plan file contains: the doc names an
// empty list, an unknown status and a depth out of range as the cases every
// renderer has to take.
describe("planRows", () => {
  test("missing or empty plan draws nothing", () => {
    expect(planRows(undefined)).toEqual([])
    expect(planRows(null)).toEqual([])
    expect(planRows({})).toEqual([])
    expect(planRows({ items: [] })).toEqual([])
    expect(planRows({ items: "nope" })).toEqual([])
    expect(planRows("garbage")).toEqual([])
  })

  test("drops entries without content", () => {
    const rows = planRows({ items: [{}, { content: "   " }, { content: "keep" }] })
    expect(rows).toHaveLength(1)
    expect(rows[0].text).toBe("keep")
  })

  test("carries status, marker and nesting", () => {
    const rows = planRows({
      items: [
        { content: "done step", status: "completed" },
        { content: "sub step", status: "in_progress", depth: 1, reason: "because" },
      ],
    })
    expect(rows[0]).toMatchObject({ marker: "[x]", status: "completed", depth: 0 })
    expect(rows[1]).toMatchObject({ marker: "[>]", status: "in_progress", depth: 1, reason: "because" })
  })

  test("an unknown status renders as an open step", () => {
    const rows = planRows({ items: [{ content: "odd", status: "blocked-ish" }] })
    expect(rows[0].status).toBe("pending")
    expect(rows[0].marker).toBe("[ ]")
  })

  test("depth is clamped into the three supported levels", () => {
    expect(clampDepth(-5)).toBe(0)
    expect(clampDepth(9)).toBe(2)
    expect(clampDepth(1.7)).toBe(1)
    expect(clampDepth(2)).toBe(2)
    expect(clampDepth("2")).toBe(0)
    expect(clampDepth(Number.NaN)).toBe(0)
    const rows = planRows({ items: [{ content: "deep", depth: 99 }] })
    expect(rows[0].depth).toBe(2)
  })

  test("flattens line breaks out of a step", () => {
    const rows = planRows({ items: [{ content: "first\nsecond\tthird" }] })
    expect(rows[0].text).toBe("first second third")
  })

  test("strips control characters from a step", () => {
    for (const content of ["a\u001b[2Jb", "a\u001b]0;pwned\u0007b", "a\u0000b", "a\u009bb"]) {
      const rows = planRows({ items: [{ content }] })
      expect(rows[0].text).not.toMatch(/[\u0000-\u001f\u007f-\u009f]/)
      expect(planSummary({ items: [{ content }] })).not.toMatch(/[\u0000-\u001f\u007f-\u009f]/)
    }
  })

  test("renders every row - the sidebar scrolls instead of the plan being cut", () => {
    const items = Array.from({ length: 40 }, (_, i) => ({ content: `step ${i}` }))
    expect(planRows({ items })).toHaveLength(40)
  })
})

describe("planSummary", () => {
  test("empty and all-done", () => {
    expect(planSummary({ items: [] })).toBe("")
    expect(planSummary(undefined)).toBe("")
    expect(planSummary({ items: [{ content: "a", status: "completed" }] })).toBe("1/1 done")
  })

  test("next step and open steps", () => {
    expect(planSummary({ items: [{ content: "a" }] })).toBe("0/1 done, next: a")
    expect(
      planSummary({ items: [{ content: "a" }, { content: "b" }, { content: "c" }, { content: "d" }] }),
    ).toBe("0/4 done, next: a, open: b, c")
  })

  test("cancelled counts as neither done nor open", () => {
    expect(
      planSummary({ items: [{ content: "skip", status: "cancelled" }, { content: "b" }] }),
    ).toBe("0/1 done, next: b")
    expect(
      planSummary({
        items: [
          { content: "a", status: "completed" },
          { content: "b", status: "cancelled" },
          { content: "c" },
        ],
      }),
    ).toBe("1/2 done, next: c")
    expect(planSummary({ items: [{ content: "a", status: "cancelled" }] })).toBe("")
  })

  test("caps the quoted step text", () => {
    const long = "x".repeat(100)
    expect(planSummary({ items: [{ content: long }] })).toBe(`0/1 done, next: ${"x".repeat(60)}…`)
  })
})

describe("status helpers", () => {
  test("known statuses survive, unknown falls back to pending", () => {
    expect(normalizeStatus("completed")).toBe("completed")
    expect(normalizeStatus("in_progress")).toBe("in_progress")
    expect(normalizeStatus("cancelled")).toBe("cancelled")
    expect(normalizeStatus("pending")).toBe("pending")
    expect(normalizeStatus(undefined)).toBe("pending")
  })

  test("markers follow the status", () => {
    expect(markerFor("completed")).toBe("[x]")
    expect(markerFor("in_progress")).toBe("[>]")
    expect(markerFor("cancelled")).toBe("[-]")
    expect(markerFor("pending")).toBe("[ ]")
  })
})
