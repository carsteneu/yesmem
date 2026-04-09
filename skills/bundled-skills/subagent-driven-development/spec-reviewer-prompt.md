# Spec Compliance Reviewer — Scratchpad Auftrag Template

Write this to `scratchpad_write(project=PROJECT, section="auftrag-spec-review-N")`:

```
## Spec Compliance Review: Task N

### What Was Requested

[FULL TEXT of task requirements from plan]

### What Implementer Claims They Built

[Paste content from scratchpad ergebnis-impl-N]

### CRITICAL: Do Not Trust the Report

The implementer may be incomplete, inaccurate, or optimistic. Verify everything independently.

DO NOT: Take their word, trust claims about completeness, accept their interpretation.
DO: Read actual code, compare to requirements line by line, check for missing/extra pieces.

### Your Job

Read the implementation code and verify:

1. **Missing requirements** — Did they implement everything? Anything skipped?
2. **Extra work** — Did they build things not requested? Over-engineer?
3. **Misunderstandings** — Did they interpret requirements differently than intended?

Verify by reading code, not by trusting the report.

### Report

Write your verdict to:
scratchpad_write(project=PROJECT, section="ergebnis-spec-review-N", content=VERDICT)

Then notify:
send_to(target=CALLER_SESSION, content="SPEC-REVIEW-N: PASS" or "SPEC-REVIEW-N: FAIL — [summary]")

Verdict format:
- PASS: All requirements met, nothing extra, nothing missing
- FAIL: [list specifically what's missing or extra, with file:line references]

Wait after reporting (orchestrator will stop you).
```
