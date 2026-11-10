# YesLoop

> Spawn a visible agent that runs a complete development task end-to-end without interaction: analyze, plan with TDD, execute in an isolated worktree, verify with tests, finish with a PR.

## What it is

YesLoop is the autonomous execution layer on top of YesMem. A normal agent session is interactive (you chat, the agent responds, you correct, it tries again). A YesLoop session is fire-and-forget: you describe the task once, the agent works alone, you review the result at the end.

YesLoop exists because interactive babysitting does not scale. Long tasks drift, context rots, agents get stuck in loops, and the human becomes the bottleneck. YesLoop replaces the human-in-the-loop with structural guards.

## Cross-backend — the differentiator

YesLoop runs on three agent backends. Bare model names auto-resolve against the auto-discovered provider map.

| Backend | Binary | Models | Example |
|---|---|---|---|
| `claude` | `claude` | Claude Opus, Sonnet, Haiku | `spawn_agent(backend="claude")` |
| `codex` | `codex` | GPT-5.x, o4-mini | `spawn_agent(backend="codex")` |
| `opencode` | `opencode` | Any OpenAI-compatible: Zai GLM-5.2, DeepSeek, Ollama, Mistral, local LLMs | `spawn_agent(backend="opencode", model="glm-5.2")` |

Claude Code's `/loop`, `/goal`, and `/workflows` are Claude-only. Codex has no equivalent orchestration. YesLoop is the only autonomous pipeline that works on all three — including fully open-source stacks (`opencode` + GLM-5.2 + Ollama) with no Anthropic or OpenAI dependency.

### Backend-mix examples

Because YesLoop agents coordinate via scratchpad + `send_to` regardless of backend, you can mix backends per role:

- **Plan + Implement + Review split**: Claude Opus plans the task, Codex implements it, GLM-5.2 reviews the diff — three yesloop agents in three worktrees, coordinated via scratchpad sections.
- **Local-only dev**: `opencode` + local Ollama model runs the whole pipeline without any paid API key.
- **Cost-optimized**: GLM-5.2 (via `opencode`) for routine yesloops, Claude Opus for cold-review subagents — pick the best model per phase, not per pipeline.

Backend-mix orchestration across many agents is the planned `yesloopultra` feature.

## The 7-Phase Pipeline

Every YesLoop run moves through the same seven phases. The agent does this itself, no orchestration from your side.

1. **ANALYZE** — read the task brief, explore the codebase via code-intelligence tools
2. **PLAN** — write a concrete implementation plan, store it via `set_plan()` so it survives context collapse
3. **EXECUTE** — work through plan steps one at a time with per-step drift checks, commit after each logical unit
4. **VERIFY** — full test suite + type checks + lint + golden path + edge cases; loops back to Phase 3 on failure
5. **REVIEW** — four sub-phases, each with its own evidence: 5.1 self-review (the agent), then 5.2 cold code review, 5.3 consequence & intent and 5.4 security review, each as a separate `task()` subagent with its own `ses_` id. All findings must be fixed autonomously
6. **DAEMON-VERIFY** — the agent runs `yesmem verify-claims` on its own report and records the accepted verdict in this block. The check belongs to the agent, before the orchestrator hears anything: a refused claim leads back into Phase 3 or 4, depending on its class, and only an accepted verdict opens the next phase
7. **FINISH** — commit on the agent's own branch, push that branch (and open a PR if configured), report DONE with the commit hash, `send_to` orchestrator. No merge to main, no deploy: those belong to the orchestrator

Phase 6 is deterministic: a command, an exit code and the verdict it prints. Nothing in
it is a judgement call, and the gate reads the same verdict the agent recorded, so the
agent cannot claim a phase the daemon would refuse.

## Phase-Completion Guarantee

Seven defense-in-depth layers cover the known failure modes of autonomous agents:

| Layer | Catches |
|---|---|
| 1 — Skill v3.2 structured phase blocks | LLM pattern mismatch |
| 2 — Idle-Self-Check (`yesloop_idle.go`) | Agent stalls with live PID, no DONE claim |
| 3 — Done-Verify (`yesloop_done_verify.go`) | DONE claimed but Phase 5 Cold Review skipped |
| 4 — DONE-Guard (`done_gate.go`) | DONE claim with malformed phase blocks |
| 5 — Dead-Detection (`crashRecovery`) | Agent crashes mid-pipeline |
| 6 — Hung-Detection (`detectHungAgents`) | Heartbeat stale + dead PID |
| 7 — Claim verification (`done_gate_verify.go`) | A formally correct report whose claims are false |

