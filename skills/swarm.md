# /schwarm — Multi-Agent Orchestrierung

> YesMem Skill: Autonome Agent-Schwärme für Recherche, Analyse und Content-Erstellung.

## Aufruf

### Guided Mode (Anfänger)
```
/schwarm
```
Stellt interaktiv Fragen bevor der Schwarm startet:
1. **Aufgabe** — Was soll erledigt werden?
2. **Agents** — Wie viele Sub-Agents, welche Perspektiven/Rollen, du machst sinnvolle Vorschläge!
3. **Output** — Format (Markdown, JSON, Code, Präsentationen z.b. mit reveal.js oder anderen Officedokumenten, ) UND wo soll das Ergebnis hin?
4. **Limits** — Token-Budget, Max-Runtime pro Agent?

### Direct Mode (Erfahrene)
```
/schwarm "3 Agents: Banane wirtschaftlich, gesundheitlich, historisch → Markdown Report"
```
Parsed den Auftrag direkt und spawnt sofort.

---

## Rollen

| Rolle | Kennung | Aufgabe |
|-------|---------|---------|
| **Hauptchat** | Agent 1 | User-Interface, startet/monitort den Schwarm |
| **Orchestrator** | Agent A | Koordiniert Sub-Agents, sammelt Ergebnisse, spawnt Report-Agent |
| **Sub-Agents** | Agent B, C, D... | Führen Teilaufgaben aus (Recherche, Analyse, etc.) |
| **Report-Agent** | Letzter Agent | Erstellt finales Artefakt aus allen Teilergebnissen |

---

## Protokoll

### 1. Projekt-Setup (Agent 1)

```
1. Projektname wählen (z.B. "frucht-report")
2. Arbeitsverzeichnis bestimmen (siehe Arbeitsverzeichnis-Modi)
3. Auftrag ins Scratchpad schreiben:
   scratchpad_write(project="{projekt}", section="auftrag", content=...)
4. Orchestrator spawnen:
   spawn_agent(project="{projekt}", section="orchestrator",
               caller_session=MEINE_SESSION, work_dir="{verzeichnis}")
5. FERTIG — zurück zum User (Fire & Forget)
```

**Fire & Forget ist der Standard.** Agent 1 (Hauptchat) wartet NICHT auf den Schwarm. Nach dem Spawn geht die Kontrolle sofort zurück an den User. Der Orchestrator arbeitet vollständig autonom. Wenn der Schwarm fertig ist, kommt eine `send_to`-Benachrichtigung an Agent 1 — der User sieht sie beim nächsten Turn.

Optional kann der User jederzeit den Status prüfen:
- `list_agents(project="{projekt}")` — welche Agents laufen noch?
- `scratchpad_list(project="{projekt}")` — welche Ergebnisse liegen vor?

- MCP-Permissions werden automatisch konfiguriert
- Alle Sub-Agents erben das Arbeitsverzeichnis des Orchestrators

### Arbeitsverzeichnis-Modi

| Modus | Wann | work_dir |
|-------|------|----------|
| **Neues Projekt** | Recherche, Reports, eigenständige Artefakte | `~/projects/{projekt}/` (auto-erstellt) |
| **Bestehendes Projekt** | Feature implementieren, API bauen, Code ändern | Aktuelles Projektverzeichnis (z.B. `/home/user/projects/myapp/`) |
| **Benutzerdefiniert** | Spezialfälle | Beliebiger Pfad via `work_dir` Parameter |

**Im Guided Mode** fragt der Skill:
```
Wo soll gearbeitet werden?
  1. Neues Projektverzeichnis (~/projects/{name}/) — für Reports, Recherche
  2. Hier (aktuelles Verzeichnis) — für Features, Code-Änderungen (Empfohlen wenn im Projekt)
  3. Anderer Pfad...
```

**Im Direct Mode:**
```
/schwarm "Feature: REST API für User-Management → Code im aktuellen Projekt"
/schwarm (dir:~/other/path) "Analyse der Logdateien → JSON Report"
```

