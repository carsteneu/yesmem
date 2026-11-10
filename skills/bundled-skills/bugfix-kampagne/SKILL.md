---
name: bugfix-kampagne
description: Use when working on the yesmem Bugfix-Kampagne (Feature-Audit, Jira YM, Board 164, Verfahren YM-1, docs/bugs.md, Lose aus batches.py, Kampagnen-yesloop-Läufe) and the question is where to start, in which order to work, what to check before accepting a DONE, or what may be done without asking. Trigger on "Bugfix-Kampagne", "Jira YM", "Board 164", "YM-1", "Los", "Welle", "docs/bugs", "batches", "Audit-Befund".
---

# Bugfix-Kampagne yesmem

Diese Skill trägt den Ablauf der Kampagne: Einstieg, Reihenfolge, Prüfpflichten, Freigabegrenzen. Den Stand trägt sie nicht. Jira und die Learning-Kette führen ihn, und beide verändern sich ohne diese Datei.

## Einstieg

1. `get_learnings(id=102019)` lesen. Das ist der Einstieg der Kampagne. Ist es superseded, dem Nachfolger folgen und die Kette weiterlesen, bis ein Learning nicht mehr superseded ist. Das jüngste Learning der Kette nennt den aktuellen Zustand: laufende Welle, offene Lose, offene Entscheidungen.
2. Ursprungs-Session: `opencode:ses_ebc14c083ffeX3thcK0w81q4OZ` (volle ID, kein Präfix-Matching).
3. Jira ist der führende Stand. Projekt YM, Board 164, Verfahren YM-1. Widerspricht ein Learning dem Board, gilt das Board.
4. Lose bauen auf `.yesmem/audit/batches.py` (gitignored, im Hauptrepo, nicht in Worktrees). `--check` prüft Vollständigkeit und Dateikonflikte je Welle.
5. Weitere Werkzeuge im selben Verzeichnis: `briefs.py` (Brief je Los), `jira_step.py` (Jira-Status und Kommentar), `classify_abnahme.py` und `verify_abnahme.py` (Abnahme).

## Reihenfolge

1. Los wählen: 3 bis 6 Tickets, dateidisjunkt zu jedem laufenden Loop (Dateien, nicht Themen). `batches.py --check` prüft Vollständigkeit und Konflikte der Welle. Bis zu 10 Loops gleichzeitig. Agent-Bugs, die die Loops selbst treffen, werden in Phase 1 des Loops vorgezogen.
2. Brief schreiben, mit den Entscheidungen aus der Learning-Kette, damit der Agent nicht neu verhandelt.
3. Loops spawnen, NACHEINANDER, je Loop ein eigener Worktree und Branch. Die Spawn-Pflichten stehen in der yesloop-Skill.
4. DONE prüfen, nach den Prüfpflichten unten. Erst dann gilt ein Ticket als gearbeitet.
5. Nach main mergen, dann die Suite auf main laufen lassen. main bewegt sich durch andere Loops, deshalb vor dem Merge den Branch gegen den aktuellen main nachziehen.
6. `make deploy` erst, wenn kein Loop läuft. Rollback über `yesmem.pre-*` oder `git revert` bereithalten.
7. Live-Prüfung am laufenden System, am Verhalten und nicht am Versionsstring.
8. Jira auf Erledigt, mit Label `verified-live` und dem Commit im Ticket. Ohne Live-Beleg bleibt der Status auf Abstimmung.
9. Nach Bitbucket (origin) pushen.
10. Agent schließen, Worktree entfernen.

## Prüfpflichten

Abnahme auf Commit plus eigenem Testlauf, nie auf Agentenstatus. Ein Agent, der idle oder paused meldet, ist nicht fertig. Den DONE-Kontrakt selbst definiert die yesloop-Skill; hier stehen die Pflichten, die die Kampagne darüber hinaus verlangt.

1. Phase 5 des Loops vollständig: `self-review`, dazu `cold-review`, `consequence` und `security` mit Session-IDs. Fehlt der Cold Review, ist der DONE nicht abnehmbar.
2. Diff selbst lesen, nicht die Zusammenfassung des Agenten.
3. Tests selbst laufen lassen: betroffene Pakete, `go vet`, Build. Wo der Loop eine Daemon-Suite behauptet, im PID-Namespace nachfahren.
4. Roter Regressionstest: der RED-Lauf stammt vom unpatched Stand und liegt als Ausgabe vor, nicht als Behauptung.
5. proof-Zeile je Jira-Key im DONE-Bericht. Eine Behauptung ohne proof gilt als UNVERIFIED und wird nicht abgenommen.
6. Nach dem Merge Kombinationstest auf main, nicht nur der Loop-Lauf im Worktree.
7. Escaped Findings werden eigene Tickets. Sie verschwinden nicht in der Review-Diskussion.
8. Ticketbeschreibungen sind Behauptungen. Lag der Fehler im Leser statt im Schreiber oder war die Wegbeschreibung falsch, wird das im Ticket korrigiert, nicht stillschweigend anders gebaut.

## Freigabegrenzen

Stehende Freigabe des Nutzers für diese Kampagne, gilt bis Abschluss oder Widerruf. Der Orchestrator der Kampagne darf ohne Rückfrage:

1. YesLoop-Branches nach unabhängiger Prüfung (Tests selbst gelaufen, Diff gelesen, Cold Review vorhanden) lokal nach main mergen.
2. `make deploy` ausführen, nur wenn kein Loop läuft, danach Live-Prüfung, Rollback bereithalten.
3. Nach Bitbucket (origin) pushen.

Nicht erlaubt: GitHub- oder Public-Sync, `yesmem setup`, `gh pr merge`.

Zwei weitere Grenzen folgen aus dem Verfahren, nicht aus dieser Freigabe: ohne Live-Beleg wird kein Ticket auf Erledigt gesetzt, und ein laufender Loop wird nicht ohne Grund beendet.

Agenten mergen und deployen nie, auch nicht bei grünen Tests. Merge, Deploy und Push bleiben Orchestrator-Sache.

## Verweise

- yesloop-Skill: Pipeline, Blockformate, DONE-Kontrakt und der Abschnitt "## Orchestrator-Pflichten" (Spawn-Briefing, DONE prüfen, Modell, Schließen, kein Selbst-Merge). Wird hier nicht wiederholt.
- `docs/bugfix-kampagne-ym.md`: kurzer Verweis auf diese Skill und auf die Learning-Kette.
- Jira YM-1: Verfahren der Kampagne, Statuskette, proof je Jira-Key.
