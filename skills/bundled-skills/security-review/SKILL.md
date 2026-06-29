---
name: security-review
description: "Use when reviewing code for vulnerabilities, checking diffs for injection/XSS/auth/crypto issues. Invokes on code changes (.go/.py/.js/.ts/.java/.rs/.php/.rb), not docs-only diffs."
user-invocable: true
disable-model-invocation: false
allowed-tools: Read Grep Glob Bash Task
license: Apache-2.0
---

<!--
Based on getsentry/skills security-review skill (Apache-2.0):
https://github.com/getsentry/skills/tree/main/skills/security-review
Reference material based on OWASP Cheat Sheet Series (CC BY-SA 4.0):
https://cheatsheetseries.owasp.org/

Adapted for yesloop: confidence-gating table diverges from Sentry's original.
Sentry's "Do not report LOW" is rejected here — see Confidence Levels below.
-->

# Security Review Skill

Identify exploitable security vulnerabilities in code. Investigate before flagging. Report findings with confidence levels and severity.

**Skip condition:** If the diff contains no executable code (docs-only, JSON-only config, comments-only), skip the review and record `skipped — diff is docs-only` (or similar) in the `**Security:**` field. If the diff touches any `.go/.py/.js/.ts/.tsx/.jsx/.java/.rs/.php/.rb` file, this skill MUST be invoked — no skip.

## Scope: Research vs. Reporting

**CRITICAL DISTINCTION:**

- **Report on**: Only the specific file, diff, or code provided
- **Research**: The ENTIRE codebase to build confidence before reporting

Before flagging any issue, research the codebase to understand:
- Where does this input actually come from? (Trace data flow)
- Is there validation/sanitization elsewhere?
- How is this configured? (Check settings, config files, middleware)
- What framework protections exist?

**Do NOT report issues based solely on pattern matching.** Investigate first, then report.

## Confidence Levels + NEW/MODIFIED Doctrine (adapted for yesloop)

The Sentry original says "Do not report LOW". That is rejected here. We also reject the "fix if <2 min" time-estimate rule from earlier Phase 5 doctrine. Instead we use a **bright-line NEW vs MODIFIED distinction** — a fact about the diff, not an estimate.

### Why NEW vs MODIFIED instead of time estimates

1. **Time estimates are unreliable** for agents and humans. "NEW vs MODIFIED" is a fact determined from `git diff`.
2. **Greenfield has zero compat constraints** — no API contract, no migration, no downstream callers that can break.
3. **Training signal** — shipping LOW findings trains future agents that LOW is acceptable. yesloop builds production code.
4. **Compounding debt** — 50 LOW findings = 4+ hours of tech-debt cleanup later plus re-orientation per finding.

### Action Matrix

Classify each finding by whether the **vulnerable lines are added by this diff (NEW)** or in **existing code the diff touches (MODIFIED)**:

| Code Type in Diff | HIGH | MEDIUM | LOW |
|---|---|---|---|
| **NEW** (added file / added function / added logic block) | Fix immediately + document | Investigate dataflow + fix (or document "not exploitable because X") | **Fix** — no exception |
| **MODIFIED** (existing function touched) | Fix introduced issues | Fix introduced issues | Fix introduced issues; pre-existing = document "OUT OF SCOPE, Learning #TBD" |
| **Docs/config only** | n/a — skip entire review | n/a | n/a |

Pre-existing issues in surrounding code (not introduced by this diff) are **OUT OF SCOPE** — document them with a Learning reference (`yesmem_remember`) so they surface later, but do not fix them here (avoids scope creep).

### Severity Definitions (for describing findings)

| Severity | Impact | Examples |
|----------|--------|----------|
| **Critical** | Direct exploit, severe impact, no auth required | RCE, SQL injection to data, auth bypass, hardcoded secrets |
| **High** | Exploitable with conditions, significant impact | Stored XSS, SSRF to metadata, IDOR to sensitive data |
| **Medium** | Specific conditions required, moderate impact | Reflected XSS, CSRF on state-changing actions, path traversal |
| **Low** | Defense-in-depth, minimal direct impact | Missing headers, verbose errors, weak algorithms in non-critical context |

## Do Not Flag

### General Rules
- Test files (unless explicitly reviewing test security)
- Dead code, commented code, documentation strings
- Patterns using **constants** or **server-controlled configuration**
- Code paths that require prior authentication to reach (note the auth requirement instead)

### Server-Controlled Values (NOT Attacker-Controlled)

These are configured by operators, not controlled by attackers:

| Source | Example | Why It's Safe |
|--------|---------|---------------|
| Process env / config | `os.Getenv("DATABASE_URL")`, `os.LookupEnv` | Set at deployment |
| Config files | `config.yaml`, `viper.GetString`, `app.config['KEY']` | Server-side files |
| Framework constants | `http.StatusOK`, default timeouts | Compile-time constants |
| Hardcoded values | `baseURL := "https://api.internal"` | Compile-time constants |
| Internal service URLs from config | `cfg.UpstreamURL` | Operator-controlled |

