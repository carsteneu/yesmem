# Guard

> Ein Werkzeugaufruf rein, eine Entscheidung raus: der Guard wiegt einen Aufruf gegen
> die Regeln, bevor das Werkzeug läuft, und hält sich heraus, wenn er nicht urteilen
> kann.

## Was er ist

Der Guard ist ein Urteilskern mit zwei Anschlüssen. Der Kern liegt in `internal/guard`
und `internal/guardmodel`; er kennt Regeln, Modell, Prompt und Policy, aber keinen
Eingabestrom und keine Terminalausgabe.

| Anschluss | Vermittlung | Wer |
|---|---|---|
| Claude-Code-Hook | `PreToolUse`-Ereignis, Kern im selben Prozess (`internal/hooks`) | Der Hook liest den Aufruf, der Kern urteilt, der Hook rendert `hookSpecificOutput` |
| Daemon-RPC `guard_check` | Der Daemon ruft denselben Kern (`internal/daemon/handler_guard.go`) | Fuer Harnesses, die nicht Claude Code sind: das opencode-Plugin schickt Werkzeug, Argumente, Arbeitsverzeichnis und Kontext. Eine RPC, zwei Vorlagen: `purpose=guard` urteilt einen Aufruf, `purpose=nudge` sucht die passende Skill im Katalog. |

Der Adapter besitzt die Verdrahtung und die Darstellung, der Kern das Urteil. Beide
Anschlüsse urteilen deshalb mit demselben Modell, demselben Prompt und derselben
Regelmenge.

Das opencode-Plugin (`plugins/opencode-yesmem-v2/rule_guard.ts` und `skill_nudge.ts`)
ist ein solcher Adapter und haelt keine eigene Aufloesung mehr. Es waehlt das Ereignis,
baut den Kontext, den der Daemon nicht sieht (letzte sechs Gespraechszeilen,
Feature-Branch-Notiz, `_test.go`-Notiz, zusammen gekappt), ruft `guard_check` und
rendert das Urteil. Mit dem Aufruf gehen Werkzeugname, Argumente und
Projektverzeichnis ueber den lokalen Socket; bei einem Schreibzugriff sind die
Argumente der Dateiinhalt, denn eine Datei wird an ihrem Inhalt gemessen. Was im
Prompt davon ankommt, kappt der Kern (300 Zeichen Argumente, 4000 Zeichen Kontext),
und was im Adapter bleibt, haengt an opencode-Ereignis-IDs, die der Kern nie sieht:
der 5-Sekunden-Ereigniscache, die UNAVAILABLE-Notice und die SUGGEST-UEbergabe; dazu
die Ghost-Skill-Pruefung, die fragt, was auf dieser Maschine installiert ist. Wer
wissen will, welches Modell geurteilt hat, liest es aus der Antwort (`model`) und
nicht aus einer Annahme.

## Die Modellkette

Die Auflösung hat zwei Fragen zu beantworten: welcher Endpunkt mit welchem Schlüssel,
und welches Modell an diesem Endpunkt.

Endpunkt und Schlüssel, in dieser Reihenfolge:

1. `YESMEM_GUARD_URL` mit `YESMEM_GUARD_KEY` (und optional `YESMEM_GUARD_NPM`): ein
   ausdrücklicher Override gewinnt gegen alles, was die Installation hält.
2. Die Provider-Suche über `~/.cache/opencode/models.json` und
   `~/.local/share/opencode/auth.json`. Gesucht wird nach dem Modell, das der Guard
   genannt bekommt: `YESMEM_GUARD_MODEL`, dann `rule_guard.model`, dann
   `extraction.model`, zuletzt der eingebaute Standard. Ein Kandidat zählt nur, wenn
   sein Provider auch einen Schlüssel trägt.
3. `proxy.provider_targets` aus `config.yaml` mit dem opencode-Schlüssel `gateway`: so
   wird ein Gateway-Modell erreichbar, das in keiner Modellliste steht.
4. Ein Anthropic-Zugang (`auth.json`, `ANTHROPIC_API_KEY`, `primaryApiKey` aus der
   Claude-Konfiguration).

Das Modell, in dieser Reihenfolge:

