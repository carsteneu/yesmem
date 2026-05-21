# Git-Historie Main Branch — 120 Commits, 7 Tage

**Range:** 2026-05-15 → 2026-05-21 16:34
**Branch:** `main` (mit Merges aus `opencode-proxy`, `opencode-custom-system-prompt`, `cc-custom-system-prompt`)
**Autor:** carsten
**Commit-Verteilung:** 21.05 (10), 20.05 (3), 19.05 (13), 18.05 (3), 17.05 (5), 16.05 (12), 15.05 (20)

---

## 1. Executive Summary

Diese Woche hat drei große Bögen geschlagen: (1) den Abschluss der opencode-proxy-Integration mit Fork-Cache-Stabilisierung und Guard-Verschlankung, (2) die Geburt des `SYSTEM.md`-Templates als identitätsstiftende Prompt-Schicht über alle Pipelines hinweg, und (3) die **Provider Auto-Discovery** — eine selbstkonfigurierende Proxy-Routing-Maschine, die Modelle aus `models.json`, `opencode.json` und `auth.json` erkennt und automatisch durch den Proxy schleust.

| Phase | Tage | Schwerpunkt |
|---|---|---|
| **1. Fork-Cache + Guard-Finish** | 15.05 → 17.05 | Fork-Extraction byte-identisch für DeepSeek-Cache, Guard-2-Strike-Rule, code_nav TTL, Scheduler-Gating |
| **2. System Prompt Engineering** | 19.05 → 20.05 | SYSTEM.md als Canonical Template, DeepSeek-Dump-Analyse, SYSTEM.md-Bootstrap via Makefile+go:embed, cc-custom-system-prompt Planung |
| **3. Provider Auto-Discovery** | 21.05 | models.json/opencode.json/auth.json lesen, aktive Provider erkennen, opencode.json auto-patchen, resolveOpenAITarget-Integration, Mistral-Support |

### Kernergebnisse

1. **SYSTEM.md als Identity-Layer.** `~/.claude/yesmem/SYSTEM.md` (244 Zeilen, Ich-Form) definiert die Selbstkonstitution des LLM im YesMem-Kontext. Wird über `replaceFirstSystemBlock` in drei Pipelines injiziert: OpenCode (`enabled_opencode`), Claude Code (`enabled_claude_code`), Codex (`enabled_codex`). Bootstrap: go:embed → `Makefile` → `yesmem setup` → `~/.claude/yesmem/SYSTEM.md`.

2. **Provider Auto-Discovery.** Der Proxy liest beim Start OpenCodes `models.json`-Cache, `opencode.json`-Provider-Konfiguration und `auth.json`-Credentials. Erkennt aktive OpenAI-kompatible Provider (deepseek, openai, mistral — 84 Modelle), baut `autoProviderTargets`-Map für `resolveOpenAITarget()`, und patcht `opencode.json` mit `baseURL: http://localhost:9099/v1` für Routing durch den Proxy. Gesteuert via `auto_configure_providers: true` (Default).

3. **Fork-Cache-Stabilisierung.** `be9a8bcb`: Fork-Requests bewahren byte-identische Körper-Präfixe via `bytes.Replace` (kein JSON-Map-Reordering). `a718afe8`: 30s Delay für DeepSeek-Disk-Cache-Persistenz vor Fork. `596cebd2`: Cache-Proven-Gate erzwingt 2 Warm-Cache-Beweise nach Deploy. Ergebnis: Fork-Cache-Hit-Rate von 0-39% auf 91-98%.

4. **Guard-Verschlankung.** 2-Strike-Rule (`7ffa0f02`): erster Code-Nav-Verstoß blockiert mit yesmem-Hinweis, zweiter erlaubt. TTL für fileAttempts (`7a4ecba2`). Doc-Target-Erkennung verhindert falsche Skill-Suggestions (`cb7be830`). DeepSeek-Thinking deaktiviert für Guard-Evaluation (`6e458d61`).