**SSRF Example — NOT a vulnerability:**
```go
// SAFE: URL comes from config (server-controlled)
resp, err := http.Get(cfg.SeerAutofixURL + path)
```

**SSRF Example — IS a vulnerability:**
```go
// VULNERABLE: URL comes from request (attacker-controlled)
resp, err := http.Get(r.URL.Query().Get("url"))
```

### Framework-Mitigated Patterns

Check before flagging. Common false positives:

| Pattern | Why It's Usually Safe |
|---------|----------------------|
| Go `html/template` `{{.Field}}` | Auto-escaped by default (context-aware) |
| Go `text/template` `{{.Field}}` | NOT auto-escaped (flag for HTML output) |
| React `{variable}` | Auto-escaped by default |
| Django `{{ variable }}` | Auto-escaped by default |
| Parameterized SQL `db.Query("...$1", input)` | Parameterized query |
| ORM `db.Where("id = ?", input)` | Parameterized |

**Only flag these when:**
- Go templates: `template.HTML(userInput)`, `template.URL(userInput)`, `template.JS(...)` — explicit bypass of escaping
- React: `dangerouslySetInnerHTML={{__html: userInput}}`
- Django: `{{ var|safe }}`, `{% autoescape off %}`, `mark_safe(user_input)`
- SQL: string interpolation `fmt.Sprintf("SELECT ... WHERE id=%s", input)`, `db.Raw(stringWithInput)`, `db.Exec` with concatenated SQL

## Review Process

### 1. Detect Context

What type of code am I reviewing?

| Code Type | What to check |
|-----------|---------------|
| API endpoints, routes | auth, authz, injection, IDOR, mass assignment |
| Frontend, templates | XSS, CSRF |
| File handling, uploads | path traversal, uploads, XXE |
| Crypto, secrets, tokens | algorithms, key management, randomness |
| Data serialization | pickle/yaml/JSON deserialization |
| External requests | SSRF |
| Business workflows | race conditions, workflow bypass |
| Config, headers, CORS | misconfiguration |
| CI/CD, dependencies | supply chain |
| Error handling | fail-open, information disclosure |
| Logging | log injection, secret leakage |

### 2. Identify Language

Based on file extension or imports:

| Indicators | Apply patterns from |
|------------|-------|
| `.go`, `go.mod` | Go stdlib + popular pkgs (`net/http`, `database/sql`, `os/exec`, `html/template`, `crypto/*`) |
| `.py`, `django`, `flask`, `fastapi` | Python: `subprocess`, `pickle`, `yaml`, `eval/exec`, Django/Flask/FastAPI |
| `.js`, `.ts`, `express`, `react`, `vue`, `next` | JavaScript: `child_process`, `eval`, `innerHTML`, `dangerouslySetInnerHTML` |
| `.rs`, `Cargo.toml` | Rust: `unsafe`, FFI, `Command::new` with user input |
| `.java`, `spring`, `@Controller` | Java: `ObjectInputStream`, `Runtime.exec`, Spring templates |
| `.php`, `.rb` | PHP/ Ruby: `unserialize`, `eval`, shell exec |

### 3. Research Before Flagging

**For each potential issue, research the codebase to build confidence:**

- Where does this value actually come from? Trace the data flow with `graph_traverse` / grep for callers.
- Is it configured at deployment (env, config) or from user input (request)?
- Is there validation, sanitization, or allowlisting upstream?
- What framework protections apply (auto-escaping, parameterization)?

Only flag issues where you have at least MEDIUM confidence after understanding the broader context.

### 4. Verify Exploitability

For each finding, confirm:

**Is the input attacker-controlled?**

| Attacker-Controlled (Investigate) | Server-Controlled (Usually Safe) |
|-----------------------------------|----------------------------------|
| `r.URL.Query()`, `r.FormValue()`, `r.Header.Get(...)` (most headers) | `os.Getenv`, `viper.GetString` |
| `request.GET`, `request.POST`, `request.args` (Python) | Hardcoded constants |
| `req.body`, `req.json` (Node) | Internal service URLs from config |
| `request.cookies` (unsigned) | Database content from admin/system |
| URL path segments: `/users/{id}` | Signed session data |
| File uploads (content and names) | Framework settings |
| Database content from other users | |
| WebSocket messages | |

**Does the framework mitigate this?**
- Check for auto-escaping, parameterization
- Check for middleware/decorators that sanitize

**Is there validation upstream?**
- Input validation before this code
- Sanitization libraries

### 5. Report Findings

All three confidence levels land in the `**Security:**` field — never silently dropped.

---