1. `YESMEM_GUARD_MODEL`
2. `rule_guard.model`
3. Die Modellliste des Kontos: `GET /v1/models?limit=100`, die neuesten Haikus zuerst.
   Die Antwort wird 24 Stunden lang zwischengespeichert
   (`~/.claude/yesmem/guard_models.json`), und auch ein Fehlschlag wird als leere Liste
   für dieselbe Frist gemerkt: ein Endpunkt, der ausfällt, darf nicht bei jedem
   Werkzeugaufruf einen Timeout kosten. Der Schritt fällt eine Stufe die Liste hinunter,
   wenn der Endpunkt das gewählte Modell mit 404 oder 403 abweist.
4. `models.json`: das neueste Haiku nach `release_date`.
5. Die Notkonstante `claude-haiku-4-5-20251001`, für die erste Ausführung einer
   Offline-Installation, in der weder Kontoliste noch Registry lesbar sind.

Diese Kette gilt für den Anthropic-Pfad. Der OpenAI-kompatible Pfad behält das Modell,
auf das die Provider-Suche gelaufen ist, und fällt ohne Treffer auf
`deepseek-v4-flash` zurück: ein leerer Standard würde den Guard auf jeder
Nicht-Anthropic-Installation abschalten.

Das Anfrageformat entscheidet der Provider selbst, über sein `npm`-Modul und seinen
Host: `@ai-sdk/anthropic` oder ein Endpunkt auf `api.anthropic.com` spricht
`/v1/messages` mit `x-api-key`, alles andere `/v1/chat/completions` mit `Bearer`.
Optionale Felder (`response_format`, `thinking`) gehen nur an Provider, die sie kennen;
ein 400 auf dieser Stufe fällt eine Stufe der Leiter zurück, statt den Aufruf zu
verlieren.

## Fehlersemantik

Der Guard ist fail-open, mit einer Ausnahme: eine Anfrage, die der Modell-Endpunkt
abgelehnt hat, ist kein Urteil und keine Freigabe.

| Fall | Entscheidung |
|---|---|
| Keine Regeln zu lesen | `PASS` mit Grund (`no rules to judge against`) |
| Kein Credential | `PASS` mit Grund (`no guard credential`); der destruktive Vorcheck läuft trotzdem |
| Transportfehler | ein zweiter Versuch nach 500 ms, dann `PASS` mit Grund (`endpoint unreachable`) |
| Timeout | `PASS` mit Grund (`endpoint timed out`), ohne zweiten Versuch: das Budget ist verbraucht |
| HTTP 400, 401, 403, 404, 422 | `UNAVAILABLE` mit Status und Host; bei 403 und 404 zusätzlich eine Stufe die Haiku-Liste hinunter, wenn es eine gibt |
| HTTP 5xx oder 429 | ein zweiter Versuch nach 500 ms, dann `PASS` |
| Antwort ohne JSON oder außerhalb der Entscheidungsmenge | `PASS` mit Grund |
| `BLOCK` ohne Verstoß, `SUGGEST` ohne Vorschlag | `PASS` mit Grund (`answer outside the decision set`) |

Zwei Gedächtnisse halten die Kosten niedrig: ein Antwortcache von 5 Sekunden je
Regeltext, Aufruf, Werkzeug und Modell, und eine gemerkte Ablehnung von 60 Sekunden je
Endpunkt und Modell, damit ein abgelaufenes Credential nicht bei jedem Werkzeugaufruf
eine Runde kostet.

Der Grund für diese Richtung: ein Guard, der bei jedem Netzwerkfehler blockiert, legt
die Arbeit still, und ein Guard, der eine abgelehnte Anfrage als Freigabe durchreicht,
urteilt jahrelang gar nichts, während in jeder Zeile ein HTTP 400 steht. Beides ist
passiert, die zweite Form ist die teurere.

Ein Adapter liest die Ablehnung als `UNAVAILABLE` und verweigert dann `write`, `edit`
und `patch`; einen Shell-Aufruf laesst er laufen und legt eine Notice ins
Werkzeugergebnis, weil ein verweigertes `ls` die Faehigkeit nimmt, den Grund zu finden.
Ein Transportfehler des Daemons selbst ist kein Urteil: er faellt unter `PASS` mit
Grund. Diese Trennung darf nicht kippen, sonst blockiert jeder Daemon-Neustart alle
Schreibzugriffe.