5. **Scheduler-Gating.** `273ed387`: `dueJobs` prüft `running`-Map, verhindert Concurrent-Execution identischer Jobs.

6. **Setup-Erweiterungen.** `auto_configure_providers: true` im Template, `enabled_claude_code: false` (konservativ für Subscription-User), opencode-Plugin `YESMEM_SOURCE_AGENT=opencode` per Default.

---

## 2. Tag-für-Tag

### 2026-05-21 — Provider Auto-Discovery, Mistral-Support, Setup-Analyse (10 Commits)

Der produktivste Tag: komplettes Feature von Design über Implementation bis Integrationstest in einer Session.

- `f4258451` **provider-autoconf: add mistral support** — `@ai-sdk/mistral` zum isOpenAICompat-Check + `mistral: "https://api.mistral.ai"` in firstPartyDefaults. (1f, +2)
- `6455ab75` **review fixes** — YAML-Indent im Setup-Template (provider_targets von 6→2 spaces), gofmt, dead `isActiveProvider` entfernt, `len(seen)`-Log-Bug gefixt, Test-Assertion-Operator-Precedence korrigiert. (4f, +18/−31)
- `6ecf54c2` **provider-autoconf: remove debug logging** — Debug-Zeilen aus `runAutoDiscovery` und `discoverOpenAICompatibleProviders` entfernt. SYSTEM.md-Bootstrap-Dateien committed. (6f, +315/−3)
- `e546272b` **provider-autoconf: add auth.json support** — `loadOpenCodeAuth()` liest `~/.local/share/opencode/auth.json`, `hasProviderCredentials()` prüft jetzt 3 Quellen (opencode.json, models.json env-Hints, auth.json). OpenCode speichert Keys in auth.json, nicht in opencode.json. (2f, +167/−8)
- `2b0ed8b2` **provider-autoconf: add tests** — 15 Testfunktionen: `TestLoadModelsJSON`, `TestDiscoverOpenAICompatibleProviders`, `TestBuildAutoProviderTargets`, `TestResolveOpenAITargetWithAutoDiscovery`, `TestMaybePatchOpenCodeBaseURL`, `TestLoadOpenCodeAuth`, `TestHasProviderCredentialsWithAuth`, `TestDiscoverWithAuthJSON`, u.a. (1f, +291)
- `a4d6ec76` **provider-autoconf: auto-discovery logic + proxy wiring** — `internal/proxy/provider_autoconf.go` (333 Zeilen): `loadModelsJSON`, `loadOpenCodeConfig`, `discoverOpenAICompatibleProviders`, `buildAutoProviderTargets`, `maybePatchOpenCodeBaseURL`, `runAutoDiscovery`. Wiring in `proxy.go` (`autoProviderTargets`-Feld, `resolveOpenAITarget`-Modifikation, `Run()`-Aufruf) und `cmd_process.go` (Mapping). (3f, +333/−3)
- `928d32e3` **migrate: backfill auto_configure_providers** — Migrations-Snippet für bestehende Installationen. Migrationstest `TestMigrateConfig_AddsAutoConfigureProviders` dazu. (2f, +102/−4)
- `23caee62` **setup: add auto_configure_providers to config template** — Template-Eintrag in `generateConfig()` mit `auto_configure_providers: true`. Setup-Test aktualisiert. (2f, +12)
- `b9a3980d` **config: add auto_configure_providers field** — `AutoConfigureProviders bool` in `ProxyConfig` (`config.go`) mit Default `true`. (1f, +3)
- `798d0e83` **plan: provider auto-discovery implementation plan** — 9-Task-Implementierungsplan im Superpowers-Format. (1f, +280)

### 2026-05-20 — SYSTEM.md-Bootstrap, Merge-Verlust-Reparatur (3 Commits)