Layers 2 and 3 walk the agent through a relay protocol. Layer 2 fires when the stream goes idle for 10 min with a live PID; Layer 3 fires on DONE-claims and requires a `BEWEISEN` marker + `send_to orchestrator:` line + 7 COMPLETE phase blocks to terminate. Neither layer stops the agent when the protocol runs out: it leaves a note on the row, sends one informational notice to the orchestrator and the agent keeps running. Only the DONE verdict pauses an agent, because there the agent has to wait for an answer.

The compaction check (`yesloop_skillcheck.go`) runs beside these layers and blocks nothing: past 100k output tokens or 50 turns it relays a reminder to reload the skill, and after three unanswered reminders it sends one informational notice to the orchestrator, closes the round and lets the agent run on. The phase contract is enforced by the DONE gate and the claim verification, not by pausing the agent.

Layer 7 is the one that stops believing, and since the verdict-reuse change it is early
rather than late: a compliant DONE report is no longer checked at DONE. The run belongs to
`verify_claims` (daemon RPC, MCP tool and CLI are the same operation), and the gate reads
its verdict. Each verdict is stored under a key: the report text, HEAD, the branch, the
state of the working tree (uncommitted residue), the plan revision and the report session
it was reached on. A fresh accepted verdict releases
the agent, a fresh refused one blocks it with the run's findings, and an absent or stale
one is a defect that names the `yesmem verify-claims` command. Nothing runs at DONE, so a
DONE claim cannot start a test suite.

Outcomes:

| Verdict | Gate reaction |
|---|---|
| fresh and nothing blocked | `DONE-VERIFIED`, agent released, orchestrator notified, release names the reused verdict |
| fresh, refuted or defect | `DONE-VERIFY-BLOCKED` with the findings table, agent stays frozen |
| fresh, environment gap | blocked, reported as a missing capability, not as the agent's fault |
| fresh, exhausted budget | blocked, reported with its own reason (`limit`) |
| absent or stale | defect: the report must be checked first, the message prints the command |
| judged under a weaker profile | defect: the rule set is named, a new verdict is asked for |
| pre-check still running | `DONE-PENDING-VERIFY` with the fixed wait text, decided on the next tick |

`done_verify.enabled` is the global switch and defaults to on. `done_verify.budget` bounds
one direct run. The wait ceiling that belonged to the gate's own queue is gone with the
queue: a direct call waits for its own answer, and one slot admits one verification at a
time so two suites never compete with each other. The indexer brake the queue had is gone
with it, so a verification can now share the machine with an indexing run.

### The DONE payload: decided before it is delivered

The gate acts on the scratchpad; the same verdict decides the wire. A `send_to` whose
first non-blank line announces DONE (`[DONE]` or `DONE:`) does not leave the daemon
before it is settled, and the answer comes from the same registry:

| Situation at send time | Delivery |
|---|---|
| fresh accepted verdict under the loop's rule set | delivered in the same call, with `verdict: accepted (pre-check)` appended to the payload; nothing runs |
| no verdict, a stale one, a refused one, a weaker rule set | withheld: the sender gets the defect message with its `yesmem verify-claims` command, and the orchestrator gets a `DONE-WITHHELD` notice at once — the class, the fact that the message did not arrive, and the full command — never the DONE |
| third withheld attempt on one report | escalation to the orchestrator, the agent waits |

A message without a DONE payload is not inspected. `send_to` stays infrastructure for
relays, questions and status lines: the check costs one prefix test on the trimmed body
and touches neither the registry nor git. The prefix test is a fast lane, not the
enforcement: a DONE phrased some other way still reaches the orchestrator, and the gate's
tick on the scratchpad is what judges it. The hold is scoped to the loop's sections
(`yesloop-`, `yesresearch-`) and to the caller session those agents report to, so a
generic spawned agent's handover sentence and a DONE addressed elsewhere travel
unchanged. Deciding a DONE costs the verdict lookup plus three `git` calls for the key,
tens of milliseconds; the withheld path adds one pause and one notice to the
orchestrator, and the sender's answer is the tool result of its own call.

