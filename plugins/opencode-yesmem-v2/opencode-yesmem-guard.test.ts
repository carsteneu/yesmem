// Plugin-level guard tests: the hook chain of index.ts, driven through the fake context, with the daemon
// answering guard_check. The judgement itself is not re-tested here (internal/guard and
// internal/daemon/handler_guard_test.go own it); what these tests pin is that the plugin's wiring turns the
// daemon's verdict into the hook result the host sees.

import { describe, expect, test } from "bun:test"
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import path from "node:path"
import {
  fakeContext,
  loadPlugin,
  mockDaemon,
  withEnv,
  withSocket,
  type DaemonRequest,
  type HookResult,
} from "./opencode-yesmem-fixture"

const RULES = `# Project rules

1. Never commit directly to main.
2. Run the test suite before pushing.

## Skill Catalog
- tdd: use before implementing.
`

/** A daemon that answers guard_check with one verdict and records what the plugin asked. */
function guardDaemon(verdict: unknown) {
  return mockDaemon((request: DaemonRequest) =>
    request.method === "guard_check" ? { result: verdict } : { result: {} },
  )
}

async function projectWithRules() {
  const directory = await mkdtemp(path.join(tmpdir(), "yesmem-project-"))
  await writeFile(path.join(directory, "RULES.md"), RULES)
  return directory
}

const blocked = (result: HookResult) => {
  expect(result.failed).toBe(true)
  expect(result.error?._tag).toBe("Tool.Error")
  return String(result.error.message)
}