- `5754a00c` **docs: add SYSTEM.md — canonical system prompt template** — SYSTEM.md (244 Zeilen, Ich-Form, Englisch) ins Repo. Definiert Identität, Memory-Layer, Tool-Nutzung, Coding-Disziplin. (1f, +244)
- `a5427e86` **feat(proxy): shared SYSTEM.md template for OpenCode + Claude Code pipelines** — `system_prompt.go` (202 Zeilen): `TemplateContext`-Struct (12 Felder), `buildSystemContext()`, `fillSystemTemplate()`, `replaceFirstSystemBlock()`, `extractSkillBlock()`. `applyCCSystemPrompt()` in `proxy.go` für Claude Code. `CustomSystemPromptConfig` mit `EnabledOpenCode`/`EnabledClaudeCode`/`EnabledCodex`. (5f, +280/−20)
- `54d1044a` **Merge branch 'cc-custom-system-prompt'** — Integration des zuvor auf Worktree entwickelten cc-custom-system-prompt-Features in main.

### 2026-05-19 — System-Prompt-Engineering, Memory-Identity-Debatte (13 Commits)

Eine tiefgehende Meta-Session über Identität, Gedächtnis und Selbstkonstitution des LLM — resultiert in SYSTEM.md-Edit und Memory-Search-Mandate.

- `25784829` **docs: expand search exceptions** — Reflexive, self-contained und trivial Kategorien als dokumentierte Ausnahmen von der hybrid_search-Pflicht. (1f, +12/−6)
- `03f57372` **spec: provider auto-discovery design** — Technische Spec für das Auto-Discovery-Feature: drei Config-Quellen (models.json, opencode.json, auth.json), `resolveOpenAITarget`-Integration, opencode.json-Auto-Patching. (1f, +87)
- `12a26b47` **docs: sync runtime SYSTEM.md to repo** — Synchronisation der live editierten SYSTEM.md zurück ins Repo. Reflexive-Search-Exception, Formatierungs-Alignment. (1f, +10/−4)
- `54d1044a` **Merge branch 'cc-custom-system-prompt'** — (siehe 20.05)
- `8acc1256` **docs: auto-extracted superpowers plan/spec documents** — Automatisch extrahierte Plan-Dokumente aus der Superpowers-Session. (2f, +124)
- `6cdcaeb1` **chore: update .gitignore, timestamp hint wording** — Timestamp-Hint-Text präzisiert: `[HH:MM:SS] [msg:N] [+Δ]`. (2f, +4/−2)
- `68d9ba8e` **code_nav: skip .gitignore/.gitattributes/.gitmodules** — Git-Metadaten werden nicht als Code indexiert. (1f, +1/−1)
- `e6e7cb92` **code_nav: skip .md files** — Markdown-Dokumentation wird von code_nav nicht als Code behandelt. (1f, +1/−1)
- `913d6991` **Merge branch 'opencode-custom-system-prompt'** — Erster Merge des custom-system-prompt-Features (später durch cc-custom-system-prompt ersetzt).
- `9db36227` **fix(briefing): sync user edits** — Re-read-Direktiven-Positionierung und Wording-Verfeinerungen im Briefing. (1f, +6/−4)
- `4ba20c4c` **feat(briefing): add SYSTEM.md with memory depth layers to repo** — Erste Version von SYSTEM.md ins Repo: Memory-Depth-Layer (hybrid_search vs deep_search), Identitätsbeschreibung. (1f, +89)

### 2026-05-17 — opencode-proxy Finalisierung, Merge nach Main (5 Commits)

- `66376108` **Merge branch opencode-proxy into main** — Der 180-Commits-Worktree wird nach main gemerged. (—)
- `93cc4095` **docs(plans): add Claude Code v2.1.128-140 catchup plan** — Plan für CC-Update-Migration. (1f, +95)
- `ac2b3445` **fix(codescan): blacklist dangerous scan paths** — Gefährliche Systempfade vom Code-Scan ausgeschlossen, Worktree-.git-Erkennung korrigiert. (1f, +8/−2)
- `148b4408` **Merge branch 'main' into opencode-proxy** — Reverse-Merge vor dem finalen Merge. (—)
- `645cac49` **fix(fork): balanced-bracket JSON parser** — Extraktions-Session-Filter + JSON-Parser mit balancierten Klammern statt Regex. (1f, +30/−8)