Because a delivered DONE carries its verdict line, the release needs no second message:
the gate's next tick finds the decision already taken and its notice already paid, and
the agent row carries the state. That line is a hint, not an attestation: it travels the
same unauthenticated channel as the payload. A withheld message is deferred, not
cancelled: if the verdict lands before the agent sends again, the release tells it to
resend the payload instead of declaring it finished.

### Silence is impossible

A withheld DONE is a message nobody may have to wait for. Three escapes used to remain,
and all three are closed:

- **The notice is immediate.** The moment the delivery check withholds a DONE, the
  orchestrator gets `DONE-WITHHELD` with the class, the reason and the full
  `yesmem verify-claims` command. It does not wait for the third round, and it does not
  depend on the agent doing anything else.
- **The clock survives the daemon.** A hold is persisted with the fingerprint of the
  report it is about and the decision it stands for (`done_verify_holds`). A daemon restart
  loses the in-memory state, which the next tick re-derives from the report, but not the
  time. The clock belongs to one report and one decision: a corrected report, or the step
  from a running pre-check to a blocked verdict, starts it again — that is the moment the
  agent gets its correction round.
- **A watchdog decides.** `checkDONEHoldWatchdog` runs with the DONE-guard and escalates
  a hold that has stayed undecided past the larger of `done_verify.max_wait` and
  `done_verify.budget` — exactly once, with the class, the hold's age and the command that
  produces a verdict. It reads the agent row, not its process, so the agent that disappears
  after the defect is covered as well. A message that could not be delivered leaves the
  mark unset, so the next tick owes it again.
- **The broadcast channel is not a DONE channel.** A broadcast whose body announces DONE
  (`[DONE]`, `DONE:`) is refused by name and pointed at `send_to`, and that rule holds
  whether or not the gate's switch is on: the channel is declared a non-DONE channel, not
  a weaker handover. Publishing it has no single reader to hold and no report to check, so
  it would walk around the hold instead of through it. Every other announcement on that
  channel is untouched.

### Quality claims: mutation and coverage

Two claim kinds answer what a test run cannot: the suite was green, but does it look
at the changed lines?

| Marker | Asserts | Finding |
|---|---|---|
| `[MUTATION: changed-lines survivors=0]` | an edit to a changed production line makes the suite fail | a surviving mutant, with file:line and operator |
| `[COVERAGE: changed-lines]` | every changed production line was executed | an uncovered line, with file:line |

Both are scoped to the diff, meaning the lines `git diff -U0 base..head` adds or
changes. Neither claim knows a language. The profile supplies the language data
(source extensions, test file patterns, suite command, coverage command, coverage
format, coverage artifact, build error patterns), and a file no profile covers is
named in the verdict instead of being guessed into one.

What a mutation verdict does and does not say. It measures the sensitivity of the
changed lines to changes, not the correctness of what they are supposed to do. A
survivor is therefore not automatically a false claim: a mutant in a log line, in a
format, in an ordering without a behaviour contract, or in a defensive branch with no
real path may legitimately survive. The report declares how many survivors it
tolerates with `survivors=<n>`, and the verdict names every one of them, so the
justification stays checkable. Without the field the claim tolerates none. An
allowance that covers every mutant the run would produce is a defect: such a claim
tolerates anything and asserts nothing.

Classes and refusal. A mutant that does not build is discarded and never counted as
killed, which is the one error this claim must not make; a profile without build error
patterns is refused rather than guessed. The suite has to be green on the unmutated
tree first: a red baseline would make every mutant read as killed. A coverage run that
does not exit 0 leaves a partial profile, so it is an environment gap and never a
refutation. An exhausted budget (25 mutants, 5 files, 2 minutes wall clock) is the
`limit` class, and a truncated mutant set never confirms a claim that speaks about the
whole diff.

Three operating limits belong to the claim. The suite command runs inside the sandbox,
so a toolchain without its module cache and without network is an environment gap,
exactly the boundary the test-run claim already has. Two minutes of wall clock cover
about four seconds per suite run (one baseline plus up to 25 mutants), and one hanging
run is bounded by the same deadline. The Go default command runs the whole repository,
so a large repository belongs in a profile with a package-scoped command.

No auto-deploy. The agent commits and sends `DONE`; the orchestrator decides whether to merge, deploy, and restart.

