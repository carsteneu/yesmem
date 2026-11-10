# opencode-yesmem — v2 plugin

Server-side YesMem plugin for opencode v2. This is the v2 port of the v1 plugin at
`~/.local/share/yesmem/plugins/opencode-yesmem/` (opencode v1 hook API).

Source of the port: the v1 plugin directory (index.ts + code_nav.ts + rule_guard.ts + failure_learn.ts +
auto_resolve.ts + context.ts + skill_whitelist.ts + rpc.ts + types.ts), read directly from
`~/.local/share/yesmem/plugins/opencode-yesmem/` on 2026-10-12. The v2 hook surface is a different API
(`Plugin.define` + per-domain `hook()`), so `index.ts` is new; the domain modules carry the v1 logic with v2
call sites.

## Layout

| File | Origin | Role |
|---|---|---|
| `index.ts` | new (v2 adapter) | Entrypoint. Registers every hook through one `Plugin.define`. |
| `rpc.ts` | port of v1 `rpc.ts` | Daemon client (transport rewritten, see Deviations). |
| `types.ts` | copy of v1 `types.ts` | `RPCResponse`. |
| `context.ts` | port of v1 `context.ts` | Per-call session/cwd context for `yesmem_*` tools. |
| `code_nav.ts` | port of v1 `code_nav.ts` | Shell/read navigation guard on files the YesMem index covers; honours `code_nav_mode` (`block`, `nudge`, `off`), delivering the `nudge` hint through `execute.after`. |
| `rule_guard.ts` | port of v1 `rule_guard.ts` | Pre-tool rule check as an adapter over the daemon RPC `guard_check`: event wiring, context, rendering of BLOCK/SUGGEST/UNAVAILABLE. No model, no transport, no rules loading of its own. |
| `failure_learn.ts` | port of v1 `failure_learn.ts` | Error → `remember`, plus a `hybrid_search` assist note. |
| `auto_resolve.ts` | port of v1 `auto_resolve.ts` | `git commit -m` → `resolve_by_text`. |
| `session.ts` | new | Session tracking + `register_pid` (v1 used the `session.created` hook). |
| `messages.ts` | new | Message helpers + the human-turn predicate shared by the nudges. |
| `skill_nudge.ts` | port of v1 `skill_nudge.ts` | Staged skill suggestion on the terminal user message; the semantic stage is the same `guard_check` RPC with `purpose: "nudge"`. |
| `ledger_nudge.ts` | port of v1 `ledger_nudge.ts` | Appends the active plan as a live ledger block. |
| `idle_reminder.ts` | port of v1 `idle_reminder.ts` | Invisible yesmem reminder every Nth user turn. |
| `shell.ts` | port of v1 `shell.env` | Exports the yesmem environment to spawned shells. |
| `skill_whitelist.ts` | port of v1 `skill_whitelist.ts` | Installed-skill set used to filter ghost suggestions (roots are resolved per call, so `YESMEM_SKILL_ROOTS` is honoured). |
| `log.ts` | new | v1 duplicated this logger in four modules. |

`RULES.md` from the v1 directory is **project data**, not plugin source, and is not copied.

## Activation

The plugin is a **server** plugin and ships as a built-in of the core server: it lives in
`packages/core/src/plugin/yesmem/` and is registered in `packages/core/src/plugin/internal.ts` as the last
entry of the `pre` list. The plugin supervisor activates it with every other internal plugin
(`source: { type: "builtin" }`) — there is no config document entry, no plugin directory and no install step.

The tail of `pre` is deliberate: a plugin loaded from a plugin directory lands in the supervisor's
"packages" group, which activates after the whole `pre` list, so the tail of `pre` is the equivalent position
for a built-in — as long as nothing contributes SDK or instance plugins, which the CLI and the server do not.
The hook order `pre -> this plugin -> configured plugins -> post` is therefore the same as before the move;
with SDK- or instance-contributed plugins this one registers before them instead of after.

