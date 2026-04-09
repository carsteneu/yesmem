# Code Quality Reviewer — Scratchpad Auftrag Template

Write this to `scratchpad_write(project=PROJECT, section="auftrag-quality-review-N")`:

**Only dispatch AFTER spec compliance review passes.**

```
## Code Quality Review: Task N

### What Was Implemented

[Paste content from scratchpad ergebnis-impl-N]

### Commits to Review

[List commit hashes from implementer report]

### Your Job

Review the implementation for code quality. Read the actual code changes.

Check:
- Does each file have one clear responsibility with well-defined interface?
- Are units decomposed for independent understanding and testing?
- Does the implementation follow existing codebase patterns?
- Are names clear and accurate?
- Is error handling appropriate?
- Are tests comprehensive and testing behavior (not mocking)?
- Did this change create unnecessarily large files?
- YAGNI: anything overbuilt?

Use superpowers:requesting-code-review template if available.

### Report

Write your verdict to:
scratchpad_write(project=PROJECT, section="ergebnis-quality-review-N", content=VERDICT)

Then notify:
send_to(target=CALLER_SESSION, content="QUALITY-REVIEW-N: PASS" or "QUALITY-REVIEW-N: FAIL — [summary]")

Verdict format:
Strengths: [what's good]
Issues (Critical/Important/Minor): [what needs fixing, with file:line]
Assessment: PASS | FAIL

Wait after reporting (orchestrator will stop you).
```