## Quick Patterns Reference

### Always Flag (Critical)

```
eval(user_input)                       # Any language
exec(user_input)                       # Python
pickle.loads(user_data)                # Python
yaml.load(user_data)                   # Python (not safe_load)
unserialize($user_data)                # PHP
ObjectInputStream.readObject(user)     # Java
shell=True + user_input                # Python subprocess
child_process.exec(user)               # Node.js
exec.Command(userInput...)             # Go os/exec
```

### Always Flag (High)

```
innerHTML = userInput                  # DOM XSS
dangerouslySetInnerHTML={user}         # React XSS
v-html="userInput"                     # Vue XSS
template.HTML(userInput)               # Go template escape bypass
fmt.Sprintf("SELECT ... WHERE id=%s", user)  # SQL injection
`SELECT * FROM x WHERE ${user}`        # SQL injection (JS)
os.system(fmt.Sprintf("cmd %s", user)) # Command injection (Go)
```

### Always Flag (Secrets)

```
password = "hardcoded"
api_key = "sk-..."
AWS_SECRET_ACCESS_KEY = "..."
private_key = "-----BEGIN"
```

### Check Context First (MUST Investigate Before Flagging)

```
# SSRF - ONLY if URL is from user input, NOT from settings/config
http.Get(r.URL.Query().Get("url"))     # FLAG: User-controlled URL
http.Get(cfg.UpstreamURL)              # SAFE: Server-controlled config
http.Get(cfg.Base + "/" + path)        # CHECK: Is 'path' user input?

# Path traversal - ONLY if path is from user input
os.Open(r.URL.Query().Get("file"))     # FLAG: User-controlled path
os.Open(cfg.LogPath)                   # SAFE: Server-controlled config
filepath.Join(baseDir, filename)       # CHECK: Is 'filename' user input? Note: filepath.Join cleans `..` — verify

# Open redirect - ONLY if URL is from user input
http.Redirect(w, r, r.URL.Query().Get("next"), 302)  # FLAG
http.Redirect(w, r, cfg.LoginURL, 302)               # SAFE

# Weak crypto - ONLY if used for security purposes
md5.Sum(fileContent)                   # SAFE: File checksums, caching
md5.Sum([]byte(password))              # FLAG: Password hashing
math/rand.Intn(N)                      # SAFE: Non-security uses (UI, sampling)
math/rand.Intn(N) for token            # FLAG: Security tokens need crypto/rand
```

---

## Output Format

Write findings to the `**Security:**` field of the Phase 5 scratchpad block. Distinguish NEW (diff-added code) from MODIFIED (existing code touched by diff) findings. Every finding must carry either a fix-commit-hash OR an "OUT OF SCOPE" annotation with Learning reference OR the block is "skipped" with a reason.

```
**Security:** reviewed via security-review skill
- NEW findings (in diff-added code):
  - [HIGH] injection in foo.go:42 — FIXED in commit abc123
  - [LOW] missing input length check in bar.go:10 — FIXED in commit def456
- MODIFIED findings:
  - [MEDIUM] SSRF risk introduced by baz.go:78 — FIXED in commit 789abc
  - [LOW pre-existing] weak hashing in qux.go:100 — OUT OF SCOPE (pre-existing, surrounding code; Learning #TBD)
- Skipped: none | OR "skipped — diff is docs-only (.md files only)"
```

Or if no findings at all:

```
**Security:** reviewed via security-review skill — none, diff reviewed, no HIGH/MEDIUM/LOW findings.
```

Or if skipped:

```
**Security:** skipped — diff is docs-only (.md files only).
```

### Finding-line grammar

Each finding line carries:
- `[SEVERITY]` — Critical / High / Medium / Low
- `(file:line)` — location
- short description — what the vulnerability is
- disposition — `FIXED in commit <hash>` | `not exploitable because <evidence>` | `OUT OF SCOPE (pre-existing; Learning #<id>)`

NEW findings never use "OUT OF SCOPE" — they are in the diff, they are in scope. Only MODIFIED findings can carry pre-existing OUT-OF-SCOPE annotations, and only for issues in surrounding code the diff did not introduce.

---

## What NOT to Do

- Do NOT skip investigation — always trace dataflow before flagging.
- Do NOT report on test files unless reviewing test security specifically.
- Do NOT flag server-controlled configuration as attacker-controlled.
- Do NOT silently drop LOW findings — fix NEW LOWs, document MODIFIED pre-existing LOWs as OUT OF SCOPE.
- Do NOT mark findings as exploitable without confirming attacker-controlled input.
- Do NOT review code you didn't actually read.
- Do NOT apply the "fix if <2 min" rule — that's deprecated. Use the NEW/MODIFIED bright line.
- Do NOT scope-creep into fixing pre-existing issues in surrounding MODIFIED code — document them, do not fix.