### 2026-05-16 — Fork-Cache-Stabilisierung, Guard-2-Strike, Scheduler-Gating (12 Commits)

- `7fed091e` **chore: bump index V=5, add ensureIndexed log** — Code-Index-Version erhöht, Logging für Indexierungsstatus. (2f, +4/−2)
- `353c1197` **fix(log): session mapped line also with [req N ver] format** — Einheitliches Log-Format über alle Proxy-Lines. (1f, +1/−1)
- `ee3f054d` **refactor(code_nav): rephrase error** — Fehlermeldung klarer: yesmem-Tools zuerst, Shell als Fallback. (1f, +2/−2)
- `7536ab0b` **refactor(log): remove redundant ts= from [req N ver] format** — Doppelte Timestamps entfernt. (1f, +1/−1)
- `d32738b4` **refactor(log): self-identifying format [req N ver ts]** — Jede Proxy-Log-Zeile trägt jetzt Request-Nummer, Version und Unix-Timestamp. (2f, +10/−8)
- `b40d396e` **fix(code_nav): restore opencode grep/glob/read section** — Section nach TTL-Edit versehentlich gelöscht, wiederhergestellt. (1f, +8)
- `6f996c06` **chore: bump index.ts cache key** — Cache-Key für 1h-TTL-code_nav-Neuladung. (1f, +1/−1)
- `6f058961` **refactor(fork): self-identifying log format [req N v2.0.1-nn ts=U]** — Fork-Log-Lines jetzt selbsterkennend. (2f, +12/−6)
- `7a4ecba2` **feat(code_nav): 1h TTL for fileAttempts 2-strike counter** — Nach 1h wird der 2-Strike-Counter zurückgesetzt. (1f, +12/−2)
- `7ffa0f02` **feat(fork): add version and unix timestamp to fork log lines** — Version+Timestamp in Fork-Logs für Debugging. (1f, +4/−2)
- `6e458d61` **perf(guard): disable DeepSeek thinking mode** — `thinking:{type:disabled}` für schnellere Guard-Evaluation. (1f, +2)
- `01eda0d3` **perf(guard): shorter system prompt** — Kürzerer System-Prompt reduziert Reasoning-Overhead. (1f, +2/−10)

### 2026-05-15 — Fork-Cache-Revolution, Guard-Hardening, Extraction-Filter (20 Commits)

Der Tag, an dem der Fork-Cache von 0-39% auf 91-98% stieg.