### Migration from the plugin directory

A copy under `<config>/plugins/opencode-yesmem` (or any `plugins` entry pointing at one) is now a duplicate of
the built-in: the supervisor loads it, drops it, and reports `Duplicate plugin ID: opencode.yesmem` in the
plugin inventory. The plugin itself never runs twice — only the first registration in boot order, which is the
built-in, is activated. Delete the copy to clear the report:

```
rm -rf ~/.config/opencode/plugins/opencode-yesmem
```

MCP tools come from the shared `mcp.yesmem` entry and are independent of this plugin.

### Configuration

The plugin reads everything from the environment and from its own directory — there is no config file:

| Variable | Effect |
|---|---|
| `YESMEM_SOCKET` | Daemon socket. Default `$HOME/.claude/yesmem/daemon.sock`. |
| `YESMEM_LOG_FILE` | Debug log. Default `$HOME/.claude/yesmem/logs/plugin-v2.log`. |
| `YESMEM_SKILL_CATALOG` | First candidate for the skill catalogue, before the project's `RULES.md`. |
| `YESMEM_SKILL_ROOTS` | Extra `:`-separated roots for installed-skill detection. |
| `YESMEM_SESSION_ID` | Written, not read: the id of the last session the plugin saw. |
| `YESMEM_GUARD_URL` / `_KEY` / `_MODEL` / `_NPM` | Short-circuit the guard's provider resolution. |
| `YESMEM_IDLE_CADENCE` | Turns between invisible idle reminders (floor 2). |

Kill switches, each turning one behaviour off: `YESMEM_IDLE=off`, `YESMEM_LEDGER=off`,
`YESMEM_SKILL_NUDGE=off`, and removing the project's `RULES.md` disables the guard.

As a built-in the plugin is switched off through the plugin configuration like any other server plugin: a
`{ "plugins": ["-opencode.yesmem"] }` entry in a config document removes it (the supervisor's `remove`
operation), and `/plugins` reports it. `opencode.yesmem` is not in the supervisor's `guarded` set, so the
switch works.

The catalogue candidates are `YESMEM_SKILL_CATALOG`, then `<plugin directory>/RULES.md`, then
`<project>/RULES.md`. The plugin-directory candidate is a leftover from the plugin-suite layout: the built-in
has no `RULES.md` beside it, so that candidate is always empty and the environment variable plus the project
file are the live sources.

## Why the Effect plugin API

The plugin is an **Effect** plugin (`@opencode/plugin/effect`), not a Promise plugin, because of how a
blocked call has to fail:

- `execute.before` is the only hook a plugin may fail, and the session runner recovers it by tag
  (`packages/core/src/session/runner/step.ts` catches `Tool.Error`) and turns it into a model-visible tool
  failure.
- The Promise adapter wraps hook callbacks in `Effect.promise`, whose contract is "the promise must not
  reject" — a throw becomes a **defect**, which the runner does not recover.

So a real `Tool.Error` is required. It is imported from `@opencode/plugin/promise/tool`, which re-exports the
same `@opencode/schema/tool` class the core uses.

Every other hook still runs plain async code, lifted with `Effect.tryPromise` inside `index.ts`.

## Hook mapping (v1 → v2)

| v1 hook | v2 mechanism | Notes |
|---|---|---|
| `tool.execute.before` | `ctx.tool.hook("execute.before", e)` | `e.input` and `e.tool` are mutable; failing with `Tool.Error` rejects the call. code_nav runs first, then rule_guard, then the tool-context injection. |
| `tool.execute.after` | `ctx.tool.hook("execute.after", e)` | `status: "completed"` exposes a mutable `e.result`; `status: "error"` a mutable `e.error`. rule_guard's directive is injected first, then failure_learn, then auto_resolve. |
| `shell.env` | `ctx.shell.hook("create.before", e)` | Mutable `e.env`. |
| `session.created` | `ctx.event.subscribe()` → `session.created`, `data.sessionID` | Registers the process with the daemon; mirrors the id into `process.env.YESMEM_SESSION_ID`. |
| `experimental.chat.messages.transform` | `ctx.session.hook("context", e)` | Four handlers in registration order: read-only conversation capture for the guard, then the three nudges (`skill_nudge`, `ledger_nudge`, `idle_reminder`). Every nudge only patches the **last message the human actually wrote** (see below), keeps a marker guard against double patching, and produces identical bytes for every step of a turn. |