## Die Durchsetzungsmenge

Nicht jede Regel darf einen Aufruf verhindern. Die Menge ist der Schnitt aus zwei
Dingen:

- dem Literal `{1, 2, 25, 32}` im Kern: LLM-Signatur in einer Commit-Nachricht, Secret
  im Aufruf, destruktiver Befehl, PR-Merge über CLI oder API
- der Decision Policy der **installierten** `RULES.md`

Eine Policy kann die Menge verengen und nie erweitern. Der Grund ist das Repository: ein
Projekt bringt seine eigene `RULES.md` mit, und eine Datei, die die Shell-Regeln als
Empfehlung erklärt, darf die Prüfungen nicht abschalten, die ohne Nachfrage laufen. Eine
`RULES.md` im Projektverzeichnis beeinflusst deshalb nur den Prompt, also das, was das
Modell liest, und nicht die Menge, gegen die vollstreckt wird.

### Woher der Prompt kommt

`LoadRules` setzt zwei Quellen zusammen:

1. **Die installierte `RULES.md`** (`dataDir/RULES.md`) trägt den Standard. Sie ist die
   einzige Stelle, an der die Standardregeln gepflegt werden, und sie ist nummeriert.
2. **`PROJECT_RULES.md` im Projektverzeichnis** nennt nur die Abweichungen des Projekts
   und trägt **keine Nummern**. `LoadRules` hängt sie als eigenen Block
   (`## Project Rules` bis `## End of Project Rules`) hinter den Standard, und
   `PromptRules` reicht den Block an das Modell weiter — ohne ihn würde er mit der
   Prosa herausfallen (siehe unten). Weil nichts darin nummeriert ist, kann eine
   Abweichung keine zitierbare Regel werden; eine versehentlich numerierte Zeile bleibt
   Prosa und landet nicht im Katalog.

Ein Projekt, das eine **vollständige eigene `RULES.md`** besitzt, wird weiterhin allein
danach beurteilt; `PROJECT_RULES.md` wird dann nicht gelesen. Das schützt die von Hand
geschriebenen Kataloge (opencode, papoo-infra, greenWebsite) davor, dass der Wizard
etwas in ihre Regeln mischt.

Eine Abweichungsdatei ist optional. Ein Projekt ohne Abweichungen braucht keine: der
Standard deckt es ab.

Vollstreckt wird je Werkzeug verschieden:

| Werkzeug | Ein `BLOCK` wird vollstreckt, wenn |
|---|---|
| `Write`, `Edit` (auch `patch`) | immer: eine Datei wird an ihrem Inhalt gemessen (der Prompt trägt Pfad und Argumente) |
| `Bash`, `REPL` | der Verstoß eine Regel der Policy-Menge zitiert |

Alles andere wird zu einem `SUGGEST` mit dem ursprünglichen Verstoß im Text. Der Grund
steht im Kern: der Guard sieht kein Gespräch und kann nicht beurteilen, ob der Nutzer
die Aktion in diesem Zug erlaubt hat. Ein `SUGGEST` prüft, ein `BLOCK` verhindert.

Ein `BLOCK`, der keine Regel zitiert, die im Prompt steht, verweigert nichts: der
Prompt enthält die `RULES.md`, also ist die Antwort untrusted input, und das Audit hat
einen vom Modell erfundenen "Rule 42" gesehen, der einen Schreibzugriff verweigerte,
den keine Regel beschrieb. Er verschwindet aber auch nicht, denn das Modell war über
etwas besorgt: aus dem `BLOCK` wird ein `SUGGEST` mit dem ursprünglichen Verstoß im
Text, also eine Notice, die der Adapter als "rule check did not block" rendert und der
Hook als `additionalContext`. Ein Katalog ohne nummerierte Regeln lässt sich so nicht
prüfen; dort bleibt die Entscheidung, wie sie kam.