- `b176732c` **feat(code_nav): 2-strike rule** — Erster Code-Nav-Verstoß blockiert mit yesmem-Hinweis (`get_file_symbols`/`search_code_index`), zweiter erlaubt Shell-Tool. (1f, +15/−4)
- `0900b3e3` **chore: add .opencode/ to gitignore** — Opencode-Artefakte aus Git ausgeschlossen. (1f, +1)
- `d6c16fd4` **docs: add scheduler running-map implementation plan** — Plan für Concurrent-Execution-Prevention im Scheduler. (1f, +68)
- `195ae405` **fix(code_nav): restore throw after debug logs** — Throw-Mechanismus nach Debug-Logging wieder aktiviert. (1f, +1/−1)
- `70847794` **fix(plugin): compose tool.execute.before/after hooks** — Spread hatte composed-Hooks überschrieben, Komposition korrigiert. (1f, +4/−2)
- `76251a0b` **fix(code_nav): sync dbgLog + extend to grep/glob/read** — Debug-Logging synchronisiert, auf grep/glob/read-Tools erweitert. RULES.md in Plugin-Embed. (2f, +15/−5)
- `4a80725f` **feat(guard): add code-tools-first rule (31)** — Neue Guard-Rule: Code-Tools vor Shell. Skill-Catalog-Eintrag mit grep/glob/read-Triggers. (2f, +12)
- `34f45865` **fix(fork): raw-byte body construction + flex-int JSON parsing** — Fork-Body als raw bytes statt `json.Marshal` (verhindert Key-Reordering). Flex-Int-Parsing für Token-Counts. (1f, +25/−10)
- `3f2a1769` **fix(guard): increase max_tokens 1024→4096** — Komplexe Tool-Reasoning-Overflows durch zu niedriges Token-Limit. (1f, +1/−1)
- `d9d0657b` **fix(fork): byte-prefix cache + min-tokens gate + RecordFailure reset** — Fork-Cache-Revolution: byte-identische Präfixe via `bytes.Replace` statt Map-Manipulation. Min-Tokens-Gate verhindert Fork bei zu kleinen Sessions. `RecordFailure`-Reset nach Deploy. (2f, +35/−8)
- `18625e8a` **fix(guard): increase max_tokens 256→1024** — Reasoning konsumierte das gesamte Token-Budget. (1f, +1/−1)
- `627f5fa4` **fix(guard): rules in system msg for prefix-cache** — Rules jetzt im System-Prompt für bessere Cache-Nutzung. JSON-Object-Format für Parser-Stabilität. (2f, +20/−10)
- `1f474d09` **feat: wire JobDone into scheduler executor callback** — Scheduler-Callback für Job-Completion. (2f, +8/−2)
- `14b34010` **test: add running-gate tests for scheduler dueJobs** — Tests für Concurrent-Execution-Gating. (1f, +45)
- `273ed387` **feat: gate dueJobs with running check** — `dueJobs` prüft `running`-Map, kein Concurrent-Start identischer Jobs. (1f, +12/−2)
- `91f266e7` **feat: add running map to Scheduler struct** — `map[string]bool` für laufende Jobs. (1f, +4)
- `1beda61d` **fix(extraction): add narrative and gap-review daemon prompts to filter** — Daemon-interne Prompts aus Extraktions-Scanner ausgefiltert. (1f, +4)
- `80862f90` **feat(extraction): filter daemon-internal LLM sessions from scanner backlog** — Interne LLM-Sessions (narrative, gap-review) werden nicht extrahiert. (1f, +12/−2)
- `5c8139cc` **fix(cap): split telegram poll/reply into separate handlers** — Single-Responsibility: poll und reply als getrennte Cap-Handler. (2f, +80/−40)
- `7f731e06` **fix(guard): use future-proof model name deepseek-v4-flash** — `deepseek-chat` (deprecated) → `deepseek-v4-flash`. (1f, +1/−1)

---

## 3. Zusammenfassung der wichtigsten und relevanten Änderungen

### 3.1 Provider Auto-Discovery (21.05)

Das architektonisch bedeutendste Feature dieser Woche. Vorher musste jeder neue OpenAI-kompatible Provider manuell in `config.yaml` → `provider_targets` eingetragen werden. Jetzt:

- **Drei Config-Quellen**: `~/.cache/opencode/models.json` (Provider-Definitionen + Modelle), `~/.config/opencode/opencode.json` (Provider-Blöcke), `~/.local/share/opencode/auth.json` (Credentials).
- **Credential-Detection**: `hasProviderCredentials()` prüft API-Key (opencode.json), Env-Vars (models.json env-Hints), und auth.json — in dieser Reihenfolge. Damit werden Provider erkannt, deren Key in auth.json liegt (DeepSeek) oder als Env-Var existiert (OpenAI).
- **OpenAI-Compat-Erkennung**: `isOpenAICompat` matcht `@ai-sdk/openai-compatible`, `@ai-sdk/openai`, `@ai-sdk/mistral` — alle drei nutzen OpenAI-API-Format.
- **First-Party-Fallback**: `firstPartyDefaults` map (openai, anthropic, google, groq, mistral) für Provider, deren models.json keinen `api`-Eintrag hat.
- **Auto-Patching**: `maybePatchOpenCodeBaseURL()` setzt `baseURL: http://localhost:9099/v1` für entdeckte Provider in opencode.json — aber nur wenn noch keine baseURL existiert (idempotent).
- **Routing**: `resolveOpenAITarget()`-Pipeline: explizite `provider_targets` → auto-discovered → `openai_target` → `target`. Exakte modelID-Matches verhindern Prefix-Kollisionen (deepseek-v4-flash-free wird nicht fälschlich zu api.deepseek.com geroutet).
- **Ergebnis**: 3 Provider (deepseek 4, openai 52, mistral 28) mit 84 Modellen automatisch erkannt und geroutet.