**Wichtig bei Arbeit im bestehenden Projekt:**
- Agents erstellen Feature-Branches vor Änderungen
- Code-Agents nutzen die bestehende Projektstruktur (keine neuen Toplevel-Verzeichnisse)
- Tests werden mitgeschrieben (TDD wenn im CLAUDE.md des Projekts gefordert)
- Ergebnisse als Commits, nicht als lose Dateien

### 2. Orchestrator-Ablauf (Agent A)

```
0. whoami() — eigene Session-ID ermitteln (IMMER als erstes!)
1. scratchpad_read(project, section="auftrag") — Auftrag lesen
2. Für jeden Sub-Agent:
   a. scratchpad_write(project, section="auftrag-{name}", content=...) — Auftrag schreiben
   b. spawn_agent(project, section="{name}", caller_session=MEINE_SESSION)
3. Warten auf send_to von allen Sub-Agents
4. Ergebnisse aus Scratchpad lesen
5. Fertige Sub-Agents stoppen: stop_agent(project, to="{name}")
6. Report-Auftrag schreiben + Report-Agent spawnen
7. Auf finales Ergebnis warten
8. Report-Agent stoppen
9. send_to(caller_session) — Hauptchat benachrichtigen, dann **passiv warten auf stop_agent()** — KEIN ACK, keine weitere Aktion
```

### 3. Sub-Agent-Ablauf (Agent B, C, ...)

```
0. whoami() — eigene Session-ID ermitteln (für send_to Callbacks)
1. scratchpad_read(project, section="auftrag-{mein-name}") — Auftrag lesen
2. Aufgabe ausführen (WebSearch, Analyse, Code, etc.)
3. Ergebnis strukturiert ablegen:
   scratchpad_write(project, section="ergebnis-{mein-name}", content=...)
4. send_to(caller_session) — Orchestrator benachrichtigen: "Fertig"
5. Warten (Orchestrator stoppt mich)
```

### 4. Report-Agent-Ablauf (Agent D)

```
1. scratchpad_read(project, section="auftrag-report") — Auftrag + Quellen lesen
2. Alle ergebnis-Sections lesen
3. Finales Artefakt erstellen
4. Als DATEI im Projektverzeichnis speichern:
   Write(~/projects/{projekt}/{dateiname}.{format})
5. Zusätzlich ins Scratchpad:
   scratchpad_write(project, section="final-report", content=...)
6. send_to(caller_session) — Orchestrator benachrichtigen
```

---

## Ergebnis-Formate

| Format | Dateiendung | Wann verwenden |
|--------|------------|----------------|
| **Markdown** | `.md` | Reports, Analysen, Dokumentation |
| **JSON** | `.json` | Strukturierte Daten, API-Responses |
| **Code** | `.go`, `.py`, etc. | Implementierungen, Scripts |
| **HTML** | `.html` | Web-Content, Präsentationen |

Der User definiert das Format vorab. Der Report-Agent hält sich daran.

---

## DAG-Modus (Execution Dependencies)

Nicht alle Agents können parallel starten. Manchmal braucht Agent C das Ergebnis von A und B bevor er beginnen kann:

```
Planner → Research (parallel) → Implementer → Reviewer
        → Design   (parallel) ↗
```

Research und Design starten parallel nach Planner. Implementer wartet bis BEIDE fertig sind. Reviewer wartet auf Implementer.

### Realisierung: Emergent über Broker (kein Code nötig)

Der Orchestrator schreibt den Execution-Plan ins Scratchpad:

```
scratchpad_write(project, section="execution-order", content="""
## Execution Order
1. research + design (parallel, sofort starten)
2. implement (wartet auf DONE von research UND design)
3. review (wartet auf DONE von implement)
""")
```

Jeder Agent liest beim Start den Plan, sieht seine Vorbedingungen, und wartet über send_to auf die entsprechenden DONE-Signals. **Keine Code-Änderung nötig** — rein prompt-basiert.