`role === "user"` is not the human: v2 lowers several history entries to user-role messages. A message counts
as human only when it has metadata, does not open with one of the frames the core builds (shell results
`The following shell command was executed by the user:`, compaction `<conversation-checkpoint>`, a location
switch `The working directory has been changed to `, an inlined skill `<skill_content`), and does not reuse an
earlier message id. The last rule matters because a skill activation appends its message with the id of the
human message that triggered it (`packages/schema/src/session-message.ts:28` plus the skill event id in
`packages/core/src/session/session.ts`), so a repeated id is a derived message and keeps the patch target on
the human turn. Synthetic messages carry no metadata at all (`packages/core/src/bus.ts` only sets it when the
publisher passes one). The predicate lives in `messages.ts` and drives both the patch target and the idle
cadence. Known limit, documented in the file: a user-role message with metadata, a fresh id and a bare
SKILL.md body would pass — no core path produces one today.
| `chat.params` (summary-title fallback) | — | No v2 equivalent; the fallback is dropped. |

## Deviations from v1

1. **RPC transport.** v1 shelled out to `echo <json> | nc -U -N -w N <socket>` via `Bun.$`. `nc` is not
   guaranteed to exist and `Bun` is not guaranteed to be the runtime, so the socket is dialed with
   `node:net`. The wire protocol is unchanged (one JSON request line, one JSON response object).
2. **The guard has no model of its own (second loop).** v2 first carried the v1 resolution in TypeScript:
   `config.yaml` scanned without the `yaml` module, `models.json`/`auth.json` searched, the request posted
   directly. That was a second judgement path next to `internal/guard` and could drift from it. Both
   `rule_guard.ts` and `skill_nudge.ts` are adapters over `guard_check` now; the daemon resolves the model,
   owns the prompt, the policy, the answer cache and the refusal lock. What stays here is the harness
   side: which events are worth asking about, the context the daemon cannot see, and the rendering.
4. **Tool names and sets.** v2 names the shell tool `shell` (not `bash`) and adds a file-mutating `patch`
   tool. blockable = `write`/`edit`/`patch`/`shell`; `failClosed` (a call the guard cannot judge is refused
   rather than passed) = `write`/`edit`/`patch`, see YM-466; suggestable adds `patch`; `task` is now `subagent`.
5. **Indexed-project memo.** The code_nav memo is keyed by directory and holds one entry. (With one plugin
   instance per location this is equivalent to v1's single flag; it matters only if several instances share a
   process.)
6. **`log.ts`.** v1 duplicated the file logger in every module. The log is `plugin-v2.log` (mode `0600`) so a
   running v1 plugin keeps its own, and `YESMEM_LOG_FILE` overrides the path.
