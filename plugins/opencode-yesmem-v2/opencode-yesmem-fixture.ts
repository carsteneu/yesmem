// Shared harness for the opencode-yesmem plugin tests.
//
// Taken over from the opencode fork (packages/core/test/plugin, commit d01ecfd2f2), where the plugin was a
// built-in of the core server. Here the plugin lives next to these tests, so the entry resolves in place.
// The module shapes are still described with local type declarations where a test only touches part of one.
//
// The plugin uses the Effect plugin API, so the fake context registers Effect-returning hooks and `fire`
// runs them through the Effect runtime, exactly like the core hook dispatcher does.

import { mock } from "bun:test"
import { mkdtemp, rm } from "node:fs/promises"
import { createServer, type Server, type Socket } from "node:net"
import { tmpdir } from "node:os"
import path from "node:path"
import { Data, Effect, Exit, Fiber, PubSub, Stream } from "effect"

// The two opencode modules the plugin imports are workspace packages of the fork and do not exist here.
// Both stand-ins are the real thing, not a behaviour fake: `define` is the identity function
// (packages/plugin/src/effect/plugin.ts:62) and `Error` is the tagged tool error (packages/schema/src/tool.ts:61).
// The tag matters twice: the runner turns it into a model-visible tool failure, and `yield* new ToolError()`
// only propagates as a typed failure because a tagged error is yieldable.
mock.module("@opencode/plugin/effect", () => ({ Plugin: { define: (plugin: unknown) => plugin } }))
mock.module("@opencode/plugin/promise/tool", () => ({ Error: Data.TaggedError("Tool.Error") }))

// The plugin resolves its debug log file at call time; keep test runs out of the live yesmem install.
process.env.YESMEM_LOG_FILE ??= path.join(tmpdir(), "yesmem-plugin-test.log")

export const yesmemEntry = (file: string) => new URL(`./${file}`, import.meta.url).href

export type RpcResponse = { ok: boolean; result?: any; error?: string }

export type EffectPluginModule = {
  default: { id: string; effect: (context: unknown) => Effect.Effect<unknown, never, never> }
}

export type DaemonRequest = { method: string; params: Record<string, any> }

export async function mockDaemon(handler: (request: DaemonRequest) => unknown | undefined) {
  const directory = await mkdtemp(path.join(tmpdir(), "yesmem-daemon-"))
  const socketPath = path.join(directory, "daemon.sock")
  const requests: DaemonRequest[] = []
  const sockets = new Set<Socket>()
  // allowHalfOpen: the real daemon keeps its side open in a decode loop, so a client half-close must not
  // auto-close the server side (that would fake an EOF response from a daemon that never answered).
  const server: Server = createServer({ allowHalfOpen: true }, (socket) => {
    sockets.add(socket)
    socket.on("close", () => sockets.delete(socket))
    let buffer = ""
    socket.on("data", (chunk) => {
      buffer += chunk.toString()
      while (true) {
        const newline = buffer.indexOf("\n")
        if (newline === -1) return
        const line = buffer.slice(0, newline)
        buffer = buffer.slice(newline + 1)
        if (!line.trim()) continue
        const request = JSON.parse(line) as DaemonRequest
        requests.push(request)
        const response = handler(request)
        if (response === undefined) continue
        socket.write(JSON.stringify(response) + "\n")
      }
    })
  })
  await new Promise<void>((resolve) => server.listen(socketPath, resolve))
  return {
    socketPath,
    requests,
    /**
     * Requests the plugin issued in response to hooks. The activation-time index priming is asynchronous
     * (it must not stall plugin activation), so it is filtered by method instead of by position.
     */
    hooked: () => requests.filter((request) => request.method !== "search_code_index"),
    close: async () => {
      for (const socket of sockets) socket.destroy()
      await new Promise<void>((resolve) => server.close(() => resolve()))
      await rm(directory, { recursive: true, force: true })
    },
  }
}

/** Runs `body` with YESMEM_SOCKET pointed at `daemon` and restores the previous value afterwards. */
export async function withSocket<T>(socketPath: string, body: () => Promise<T>): Promise<T> {
  const previous = process.env.YESMEM_SOCKET
  process.env.YESMEM_SOCKET = socketPath
  try {
    return await body()
  } finally {
    if (previous === undefined) delete process.env.YESMEM_SOCKET
    else process.env.YESMEM_SOCKET = previous
  }
}