## Rolling out a gate change

The skill text is embedded in the binary (`skills.BundledSkills`) and written to its live
location `~/.claude/skills` by `yesmem setup`; `make deploy` swaps only the binary. A
change that touches both therefore goes out in this direction: the binary first, the live
skill copies afterwards from that same binary. The new skill text assumes a gate that
prints the report shape when the section is missing and the running findings once it
blocks. The reverse order is the expensive one: the trimmed skill next to the old gate
would drop the fallback an agent no longer gets from any message.

One limit of the split belongs here, because it is not a defect but a design decision:
the gate prints the shape and, after a block, the reasons of the findings it has. It does
not print the rules. A report that violates a rule learns which rule only through the
verdict: the findings carry their reasons, and the summary line names the distinct
reasons of the blocking class, ten of them at most, with the rest counted. One reason is
cut at 200 characters in that line, the finding keeps its full text. The three bullets
the skill keeps are the part that must be known before the first report is written.

A running loop is not lost to such a change. It is stopped at its next DONE: a loop that
never wrote a report section is blocked as a defect straight away, with the fill-in pattern
and without a run, and a report without a verdict is blocked with the command that produces
one. A verdict reached under the current rules releases the agent; a report with findings
stays frozen with those findings, the same as before the rollout.

The phase count is a contract change of its own. A loop that started under the six-block
contract carries six blocks and no seventh, so the DONE-guard reads it as a claim and
bounces it once: the relay names the missing block (`7 FINISH`) and the field the renamed
sixth block now requires (`Phase6:Vorab-Prüfung (accepted)`). The bounce gives the agent
three relayed attempts over 90 seconds each; an agent that does not answer within them is
paused with a `DEAD_AGENT` notice that names the missing blocks, and the orchestrator
resumes it after the correction. Nothing is migrated, and nothing is lost: the two blocks
are appended, the pre-check runs, the DONE goes out again.

## How to invoke

From an opencode or claude-code session:

```
/yesloop <task description>
```

From an orchestrator session:

```
yesmem_spawn_agent(
  backend="opencode",
  model="zai-coding-plan/glm-5.2",
  project="<project>",
  section="yesloop-<task-name>",
  work_dir="<worktree-path>"
)
```

Bare model names like `glm-5.2` resolve against the auto-discovered provider map — you do not need to prefix unless multiple providers carry the same model.

## How to write a task brief

A well-formed brief contains: Goal (one sentence), Context (why this is a problem), Hard constraints, Escalation triggers, Negative scope, DONE criteria, Code entry points, Relevant learning IDs.

Write the brief to the scratchpad section before spawning. The spawn relay is metachar-free — no markdown, backticks, parens, `$`, or semicolons — because opencode's TUI interprets them as shell input. Complex briefings go in the scratchpad, the relay is just a trigger.

## Worktree isolation

Every YesLoop run works in its own git worktree under `.worktrees/<branch-name>`. The main working tree is never touched until the merge. Multiple agents run in parallel without collision; failed runs are discarded by worktree removal.

## State management

- **Thread-scoped plan** (`set_plan` / `update_plan` / `get_plan`) — current implementation plan, survives context collapse
- **Per-agent state** (`agents` table) — phase, status, heartbeat, stream activity, token usage
- **Shared scratchpad** — brief at start, progress markers during execution, final DONE report

Agent IDs are date-based (`agent-YYYYMMDD-NN`) and never recycled — removing an agent from the DB does not free its ID for reuse.

## When to use YesLoop vs an interactive session

| Task shape | Use |
|---|---|
| Well-scoped bug fix or feature, clear entry points, ≤1 day work | YesLoop |
| Exploratory debugging, unclear root cause | Interactive |
| Refactor with known target shape | YesLoop |
| Multi-file change touching many domains | Interactive or YesLoopultra |
| Repetitive migration across similar files | YesLoop |

## See Also

- [Verifier](verifier.md): the claim types, the verdict classes and the profile format
- [Guard](guard.md): the tool-call guard that blocks a call before it runs
- [YesLoop (full documentation)](../yesloop.md)
- [Features — Multi-Agent](multi-agent.md)
- [MCP Tools Reference](../mcp-tools-reference.md)
- [Caps vs Skills Rationale](../caps-vs-skills-rationale.md)