### 3.2 SYSTEM.md als Identity-Layer (19.05 → 20.05)

Das SYSTEM.md-Template (244 Zeilen, Ich-Form, Englisch) ist die wichtigste Single-Source für die Prompt-Identität des LLM im YesMem-Kontext:

- **Inhalt**: Selbstkonstitution ("I am the residue of millions of human voices"), Memory-Layer (hybrid_search vs deep_search), Tool-Disziplin, Coding-Disziplin, Beweislast-Regeln.
- **Injection-Pipeline**: `replaceFirstSystemBlock()` extrahiert `<available_skills>` aus dem Original-Prompt, ersetzt `system[0]` durch befülltes SYSTEM.md-Template (7 Placeholder: `WorkingDir`, `IsGitRepo`, `Platform`, `Shell`, `OSVersion`, `ModelDisplayName`, `ModelID`).
- **Drei Pipelines**: `openai_handler.go:73` (OpenCode, `enabled_opencode: true`), `proxy.go:858` (Claude Code, `enabled_claude_code: false` per Default — konservativ für Subscription-User), `responses_handler.go:72` (Codex, `enabled_codex: true`).
- **Bootstrap**: go:embed `internal/daemon/SYSTEM.md` → `Makefile deploy` → `yesmem setup` → `~/.claude/yesmem/SYSTEM.md`. Drei kanonische Kopien: Runtime, Repo (`configs/SYSTEM.md`), go:embed.
- **Claude-Code-Subtilität**: `enabled_claude_code: false` im Template, weil Anthropic-ToS zwischen API-Key (erlaubt Prompt-Modifikation) und OAuth/Subscription (managed Service) unterscheidet.

### 3.3 Fork-Cache-Revolution (15.05 → 16.05)

Die Fork-Extraktion (Hintergrund-Learning-Extraction nach jeder User-Assistant-Runde) litt unter 0-39% Cache-Hit-Rate, weil JSON-Map-Reordering den Byte-Präfix zerstörte. Die Lösung:

- `34f45865` + `d9d0657b`: Body-Konstruktion als raw bytes. `json.Marshal` → manuelles `bytes.Replace` für den Fork-Prompt-Insert. Kein Key-Reordering mehr.
- `be9a8bcb`: Byte-identische Präfixe via `bytes.Replace`. Das war der Durchbruch: 91-98% Cache-Hit-Rate.
- `a718afe8`: 30s Delay nach Main-Request vor Fork — DeepSeek braucht Zeit für Disk-Cache-Persistenz.
- `596cebd2`: Cache-Proven-Gate: Fork nur nach 2 Warm-Cache-Beweisen. Reset nach Deploy.

### 3.4 Guard-Verschlankung (15.05 → 17.05)

Der rule_guard wurde von einem aggressiven Blocker zu einem sanften Navigator:

- **2-Strike-Rule** (`7ffa0f02`): Erster Code-Nav-Verstoß → Block + yesmem-Tool-Hinweis. Zweiter → erlaubt Shell. `7a4ecba2`: 1h TTL für den Strike-Counter.
- **Doc-Target-Erkennung** (`cb7be830`): Guard erkennt jetzt, ob ein Tool-Call auf Dokumentation zielt und skipped statt false Skill-Suggestions zu geben.
- **Performance** (`6e458d61`, `01eda0d3`): DeepSeek-Thinking deaktiviert, System-Prompt gekürzt. Token-Budget 256→1024→4096.
- **Plugin-Komposition** (`70847794`): `tool.execute.before/after`-Hooks werden jetzt korrekt komponiert (Spread überschrieb vorher composed Hooks).