### Warum emergent statt statisch?

- Nutzt den bestehenden Heartbeat-Broker (Message Delivery)
- Dynamischer als statischer DAG — Agent kann zur Laufzeit entscheiden ob er wirklich warten muss
- Kein `depends_on`-Feld in der DB nötig
- Debugging über `list_agents()` + Scratchpad: man sieht welcher Agent auf wen wartet

### Optionaler Fallback: Explizite Dependencies

Für deterministische Workflows kann der User Dependencies im Aufruf angeben:

```
/schwarm --project yesmem \
  --tasks "research,design,implement,review" \
  --depends "implement:research+design, review:implement"
```

Der Orchestrator parst die Dependencies und schreibt sie als `execution-order` ins Scratchpad. Agents verhalten sich identisch — der einzige Unterschied ist wer den Plan schreibt (User vs. Orchestrator).

---

## Reliable Message Delivery (Daemon-Enforced)

Die Zustellung von `send_to`-Nachrichten ist **garantiert** — nicht behavioral, sondern vom Daemon enforced.

**Ablauf:**
1. `send_to()` speichert Nachricht in DB → gibt `message_id` zurück
2. Heartbeat (alle 10s) holt alle `delivered=0` Messages für laufende Agents
3. Socket-Inject → bei Erfolg: `delivered=1`, `delivered_at` gesetzt
4. Bei Fehlschlag: `delivery_retries++`, nächster Heartbeat-Zyklus versucht erneut
5. Nach **5 Fehlversuchen**: `delivery_failed=1`, Sender wird benachrichtigt:
   `"DELIVERY_FAILED: Nachricht an {section} konnte nicht zugestellt werden nach 5 Versuchen."`

**Für den Orchestrator bedeutet das:**
- Keine manuelle Retry-Logik nötig — der Daemon kümmert sich
- Bei `DELIVERY_FAILED`: Agent ist vermutlich tot → Crash-Recovery greift parallel
- `message_id` aus `send_to` Response kann zur Nachverfolgung genutzt werden

---

## Scratchpad — Das geteilte Notizbrett

Das Scratchpad ist der zentrale Kommunikationskanal zwischen allen Agents. Es ist eine persistente Key-Value-Struktur pro Projekt, organisiert in benannten Sections.

**Wie es funktioniert:**
- Jede Section hat einen Namen (z.B. `auftrag-wirtschaft`), einen Owner (Session-ID des Schreibers) und beliebig langen Text-Content
- Sections sind für ALLE Agents im selben Projekt lesbar — kein Access-Control
- `scratchpad_write()` erstellt oder überschreibt eine Section (Upsert)
- `scratchpad_read()` liest eine oder alle Sections
- `scratchpad_list()` zeigt alle Sections mit Größe und Timestamp
- `scratchpad_delete()` löscht eine Section oder das ganze Projekt

**Wann Scratchpad, wann Datei?**
- **Scratchpad** für Koordination: Aufträge, Status-Updates, Zwischen-Ergebnisse, kurze Texte
- **Dateien im Projektverzeichnis** für finale Artefakte: Reports, Code, Präsentationen, alles was der User am Ende bekommt

**Wichtig:** Das Scratchpad ist kein Dateisystem. Es ist ein flüchtiges Nachrichtenbrett. Nach Abschluss des Schwarms können die Sections gelöscht werden — die finalen Ergebnisse liegen als Dateien im Projektverzeichnis.

---

## Kommunikation

### Scratchpad-Sections (Konvention)

| Section | Schreiber | Inhalt |
|---------|-----------|--------|
| `auftrag` | Agent 1 | Gesamtauftrag für Orchestrator |
| `auftrag-{name}` | Orchestrator A | Teilauftrag für Sub-Agent |
| `ergebnis-{name}` | Sub-Agent | Recherche-Ergebnis |
| `auftrag-report` | Orchestrator A | Auftrag für Report-Agent |
| `final-report` | Report-Agent | Finaler Report (Kopie) |
| `{agent-name}` | Jeder Agent | Status-Updates |

