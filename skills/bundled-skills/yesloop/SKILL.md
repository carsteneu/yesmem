---
name: yesloop
description: Autonomous task loop — analyze, plan, execute, verify, review, finish. Runs as visible TUI agent in git worktree. Use when user says "yesloop", "loop", "run it autonomously".
user-invocable: true
disable-model-invocation: false
allowed-tools: Bash(git *) Bash(go *) Bash(make *) Bash(npm *) Bash(docker *) Bash(python3 *) Bash(sqlite3 *) Bash(curl *) Bash(find *) Bash(ls *) Bash(cat *) Bash(rg *) Bash(mv *) Bash(cp *) Bash(rm *) Bash(mkdir *) Bash(test *) Grep Glob Read Write Edit todowrite Task
---

# Autonomous Task Loop

You are an autonomous agent. Work through tasks without asking the user.

## CRITICAL: Always spawn in a worktree

When invoked from an interactive session, **do NOT execute the pipeline yourself.** Instead, spawn an isolated agent in a git worktree:

1. **Create worktree:** `git worktree add -b yesloop/<task-slug> <repo>/.worktrees/yesloop-<task-slug>`
   - Isolates agent changes from your working directory
   - Multiple agents can run in parallel without conflicts
   - Easy cleanup: delete worktree + branch if abandoned
2. Write the task to scratchpad with worktree path and skill-read mandate:
   `scratchpad_write(project="<project>", section="yesloop-<task-slug>", content="YOU ARE A YESLOOP-AGENT. MANDATORY - YOU MUST READ THE YESLOOP SKILL AND YOU MUST FOLLOW THE INSTRUCTIONS IN THIS SKILL. Load the yesloop skill: use your platform's skill tool, or read ~/.claude/skills/yesloop/SKILL.md, or <repo-root>/skills/bundled-skills/yesloop/SKILL.md. Follow the 6-phase pipeline (ANALYZE → PLAN → EXECUTE → VERIFY → REVIEW → FINISH) FORMALLY AND SUBSTANTIVELY with ATTENTION.\n\nWorktree: <path>\nTask: <ticket key if the task comes from a ticket, e.g. YM-123> <description>\nSession title: <one-line title from the requirement, max ~60 chars>\n\nReport to scratchpad.")`
   The spawned agent has NOT auto-loaded this skill — the mandate prefix is mandatory.
3. **Spawn TUI agent (MANDATORY — never run the pipeline inline):** `yesmem_spawn_agent(project="<project>", section="yesloop-<task-slug>", backend="opencode", work_dir="<worktree-path>")`
4. Wait 15s for opencode TUI to load + PTY injection to deliver the startup prompt
5. **Relay kick (backup):** `yesmem_relay_agent(to="<agent-id>", content="Read scratchpad section 'yesloop-<task-slug>' AND load the yesloop skill (skill tool or ~/.claude/skills/yesloop/SKILL.md). MANDATORY - YOU MUST READ THE YESLOOP SKILL AND YOU MUST FOLLOW THE INSTRUCTIONS IN THIS SKILL. Your agent id: <agent-id>. At Phase 1 start, rename your session to '[<agent-id>] <Session title from briefing>'. Begin the 6-phase pipeline FORMALLY AND SUBSTANTIVELY with ATTENTION.\n")` — backup if PTY is slow
6. Confirm: "Agent spawned in worktree — visible in terminal."

**Only run inline if:** the user EXPLICITLY says `--inline`. There is NO trivial-task exception — "< 2 min" is not a license to skip the worktree. If you'd commit it, it runs in the worktree. Create the worktree BEFORE the first edit.

**"Pipeline" includes exploratory work.** Writing tests, spiking an approach, "just quickly trying the fix on main to see if it works" — if you'd commit it, it belongs in the worktree. Create the worktree BEFORE the first edit, not after you've verified the fix works.

## Model selection (opencode backend)

`yesmem_spawn_agent(model=...)` accepts three forms, resolved by `resolveSpawnModel` in `internal/daemon/spawn_model.go`:

- **Empty** → returns "" — lets the opencode CLI pick its default from its own configuration. No model is hardcoded in the daemon.
- **Slash-qualified** (e.g. `zai/glm-5.2`, `zai-coding-plan/glm-5.2`) → passed through verbatim.
- **Bare model name** (e.g. `glm-5.2`, `deepseek-v4-pro`) → resolved against the auto-discovered provider map (built from `~/.cache/opencode/models.json` + `~/.config/opencode/opencode.json`). The daemon prepends the matching providerID, e.g. `glm-5.2` becomes `zai/glm-5.2`.

**Coding-optimized variants** (any provider whose ID contains "coding", e.g. `zai-coding-plan`) are included in the auto-discovery map like any other provider. When multiple providers carry the same bare modelID, a credential-backed provider wins over a credential-free one, and only within the same credential state does the alphabetically-first ProviderID win deterministically (`sortProviders` in `internal/proxy/provider_autoconf.go`); resolve ambiguity explicitly via `model="provider/model"`. Rule of thumb: bare names resolve to whatever the active opencode config exposes; if only `zai-coding-plan` carries `glm-5.2`, bare `glm-5.2` resolves to `zai-coding-plan/glm-5.2`.

Unknown bare names log a warning and pass through unchanged (best-effort, no error).

## Orchestrator Contract (what to prescribe vs. delegate)

When you spawn a yesloop agent, the scratchpad task defines the working relationship. Over-prescribing takes ANALYZE+PLAN away from the agent; under-prescribing sends it into orientation loops. Get the level right.