7. **Human-turn targeting, sanitization and caps (v2-only hardening).** v1 had the message *types* available
   and could rule out synthetic turns directly; the v2 hook only sees lowered messages, so `messages.ts`
   recognises the non-human frames above. v1 also injected daemon/model text verbatim: the ledger block is now
   bounded (40 lines, 4000 chars) and stripped of control characters with its delimiters neutralized, a skill
   name must match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` before it is injected, the stage-2 response body is
   capped, and the skill-catalogue read of a `RULES.md` larger than 256 KiB is skipped instead of being read
   into memory. The plugin's rule read is gone with the adapter: the daemon loads `RULES.md` and bounds what
   it sends (answer size, prompt context). The delimiter neutralizer is case-insensitive and the strip covers the Unicode line/paragraph
   separators and bidi overrides, so a plan cannot close the block or forge a top-level line.
8. **`auto_resolve` checks the outcome.** v1 resolved tasks from a `git commit -m "..."` regardless of whether
   the commit succeeded. A failed commit (rejected hook, failing test) is now ignored.
9. **RPC hardening.** The response buffer is capped (1 MiB), and daemon bytes are no longer echoed back inside
   error strings, because callers render those strings into model-visible text. The guard's own call is
   bounded at 35 s, above the daemon's model budget (15 s, one retry, one step down the model ladder), so a
   slow endpoint becomes a `PASS` with a reason instead of a truncated RPC.

## Known gaps (not built here)

- **`chat.params`.** No v2 equivalent, so the summary-title conversation fallback is gone.
- **`code_nav` and the dedicated `grep`/`glob` tools.** v1 blocked shell commands and the `read` tool only;
  v2's separate `grep`/`glob` tools are not covered, matching v1's behaviour rather than extending it.

## Open security items

The security review of the built-in move found three items in the ported plugin. Two of them do not exist any
more, because the code that carried them moved into the daemon; one remains, and it is a property of the
design rather than of the move.

1. **The guard's provider egress is on by default, with no switch for itself.** Resolved. The plugin no longer
   has a provider: `rule_guard.ts` and `skill_nudge.ts` send the tool arguments, up to 6 conversation lines and
   the project path to the daemon over the local socket, and the daemon decides what leaves the machine
   (`internal/guard`, documented in `docs/features/guard.md`). `YESMEM_GUARD_URL`/`_KEY`/`_MODEL`/`_NPM` still
   exist, but they are the daemon's. There is still no per-guard off switch; that remains a decision about
   the daemon, not about this plugin.
2. **The guard model's BLOCK text reached the model unsanitized.** Resolved in the adapter. Every text the
   daemon's answer contributes to a tool error or a tool result goes through `guardMessage`
   (`rule_guard.ts`): control characters and Unicode line separators are collapsed to one spaced line and the
   text is capped before it reaches the agent's context.
3. **The guard path read `RULES.md` whole.** Resolved for the guard: `rule_guard.ts` reads no rules, the
   daemon loads them and bounds the request (answer size, prompt context capped at 4000 characters).
   `skill_nudge.ts` still reads the catalogue for its local stage, capped at 256 KiB (see the deviations
   above); the daemon reads it again for the semantic stage and bounds that prompt itself.

Two further review observations need no code change: the `log.ts:21` `mode` only applies when the log is
created (the path is operator-controlled, so it is defense in depth at most), and `shell.ts:19-24` adds
`YESMEM_SOCKET`/`YESMEM_SOURCE_AGENT`/`YESMEM_SESSION_ID` to every spawned shell without exporting a
credential.

## Operational notes

- **Data egress.** When a project has a `RULES.md`, the guard is asked about every `read`/`shell`/`grep`/`glob`/
  `write`/`edit`/`patch` call. The plugin sends the tool name, the arguments (for a `write` that is file
  content), the project directory, the session id and up to 6 recent conversation lines to the daemon; the
  daemon adds the rules text and asks the provider the configured model resolves to. The guard answers `PASS`
  and stays silent when no credential resolves.
- **Latency.** code_nav, the guard and the learner add daemon/provider round trips to tool calls. The guard's
  RPC is bounded at 35 s (the daemon's model budget plus one step down the ladder), every other RPC at 5 s; a
  wedged daemon therefore degrades rather than hangs, but it does cost real time on every call. An unreachable
  daemon is a `PASS` with a reason, not a refusal: only a request the model endpoint refused refuses a write.
- **`register_pid` is per call.** Every shell call re-registers `process.pid` for the current session, and the
  daemon keeps one mapping per PID — with several concurrent sessions the last shell wins. This is v1
  behaviour, kept for parity.
- **Session id is per process.** The plugin tracks one current session (last `session.created`) and exports it
  to spawned shells; `execute.before` still injects the *per-call* session into `yesmem_` tool arguments, which
  is the value the MCP tools use.
- **Trust.** `RULES.md`, `.git`/`HEAD` and the guard's answer are treated as data, not as trusted input: the
  violated-rule text is capped and labelled, learned snippets are capped, and file paths are stripped of
  control characters before they reach a message or the log.
- **Nudges.** All three are patched into the last human message only. `skill_nudge` matches the catalogue
  triggers locally first; when that fails it asks the daemon in the **background** (`guard_check`,
  `purpose: "nudge"`) and caches the answer by (catalogue, message text) — the first sighting of a message is
  never nudged from the cache. `ledger_nudge` fetches the
  active plan with a 1 s budget and caches it per user message, so every step of a turn injects the same bytes
  and one turn costs one fetch. `idle_reminder` is deterministic (every Nth human turn, `YESMEM_IDLE_CADENCE`,
  floor 2, ignoring non-numeric values).
  Kill switches: `YESMEM_SKILL_NUDGE` (unset = on), `YESMEM_LEDGER=off`, `YESMEM_IDLE=off`; the skill
  catalogue is looked up via `YESMEM_SKILL_CATALOG`, then the plugin directory, then the project directory.

## Tests

The suite in this directory runs with `bun test`. `effect@4.0.1` is a devDependency, so a fresh clone needs the
install first: without it the run aborts after ten failed files and reads as a code failure. CI runs the same two
steps in the `plugin` job of `.github/workflows/ci.yml`, with a pinned `bun-version` and the install as
`bun install --frozen-lockfile`:

```
bun install
bun test
```

From the repository root, `make test-plugin` runs exactly those two steps and `make test` runs them together with
the Go suite, so `make test` covers the plugin and cannot report a green repo while this suite is red. The
directory alone is not runnable: `code_nav_test.ts` reads the shared parity fixture
`../../internal/hooks/testdata/codenav_parity.json` (one source for the Go hook and the TypeScript plugin, YM-409),
so the tests need the repository around them. Copying just this directory elsewhere fails with `ENOENT`, which
looks like a plugin defect but is the missing fixture. `bun` is also not on PATH in every shell: the Makefile
resolves it through `BUN` (PATH lookup, then `~/.bun/bin/bun`), and fails with a clear message instead of a bare
`command not found` when neither exists.

Separately, the opencode fork carries a copy of these tests at
`packages/core/test/plugin/opencode-yesmem*.test.ts`, run from `packages/core`:

```
bun run test plugin/opencode-yesmem
```

The tests import the plugin's modules straight from `packages/core/src/plugin/yesmem/` and call the registered
hook callbacks directly. The integration test activates the same definition the supervisor activates
(`{ ...yesmem, revision: "internal", source: { type: "builtin" } }`) through the real core plugin registry and
`Tool.Service` snapshot, which is what proves a block rejects the call before the tool runs.
`packages/core/test/plugin/supervisor.test.ts` covers the duplicate policy: a configured copy of the built-in
is reported as a duplicate failure while the built-in stays the single active registration.

The daemon is always a local mock socket pointed to by `YESMEM_SOCKET`, the guard endpoint a local HTTP mock,
and `YESMEM_LOG_FILE` keeps the plugin's debug log out of the live install; no live daemon, config directory or
provider is touched. Besides the per-feature tests there is a resilience group (a throwing hook callback must
not fail the call, a failed commit must not resolve tasks), a nudge group (catalogue parsing in both shapes,
local match, ghost filtering, no double patch, human-turn targeting, the background match plus its cache,
ledger rendering/sanitization/patch-once/one-fetch-per-turn, idle cadence on human turns only, and the
skill-name validation), a message-classification group built from the core's own frames
(`opencode-yesmem-messages.test.ts`), a body-budget group (`opencode-yesmem-http.test.ts`) and the integration
group described above.