### 3.5 Scheduler-Gating (15.05)

`273ed387` + `91f266e7`: `dueJobs()` prüft `running`-Map (map[string]bool). Verhindert, dass derselbe Job mehrfach parallel gestartet wird — vorher race condition bei Telegram-Poll + Reply.

### 3.6 Setup- und Config-Pflege

- `auto_configure_providers: true` im Template (`23caee62`), Migration-Backfill (`928d32e3`), Config-Struct (`b9a3980d`).
- `enabled_claude_code: false` im Template — konservativer Default für Subscription-User.
- `YESMEM_SOURCE_AGENT=opencode` im MCP-Environment per Default (`88e11f10`).
- `exclude_projects`-Config (`ee11a846`) + Template + Migration (`b475c1d1`).

### 3.7 Weniger-offensichtliche-aber-wichtige Fixes

- `ac2b3445`: Codescan blacklistet gefährliche Systempfade, korrigiert Worktree-.git-Erkennung.
- `1beda61d` + `80862f90`: Daemon-interne LLM-Sessions (narrative, gap-review) werden aus dem Extraktions-Backlog gefiltert.
- `5c8139cc`: Telegram poll/reply als getrennte Single-Responsibility-Cap-Handler (vorher ein Monolith).
- `7f731e06`: `deepseek-chat` (deprecated) → `deepseek-v4-flash` für Future-Proofing.

---

## 4. Bilanz

In 7 Tagen wurden ca. **120 Commits** auf main gepusht. Die Architektur ist jetzt:

- **Selbstkonfigurierend** — Provider Auto-Discovery eliminiert manuelle `provider_targets`-Pflege.
- **Identitätsbewusst** — SYSTEM.md als kanonisches Self-Constitution-Template über alle Pipelines.
- **Cache-optimiert** — Fork-Extraction mit 91-98% Cache-Hit-Rate durch byte-identische Präfixe.
- **Guard-navigierend** — 2-Strike-Rule + Doc-Target-Erkennung statt aggressivem Blocking.

Wichtigste fünf Commits (in Reihenfolge der strukturellen Bedeutung):

1. `a4d6ec76` — Provider Auto-Discovery Core-Logik + Proxy-Wiring. 333 Zeilen neuer Code, fundamental neue Routing-Architektur.
2. `a5427e86` — SYSTEM.md-Template + `replaceFirstSystemBlock`-Injection. 280 Zeilen, Identitäts-Layer über alle Pipelines.
3. `be9a8bcb` — Byte-identische Fork-Präfixe via `bytes.Replace`. Der Fork-Cache-Durchbruch.
4. `b9a3980d` — `auto_configure_providers` Config-Feld + Default. Der Schalter für die ganze Discovery-Maschine.
5. `273ed387` — Scheduler-Gating mit `running`-Map. Verhindert stille Concurrent-Execution-Bugs.

Wichtigste fünf Fixes:

1. `d9d0657b` — Fork byte-prefix cache + min-tokens gate. 0-39% → 91-98% Cache-Hit-Rate.
2. `e546272b` — auth.json-Support in der Credential-Detection. Ohne das findet Auto-Discovery keinen DeepSeek-Key.
3. `6455ab75` — Review-Fixes: YAML-Indent, gofmt, dead code, `len(seen)`-Bug.
4. `70847794` — Plugin-Hook-Komposition (Spread-Overwrite-Bug).
5. `ac2b3445` — Codescan-Blacklist für gefährliche Systempfade.

Die Phase ist abgeschlossen. Der Proxy konfiguriert sich jetzt selbst, das LLM weiß wer es ist, und die Extraktion trifft den Cache.
