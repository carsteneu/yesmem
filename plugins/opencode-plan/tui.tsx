/** @jsxImportSource @opentui/solid */

// The active plan as its own sidebar block, the way the todo plugin shows the
// todo list: one claim on sidebar.content, nothing else.
//
// A v2 TUI plugin (Plugin.define + context.ui.slot). The outline rides in the
// per-thread status file the proxy writes, which is the only channel a TUI
// plugin has for daemon state.

import { Plugin } from "@opencode/plugin/tui"
import { homedir } from "node:os"
import { join } from "node:path"
import { type Plan, planRows, planSummary } from "./plan_block"
import { readStatusFile } from "./status_file"

type StatusFile = {
  plan?: Plan
}

const cacheStatusDir = join(homedir(), ".claude", "yesmem", "cache-status")

// The status file is rewritten every second, but a slot body renders once per
// mount: without a refresh the block would freeze at the state of the moment the
// session was opened.
const REFRESH_MS = 2000

// Colours come from the theme (theme.text.*), so light and dark themes both read.

export default Plugin.define({
  id: "yesmem.sidebar.plan",
  setup(context) {
    const theme = context.theme

    // Host-provided reactive state: the timer mutates it, the slot body reads it.
    // No solid-js import, so the plugin needs nothing beyond the JSX runtime.
    const [state, mutate] = context.storage.memory<{ plan: Plan | null }>("yesmem.plan", {
      initial: { plan: null },
    })

    let sessionID = ""
    const refresh = () => {
      if (!sessionID) return
      const next = readStatusFile<StatusFile>(cacheStatusDir, sessionID)?.plan ?? null
      mutate((draft) => void (draft.plan = next))
    }
    const timer = setInterval(refresh, REFRESH_MS)
    timer.unref?.()

    const claim = context.ui.slot({
      append: "sidebar.content",
      render: (props) => {
        // The first render learns the session, and a session switch inside the
        // same TUI changes it.
        if (props.sessionID !== sessionID) {
          sessionID = props.sessionID
          refresh()
        }

        const plan = state.plan
        const rows = planRows(plan)
        if (rows.length === 0) return null

        return (
          <box flexDirection="column" gap={1}>
            <box flexDirection="column">
              <text fg={theme.text.base}>
                <b>Plan</b>
              </text>
              <text fg={theme.text.muted}>{planSummary(plan)}</text>
            </box>
            {/* One blank line between steps, like the prompt notes: this TUI has no
                sub-line spacing, gap is a whole row. */}
            <box flexDirection="column" gap={1}>
              {rows.map((row) => (
                <text
                  fg={
                    // Theme tokens, not hex: the same accent has to read on the light
                    // and the dark theme, and a fixed yellow does not.
                    row.status === "completed" || row.status === "cancelled"
                      ? theme.text.faint
                      : row.status === "in_progress"
                        ? theme.text.feedback.warning.base
                        : theme.text.base
                  }
                >
                  {row.depth > 0 ? "  ".repeat(row.depth) : ""}
                  {row.marker} {row.text}
                  {row.reason ? ` (${row.reason})` : ""}
                </text>
              ))}
            </box>
          </box>
        )
      },
    })

    return () => {
      clearInterval(timer)
      claim()
    }
  },
})