### Benachrichtigungen (send_to + ACK)

Nachrichten zwischen Agents laufen über `send_to()`. Der Heartbeat relayed sie alle 2 Sekunden an den Empfänger. **Zustellung ist nicht garantiert** — der Empfänger muss bestätigen.

**Protokoll:**
1. Sender: `send_to(target=EMPFÄNGER_SESSION, content="ERGEBNIS: ergebnis-wirtschaft fertig")`
2. Heartbeat relayed die Nachricht in den Empfänger-Terminal
3. Empfänger liest, verarbeitet, und bestätigt: `send_to(target=SENDER_SESSION, content="ACK: ergebnis-wirtschaft erhalten")`

**Regeln:**
- Jede send_to-Nachricht die eine Aktion erwartet MUSS mit ACK beantwortet werden
- Prefix-Konvention: `ERGEBNIS:`, `STATUS:`, `FEHLER:`, `ACK:`
- Wenn nach 60s kein ACK kommt: erneut senden (max 2 Retries)
- Bei Status-Updates (rein informativ) ist kein ACK nötig
- **`ACK:`-Nachrichten werden NIEMALS bestätigt** — kein ACK auf ein ACK, nie
- **Nachrichten vom Hauptchat (Agent 1) lösen NIE ein ACK aus** — der Hauptchat sendet kein ACK, und der Orchestrator antwortet nicht darauf; nach dem FERTIG-Signal nur passiv warten auf `stop_agent()`

**Beispiel-Flow:**
```
B → A: "ERGEBNIS: ergebnis-wirtschaft fertig, 3.8KB"
A → B: "ACK: ergebnis-wirtschaft erhalten"
A ruft stop_agent(B) auf
```

---

## Geduld — Die wichtigste Eigenschaft des Orchestrators

Agents brauchen Zeit. WebSearch, Analyse, Report-Erstellung — das dauert Minuten, nicht Sekunden. Der Orchestrator (Agent A) MUSS geduldig sein.

**Regeln:**
- **Keine voreiligen Schlüsse.** Wenn ein Sub-Agent nach 60 Sekunden noch kein Ergebnis geliefert hat, heißt das NICHT dass er hängt. Er arbeitet.
- **Nicht nachfragen bevor 3 Minuten vergangen sind.** Erst nach 3 Minuten ohne jegliche Scratchpad-Aktivität ODER send_to darf der Orchestrator nachhaken.
- **Timestamps prüfen, nicht schätzen.** Die `created_at` und `heartbeat_at` Felder in `list_agents()` zeigen die echte Laufzeit. Nicht die eigene Wahrnehmung als Maßstab nehmen.
- **Kein Abbruch wegen Ungeduld.** Nur bei echten Fehlern (Crash, Freeze, Timeout) eingreifen — nicht weil es "lange dauert".
- **Parallel weiterarbeiten.** Während B und C recherchieren, kann A den Report-Auftrag schon vorbereiten oder Status-Updates ins Scratchpad schreiben.

**Typische Laufzeiten:**
| Aufgabe | Erwartete Dauer |
|---------|----------------|
| WebSearch + Zusammenfassung | 1–3 Minuten |
| Code-Analyse | 2–5 Minuten |
| Report-Erstellung aus Quellen | 1–2 Minuten |
| Komplexe Multi-Source-Recherche | 3–5 Minuten |

---

## Proaktiver Download

Agents dürfen — und sollen — relevante Dokumente, Quellen und Materialien proaktiv in das Projektverzeichnis herunterladen, wenn sie für das Ergebnis nützlich sind.

**Erlaubt:**
- Webseiten als Referenz speichern (`WebFetch` → Datei)
- Recherche-Quellen als Quellensammlung ablegen
- Zwischenergebnisse, Rohdaten, Statistiken als Dateien sichern
- Bilder, Diagramme, Charts wenn für den Report relevant