/** Runs `body` with the given env overrides and restores the previous values afterwards. */
export async function withEnv<T>(environment: Record<string, string | undefined>, body: () => Promise<T>): Promise<T> {
  const previous = new Map(Object.keys(environment).map((key) => [key, process.env[key]]))
  for (const [key, value] of Object.entries(environment)) {
    if (value === undefined) delete process.env[key]
    else process.env[key] = value
  }
  try {
    return await body()
  } finally {
    for (const [key, value] of previous) {
      if (value === undefined) delete process.env[key]
      else process.env[key] = value
    }
  }
}

export type HookEvent = Record<string, any>
export type HookCallback = (event: HookEvent) => unknown
export type HookResult = { readonly event: HookEvent; readonly failed: boolean; readonly error?: any }

export type HookRecorder = {
  readonly hooks: Map<string, HookCallback[]>
  readonly context: any
  /** Runs every registered callback in registration order; a failure stops the chain, as in the host. */
  fire: (key: string, event: HookEvent) => Promise<HookResult>
  /** Publishes one host event into the plugin's subscription. */
  emit: (event: HookEvent) => void
}

// A hook that returns a rejected Effect fails with "Fail"; a callback that throws inside Effect.gen
// surfaces as "Die" (index.ts throws the ToolError). Both carry what a test wants to inspect. `reasons`
// is the effect 4 cause payload; `failures` is kept as a fallback for the other spelling.
function firstFailure(cause: any) {
  for (const reason of cause?.reasons ?? cause?.failures ?? []) {
    if (reason?._tag === "Fail") return reason.error as any
    if (reason?._tag === "Die") return reason.defect as any
  }
  return undefined
}

/**
 * Minimal stand-in for the effect plugin context: it records hook registrations under
 * `<domain>.<name>` so tests can invoke the plugin's own callbacks directly.
 */
export function fakeContext(directory: string): HookRecorder {
  const hooks = new Map<string, HookCallback[]>()
  const events = Effect.runSync(PubSub.unbounded<HookEvent>())
  const domain = (name: string) => ({
    hook: (hookName: string, callback: HookCallback) => {
      const key = `${name}.${hookName}`
      hooks.set(key, [...(hooks.get(key) ?? []), callback])
      // The real registration also closes over a scope; tests need no finalizers.
      return Effect.succeed({ dispose: () => Effect.void })
    },
  })
  const context = {
    app: { name: "opencode", version: "2.0.26", channel: "test" },
    location: { directory },
    options: {},
    tool: domain("tool"),
    shell: domain("shell"),
    session: domain("session"),
    event: { subscribe: () => Stream.fromPubSub(events) },
  }
  return {
    hooks,
    context,
    emit: (event) => Effect.runSync(PubSub.publish(events, event)),
    async fire(key, event) {
      for (const callback of hooks.get(key) ?? []) {
        const result = callback(event)
        if (Effect.isEffect(result)) {
          const exit = await Effect.runPromiseExit(result as Effect.Effect<unknown, unknown>)
          if (Exit.isFailure(exit)) return { event, failed: true, error: firstFailure(exit.cause) }
        } else {
          await result
        }
      }
      return { event, failed: false }
    },
  }
}

/**
 * Loads the plugin against the fake context.
 *
 * `keepAlive` mirrors the host, which keeps the plugin's scope open for the plugin's lifetime: without it
 * the forked event subscription is interrupted as soon as the setup effect completes.
 */
type ResettableModule = { _resetCaches?: () => void; resetGuardState?: () => void }

/** The plugin keeps process-lifetime caches; a test must not inherit another test's answers. */
async function resetPluginCaches() {
  for (const file of ["skill_nudge.ts", "rule_guard.ts"]) {
    const pluginModule = (await import(yesmemEntry(file))) as ResettableModule
    pluginModule._resetCaches?.()
    pluginModule.resetGuardState?.()
  }
}

export async function loadPlugin(recorder: HookRecorder, options: { keepAlive?: boolean } = {}) {
  await resetPluginCaches()
  const module = (await import(yesmemEntry("index.ts"))) as EffectPluginModule
  const setup = module.default.effect(recorder.context) as Effect.Effect<unknown>
  if (!options.keepAlive) {
    // The plugin registers its hooks inside a scope; the host supplies it for the plugin's lifetime.
    await Effect.runPromise(Effect.scoped(setup) as Effect.Effect<unknown>)
    return { module, stop: () => Promise.resolve() }
  }

  let ready!: () => void
  const started = new Promise<void>((resolve) => (ready = resolve))
  const fiber = Effect.runFork(
    Effect.scoped(
      Effect.gen(function* () {
        yield* setup
        yield* Effect.sync(() => ready())
        yield* Effect.never
      }),
    ) as Effect.Effect<never>,
  )
  await started
  return { module, stop: () => Effect.runPromise(Fiber.interrupt(fiber)).then(() => undefined) }
}