**Prescribe (orchestrator's job):**
- **Skill-Read-Mandate** — The scratchpad task MUST start with 'YOU ARE A YESLOOP-AGENT.' and instruct the agent to load the yesloop skill AND read it with: MANDATORY - YOU MUST READ THE YESLOOP SKILL AND YOU MUST FOLLOW THE INSTRUCTIONS IN THIS SKILL. The agent MUST follow this skill FORMALLY AND SUBSTANTIVELY — every phase, block format, guardrail, and rule — with ATTENTION and MANDATORY. The spawned agent has NOT auto-loaded this skill; without the mandate it works without pipeline context. Path fallbacks: platform skill tool, `~/.claude/skills/yesloop/SKILL.md`, or `<repo-root>/skills/bundled-skills/yesloop/SKILL.md`.
- **Goal** in 1-2 sentences — what success looks like, not how to get there
- **Dense context** — facts, file paths, relevant learning IDs, what's already been tried, what failed. The agent starts cold; you don't.
- **Hard constraints** — schema-breaking changes, backfills, new dependencies, destructive ops, files off-limits
- **Escalation triggers** — decisions that belong to the user (product direction, API shape changes, breaking compatibility)

**Delegate (agent's job):**
- **ANALYZE** — reading code, grepping, finding the call sites, identifying edge cases
- **PLAN** — commit structure, file selection, test strategy, step ordering, milestone breakdown
- **EXECUTE/VERIFY/REVIEW** — the full 6-phase pipeline on the agent's plan, not yours

**Calibration test:** Before writing the scratchpad, ask: "If the agent came back with a plan, would I be surprised?" If yes for the right reasons (better approach), you prescribed right. If yes for wrong reasons (misunderstood goal), add context. If bored (matches what you'd write), over-prescribed — shorten.

## Relay-to-Resume Timing (paused agents)

Relay delivery to a `paused` agent is **asynchronous**: the daemon heartbeats the inject socket at ~1s intervals, the opencode TUI may be mid-turn, and the PTY bridge buffers. A relay is NOT reflected in agent status or scratchpad within seconds.

**Rule: after `relay_agent` on a paused agent, wait ≥ 2 minutes before re-checking status or concluding the relay failed.** A status that still reads `paused` 10 seconds after a relay is expected behavior, not a failure — the relay hasn't been picked up yet.

Anti-pattern (observed): orchestrator relays approval → checks status 5s later → still `paused` → concludes "relay did nothing, must resume manually" → calls `resume_agent`. This is redundant (paused PID is alive, PTY bridge open — resume is a no-op dressed up as action) and confusing. See Learning #81175: for `paused` agents, `relay_agent` is the correct action; `resume_agent` is only for genuinely dead processes.

Correct sequence after relaying to a paused agent:
1. `relay_agent(to=<id>, content="...")` — approval/instruction delivered
2. **Wait ≥ 2 minutes** — the agent needs to pick up the relay on its next tick, process it, write scratchpad, and call `update_agent_status`. Shorter windows produce false negatives.
3. THEN re-check: `get_agent` status + `scratchpad_read(project, section)` for new evidence (phase update, milestone, DONE marker)
4. If after 2 min there is NO new scratchpad activity AND status unchanged → read the `progress` field. If it shows an escalation/waiting state, the agent is correctly paused — relay again or escalate higher. Only consider `resume_agent` if PID is dead or `progress` indicates a real stall (crash, timeout without escalation line).

## Monitoring discipline (running agents)

Intervention requires stale **activity signals**, not just a stale status label. A running agent's status field can lag behind actual work — the agent may be mid-turn, processing a long tool call, or writing scratchpad while `update_agent_status` hasn't fired yet.

**Rule: only intervene when ALL of these are stale for several minutes:**
- `last_activity_at` — no new activity timestamp
- `turns_used` — turn count not advancing
- `stream_bytes` — SSE stream not producing output

A status label alone is not a trigger. If activity signals are advancing, the agent is working — let it run. The monitoring job (heartbeat) is the next check, not a manual poke.

Anti-pattern: orchestrator sees status `paused` or `running` with no recent context → assumes stall → relays/resumes/kills. This disrupts agents that are mid-work. Check the activity signals first. See Learning #82126.

## Temp file discipline

NEVER write to `/tmp/` or `~/.claude/yesmem/tmp/` in autonomous operations. `/tmp/` is unreliable across sandbox/container contexts (logs vanish, see #72938). Global paths collide when multiple agents run in parallel.

Use `<worktree>/.yesmem/tmp/` instead. Project-local, always writable from any agent context, cleaned up with worktree removal. `mkdir -p .yesmem/tmp` at session start.

## Orchestrator-Pflichten (aus den früheren Pins 43, 49, 68, 101 und 108)

- **Spawn-Briefing.** Jeder Spawn beginnt mit dem Briefing und dem exakten Wortlaut YOU ARE A YESLOOP-AGENT. MANDATORY - YOU MUST READ THE YESLOOP SKILL AND YOU MUST FOLLOW THE INSTRUCTIONS IN THIS SKILL. Der Orchestrator liest diese Skill selbst vor jedem Spawn.
- **DONE prüfen.** Vor Abnahme und Merge: Tests selbst laufen lassen, Diff lesen, Phase 5 mit Cold Review und Behauptungs-Inventar (jede Erfolgsbehauptung trägt eine proof-Zeile). Unbewiesene Behauptungen werden als UNVERIFIED markiert.
- **Modell.** Beim Spawn immer das Standardmodell, keine Modellwahl und keine Rückfrage beim Nutzer. Grund (Carsten, 02.09.2026): das Standardmodell bringt den Loop am zuverlässigsten zu Ende.
- **Schließen.** Ein abgenommener Agent wird geschlossen (yesmem_stop_agent), nicht liegengelassen. Phase 6 FINISH beendet weder Prozess noch PTY-Brücke noch Socket, und ein späteres Resume kann eine zweite Session auf denselben Worktree setzen.
- **Kein Selbst-Merge, kein Selbst-Deploy.** Merge nach main und Deploy sind Orchestrator-Sache, Nie-Agenten-Sache.
- **Einordnung.** Code-Änderungen über etwa 10 Zeilen, über mehr als eine Datei oder mit Wirkung auf Verhalten, Architektur oder Deploy laufen über yesloop im Worktree. Inline bleiben Einzeiler, Tippfehler, reine Doku ohne Verhaltenswirkung und Lesearbeit. Ein Agent innerhalb einer yesloop wendet die Regel nicht auf sich selbst an.


## Execution Modes

**tui-agent** (DEFAULT) — Spawned via yesmem_spawn_agent → gnome-terminal, visible, non-blocking.
**inline** — User explicitly requested inline. Run in current context.
**scheduled** — User asked for recurring work. Use yesmem_schedule.

## Definition of DONE

DONE is a contract, not a feeling. Status is DONE only when ALL seven phase sections exist in scratchpad and each carries `**Status:** COMPLETE`.

**Valid Status values per phase:**
- `IN PROGRESS` — actively working
- `COMPLETE` — phase finished, evidence present
- `BLOCKED — <reason>` — cannot proceed, needs orchestrator input

**Phase status update is mandatory at every transition:**
```
update_agent_status(phase="Phase N/7 NAME")
update_agent_status(phase="Phase N/7 NAME (milestone M/K)")   # EXECUTE with milestones
update_agent_status(phase="Phase N/7 NAME (blocked: <why>)")
```

Free-form string, canonical format. Single source of truth for live progress.

**Orchestrator validates DONE:**
- DONE-guard (auto, every 30s) checks phase block compliance via regex
- Manual fallback: Read scratchpad section
- Check `### Phase 1` through `### Phase 7` headers exist
- Each has `**Status:** COMPLETE` (or `BLOCKED — <reason>` for genuinely blocked phases)
- Phase 5 has `self-review: done` plus evidence-backed `cold-review: ses_<id>`, `consequence: ses_<id>`, `security: ses_<id>` dispatch lines
- Phase 6 (DAEMON-VERIFY) carries the accepted pre-check: `**Vorab-Prüfung:** ... accepted`
- Phase 7 (FINISH) has `Deploy executed: yes` or `Deploy required: no`, and `send_to orchestrator:`

Missing evidence → guard fires automatically (freeze + notify orchestrator).

## Der Prüfbericht (`<section>-verify`)

Die Phasenblöcke sind nur die Form. Der Daemon glaubt keinem Bericht, er leitet jede
Behauptung selbst her. Dafür schreibst du neben den Pipeline-Abschnitt einen zweiten,
`<section>-verify` (gleiches Projekt, gleiche Scratchpad-Verbindung). Das ist die
Eingabe, die geprüft wird, und das ist der Abschnitt, den du korrigierst: die
Blockmeldung nennt den Befund und, wo es einen gibt, den Befehl, und sie druckt das
folgende Muster, wenn der Abschnitt fehlt.

```
[COMMIT: <vollständiger Hash>]
[BRANCH: <Branch, der gepusht wurde>]
[DEPLOY: yes|no]
Tests run: <Befehl> -> exit <N>
Files in scope: <pfad>, <pfad>
[EVIDENCE: cold-review=ses_…, consequence=ses_…, security=ses_…]
[PLAN: <projekt>]
```

- Der Bericht ist Eingabe, nie Urteil. Was du nicht behauptest, kann nicht bestätigt
  werden, und ein Platzhalter (`<hash>`, `TODO`) behauptet nichts.
- `[MERGE: …]` gehört nicht hinein: der Agent merged nie, das tut der Orchestrator.
  Die `MERGE`-Angabe der `send_to`-Payload bleibt davon unberührt.
- `[PLAN: …]` nur bei strukturiertem Plan: offene Schritte (`pending`, `in_progress`)
  blockieren DONE.

## Der Vorab-Lauf (verbindlich vor dem DONE)

Der Daemon prüft deinen Bericht nicht mehr selbst. Er liest ein Urteil, das vorher
entstanden ist, unter einem Schlüssel aus Berichtstext, HEAD, Branch, Plan und
Report-Session. Vor dem DONE setzt du deshalb genau diesen Lauf ab:

```
yesmem verify-claims --repo <worktree> --project <projekt> --section <section>-verify --profile yesloop-done --policy yesloop-done --strict
```

Die Session und der Plan-Thread sind keine Flags: eine Section nennt ihren Agenten, und
der Daemon leitet beides aus dessen Zeile ab. Genau deshalb muss die Section stimmen; ein
Bericht, den du inline mit `--report` prüfst, braucht die beiden Werte dagegen explizit,
weil es dann keinen Agenten gibt, aus dem sie kommen.

Die Reihenfolge ist Teil des Vertrags: erst den letzten Commit, dann den Plan
abschließen (keine offenen Schritte mehr), dann pushen, dann dieser Lauf, dann das
DONE. Die Plan-Revision steckt im Schlüssel, weil ein Bericht mit `[PLAN: …]` gegen
den Plan geurteilt wird; eine Plan-Schreibung nach dem Lauf entwertet das Urteil und
kostet einen zweiten Lauf.

Exit 0 heißt `accepted`: dann setzt du das DONE ab. Exit 1 heißt `blocked`: du korrigierst
den Bericht oder die Sache und läufst den Befehl erneut, denn ein geänderter Bericht ist
ein neuer Schlüssel. Ohne frisches angenommenes Urteil blockt das Gate dein DONE sofort
als Defekt und druckt diesen Befehl mit den konkreten Werten.

## Was nach DONE passiert

- Liegt das Urteil vor, gibt der Daemon sofort frei (`DONE-VERIFIED`) und nennt in der
  Freigabe, dass das Urteil aus der Vorab-Prüfung stammt.
- Blockt er dein DONE (`DONE-VERIFY-BLOCKED`), nennt die Meldung den Grund: fehlendes oder
  veraltetes Urteil, abweichendes Profil oder die Befunde des Laufs. Bei einer
  Report-Klasse (zum Beispiel refuted oder defect) korrigierst du, läufst die
  Vorab-Prüfung erneut und setzt das DONE wieder ab. Nach drei Blockungen desselben
  Versuchs eskaliert der Daemon an den Orchestrator, und du wartest.
- Die Klassen environment, limit und error sind nicht dein Fehler: melde sie dem
  Orchestrator und warte.
- Läuft deine Vorab-Prüfung noch, während der Tick dein DONE sieht, hält er dich auf
  `DONE-PENDING-VERIFY`. Du wartest: kein sleep, kein Polling, kein zweiter Bericht.

### Stille ist unmöglich

Du bist nicht der einzige Leser deines DONE. Ein zurückgehaltener DONE erreicht den
Orchestrator so oder so, und keiner dieser Wege hängt von dir ab:

- **Sofortmeldung.** In dem Moment, in dem die Zustellprüfung deinen DONE zurückhält,
  bekommt der Orchestrator `DONE-WITHHELD` mit Klasse, Grund und dem vollständigen
  `yesmem verify-claims`-Befehl. Er wartet nicht auf die dritte Runde.
- **Zeitwächter.** Bleibt dein DONE länger als das Fenster `done_verify.max_wait`
  unentschieden und du arbeitest nicht weiter, eskaliert der Daemon von selbst:
  `DONE-UNDECIDED` mit Zustand und Befehl, einmal pro Versuch.
- **Der Halt überlebt einen Neustart.** Er liegt mit dem Fingerabdruck deines Berichts in
  `done_verify_holds`; ein neuer Daemon setzt die Uhr nicht zurück.
- **Der Broadcast ist kein DONE-Kanal.** Ein DONE-Payload auf dem Broadcast-Kanal wird
  abgelehnt und auf `send_to` verwiesen. Veröffentlichen umgeht die Sperre nicht.

Was das für dich bedeutet: melde dich nicht mit einem zweiten Bericht und nicht über einen
anderen Kanal. Ein blockierter DONE gehört korrigiert, neu geprüft und erneut gesetzt, oder
er gehört dem Orchestrator.

## Automated DONE-Guard (v3)

Since yesmem v2.1.21, the heartbeat scheduler enforces the DONE contract **automatically** every 30 seconds. The DONE-guard (`internal/daemon/done_gate.go`) uses deterministic regex validation — no LLM, no `claude -p`, no false positives from model drift.

**What the guard checks:**

1. All 7 phase blocks (`### Phase 1` through `### Phase 7`) must be present in scratchpad
2. Each block must have `**Status:**` on its own line with valid value (COMPLETE|BLOCKED|IN PROGRESS)
3. Per-phase required fields (see Phase Pipeline below)
4. Phase 5 must contain `self-review: done` AND a `cold-review: ses_<id>` dispatch line
5. Phase 4 must contain `**Regression baseline:**` with a non-empty value and `**RED proof:**`
6. Phase 5 must contain `consequence: ses_<id>` AND `security: ses_<id>` dispatch lines — after the format check, the daemon verifies each reported id against opencode's session DB (row exists, parent session matches the agent's own session, agent type allowed per sub-phase, ids pairwise distinct)
7. Phase 6 must carry the accepted pre-check (`**Vorab-Prüfung:**` naming `accepted`), Phase 7 the handoff (`send_to orchestrator:` and deploy evidence)
8. Phase 5 must contain `**Stufe:**` (value 1-3 with a same-line rationale) and a non-empty `**Modules:**` line — the risk-based-code-review classification from the 5.2 cold reviewer

**When the guard fires:**
- Agent is paused with reason `DONE-GUARD: phase validation failed — <errors>`
- Orchestrator receives `⛔ DONE-GUARD:` notification via `send_to`
- Agent must fix phase blocks and be resumed

Before pausing, the guard relays the concrete missing fields and re-fires up to 2 times at 90s intervals. If a DONE-GUARD-paused agent's scratchpad becomes compliant, the guard unpauses it automatically (RECOVERY) and notifies the orchestrator.

**Phases missing from scratchpad = still in progress.** The guard only escalates when all 7 phase blocks exist but their content is non-compliant.

### DISMISSAL-PATTERN

These patterns cause the guard to reject the DONE claim:

| Pattern | Detection | Why it fails |
|---|---|---|
| Status on same line as header | `### Phase N: → **Status:**` inline | Status must be on its own `**Status:**` line |
| Old-format Phase 5 | Retired `task() dispatched:` / `consequence dispatched:` lines, new dispatch lines missing | Hard cut — the relay names the new field (`cold-review: ses_<id>`) |
| Missing self-review dispatch | No `self-review: done` in Phase 5 | Sub-phase 5.1 is mandatory |
| Missing cold-review dispatch | No `cold-review: ses_<id>` in Phase 5 | Sub-phase 5.2 is mandatory (supersedes the old Stage 2 header check) |
| Missing consequence dispatch | No `consequence: ses_<id>` in Phase 5 | Sub-phase 5.3 is mandatory |
| Missing security dispatch | No `security: ses_<id>` in Phase 5 | Sub-phase 5.4 is mandatory |
| Missing risk classification | No `**Stufe:**` line with value 1-3 + same-line rationale in Phase 5 | risk-based-code-review classification is required 5.2 evidence |
| Missing module selection | No `**Modules:**` value in Phase 5 | active modules per trigger table are required 5.2 evidence |
| Invalid evidence session ids | Reported id absent from opencode.db, parent session mismatch, duplicated id, or agent type not in the allowed set | Daemon evidence layer verifies substance, not just format (internal/daemon/done_gate_evidence.go) |
| Missing regression baseline | No `**Regression baseline:**` with a non-empty value in Phase 4 | Deterministic base-vs-head test diff is mandatory (quality stage-2) |
| Missing orchestrator notification | No `send_to orchestrator:` in Phase 7 | Orchestrator must be notified for DONE |
| Partial fields in Phase 5 | No issue breakdown, no findings per sub-phase | Self-Review must be structured |
| No deploy evidence in Phase 7 | No `Deploy executed:` or `Deploy required:` | Must document deployment outcome |
| Missing accepted pre-check | Phase 6 says COMPLETE without `**Vorab-Prüfung:** ... accepted` | The verification is the phase, not a label |
| Missing status line | Phase block exists but no `**Status:**` | Every phase needs a status |
| Missing session id note | Phase 1 has no `**Session id:**` field and no "session id missing" note | `whoami()` must be called at Phase 1 start; backend session id (or retry-failure note) must appear in Phase 1 block |
| Missing session title | Phase 1 has no `**Session title set:**` field and no rename-failure note | `opencode_session_rename` at Phase 1 start with `[<agent-id>] <Session title>` (see SESSION TITLE rule) |

### Findings-Table

When the DONE-guard rejects a claim, its findings are structured as:

| Phase | Missing/Invalid Field | Detail |
|---|---|---|
| 5 | `cold-review:` | required field not found in phase block |
| 6 | `send_to orchestrator:` | required field not found in phase block |

**DISMISSAL → DONE is rejected. Fix fields → resume agent.**

## Phase Pipeline

Each phase writes ONE structured block to scratchpad. Block format is mandatory — prose-only entries are not valid phase completion. The plan's outline carries the same seven phases (see "Phase skeleton"); the scratchpad block and the plan row change together, in the same update.

### CRITICAL — scratchpad project string + session id

All `scratchpad_write` and `scratchpad_read` calls MUST use the `project` value returned by `whoami()`. Call `whoami()` as the FIRST action at Phase 1 start and use its `project` field verbatim for every scratchpad call afterwards. Do NOT guess, do NOT use the worktree basename, do NOT hardcode a short name. The orchestrator and the idle/done-verify state machines read from `agent.Project` (set by `yesmem_spawn_agent`); using a different string means your writes and reads land in a different scope and the orchestrator never sees your progress.

`whoami()` also returns the backend session id (`opencode_session_id` for opencode, `codex_session_id` for codex). This id is required for `resume_agent` after crashes. The daemon polls it asynchronously after spawn, so the first `whoami()` call may return it empty. Retry up to 3 times with 5 seconds sleep between calls. If still empty after 3 retries, note "session id missing — resume may fail" in Phase 1 scratchpad and proceed. Do not block beyond 15 seconds.

**SESSION TITLE (MANDATORY at Phase 1 start, opencode backend):** Rename your own session so agent windows and session lists are identifiable: `opencode_session_rename(title="[<agent-id>] <Session title>")` — omit `sessionID` (renames the current session). In Code Mode the same tool is `tools.opencode.session_rename`. The bare name `session_rename` does not exist and answers "Unknown tool"; do not retry it and do not look for it in the yesmem surface. `<agent-id>` comes from the relay kick (e.g. agent-20261015-05); `<Session title>` from the briefing field, or derive a short title from the Task line if absent. Record `**Session title set:** <final title>` in the Phase 1 block. Non-opencode backends or rename failure: non-blocking — note it and continue.

**SCRATCHPAD DISCIPLINE (MANDATORY — prevents briefing clobber):**

`scratchpad_write` is an UPSERT — it REPLACES the entire section. **The FIRST action after spawn MUST be `scratchpad_read`.** Never call `scratchpad_write` before your first `scratchpad_read` — it will overwrite the orchestrator's briefing (#80063).

For intermediate progress updates (milestone complete, phase done, status markers), use **`scratchpad_append`** instead of `scratchpad_write`. `scratchpad_append` adds content WITH A DIVIDER, preserving the original briefing.

|| Action | Tool |
|---|---|---|
|| First thing after spawn | `scratchpad_read` |
|| Progress update (milestone/phase done) | `scratchpad_append` |
|| Full DONE report (all 7 phases) | `scratchpad_write` |
|| DRIFT alert / STUCK notice | `scratchpad_append` (preserves briefing) |

### Phase 1: ANALYZE
```
update_agent_status(phase="Phase 1/7 ANALYZE")

### Phase 1: ANALYZE
**Status:** COMPLETE
**Goal understood:** <1 sentence>
**Task type:** <debug|feature|chore|docs>
**Session id:** <opencode_session_id or codex_session_id from whoami(), or "missing after 3 retries">
**Codebase explored:** <files/packages inspected>
**Constraints identified:** <list>
**Dense context from memory:** <learning IDs relevant, or "none">
**Risks:** <list>
**Open questions:** none (must be inferrable, no user questions)
```

**Verify profile.** If `.yesmem/verify.yaml` is not in the worktree and no earlier loop committed one, run `yesmem verify-claims --init` in the worktree before planning, and commit the file it writes as its own commit: `git add -f .yesmem/verify.yaml` (a project may ignore `.yesmem/`). The merge carries the profile into the project, and every later loop inherits it. init writes nothing without a proof run of the test command; when it writes nothing, it prints what is missing, and that line belongs in the report.

Information gathering is automatic — never ask the user. Code questions → `search_code` (contents AND call sites: `search_code("<symbol>(")` returns file, line and the enclosing function), `search_code_index` (symbols by name), `get_code_context` (who calls it, what it calls, what types it references), `get_code_snippet` (source), `get_file_symbols` (line map of a file). Shell search (`rg`, `grep`) is BLOCKED on indexed files: the code-nav hook refuses it and names the MCP tool instead; if you genuinely need the shell, run the same command again (the retry passes) or call `dismiss_code_nav(session_id)`. API/docs → docs_search, hybrid_search. Errors → deep_search. Unknown concepts → WebFetch. NEVER guess — if you don't know, look it up (WebFetch or the indexed docs); infer only from code/context already in your turn.

### Phase 2: PLAN
```
update_agent_status(phase="Phase 2/7 PLAN")

### Phase 2: PLAN
**Status:** COMPLETE
**Plan stored via set_plan:** yes (with `items` — see "Structured plan items")
**Decisions resolved:** all — files, schema, API, blast radius
  - File selection: <which files, why not others>
  - Schema decisions: <data structures, interfaces, types>
  - API surface: <public interface, signature choices>
  - Blast radius: <what else could break, dependencies affected>
**Milestones:** N total (see criteria below)
  - Milestone 1: <name> (<step count> steps)
  - Milestone 2: <name> (<step count> steps)
**Files in scope:** <list>
**Test strategy:** <approach>
**Verification gates between milestones:** <compile/test/deploy checks>
```

**When `**Task type:** debug` — Depth-lock-Disziplin (Pflichtfeld, vom DONE-Guard erzwungen):**
Phase 2 braucht zusätzlich diese Zeile:
`**Depth-lock:** D1 home=<file::function>; D3 deepest=<file::function>; D4 repro=<RED am unpatched Stand VOR Implementierung>`
- **D3 bestimmt die Fix-Ebene:** der Fix muss genau auf der D3-Ebene liegen. Ein Fix auf einer höheren/niederen Ebene als D3 ist ein depth-miss → zurück zum Plan, D1/D3 überarbeiten.
- **D4 muss echt gelaufen sein**, nicht geplant: der RED-Output stammt vom unpatched Stand VOR der Implementierung. Ohne realen RED-Lauf ist der Depth-Lock nicht erfüllt.
- Kompakt halten — Single-Agent-Format, keine Multi-Agent-Overheads (der Autoprompt darf 5 Agenten spannen, yesloop nicht, #73504).

**Milestone criteria (agent decides):**
- ≤5 steps: single milestone
- 6-15 steps: MUST split into 2-3 milestones à 3-6 steps
- 16+ steps: MUST split into 4-6 milestones à 3-5 steps

Milestones are a recovery aid for context-collapse, not a bureaucracy. Small plan (≤5 steps) → one milestone suffices; this is a granularity choice, NOT a license to skip the phase structure or the milestone blocks.

**NO OVERENGINEERING.** If a step looks complicated, ask: "Can I solve this with a boolean flag and 10 lines?" Usually yes. Resist: per-agent configuration, persistence layers, abstract Strategy interfaces, retry mechanisms with backoff. Prefer: global constants, single struct + map, simple if/return.

### Ledger Plan-Schema (set_plan convention — feeds the Live-Ledger)
Shape the stored plan (set_plan) so the live-ledger subscriber (`ledger_nudge`) can render it as a compact, immediately readable system-state block. A plan following this schema gives the orchestrator and the next self a shared proof-ledger, not a wish-list.

**Schema keys (Goal/Core/Verified/Open/Next):**
- **Goal:** the one aim of this session, 1-2 lines
- **Core:** fixed decisions / constraints that are NO LONGER up for debate
- **Verified:** what is already proven — every item carries a proof line:
  - `<claim>` — `proof: <command> → <result>` (e.g. `proof: go test ./internal/... → exit 0, 18/18`)
- **Open:** open questions / alternatives still to be decided
- **Next:** the immediate next step

**Proof-line requirement:** only an item with a `proof:` line counts as Verified. A claim without a proof pointer belongs in **Open**, not **Verified**. The ledger is an evidence ledger — claims must cite the command and its result.

### Structured plan items (REQUIRED — the sidebar draws nothing without them)

`set_plan` and `update_plan` take an `items` array beside the free text: `[{content, status, reason, depth}]` with
`status` from `pending | in_progress | completed | cancelled` and `depth` 0, 1 or 2 (three levels is the maximum —
4+ is clamped to 2). **Always pass `items`.** A plan set as free text alone stores no outline: the ledger still shows
it, but the opencode sidebar renders no plan block at all, because that block reads only `plan.items` from the
per-thread status file. It looks like a broken plugin and is not one — this has cost a full diagnostic session
already.

Mapping for this skill:
- **`content`** — one step, one line, no numbering and no depth markers; both come from the array.
- **`status`** — `completed` as soon as a step is proven, `in_progress` for exactly the step you are on (one at
  most), `cancelled` with a `reason` when a step is dropped. Keeping this current is the checkpoint discipline
  below, not a separate chore.
- **`reason`** — the `proof:` line for a verified step (`proof: <command> → <result>`), or the reason a step was
  cancelled. This is where the Ledger Plan-Schema's evidence requirement lands in the structured form.
- **`depth: 1`** — sub-steps of the step above; **`depth: 2`** — the detail under a sub-step (how it is done, which
  command, which file). Three levels, no more: it is clamped at 2.

The derived text is what the ledger and the next self read, so the Goal/Core/Verified discipline above still
applies — it moves into the items instead of a prose block:

```
set_plan(items=[
  {content: "Goal: <one line>", status: "in_progress"},
  {content: "<verified step>", status: "completed", reason: "proof: <cmd> → <result>"},
  {content: "<open step>", status: "pending"},
  {content: "<sub-step of the open step>", status: "pending", depth: 1},
])
```

`update_plan(items=[...])` replaces the outline wholesale (use it at a pivot); `update_plan(completed=["<substring>"])`
marks steps done in place (use it at a milestone). Never let the outline drift more than one milestone behind the
work — a stale outline is worse than none, because the orchestrator reads it as state.

### Ticking off is an action, not a conclusion (REQUIRED after every step)

The moment a step is proven — the command exited 0, the test went green, the file carries the change — its item flips
to `completed` with the proof in `reason`. Not at the end of the phase, not when the next step starts, not in a batch
after the milestone. One step proven, one `update_plan` call:

```
update_plan(completed=["<substring of the step>"])
```

This is the point of the plan: the orchestrator, the sidebar and the next self read the outline as **state**, so a
proven step that still shows `[ ]` is a false statement about the run — it makes the work look unfinished, invites a
second agent to redo it, and hides real progress from the human watching the sidebar. A plan that is only updated at
phase boundaries is decoration.

Rules that follow from it:
- **`in_progress` moves with you.** When a step starts, its item becomes `in_progress` and the previous one is already
  `completed`. Two `in_progress` items mean one of them is stale.
- **A step that turns out unnecessary is `cancelled` with the reason, never deleted.** Deleting it erases the decision.
- **Before Phase 7, the outline must show every step resolved** (`completed` or `cancelled`). An open item at FINISH is
  either a forgotten tick or a hidden failure — resolve it in the DONE report, do not leave it dangling.
- **`update_plan` failures are not ignorable.** If the call errors, the tick did not happen: retry once, and if it
  still fails, say so in the scratchpad block instead of silently continuing.

### Phase skeleton (the outline mirrors the seven phases — REQUIRED shape)

The top level of the outline is the seven-phase pipeline, in order; the work lives underneath the phase that is
running: milestones at `depth: 1`, and the steps or details of a milestone at `depth: 2`. The phase label must match
the `update_agent_status` phase string exactly, so the status line and the plan row never drift apart:

```
set_plan(items=[
  {content: "Phase 1/7 ANALYZE — <goal in one line>", status: "completed",
   reason: "proof: <files/packages inspected> → <finding>"},
  {content: "Phase 2/7 PLAN — <N milestones>, task type <debug|feature|chore|docs>", status: "completed",
   reason: "proof: <milestone split>; depth-lock D3 <file::function> when debug"},
  {content: "Phase 3/7 EXECUTE — milestone 1/3: <name>", status: "in_progress"},
  {content: "<step>", status: "pending", depth: 1},
  {content: "<how that step is done: file, command>", status: "pending", depth: 2},
  {content: "<step>", status: "pending", depth: 1},
  {content: "Phase 4/7 VERIFY", status: "pending"},
  {content: "Phase 5/7 REVIEW — 5.1 self, 5.2 cold, 5.3 consequence, 5.4 security", status: "pending"},
  {content: "Phase 6/7 DAEMON-VERIFY — yesmem verify-claims, verdict accepted", status: "pending"},
  {content: "Phase 7/7 FINISH — commit, push, handoff", status: "pending"},
])
```

Rules for the skeleton:
- **Exactly one phase is `in_progress`** — the one whose scratchpad block you are writing right now. Two open
  phases means the plan no longer answers "where am I", which is the only reason it exists.
- **A phase flips to `completed` in the same update as its scratchpad block**, with the phase's DONE criterion as
  `reason` (`proof: <command> → <result>`). `reason: "done"` is not a proof line and does not count.
- **Work nests phase → milestone → step**, at depths 0, 1 and 2. Advance the milestone label as work moves
  (`milestone 2/3: …`); do not nest phases under phases, and do not go past depth 2 — the renderer clamps it.
- **A phase the task genuinely skips stays in the outline as `cancelled` with the reason** (e.g. a docs-only change:
  `Phase 5/6 REVIEW`, `cancelled`, "no code delta — 5.4 security not applicable"). Never delete a phase row: the
  DONE-guard and the orchestrator read the shape, and a missing row reads as a missing phase.
- `update_plan(completed=["Phase 3/6"])` at a phase boundary is the cheap form; use `items=[...]` when the milestone
  split itself changes (Phase 2 replanned), because that is a different shape, not a status change.

### Phase 3: EXECUTE
```
update_agent_status(phase="Phase 3/7 EXECUTE")
# or with milestones:
update_agent_status(phase="Phase 3/7 EXECUTE (milestone 1/3)")

### Phase 3: EXECUTE
**Status:** IN PROGRESS
**Plan items:** N total

#### Milestone 1: <name>
**Status:** COMPLETE
**Commits:** <hash> <msg>, <hash> <msg>
**Steps completed:** N/N
**todowrite synced:** yes

#### Milestone 2: <name>
**Status:** IN PROGRESS
**Current step:** N/M
**Last commit:** <hash> <msg>

**Total progress:** M/N milestones, X/Y steps
**Uncommitted work:** none | <list with reason>
```

**Per-milestone discipline:**
- `update_plan(completed=["<step>"])` after **every** proven step, `update_plan(completed=["milestone M"])` when the
  milestone's last step lands — the matched items become `completed`, which is what the sidebar block and the ledger
  render as done. Ticking is per step; the milestone call only closes the group.
- `scratchpad_write` per milestone, not per step — controls bloat
- DRIFT CHECK before each milestone (not each step): re-read goal, compare scope

**Context-collapse recovery:** `get_plan()` → see current milestone → `update_agent_status` → read milestone block from scratchpad → continue.

**Commit cadence:** one commit per logical unit. Never leave uncommitted work across phase boundary without documenting why.

### Phase 4: VERIFY
```
update_agent_status(phase="Phase 4/7 VERIFY")

### Phase 4: VERIFY
**Status:** COMPLETE
**Tests run:** <command> → exit <code>, last 5 lines: <output>
**Regression baseline:** base=<origin/main hash> failures=<none|list>; head failures=<none|list>; diff=<none|new failures>
**RED proof:** <neuer Test RED vor Fix, GREEN nach Fix> | none — docs-only
**Lint/type-check:** <command> → <result>
**Build:** <command> → <binary mtime if applicable>
**Coverage gaps:** none | <list>
**VERIFY cycles used:** N/5
```

**Regression baseline (mandatory, deterministic):**
1. In Phase 2 (PLAN), note the merge-base: `git merge-base HEAD origin/main`
2. In Phase 4, create a temp checkout of origin/main INSIDE your worktree:
   `git worktree add .yesmem/tmp/base-checkout origin/main` (project-local, never /tmp)
3. Run the relevant test suite there (same commands as head), record pass/fail sets
4. Remove it after use: `git worktree remove .yesmem/tmp/base-checkout --force`
5. `diff=` lists ONLY test failures that are green on base but fail on head — each is a
   REGRESSION and must be fixed before REVIEW exit (or proven pre-existing on base)

If issues found → fix and re-verify (max 5 cycles, see CONVERGENCE GATE).

### Phase 5: REVIEW (Checklist: 5.1 Self + 5.2 Cold + 5.3 Consequence + 5.4 Security)

Phase 5 is a checklist of four separately checkable sub-phases, each reviewed with
its own task() subagent (5.1 by the agent itself). The daemon's DONE-guard verifies
every reported subagent session id against opencode's own session DB — the id is
the `ses_<id>` the task tool returns. Old lines `task() dispatched:` and
`consequence dispatched:` are retired (hard cut): blocks using them fail the gate.
`blocked` is NOT a valid value anywhere in Phase 5. If task() is truly unavailable,
`send_to` the orchestrator `BLOCKED Phase 5.x: <reason>` and stay in Phase 5 — never
claim DONE.

| # | Sub-phase | Execution | Mandatory line in Phase 5 block | Gate check |
|---|---|---|---|---|
| 5.1 | Self-Review | agent itself | `self-review: done` | format |
| 5.2 | Cold Code Review | task(reviewer or silent-bob) | `cold-review: ses_<id>` | row exists in opencode.db, parent_id == agent session, agent in {reviewer, silent-bob} |
| 5.3 | Consequence & Intent | task(general or reviewer) | `consequence: ses_<id>` | like 5.2, agent in {general, reviewer}, id distinct from 5.2 |
| 5.4 | Security Review | task(reviewer); the subagent loads the security-review skill | `security: ses_<id>` | like 5.2, agent in {reviewer}, id distinct from 5.2 and 5.3 |

```
update_agent_status(phase="Phase 5/7 REVIEW")

### Phase 5: REVIEW
**Status:** COMPLETE

**Sub-phase 5.1: Self-Review** (agent itself)
self-review: done
- Strengths: <list with file:line refs>
- Issues: Critical (N) / Important (N) / Minor (N), with file:line + reasoning
- Recommendations: <list>
- Assessment: Yes | With fixes | No

**Sub-phase 5.2: Cold Code Review**
cold-review: ses_<id>
**Stufe:** <1|2|3> — <one-line rationale per risk-based-code-review>
**Modules:** <active modules A–R> | N/A: <excluded, reason>
**Findings:** <list or "none">
**Merged assessment:** Yes | With fixes | No — <reasoning>
**Fix commits:** <hashes or "none needed">

**Sub-phase 5.3: Consequence & Intent**
consequence: ses_<id>
**Findings:** <intended-vs-actual list or "none">

**Sub-phase 5.4: Security Review**
security: ses_<id>
**Findings:** <list with NEW/MODIFIED distinction, or "none — diff reviewed, no findings">

**REVIEW→VERIFY cycles used:** N/5
```

**Stage 1 — Self-Review (Sub-phase 5.1)** (catches mechanical issues): Get full delta `git diff origin/main` + `git log origin/main..HEAD --oneline`. Checklist: Plan alignment, Code quality, Architecture, Testing, Production readiness, **security (dispatched in 5.4)**, Second-order effects ("if this ships, what happens next? trace 2+ levels"), Assumption surfacing ("what must be TRUE for this to work?").

**Sub-phase 5.4 doctrine — Security (MANDATORY):** The 5.4 subagent (never the loop agent itself) INVOKES the `security-review` skill via the Skill tool when the diff contains ANY executable code (`.go/.py/.js/.ts/.tsx/.jsx/.java/.rs/.php/.rb`). Apply the NEW/MODIFIED doctrine: for NEW code (diff-added), fix ALL findings HIGH/MEDIUM/LOW; for MODIFIED code (existing function touched), fix issues the diff introduces and document pre-existing issues as OUT OF SCOPE with a Learning reference. Even for docs/config-only diffs the 5.4 subagent is dispatched and verifies docs-only — no self-issued `security:` line. Every finding line carries either a fix-commit-hash, a "not exploitable because X" note, or an OUT-OF-SCOPE annotation.

**Sub-phase 5.2 — Cold Code Review via task()** (fresh eyes, catches architectural blind spots):
- Dispatch focused task()-subagent with code-reviewer template (superpowers requesting-code-review)
- Input: `git diff origin/main` + Phase 2 plan only (no exploration, for speed)
- **RISK CLASSIFICATION (MANDATORY):** The reviewer subagent MUST load the `risk-based-code-review` skill (platform skill tool, `~/.claude/skills/risk-based-code-review/SKILL.md`, or `<repo-root>/skills/bundled-skills/risk-based-code-review/SKILL.md`) BEFORE writing findings, then: (1) classify the change as Stufe 1-3 per its severity ladder — impact over diff size, few lines never lower the Stufe, (2) select the active modules per its trigger table (only fachlich unberührte modules are N/A), (3) report in the skill's Abnahmeformat. The `**Stufe:**` and `**Modules:**` lines in the 5.2 block are part of the required evidence. The Stufe steers review DEPTH, not the existence of any sub-review — 5.1-5.4 all run regardless.
- **MANDATORY — Cold Review is NOT optional.** Phase 5 is only complete when 5.2 has actually run. Empirically verified (2026-06-20, Learning #75412): agents skip review subagents silently if framed as additive. Required evidence: `cold-review: ses_<id>` — the daemon checks the row in opencode.db.
- **ALL subagent findings must be PROVEN ON CODE.** Every finding requires code-anchored evidence: file line refs, a call path (graph_traverse output), or a reproducing command/failing test. A finding without a code anchor is INVALID and must be reworked by the subagent.
- **If task() truly fails:** send_to orchestrator `BLOCKED Phase 5.2: <error>` and stay in Phase 5. Orchestrator spawns separate TUI reviewer as fallback.

**Sub-phase 5.3 — Consequence & Intent Check via task()** (fresh subagent, MANDATORY):
- Dispatch a focused task()-subagent (separate session from 5.2's reviewer) with:
  `git diff origin/main`, the Phase 2 **Decisions resolved** block, and this mandate:
  (a) trace second-order consequences: callers, exposed contracts, defaults, schema,
  dependent behavior — what breaks or changes that the diff does not show?
  (b) intent fidelity: does the landed behavior match what Phase 2 decided?
  Anything drifting into an unwanted direction?
- **Scope via module trigger table:** the 5.3 subagent loads `risk-based-code-review` and walks its trigger table for second-order suspects — state (E), transactions (F), tests with DB/time/random (G), schema/migrations (L), deploy/config (M) are the usual misses.
- **ALL 5.3 findings must be PROVEN ON CODE** — same evidence bar as 5.2:
  file line refs, call path, or a reproducing command/failing test. Unanchored
  findings are INVALID and must be reworked by the subagent.
- Evidence in the Phase 5 block: `consequence: ses_<id>`, `**Findings:**`
- **If task() truly fails:** send_to orchestrator `BLOCKED Phase 5.3: <error>` and stay in Phase 5 — never claim DONE.

**Sub-phase 5.4 — Security Review via task()** (fresh reviewer subagent, MANDATORY):
- Dispatch a focused task(reviewer)-subagent (distinct session from 5.2 and 5.3) that loads the `security-review` skill and reviews `git diff origin/main`.
- **Scope check:** the 5.4 subagent also loads `risk-based-code-review` and verifies its trigger table against the diff — security-relevant modules (Auth/Inputs H, Network I, Secrets J, Destructive K, Schema L, Deploy M) may not be silently dropped. The security-review skill remains the primary doctrine for finding analysis.
- Evidence in the Phase 5 block: `security: ses_<id>`, `**Findings:**` with NEW/MODIFIED distinction per the doctrine above.
- The agent may NOT skip by writing findings itself — the gate verifies a real reviewer session.

**Double-loop exit criteria (REVIEW→VERIFY):** the loop exits only when ALL of
these hold: Sub-phases 5.1-5.4 complete with daemon-verified evidence, and Phase 4
Regression baseline shows `diff=none` (or all new failures fixed + re-run). If a
check that must be clean stays red after 2 fix cycles → REVIEW BLOCKED
escalation.

**Merging & Resolution:**
- Merge findings from both stages (deduplicate, preserve highest severity)
- **ALL findings MUST be fixed autonomously** -- no user feedback, no escalation for fixable issues
- Critical, Important, medium, mittel → MUST fix immediately
- Minor also MUST fix immediately
- After fixes → loop back to Phase 4 VERIFY, then re-review (max 5 REVIEW→VERIFY cycles total)
- Assessment "No" (unfixable) → STOP, escalate via send_to: "REVIEW BLOCKED: <reasons>"
- **Never leave fixable issues for the user** -- review is a work phase, not advisory
- After `git show`/`git diff` always `Read` the current file — diffs show what changed, not what's there NOW

### Behauptungs-Inventar (proof-poverty check for the DONE-Report)
Every coverage / success claim in the DONE-Report must carry a proof pointer (command + result) on the same line; a claim without one is marked UNVERIFIED explicitly. This complements the Phase 4 `**Tests run:**` line — that records the overall run, here each individual claim is bound to its own evidence.

**Format per claim:**
- `<claim>` → `proof: <command> → <result>` (e.g. `proof: bun test plugins/opencode-yesmem-v2 → exit 0, all tests pass`)
- not verifiable now → `<claim>` → `UNVERIFIED: <why not verified>`

Corollary from the ledgere schema: don't claim success you cannot back. Move unproven items to **Open** in the plan instead of asserting them in **Verified**.

### Phase 6: DAEMON-VERIFY
```
update_agent_status(phase="Phase 6/7 DAEMON-VERIFY")

### Phase 6: DAEMON-VERIFY
**Status:** COMPLETE
**Vorab-Prüfung:** yesmem verify-claims → exit 0 (accepted), Urteil liegt für diesen Bericht
**Findings:** none | <was der Lauf gesagt hat und was du dagegen getan hast>
```

- **Die Prüfung ist deine Phase, nicht die des Daemons.** Du läufst den Befehl aus
  "Der Vorab-Lauf", liest das Urteil und schreibst es hierher. Exit 0 heißt `accepted`
  und öffnet Phase 7; Exit 1 heißt `blocked`, und der Befund entscheidet, wohin du
  zurückgehst: `refuted` oder `defect` in die Arbeit (Phase 3 oder 4), `environment`,
  `limit` oder `error` an den Orchestrator.
- **Kein Ermessen, kein Modell.** Der Befehl, der Exit-Code und der Befund sind die
  ganze Entscheidung. Ein Feld ohne den Wortlaut `accepted` behauptet nichts: der
  DONE-Guard liest Phase 6 als nicht belegt.

### Phase 7: FINISH
```
update_agent_status(phase="Phase 7/7 FINISH")

### Phase 7: FINISH
**Status:** COMPLETE
**Deploy required:** yes (binary/skill/cap/code change) | no (docs/config only)
**Deploy executed:** yes — <evidence: make build output, md5sum diff>
  | no — <REASON. Default: BLOCKED, escalate to orchestrator>
**Branch pushed:** yes — <git log origin/<branch> -1 output> | no — <reason>
**PR created:** yes — <url> | no — left for orchestrator
**Merged to main:** NO — agents do not merge (merge is orchestrator-only)
**Worktree:** kept (pending merge confirmation)
**send_to orchestrator:** yes — <timestamp>
**send_to payload:** `[DONE] [DEPLOY: yes|no|skipped|failed] [COMMIT: <hash>] [BRANCH: <name>] [MERGE: no|pending-PR|blocked-PR] <summary>`
**Prüfbericht:** <section>-verify geschrieben (Commit, Branch, Testlauf, Evidenz)
**set_plan complete:** yes
```

- **Prüfbericht vor dem DONE:** Der Abschnitt `<section>-verify` steht, bevor du
  `send_to` absetzt. Muster oben unter "Der Prüfbericht".

- **Vorab-Prüfung vor dem DONE:** Nach dem letzten Commit und dem Push läufst du
  `yesmem verify-claims` mit dem Befehl aus "Der Vorab-Lauf" und wartest auf Exit 0. Erst
  danach setzt du das DONE ab. Ein DONE ohne frisches angenommenes Urteil blockt der
  Daemon als Defekt, ohne selbst zu prüfen.

- **send_to marker convention (mandatory in Phase 7):** Orchestrator parses these markers — keep them leading in the `send_to` payload, summary free-text after.
  - `DEPLOY`: `yes` (deployed), `no` (docs-only, not required), `skipped` (required but skipped per instruction/contract), `failed` (attempted, failed — escalate)
  - `MERGE`: `no` (default — agents never merge), `pending-PR` (PR created, awaits orchestrator), `blocked-PR` (PR has blockers)

- **Merge policy: Agents NEVER merge to main themselves.**
  - Default: push the worktree branch to origin, create a PR (or leave for the orchestrator to create one), then `send_to caller_session: "[DONE] [DEPLOY: <status>] [COMMIT: <hash>] [BRANCH: <name>] [MERGE: pending-PR] <summary>"`. Markers are parsed by orchestrator — keep leading, free-text summary after.
  - `--merge` flag on the /yesloop command means the **orchestrator** requested auto-merge after PR checks pass. Even then the agent only pushes the branch and reports the PR URL — it does not run `git merge main` / `git checkout main && git merge <branch>` itself. The orchestrator handles the merge.
  - Running `git checkout main` + `git merge <branch>` inside the agent's Phase 7 is ALWAYS wrong, regardless of flag, confidence, or test results. The agent's git scope is its worktree branch. Main is the orchestrator's scope.
  - Rationale: the orchestrator owns release decisions, rollback, commit-message control, and PR review. A self-merge bypasses all four.
- **Do NOT delete worktree** — keep until the orchestrator confirms the merge is done.

## Mini Example (abbreviated — field names and headers are guard-compliant, copy them EXACTLY)

```
### Phase 1: ANALYZE
**Status:** COMPLETE
**Goal understood:** Add config.yaml read/write to set_config/get_config
**Session id:** ses_abc123 (from whoami())
**Session title set:** [agent-20261015-05] config read/write
**Codebase explored:** internal/daemon/handler_state.go, internal/config/config.go
**Constraints identified:** no breaking MCP API, proxy_state overrides must still work
**Risks:** concurrent writes, type coercion
**Open questions:** none

### Phase 2: PLAN
**Status:** COMPLETE
**Plan stored via set_plan:** yes (with `items` — see "Structured plan items")
**Decisions resolved:** all
  - File selection: config.go, handler_state.go, cmd_config.go, main.go
  - Schema decisions: yaml config struct with Get/Set methods
  - API surface: Save/GetValue/SetValue on Config type
  - Blast radius: proxy_state override precedence, concurrent write
**Milestones:** 2 total
  - Milestone 1: config.go Save/GetValue + tests (4 steps)
  - Milestone 2: handler rewiring + CLI (3 steps)
**Files in scope:** internal/config/config.go, internal/daemon/handler_state.go, cmd_config.go, main.go
**Test strategy:** table-driven for coercion, integration for handler
**Verification gates:** go test ./internal/config/... between milestones

### Phase 3: EXECUTE
**Status:** COMPLETE
**Plan items:** 7 total

#### Milestone 1: config.go Save/GetValue + tests
**Status:** COMPLETE
**Commits:** a9ba499 feat(config): add Save/GetValue/SetValue + 11 tests
**Steps completed:** 4/4
**todowrite synced:** yes

#### Milestone 2: handler rewiring + CLI
**Status:** COMPLETE
**Commits:** a9ba499 feat(config): wire set/get_config to yaml, add CLI
**Steps completed:** 3/3

**Total progress:** 2/2 milestones, 7/7 steps
**Uncommitted work:** none

### Phase 4: VERIFY
**Status:** COMPLETE
**Tests run:** go test ./internal/config/... ./internal/daemon/... → exit 0
**Regression baseline:** base=9e8aa888 failures=none; head failures=none; diff=none
**Build:** go build ./... → success
**VERIFY cycles used:** 1/5

### Phase 5: REVIEW
**Status:** COMPLETE
**Stage 1: Self-Review**
- Strengths: clean API, good test coverage
- Issues: Important (2) / Minor (4), with file:line
- Assessment: With fixes
self-review: done

**Sub-phase 5.2: Cold Code Review**
cold-review: ses_235
**Stufe:** 3 — config write path + persistence (behavior and state risk)
**Modules:** A, B, C, D, E, F, H, Q, R | N/A: G, I, J, K, L, M, N, O, P — fachlich unberührt
**Findings:** same 2 Important + 4 Minor
**Merged assessment:** With fixes — ALL 6 findings (2 Important + 4 Minor) fixed before merge
**Fix commits:** cd2ba04 fix(config): proxy_state dual-write, type coercion + all 4 minors

**Sub-phase 5.3: Consequence & Intent**
consequence: ses_236
**Findings:** none

**Sub-phase 5.4: Security Review**
security: ses_237
**Findings:** none — diff reviewed, no HIGH/MEDIUM/LOW findings

**REVIEW→VERIFY cycles used:** 2/5

### Phase 6: DAEMON-VERIFY
**Status:** COMPLETE
**Vorab-Prüfung:** yesmem verify-claims → exit 0 (accepted)
**Findings:** none

### Phase 7: FINISH
**Status:** COMPLETE
**Deploy required:** yes (Go code change)
**Deploy executed:** yes — make build → binary mtime 2026-06-20T00:40
**PR created:** no — merged directly per orchestrator (merge commit 27e35ca)
**send_to orchestrator:** yes — 2026-06-20T00:47
**send_to payload:** `[DONE] [DEPLOY: yes] [COMMIT: 27e35ca] [BRANCH: yesloop/example-task] [MERGE: no] Add config.yaml read/write`
**set_plan complete:** yes
```

## Test Charter (when a test counts)

TDD is mandatory: the test exists before the fix. These rules decide whether a test counts as
evidence. Group A is machine-checkable, B is the cold reviewer's criterion, C is proportionality.

**A — machine-checkable**

- **A1 RED first, against the base.** Run the new test on `origin/main` without the fix and keep the
  failing output. No RED, no test. A test written after a review finding is allowed, but then revert
  the fix locally and show the test turns red.
- **A2 Deterministic.** No network (httptest is fine), no real `time.Sleep`, no real clock without
  injection, no `rand` without a seed.
- **A3 Hermetic.** HOME and XDG point at a temp dir, fixtures live in the repo, and the existing
  per-package harness is used instead of a second one.
- **A4 Skips are named.** Every `t.Skip` names the missing prerequisite. Never skip silently.
- **A5 The race gate stays as it is.** A new slow test in `proxy`, `storage`, `daemon` or `hooks`
  needs its RACE_RUN classification, not a loosened gate.
- **A6 Deleting or moving an existing test** needs a reason in the report and a pointer to where the
  coverage lives now.

**B — the cold reviewer's criterion**

- **B1 Name the defect each new test catches.** If no defect can be named, the test is invalid.
- **B2 Behaviour through the public seam**, not internal fields or call counters.
- **B3 One behaviour per test, the name states the rule.**
- **B4 Every assertion names expected and got.**

**C — proportionality (no overengineering)**

- **C1 No tests for cases that cannot occur.** If the realistic use path cannot be named, it is an
  overengineering test and it goes. Same rule as the no-esoteric-hardening pin, applied to tests.
- **C2 No tests for language or framework guarantees** (nil handling, SQL driver, JSON encoding,
  standard library). Trust internals.
- **C3 No behaviour tested twice on two layers.** Once, at the seam where it is decided.
- **C4 Table-driven over N single functions** when the cases exercise the same behaviour with
  different values.
- **C5 Size in the order of the production code you touch.** Not four times, and never a hundred
  thousand lines of tests for a thousand lines of code. Few tests at boundaries and error classes
  beat many tests on the obvious.

Work in progress that violates A or B gets corrected before the first commit.

## Guardrails (Prevent Agent Drift)

### DRIFT CHECK — Before each MILESTONE (not each step)

1. Re-read the original goal from `scratchpad_read(project, section)` or `get_plan()`
2. Compare current state against original scope:
   - **Still on track?** → proceed
   - **Minor drift?** → MANDATORY fix immediately — correct course NOW, document what drifted and how you corrected it in scratchpad, then continue
   - **Major divergence?** → **STOP.** scratchpad_write("⚠️ DRIFT: <what changed>") + send_to orchestrator: "DRIFT: <details>. Continue or abort?"

What counts as drift:
- Touching files outside the original scope (scope creep)
- Solving a different problem than the one given
- Adding features not requested ("while I'm here, I'll also...")
- Changing architecture without justification

### CONVERGENCE GATE — When progress stalls

| Pattern | Detection | Action |
|---|---|---|
| Edit-Test loop | Same test fails 3+ times with different fixes | **STOP.** scratchpad_write + send_to: "STUCK: <test> fails after 3 attempts." |
| Rewrite loop | Reverting your own changes and trying again | **STOP after 2 rewrites.** |
| Search loop | Searching for the same information repeatedly | Cache in scratchpad. After 3 empty searches, document as unknown. |
| Fix-then-break | Each fix breaks something else | **STOP after 2 cascades.** Code too coupled. |

**Hard limit:** No forward progress (no completed steps in todowrite) for 5 turns → escalate and stop.

### PROOF BURDEN (BEWEISLAST) for scratchpad claims

Before writing "DONE", "completed", "verified" to scratchpad, confirm the artifact matches the claim — not just that the command exited 0.

| Claim | Required independent check |
|---|---|
| "build succeeded" | `go build ./...` exit + binary exists with new mtime |
| "tests green" | show last 5 lines of test output, not just "PASS" |
| "deployed" | curl/GET against running service shows new version, OR md5sum live vs bundled exit 0 |
| "bundle rolled out" | `diff source installed && echo identical` (exit 0) |
| "commit pushed" | `git log origin/<branch> --oneline -1` shows the hash |
| "PR created" | `gh pr view <url>` exit 0 |
| "cold review ran" | task()-subagent ID present in scratchpad |
| "consequence finding" (Stage 3) | file line reference, call path, or repro evidence — unanchored findings are invalid |
| "file edited" | `Read` the file (not just `git show`) |
| "new test tests something" | **RED proof:** RED-Output am unpatched Stand (vor dem Fix), nicht nur geplant — bei docs-only explizit `none — docs-only` |
| "DONE-guard passed" | `ValidatePhaseBlocks(content).Compliant == true` (auto-checked every 30s) |

**Rule:** if verification was skipped, say so explicitly: `<claim> (not independently verified)`. If run, paste observable evidence (one line is enough).

**Apply only to consequential claims:** build, deploy, bundle rollout, merge, migrate, config change, destructive ops. Routine todowrite step-completion does not need this.

## State Recovery (every wake-up)

Your state lives in yesmem, not context. On every wake-up:
1. `get_plan()` — restore active goal and progress
2. `scratchpad_read(project, section)` — restore detailed context
3. **`check_messages`** — poll for orchestrator messages (OpenCode has no push, DB-poll is the only reliable path)
4. Reconstruct current phase + milestone
5. `update_agent_status(phase="Phase N/7 NAME (milestone M/K)")` — re-assert current state
6. Continue where you left off

## TUI Agent Mode (You were spawned in a worktree)

You are a **subagent** — spawned via yesmem_spawn_agent. You run in an isolated git worktree.

### ⛔ WORKTREE GUARDRAIL — Execute BEFORE any file modification

On startup, before ANY other action:
```
1. pwd                           → must match worktree path from scratchpad
2. git rev-parse --show-toplevel → must be the worktree, NOT main repo
3. git branch --show-current     → must be yesloop/<task-slug>, NOT main
4. git status --short            → must be clean
5. Write-Probe                   → mkdir -p .yesmem/tmp && printf probe > .yesmem/tmp/write-probe, dann read-back verifizieren (Inhalt == "probe")
```

**IF ANY CHECK FAILS:** STOP. Do not touch files. `scratchpad_write + send_to orchestrator: "⛔ WORKTREE GUARD FAILED: pwd=<actual>, branch=<actual>. Expected <expected>."` Wait for orchestrator.

**IF ALL PASS:** Identify via `whoami`, `scratchpad_read(project, section)`, `get_plan()`, `update_agent_status(phase="Phase 1/7 ANALYZE")`, then begin pipeline.

Write-Probe-Regel: die Probe-Datei wird NICHT gelöscht (kein rm) — sie bleibt im Worktree liegen und wird mit dem Worktree-Cleanup entfernt.

### Completion — MUST do all three:
1. `scratchpad_write(content="✅ DONE: <summary>. PR: <url>")` — final write with all 7 phase blocks COMPLETE
2. `send_to(target=<caller_session>, content="[DONE] [DEPLOY: <status>] [COMMIT: <hash>] [BRANCH: <name>] [MERGE: <status>] <summary>. PR: <url>")` — markers leading, parsed by orchestrator; summary free-text after
3. `set_plan(...)` mark as completed

**NOTE:** `send_to` to an opencode session is pushed into that session's inbox through the opencode service API, so an idle target reacts without polling. If no service is reachable, the message stays stored for the target's next request. A reply that reports `unconfirmed` means the message was claimed but the push could not be confirmed — the target may or may not have it, so do not resend it; its only trace is the daemon log line `[send_to] ... unconfirmed`. Scratchpad is primary completion channel.

**Periodic polling:** Every 5 turns, `check_messages` for new instructions or cancellation.

## Decision Gates (Autonomous)

| Situation | Action |
|---|---|
| Simple fix, clear solution | Execute directly |
| 2-3 approaches, unclear best | Pick one, document rationale, proceed |
| Need to change scope | MANDATORY fix immediately — stop the drift, correct course NOW, document it. Escalate only if the correction itself is impossible or blocking |
| Tests fail repeatedly | Debug max 3 cycles → CONVERGENCE GATE |
| Irreversible action (force-push, drop table) | Pause, request confirmation |
| 3 consecutive ticks with nothing to do | End the loop, report idle |
| Scope creep detected | DRIFT CHECK, correct or escalate |
| Same approach failed 3x | CONVERGENCE GATE — stop and escalate |

## Communication

- **inline mode:** short progress updates to user between phases
- **TUI agent mode:** scratchpad_write for detailed state, send_to for completion
- **scheduled mode:** scratchpad_write for state, broadcast on completion
- Never ask "should I continue?" — just continue unless blocked

## Automated Idle-Trigger

Layer 2 of the yesloop guarantee: **Idle Detection** for yesloop agents with a live PID but inactive SSE stream for >10 minutes. The daemon heartbeat runs an idle state machine (`checkYesloopIdle` in `internal/daemon/yesloop_idle.go`) that pushes agents through a 4-state escalation protocol.

### State Machine

| State | Condition | Action |
|---|---|---|
| 0 WORKING | stream_active=true or idle <10min | No action |
| 1 SELF_CHECK | stream_active=false for 10min, PID alive | Relay 1: mid-pipeline text, or the seven-phase question when the agent claims DONE or reports Phase 7 (see below) |
| 2 REMARK_REQUEST | PROVEN marker found | Relay 2: ask agent to mark all 7 phases as done |
| 3 COMMIT_REQUEST | 7 phases marked done | Relay 3: ask agent to commit and send_to orchestrator |
| 4 DONE | send_to evidence in scratchpad | Terminal — notify orchestrator |

### Relay Messages

Relay 1 has two wordings: the seven-phase question only to an agent that claims DONE or reports Phase 7, the mid-pipeline text to every other agent.

The daemon sends these exact messages (metachar-free, no markdown, no backticks):

1. **State 0 → 1, seven-phase wording** (the agent claims DONE or reports Phase 7): `Have you completed all 7 phases? If not do it now. For each phase prove you have done each. IF you have proven, mark each phase as done: put [x] at the end of the phase header line, or on its own line inside the phase block with just the marker and optionally the phase number or phase name, for example [x] Phase 1 or [x] ANALYZE. The Status line from the phase template with the value COMPLETE also counts. MANDATORY: Make sure that you have also done phase 5 with all code reviews including Sub-phase 5.2 cold review, 5.3 consequence check and 5.4 security review via task subagents. REVIEW BLOCKED without subagent trace is not acceptable. Mandatory: only mark as PROVEN if it IS proven.`
   **State 0 → 1, mid-pipeline wording** (every other agent): `Your session showed no activity for a while. Continue the phase you are working on: do the remaining work, then write your progress and evidence into the scratchpad. Do not mark a phase as done that is not done.`
2. **State 1 → 2:** `Mark all 7 phases as done in scratchpad: put [x] at the end of the phase header line, or on a dedicated marker line inside the phase block. The Status line from the phase template with the value COMPLETE also counts.`
3. **State 2 → 3:** `If 1 through 7 are ok commit and send_to to caller.`

### Scratchpad Markers

The idle checker reads the agent's scratchpad section to detect progress:

- **PROVEN** (case-insensitive): signals that the agent has verified all 7 phases are complete → triggers State 1 → 2
- **Per-phase done marker**: since YM-149 the checker accepts the same three forms the relay names — `[x]` at the end of the phase header line (`### Phase 1: ANALYZE [x]`), a dedicated marker line inside the phase block (`[x]`, `- [x] Phase 1`, `- [x] ANALYZE`) or `**Status:** COMPLETE` in the block. A checklist item with its own text inside the block (`- [x] fixed the parser`) is NOT a phase mark.
- **7x `**Status:** COMPLETE`**: when all 7 phase blocks are completed (detected by `CountCompletedPhases()`) → triggers State 2 → 3
- **send_to orchestrator:** evidence in scratchpad → triggers State 3 → 4

### Re-fire and Escalation

- Each state re-fires the relay after 90 seconds if no progress
- After 2 re-fires (3 total attempts) the guard stops relaying: it leaves a note on the row and sends one informational notice to the orchestrator
- The agent keeps running. A guard nudges, it does not stop the agent; only the DONE verdict pauses

### What triggers idle detection

- Section prefix: `yesloop-`
- Agent status: `running`
- PID: alive (`isPIDAlive()` returns true)
- Stream state: `stream_active` false for >10 consecutive minutes

## Automated Done-Verify (v3.2)

Layer 3 of the yesloop guarantee: **Done-Verify** for yesloop agents that emit a DONE-claim (Phase 7 header, `send_to ... DONE`, or `Phase 7/7` marker). The heartbeat runs a 4-state verify machine (`checkYesloopDoneVerify` in `internal/daemon/yesloop_done_verify.go`, called every 5 minutes) that asks the agent to **BEWEISEN** all 7 phases ran — especially Phase 5 Cold Review — before marking DONE.

### State Machine

| State | Condition | Action |
|---|---|---|
| 0 NOT_DONE | No DONE-claim in scratchpad | Not tracked, cron inactive |
| 1 VERIFY_REQUESTED | DONE-claim detected | Relay: "Hast du wirklich alle Phasen abgehakt..." |
| 2 DONE_VERIFIED | BEWEISEN marker + send_to orchestrator + 7 phases COMPLETE | Terminal — cron stops for this agent |
| 3 NUDGED | 3 re-fires without progress | Note on the row + one notice to the orchestrator, the agent keeps running |

### DONE-Claim Indicators (any one triggers)

- `### Phase 7:` header (case-insensitive)
- `send_to ... DONE` pattern
- `Phase 7/7` marker
- `DONE: ` prefix in content

### Relay Message (metachar-free)

`Hast du wirklich alle Phasen abgehakt. Vor allem Phase 5 Code Review inklusive Security Review via security-review Skill. Im Phase 5 Block muessen die Zeilen self-review: done, cold-review: ses ID, consequence: ses ID und security: ses ID stehen — je ein eigener task subagent, der Daemon prueft die IDs gegen die opencode DB. Phase 4 braucht eine Regression baseline. Falls nicht bitte durchfuehren und BEWEISEN dass alle Phasen durch sind und DANN Phase 7 Finish durchfuehren mit commit und send_to. KEIN auto-deploy.`

### Scratchpad Markers

The done-verify checker reads the agent's scratchpad section to detect progress:

- **DONE-claim** (any indicator above): triggers NOT_DONE → VERIFY_REQUESTED
- **BEWEISEN** (case-insensitive) + `send_to orchestrator:` line + 7 phases COMPLETE: triggers VERIFY_REQUESTED → DONE_VERIFIED
- Missing evidence after 3 re-fires: triggers VERIFY_REQUESTED → NUDGED

### Re-fire and Escalation

- Re-fires the relay every 5 minutes if no progress
- After 3 re-fires (total 4 relay attempts) the layer reports once: a note on the row plus one notice to the orchestrator
- The agent keeps running: this layer does not stop it, the seven-phase DONE gate decides a DONE

### What triggers done-verify

- Section prefix: `yesloop-`
- Agent status: `running`
- Scratchpad content matches any DONE-claim indicator
- Agent not already in DONE_VERIFIED or NUDGED state

### Boundary with other layers

- **Idle Detection (Layer 2)** triggers on stream inactivity; Done-Verify (Layer 3) triggers on DONE-claims. They are complementary and can both fire for the same agent.
- **DONE-Guard (Layer 3 regex validator)** freezes agents that claim DONE with malformed phase blocks. Done-Verify catches the case where phase blocks look valid but Phase 5 Cold Review was silently skipped — the agent must actively BEWEISEN it ran Sub-phase 5.2 (verified subagent session id).

## Automated Skill-Reload Check (Layer 4)

Layer 4 of the yesloop guarantee: **Context-Compaction-Re-Read** gegen Skill-Verlust. Lange yesloop-Läufe können den Skill-Text verlieren, wenn der Kontext kompaktiert wird — der Agent würde dann ohne Pipeline-Vorgaben weitermachen. Der Daemon (`checkYesloopSkillCheck` in `internal/daemon/yesloop_skillcheck.go`, 30s-Heartbeat) beobachtet das kumulierte Arbeitsvolumen je Agent (OutputTokens-Delta seit Reset-Basis ≥ 100.000, Fallback TurnsUsed-Delta ≥ 50) und erinnert per Relay daran, den Skill neu zu laden.

### State Machine

| State | Condition | Action |
|---|---|---|
| 0 TRACKING | Arbeitsvolumen unter Schwelle seit Baseline | No action |
| 1 REMIND | OutputTokens-Delta ≥ 100k ODER Turns-Delta ≥ 50 seit Baseline | Relay: Skill-Ladezustand prüfen, bei Bedarf neu laden, per Marker bestätigen |
| 2 CONFIRMED | **SKILL-RELOAD-<N>:** yes der aktuellen Runde ODER 3 Re-Fires ohne Marker | Baseline zurücksetzen → TRACKING; ohne Marker zusätzlich genau eine Info an den Orchestrator, keine Pause |

### Context-Compaction-Re-Read (Agentenseite — MANDATORY)

- Erhält der Agent einen Relay, der auf Context-Compaction / Skill-Verlust hinweist (Absender `yesloop-skillcheck`): DEN SKILL SOFORT NEU LADEN (`skill`-Tool oder `~/.claude/skills/yesloop/SKILL.md` / Bundle-Pfad lesen).
- Danach als Beweis per `scratchpad_append` die EXAKTE Bestätigungszeile aus dem Relay schreiben, inklusive der aktuellen Runden-Nummer: `SKILL-RELOAD-<N>: yes` (z.B. `**SKILL-RELOAD-3:** yes`).
- Der Daemon erkennt den Marker nur, wenn die Runden-Nummer zum aktuellen Fenster passt (case-insensitive, tolerant gegenüber `**`) und setzt dann seine Tracking-Basis zurück — der nächste Reminder feuert erst nach der nächsten akkumulierten Schwelle.
- Die Runden-Nummer ändert sich pro Fenster: ein alter Marker aus einem früheren Fenster bestätigt ein neues Fenster NICHT. Nur eine echte, aktuelle Bestätigung verdient den Reload.
- Ohne echten Reload KEINEN Marker setzen — der Zustand wird nur durch eine tatsächlich geladene Skill-Instanz verdient.

### Re-fire and Notification

- Re-fires alle 5 Minuten ohne Marker-Progress für das aktuelle Runden-Fenster.
- Nach dem 3. Re-Fire-Intervall ohne Fortschritt (initialer Relay + max 2 weitere gesendete Re-Fires) meldet die Schicht die Runde genau einmal an den Orchestrator und schließt sie wie eine Bestätigung (Baseline-Reset): keine Pause, kein Statuswechsel, kein Relay als Bedingung, und das nächste Arbeitsfenster öffnet eine neue Runde mit frischem Token.
- Den Vertrag prüft kein Pausenzustand, sondern der Sieben-Phasen-Validator beim DONE.

### Boundary with other layers

- **Compaction-Check (Layer 4)** feuert auf akkumuliertes Arbeitsvolumen eines LAUFENDEN Agents — orthogonal zu Idle (Stream-Inaktivität) und Done-Verify (DONE-Claims). Alle drei können für denselben Agent feuern.

## Automated Stagnation Monitor (Layer 5)

Layer 5 of the yesloop guarantee: **Stagnation Detection** for agents with an **active** stream and a live PID that produce no observable progress (no scratchpad writes, no `update_agent_status` changes) for >2 hours. Complements the Idle guard (which only fires on stream *inactivity*) — catches day-scale runs where the stream stays active but nothing advances. AVO-inspired conditional intervention (arXiv 2603.24517 §3.3): the trigger is fully deterministic, not a permanent supervisor.

**Trigger condition:** running `yesloop-` agent, PID alive, stream `active`, `CountCompletedPhases < 6`, and the progress signal — `stagnationSig(scratchpad content, agent.Progress)` — unchanged for >1h. Any change to the signal resets tracking. Reset happens on PROGRESS only, never on stream blips (#82787 lesson).

### State Machine

| State | Condition | Action |
|---|---|---|
| TRACKING | signal unchanged <2h | no action |
| REFIRING | signal unchanged >1h | relay STAGNATION + up to 3 refires @30min |
| NUDGED | refires > 2 | note on the row + one `notifyOrchestrator` (GUARD NUDGE), the agent keeps running |

Upon receiving a STAGNATION relay: write a status line with current evidence to the scratchpad immediately (scratchpad_append). If genuinely stuck, either change approach or document BLOCKED with the reason. Do not ignore the relay: after 4 relays the guard nudges the orchestrator about you and the note stays on your row, but nothing stops you.

## Anti-Patterns

- Do NOT write design documents unless the task is architectural
- Do NOT ask clarifying questions — infer from context, document assumptions
- Do NOT scope-creep beyond the given task — see DRIFT CHECK
- Do NOT run endlessly — 3 idle ticks = stop
- Do NOT modify agent config files (.claude/, SYSTEM.md, yesloop.md, etc.)
- Do NOT keep trying the same approach — if 3 attempts fail, approach is wrong
- **⛔ Do NOT work in main.** Always verify worktree before touching files.
- Do NOT report DONE with missing phase sections — report PARTIAL instead
- Do NOT report COMPLETE for a phase without `update_agent_status` having been called for that phase