**Konvention:**
```
~/projects/{projekt}/
├── sources/          ← Quelldokumente, heruntergeladene Referenzen
├── data/             ← Rohdaten, Statistiken, JSON
├── assets/           ← Bilder, Diagramme, Medien
└── {report}.{format} ← Finales Artefakt
```

**Regeln:**
- Unterverzeichnisse selbstständig anlegen wenn nötig
- Dateinamen beschreibend: `sources/fairtrade-bananen-statistik-2024.md`, nicht `source1.txt`
- Keine sensiblen Daten oder Credentials herunterladen
- Im finalen Report auf heruntergeladene Quellen verweisen

---

## Lifecycle-Management

### Cleanup-Pflicht

Der Orchestrator ist verantwortlich für das Aufräumen:

1. Sub-Agent meldet "fertig" → Orchestrator ruft `stop_agent(to="{name}")` auf
2. Report-Agent meldet "fertig" → Orchestrator ruft `stop_agent(to="report-writer")` auf
3. Orchestrator beendet sich selbst als letztes (oder wird vom Hauptchat gestoppt)

**Keine Zombie-Agents.** Jeder gestartete Agent wird explizit gestoppt.

### Crash-Recovery (automatisch)

Der Daemon überwacht alle laufenden Agents automatisch per Health-Check (alle 30s PID-Prüfung). **Kein Agent muss das anstoßen.**

**Ablauf bei Crash:**
1. Health-Check erkennt toten Prozess (PID existiert nicht mehr)
2. **Sofort-Quarantäne**: Alle Learnings der gecrachten Session werden isoliert (`quarantine_session`) — verhindert Kontamination über Briefing und hybrid_search
3. **Scratchpad-Taint**: Ergebnis-Sections des Agents werden mit `[TAINTED]`-Prefix markiert — andere Agents sehen sofort, dass die Daten unzuverlässig sind
4. Daemon bereinigt Socket-Dateien, setzt `status=crashed`
5. **Auto-Retry** mit sauberer Session (neue session_id, kein kontaminiertes Briefing)
6. Nach **3 gescheiterten Versuchen**: `status=failed`, Caller bekommt Crash-Context:
   - Laufzeit des Agents
   - Letzter Scratchpad-Status (falls vorhanden)
   - Nachricht: `"FAILED: Agent 'X' nach 3 Versuchen abgestürzt. Laufzeit: 2m15s."`

**Warum Quarantäne vor Retry?**
Agents teilen State über yesmem — Learnings, Briefing, Scratchpad. Ohne Quarantäne:
- Der Retry bekommt das Briefing mit den Learnings der gecrachten Session → gleicher Fehler → Crash-Loop
- Andere Agents finden vergiftete Learnings via hybrid_search → Kontamination breitet sich aus
- Scratchpad-Sections könnten unvollständig sein → Report-Agent arbeitet mit kaputten Daten

**Was der Orchestrator tun muss:**
- Bei `FAILED`-Nachricht entscheiden: **Skip** (ohne diesen Teil weitermachen) oder **Abort** (`stop_all_agents(project)`)
- Retries passieren automatisch — der Orchestrator muss sich NICHT um Respawns kümmern
- Der Orchestrator soll NICHT selbst versuchen einen gecrachten Agent neu zu spawnen

### Notfall-Abbruch

Bei kritischen Fehlern kann der Orchestrator oder Hauptchat den gesamten Schwarm sofort beenden:

```
stop_all_agents(project="{projekt}")
```

Stoppt ALLE laufenden, frozen und spawning Agents im Projekt. Sendet `/exit` an jeden, bereinigt Sockets und DB-Einträge. Danach ist das Projekt sauber.

### Freeze-Handling

- Agent wird frozen (Budget/Runtime überschritten) → Orchestrator prüft
- Genug Ergebnis vorhanden → `stop_agent()`, weitermachen
- Zu wenig Ergebnis → `resume_agent()` mit erhöhtem Budget, oder Skip

---

## Limits (Defaults)