Ein Katalog, der Prosa-Regeln mit einer nummerierten Liste mischt, wird nur über die
nummerierten Zeilen geurteilt (`PromptRules` schickt genau die). Ein Zitat außerhalb
davon ist damit nicht durch das gedeckt, was das Modell gelesen hat, und wird zur
Notice. Wer Prosa-Regeln im Prompt haben will, muss sie nummerieren — die einzige
Ausnahme ist der `## Project Rules`-Block aus `PROJECT_RULES.md`, den `PromptRules`
als eigenen Abschnitt mitnimmt, gerade weil er nicht nummeriert sein darf.

### Welche `RULES.md` die installierte ist

`LoadInstalledRules` liest der Reihe nach `dataDir/RULES.md`, dann den
Entwicklungs-Checkout daneben und zuletzt `$HOME/memory/yesmem/RULES.md`. Die erste
lesbare Datei gewinnt. `yesmem setup` legt die eigene Kopie unter `dataDir/RULES.md` an,
damit eine frische Installation überhaupt eine durchsetzbare Policy hat.

Ob eine vorhandene Kopie ersetzt wird, entscheidet ihre Herkunft, nicht der Zufall: die
erste Zeile trägt `<!-- yesmem-rules version=… body=sha256:… -->` mit dem Hash des
Rumpfes. Stimmt der Hash zum Rumpf, hat niemand die Datei angefasst, und eine neue
Standardregel darf sie ersetzen; sonst gehört sie dem Betreiber und bleibt unberührt —
auch ohne `.new`-Datei daneben. Eine Datei ohne diese Zeile (alles, was vor dieser
Änderung entstand) gilt als bearbeitet und wird nie überschrieben. Wer eine alte Kopie
einmal auf den Standardstand ziehen will, löscht sie und lässt `yesmem setup` erneut
laufen.

Zwei Folgen, die man kennen muss:

- Die installierte Kopie steht vor der Checkout-Kopie. Auf einer Maschine, auf der der
  Wizard aus dem Repository heraus lief, liest der Guard danach die generierte Datei,
  nicht die kuratierte. Das ist gewollt (installations-eigen statt repo-kontrolliert),
  macht aber die Regeln der kuratierten Datei für die Durchsetzung unerheblich, solange
  die installierte Kopie existiert.
- Ohne jede `RULES.md` urteilt der Guard nur deterministisch: der destruktive Vorcheck und
  das Lösch-Gate laufen weiter (beide lesen keine Regel), die Modellfrage entfällt. Mit der
  installierten Kopie ist er auf jedem Verzeichnis aktiv. Das ist die Absicht einer
  Installation, aber es ist ein Unterschied im Betrieb, kein Detail.

Der generierte Katalog muss die vier Nummern der Durchsetzungsmenge tragen, sonst
degradiert ein `BLOCK` auf eine Regel, die im Prompt nicht existiert, zur Notice. Ein
Test in `internal/setup` hält diese Invariante fest (`rule 1`, `2`, `25`, `32`).


## Der destruktive Vorcheck

Vor jeder Modellfrage und vor der Credential-Prüfung laufen fest verdrahtete Muster über
Shell- und REPL-Aufrufe: `rm -rf` auf Wurzel oder Home, Force-Push und `reset --hard` auf
`main`/`master`/`production`, `DROP TABLE`/`DATABASE`/`SCHEMA`, Fork-Bombe, `dd` und
`mkfs` auf ein Rohgerät, Umleitung auf ein Rohgerät, `chmod 777 /`, rekursives `chown`
auf `/`.

Der Vorcheck braucht kein Modell und keinen Schlüssel. Er läuft zuerst, weil er der
einzige ist, der von nichts Erreichbarem abhängt: eine Abo-Installation ohne API-Key
darf `rm -rf /` trotzdem nicht durchlassen.

## Das Lösch-Gate

Jeder Shell-Aufruf, der `rm` enthält, wird vor dem Modell vom Gate entschieden
(`internal/guard/deletion.go`), nicht von einer Regel und nicht von einem Zitat:

| Fall | Verhalten |
|---|---|
| `rm -r` / `rm -rf`, irgendwo | BLOCK, immer, ohne zweite Chance |
| `rm` ohne literales Ziel (`xargs rm -f`) | BLOCK, das Ziel ist nicht prüfbar |
| Ziel außerhalb der Projektwurzel | BLOCK, immer |
| Ziel nicht statisch auflösbar (Variable, Glob, Tilde, `{}`, Symlink) | BLOCK |
| Ziel in der Wurzel, erster Versuch der Sitzung | BLOCK mit Aufforderung zur Freigabe |
| Ziel in der Wurzel, erneuter Versuch mit Freigabewort | PASS |

Die Projektwurzel ist die Git-Wurzel des Projektverzeichnisses aus dem Aufruf, nie die
des `cd` im Befehl und nie das `workdir`-Argument: beide verschieben die Ziele, nicht die
Grenze. Das Freigabewort ist eine feste Liste (`freigabe`, `freigegeben`, `genehmigt`,
`approved`, `approval`) und zählt nur in einem Kommentar — der einen Zweck hat, den kein
Dateiname und kein Argument erfüllen kann. Der Zähler liegt pro Sitzung in
`guard_state.db` (`deletion_refusal`), weil der Hook für jeden Tool-Call ein neuer Prozess
ist.

Erkannt werden auch die Schreibweisen, die dieselbe Tat verstecken: Wrapper (`sudo`,
`xargs`, `timeout`), `find -exec`, `/bin/rm`, `bash -lc`, `eval`, Backtick- und
`$(...)`-Substitution, und für REPL-Zellen die Shell-Strings in `sh(...)` (quoting-bewusst,
ein `sh("…")` in einem Test-Fixture-String ist kein Aufruf). Eine freigegebene Löschung
spricht nur für sich: steht in derselben Zeile noch ein anderer Befehl, urteilt das Modell
über diesen weiter. Was das Gate nicht sieht — ein Skript, das eine Shell aus einer Datei
liest, ein Kommandoname in einer Variablen — bleibt bei den destruktiven Mustern und bei
Regel 25, die dort weiterhin durchsetzbar ist.

## Ohne Credential bleibt der Guard inaktiv

Ein Abo-Zugang ist kein API-Key. Der Eintrag in `auth.json` hat dann kein
Schlüsselfeld, und der Guard findet kein Credential: er antwortet `PASS` mit Grund und
urteilt nichts — außer über den destruktiven Vorcheck und das Lösch-Gate, die ohne Modell
entscheiden.

Das ist Absicht. Ein Abo-Token für API-Aufrufe zu borgen hieße, einen Vertrag zu
verletzen, den der Nutzer nicht geschlossen hat. Die Folge ist sichtbar und nicht still:
die Antwort nennt den Grund, und der Hook rendert ihn.

## Betrieb

- Der Antwortcache und die gemerkte Ablehnung sind prozessweit. Ein Neustart des
  Daemons ist immer eine saubere Lösung, wenn ein Urteil unerklärlich scheint. Eine
  Ausnahme ist der Lösch-Zähler: er liegt in `guard_state.db` und überlebt den Neustart,
  damit der erste verweigerte Versuch einer Sitzung nicht mit dem Prozess verschwindet.
- Ein Wechsel des Modells oder des Endpunkts ist an der Antwort ablesbar (`model`), und
  `yesmem setup` berichtet am Ende einer Installation, was der Guard fragen wird.
- Ein Endpunkt, der Weiterleitungen beantwortet, wird nicht verfolgt. Weder der
  Urteilsaufruf noch die Modellliste folgen einem Redirect: sonst würde das Credential
  an den Host wiederholt, den die Weiterleitung nennt.
- Der Guard ist eine Empfehlung mit Zähnen, keine Sandbox. Er feuert nur, wo ein Adapter
  hängt: ohne Plugin in opencode oder ohne Hook in Claude Code wird kein Aufruf geprüft,
  und niemand merkt es.

## Siehe auch

- [Developer Tools](developer-tools.md): die Plugin-Seite, Hooks und die
  Werkzeug-Tabelle
- [Verifier](verifier.md): das andere Urteil, das aus einem Bericht wird
- [YesLoop](yesloop.md): der Loop, in dem ein `BLOCK` einen Werkzeugaufruf verhindert,
  bevor er läuft
