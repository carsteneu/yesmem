# Implementer Agent — Scratchpad Auftrag Template

Write this to `scratchpad_write(project=PROJECT, section="auftrag-impl-N")`:

```
## Task N: [task name]

[FULL TEXT of task from plan — paste it here, don't make agent read file]

## Context

[Scene-setting: where this fits, dependencies, architectural context.
Include relevant file paths, function names, existing patterns.]

## Working Directory

[absolute path]

## Your Job

1. Read this auftrag from scratchpad
2. If you have questions about requirements, approach, or dependencies:
   send_to(target=CALLER_SESSION, content="NEEDS_CONTEXT: [your questions]")
   Wait for relay_agent response before proceeding.
3. Implement exactly what the task specifies
4. Write tests (TDD if project requires it)
5. Verify implementation works
6. Commit your work
7. Self-review (see below)
8. Write your report:
   scratchpad_write(project=PROJECT, section="ergebnis-impl-N", content=REPORT)
9. Notify orchestrator:
   send_to(target=CALLER_SESSION, content="DONE: impl-N — [brief summary]")
10. Wait (orchestrator will stop you or relay fix instructions)

## Code Organization

- Follow the file structure defined in the plan
- Each file: one clear responsibility, well-defined interface
- If a file grows beyond plan's intent: report as DONE_WITH_CONCERNS
- In existing codebases: follow established patterns
- Improve code you're touching, don't restructure outside your task

## When You're Stuck

STOP and escalate. Bad work is worse than no work.

send_to(target=CALLER_SESSION, content="BLOCKED: [what you're stuck on, what you tried]")

## Self-Review Before Reporting

- Did I implement everything in the spec?
- Did I miss any requirements or edge cases?
- Is this my best work? Names clear? Code clean?
- Did I avoid overbuilding (YAGNI)?
- Do tests verify behavior (not just mock behavior)?

Fix issues found during self-review before reporting.

## Report Format (write to ergebnis-impl-N)

Status: DONE | DONE_WITH_CONCERNS | BLOCKED | NEEDS_CONTEXT
What: [what you implemented]
Tests: [what you tested, results]
Files: [files changed]
Self-review: [findings, if any]
Concerns: [issues or doubts, if any]
Commit: [commit hash]
```