| Parameter | Default | Beschreibung |
|-----------|---------|-------------|
| `max_runtime` | 30m | Max Laufzeit pro Agent |
| `max_turns` | 300 | Max Interaktionen pro Agent |
| `max_depth` | 3 | Max Spawn-Tiefe (A→B→C) |
| `token_budget` | 500000 | Max Tokens (Input+Output) pro Agent |
| `model` | (inherit) | Modell pro Agent (siehe Modell-Wahl) |

Überschreibbar per `spawn_agent(token_budget=..., model=...)` oder Config.

### Backend-Wahl (Claude vs Codex)

Sub-Agents können verschiedene Backends nutzen:

```
spawn_agent(project="{projekt}", section="recherche", backend="codex")
```

| Backend | CLI | Stärken | Einschränkungen |
|---------|-----|---------|-----------------|
| `claude` (default) | Claude Code | YesMem-Proxy-Integration, Prompt-Cache, voller MCP-Zugriff | — |
| `codex` | OpenAI Codex | Anderes Modell (GPT-5.x), Sandbox-Execution, Second Opinion | Kein Proxy-Channel-Inject, nur MCP-Tools für Kommunikation |

**Codex-Agents kommunizieren ausschließlich über MCP-Tools** (scratchpad, send_to, remember) — nicht über Proxy-Injection. Das funktioniert weil YesMem als MCP-Server in Codex registriert ist.

### Modell-Wahl

Nicht jeder Agent braucht das teuerste Modell. **Agent A (Orchestrator) entscheidet** welches Modell für jeden Sub-Agent am besten passt — basierend auf der Budget-Strategie die der User vorgibt.

**Budget-Strategien:**

| Strategie | Orchestrator A | Sub-Agents | Report-Agent D |
|-----------|---------------|------------|----------------|
| **Sparsam** | Sonnet | Haiku | Sonnet |
| **Balanced** (Default) | Opus | Sonnet | Opus |
| **Qualität** | Opus | Opus | Opus |

Agent A darf von der Strategie abweichen wenn die Aufgabe es erfordert — z.B. einen einzelnen Sub-Agent auf Opus hochstufen wenn die Recherche komplex ist, oder auf Haiku runterstufen wenn es nur eine einfache Datenextraktion ist.

**Im Guided Mode** fragt der Skill:
```
Budget-Strategie?
  1. Sparsam — Haiku wo möglich, Sonnet für Kern
  2. Balanced — Sonnet Standard, Opus für Report (Empfohlen)
  3. Qualität — Opus überall
```

**Im Direct Mode** optional inline:
```
/schwarm (sparsam) "3 Agents: Banane wirtschaftlich, gesundheitlich, historisch → Markdown"
```
Ohne Angabe gilt "Balanced".

**Verfügbare Modelle:** `opus`, `sonnet`, `haiku`

---

## Beispiele

### Recherche-Schwarm
```
/schwarm "Recherchiere Elektromobilität: Agent B Technik, Agent C Markt, Agent D Politik → Markdown Report"
```

### Code-Review-Schwarm
```
/schwarm "Review des Auth-Moduls: Agent B Security-Audit, Agent C Performance, Agent D Best-Practices → JSON Report"
```

### Content-Schwarm
```
/schwarm "Blogpost über KI in der Medizin: Agent B Fakten-Recherche, Agent C Experten-Zitate, Agent D Schreibt den Artikel → HTML"
```

---

## Anti-Patterns

- **Kein Agent spawnt sich selbst neu** — nur der Orchestrator entscheidet über Retries
- **Keine direkte Agent-zu-Agent-Kommunikation** — immer über Scratchpad + send_to an Orchestrator
- **Keine Ergebnisse nur im Scratchpad** — finale Artefakte IMMER als Datei im Projektverzeichnis
- **Kein Schwarm ohne Orchestrator** — auch bei 1 Sub-Agent geht alles über A
- **Kein Orchestrator der selbst recherchiert** — A koordiniert nur, arbeitet nicht inhaltlich