describe("rule_guard", () => {
  test("rejects a write that the rule check blocks, in the project it runs in", async () => {
    const daemon = await guardDaemon({ decision: "BLOCK", violations: ["Rule 1: no direct commits to main"], enforceable: true })
    const project = await projectWithRules()
    try {
      await withSocket(daemon.socketPath, async () => {
        const recorder = fakeContext(project)
        await loadPlugin(recorder)

        const result = await recorder.fire("tool.execute.before", {
          tool: "write",
          sessionID: "ses_guard",
          input: { filePath: "src/feature.ts", content: "x" },
        })

        expect(blocked(result)).toContain("Rule 1: no direct commits to main")
      })
      // The call is judged in this project: the daemon loads the rules from the cwd the plugin sends.
      const params = daemon.hooked().find((request) => request.method === "guard_check")?.params
      expect(params?.cwd).toBe(project)
      expect(params?.purpose).toBe("guard")
      expect(params?.tool_input).toEqual({ filePath: "src/feature.ts", content: "x" })
    } finally {
      await daemon.close()
      await rm(project, { recursive: true, force: true })
    }
  })

  test("rejects a shell call that the rule check blocks", async () => {
    const daemon = await guardDaemon({
      decision: "BLOCK",
      violations: ["Rule 1: LLM signature in the commit message text"],
      enforceable: true,
    })
    const project = await projectWithRules()
    const command = 'git commit -m "fix: thing\n\nCo-Authored-By: Claude <noreply@anthropic.com>"'
    try {
      await withSocket(daemon.socketPath, async () => {
        const recorder = fakeContext(project)
        await loadPlugin(recorder)

        const result = await recorder.fire("tool.execute.before", {
          tool: "shell",
          sessionID: "ses_guard",
          input: { command },
        })

        // The same ToolError the file tools get: the refusal is not tool-specific (YM-466).
        expect(blocked(result)).toContain("Rule 1: LLM signature in the commit message text")
      })
      // The command is what has to be judged, so it must reach the daemon as the call's arguments.
      const params = daemon.hooked().find((request) => request.method === "guard_check")?.params
      expect(params?.tool_input?.command).toBe(command)
    } finally {
      await daemon.close()
      await rm(project, { recursive: true, force: true })
    }
  })

  test("lets a call through when the check passes", async () => {
    const daemon = await guardDaemon({ decision: "PASS" })
    const project = await projectWithRules()
    try {
      await withSocket(daemon.socketPath, async () => {
        const recorder = fakeContext(project)
        await loadPlugin(recorder)

        const result = await recorder.fire("tool.execute.before", {
          tool: "edit",
          sessionID: "ses_guard",
          input: { filePath: "src/feature.ts", oldString: "a", newString: "b" },
        })

        expect(result.failed).toBe(false)
      })
      expect(daemon.hooked().filter((request) => request.method === "guard_check").length).toBe(1)
    } finally {
      await daemon.close()
      await rm(project, { recursive: true, force: true })
    }
  })

  test("does not ask the guard about documentation targets", async () => {
    const daemon = await guardDaemon({ decision: "BLOCK", violations: ["Rule 1: should not fire here"], enforceable: true })
    const project = await projectWithRules()
    const asked = () => daemon.hooked().filter((request) => request.method === "guard_check").length
    try {
      await withSocket(daemon.socketPath, async () => {
        const recorder = fakeContext(project)
        await loadPlugin(recorder)

        // Discriminating half: the same tool on a source path is checked.
        const source = await recorder.fire("tool.execute.before", {
          tool: "write",
          sessionID: "ses_guard",
          input: { filePath: "src/doc-check.ts", content: "x" },
        })
        expect(blocked(source)).toContain("should not fire here")
        expect(asked()).toBe(1)

        const docs = await recorder.fire("tool.execute.before", {
          tool: "write",
          sessionID: "ses_guard",
          input: { filePath: "docs/notes.md", content: "x" },
        })
        expect(docs.failed).toBe(false)
        expect(asked()).toBe(1)
      })
    } finally {
      await daemon.close()
      await rm(project, { recursive: true, force: true })
    }
  })

  test("injects the suggested skill into the tool result, but only for installed skills", async () => {
    const daemon = await guardDaemon({ decision: "SUGGEST", suggestion: "myskill" })
    const project = await projectWithRules()
    const skills = await mkdtemp(path.join(tmpdir(), "yesmem-skills-"))
    await mkdir(path.join(skills, "myskill"))
    await writeFile(path.join(skills, "myskill", "SKILL.md"), "# myskill")
    try {
      await withEnv({ YESMEM_SKILL_ROOTS: skills }, () =>
        withSocket(daemon.socketPath, async () => {
          const recorder = fakeContext(project)
          await loadPlugin(recorder)

          const before = await recorder.fire("tool.execute.before", {
            tool: "shell",
            sessionID: "ses_guard",
            id: "call_1",
            input: { command: "echo myskill" },
          })
          expect(before.failed).toBe(false)

          const after = await recorder.fire("tool.execute.after", {
            tool: "shell",
            sessionID: "ses_guard",
            id: "call_1",
            input: { command: "echo myskill" },
            status: "completed",
            result: { content: "hi" },
          })

          expect(after.failed).toBe(false)
          expect(String(after.event.result.content)).toContain("MANDATORY CHECK")
          expect(String(after.event.result.content)).toContain("myskill")
          expect(String(after.event.result.content)).toEndWith("hi")
        }),
      )
    } finally {
      await daemon.close()
      await rm(project, { recursive: true, force: true })
      await rm(skills, { recursive: true, force: true })
    }
  })

  test("drops suggestions for skills that are not installed", async () => {
    const ghost = `ghost-${process.pid}-${Date.now()}`
    const daemon = await guardDaemon({ decision: "SUGGEST", suggestion: ghost })
    const project = await projectWithRules()
    const skills = await mkdtemp(path.join(tmpdir(), "yesmem-skills-"))
    try {
      await withEnv({ YESMEM_SKILL_ROOTS: skills }, () =>
        withSocket(daemon.socketPath, async () => {
          const recorder = fakeContext(project)
          await loadPlugin(recorder)

          await recorder.fire("tool.execute.before", {
            tool: "shell",
            sessionID: "ses_guard",
            id: "call_2",
            input: { command: "echo ghost" },
          })
          const after = await recorder.fire("tool.execute.after", {
            tool: "shell",
            sessionID: "ses_guard",
            id: "call_2",
            input: { command: "echo ghost" },
            status: "completed",
            result: { content: "hi" },
          })

          expect(String(after.event.result.content)).toBe("hi")
        }),
      )
    } finally {
      await daemon.close()
      await rm(project, { recursive: true, force: true })
      await rm(skills, { recursive: true, force: true })
    }
  })
})
