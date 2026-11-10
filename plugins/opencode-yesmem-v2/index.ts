// YesMem plugin for opencode v2 (server-side).
//
// Ported from the v1 plugin (~/.local/share/yesmem/plugins/opencode-yesmem/index.ts, opencode v1 hook API).
// The v2 hook surface is different, so this file is a new adapter layer; the domain modules
// (code_nav, rule_guard, failure_learn, auto_resolve, context) keep their v1 logic and are adapted in place.
//
// This uses the Effect plugin API on purpose: `execute.before` is the only hook a plugin may fail, and the
// session runner only turns a genuine `Tool.Error` into a model-visible tool failure
// (packages/core/src/session/runner/step.ts catches `Tool.Error` by tag). Through the promise adapter a
// thrown value becomes a defect instead, which the runner does not recover.
//
// See README.md for the mapping table and the documented gaps.

import { fileURLToPath } from "node:url"
import { homedir } from "node:os"
import { Plugin } from "@opencode/plugin/effect"
import { Error as ToolError } from "@opencode/plugin/promise/tool"
import { Effect, Stream } from "effect"
import { autoResolve } from "./auto_resolve"
import { codeNavGuard, type ToolCallEvent, type ToolResultEventLike } from "./code_nav"
import { injectYesMemToolContext } from "./context"
import { failureLearn } from "./failure_learn"
import { idleReminder, type ContextEvent } from "./idle_reminder"
import { ledgerNudge } from "./ledger_nudge"
import { ruleGuard, type ToolResultEvent } from "./rule_guard"
import { YesMemRPC } from "./rpc"
import { sessionTracker, type HostEvent } from "./session"
import { shellEnvironment, type ShellCreateEvent } from "./shell"
import { catalogCandidates, skillNudge } from "./skill_nudge"

const pluginDirectory = fileURLToPath(new URL(".", import.meta.url))
// The guard's standard rules live here since setup stopped writing a full copy into the project, and the catalogue
// the nudge matches against lives in them.
const installationDirectory = `${process.env.HOME || homedir()}/.claude/yesmem`

export default Plugin.define({
  id: "opencode.yesmem",
  effect: (context) =>
    Effect.gen(function* () {
      const directory = String(context.location.directory)
      const rpc = new YesMemRPC()
      const codeNav = codeNavGuard(rpc, directory)
      const guard = ruleGuard(rpc, directory)
      const learn = failureLearn(rpc, directory)
      const resolve = autoResolve(rpc, directory)
      const session = sessionTracker(rpc)
      const shell = shellEnvironment(rpc, rpc.socket)
      const skills = skillNudge(
        rpc,
        directory,
        catalogCandidates({
          override: process.env.YESMEM_SKILL_CATALOG,
          pluginDirectory,
          projectDirectory: directory,
          installationDirectory,
        }),
      )
      const ledger = ledgerNudge(rpc)
      const idle = idleReminder()

      // v1 primed the project index without waiting for it, and activation must not stall on the daemon:
      // `Plugin.awaitActivation` gates session entry points on this effect completing. Priming goes through
      // the guard, so it asks the same question in the same shape and fills the probe cache.
      yield* Effect.promise(() => codeNav.prime()).pipe(Effect.forkScoped)

      // Returns the message the call must be rejected with, or undefined to let it run.
      async function before(event: ToolCallEvent): Promise<string | undefined> {
        const blocked = await codeNav.before(event)
        if (blocked) return blocked
        const violation = await guard.before(event)
        if (violation) return violation
        // Session and cwd context rides on every yesmem_ tool call.
        injectYesMemToolContext(event, directory)
        return undefined
      }

      // v1 wrapped every composed call in its own try/catch: the plugin's bookkeeping must never fail a tool
      // call, a shell, or the plugin itself. `Effect.promise` takes a promise that cannot reject, and an
      // async IIFE cannot throw synchronously, so a throwing callback is swallowed here.
      const attempt = (evaluate: () => Promise<unknown>) =>
        Effect.promise(async () => {
          try {
            await evaluate()
          } catch {
            // best effort
          }
        })

      const reject = (evaluate: () => Promise<string | undefined>) =>
        Effect.promise(async () => {
          try {
            return await evaluate()
          } catch {
            return undefined
          }
        })

      yield* context.tool.hook("execute.before", (event) =>
        Effect.gen(function* () {
          const message = yield* reject(() => before(event))
          if (message) return yield* new ToolError({ message })
        }),
      )

      yield* context.tool.hook("execute.after", (event) =>
        Effect.gen(function* () {
          // Order matters: the guard may replace the error message, and the learners must see (and extend)
          // whatever is current. code_nav comes first because it only annotates and never rewords, so the
          // guard's notice keeps its leading position.
          codeNav.after(event as unknown as ToolResultEventLike)
          guard.after(event as ToolResultEvent)
          yield* attempt(() => learn.after(event as ToolResultEvent))
          yield* attempt(() => resolve.after(event as ToolResultEvent))
        }),
      )

      // Message-level hooks run in registration order. The guard capture must see the request BEFORE the
      // nudges patch it, and every nudge only touches the terminal user message (prompt-cache safety).
      yield* context.session.hook("context", (event) =>
        Effect.sync(() => guard.capture((event as { messages: ReadonlyArray<any> }).messages)),
      )
      // The nudge modules take the host event structurally (they read `messages` and patch the terminal
      // human message), so the cast widens through unknown: the host's SessionContext carries readonly
      // parts and v2 adds part kinds the local shape does not name. Runtime is unaffected — casts erase.
      yield* context.session.hook("context", (event) => attempt(() => skills.context(event as unknown as ContextEvent)))
      yield* context.session.hook("context", (event) => attempt(() => ledger.context(event as unknown as ContextEvent)))
      yield* context.session.hook("context", (event) => attempt(() => idle.context(event as unknown as ContextEvent)))

      yield* context.shell.hook("create.before", (event) => attempt(() => shell.before(event as ShellCreateEvent)))

      // Session tracking: register the owning process with the daemon for every session that starts.
      yield* context.event.subscribe().pipe(
        Stream.runForEach((event) => attempt(() => session.observe(event as HostEvent))),
        Effect.forkScoped,
      )
    }),
})
