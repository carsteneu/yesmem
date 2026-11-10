# Verifier

> Ein Prüfbericht geht hinein, ein Urteil kommt heraus: jede Behauptung wird aus dem
> Repository, dem Sitzungsspeicher und dem Plan neu hergeleitet, statt dem Bericht zu
> glauben.

## Was er ist

Der Verifier ist eine Daemon-Operation, keine Funktion im Loop. Ein Aufrufer übergibt
einen Berichtstext und ein Repository und bekommt ein Urteil. Wer den Bericht
geschrieben hat, weiß die Operation nicht: das DONE-Gate ist ein Konsument, ein
Harness, ein Skript oder ein Mensch kann ein anderer sein.

Der Code liegt in drei Schichten:

| Paket | Aufgabe |
|---|---|
| `internal/verify` | Die öffentliche Fläche: `Request`, `Report`, Profile, Policy, Urteilsmodell |
| `internal/verify/engine` | Ausführung: Git-Zugriff, Wegwerf-Checkout, Befehlsrunner, Sandbox, Modelltypen |
| `internal/verify/claimtypes` | Ein Anspruchstyp pro Datei, dazu die Registry als einzige Liste |

Der Bericht ist die Schnittstelle. Alles andere (Repository, Basisrevision, Profil,
Policy, Budget) ist die Aussage des Aufrufers darüber, wie geurteilt werden soll.

## Anspruchstypen

Ein Bericht wird zeilenweise gelesen. Jede Zeile, die ein registrierter Typ
beansprucht, wird zu einem Anspruch, und der Typ entscheidet, was ihn bestätigt.

| Marker oder Zeile | Typ | Was geprüft wird |
|---|---|---|
| `[COMMIT: <hash>]` | `commit_exists` | Der Hash muss ein Commit im geprüften Repository auflösen. Ein Blob oder Tree ist eine Widerlegung, keine Bestätigung. |
| `[BRANCH: <name>]` | `branch_pushed` | Der Branch wird in einen eigenen Ref geholt und muss den festgelegten Commit enthalten. Ein Repository ohne Remote kann die Frage nicht beantworten, das ist ein Umgebungsbefund. |
| `[MERGE: <branch>]` | `merge` | Nennt der Bericht einen Branch, muss dieser den Commit enthalten. Ein Statuswort (`no`, `pending-PR`) nennt keinen Branch und bleibt offen, statt zur Bestätigung durch Vorstellungskraft zu werden. |
| `[DEPLOY: yes\|no]` | `deploy` | Nichts. Der Verifier sieht das Repository, nicht die Maschine, die der Deploy erreicht hat. Ob das offen bleiben darf, entscheidet die Policy des Aufrufers. |
| `[FOO: …]` | `unknown_marker` | Ein Marker, den kein registrierter Typ beansprucht, ist ein Formfehler. Ihn zu übergehen ließe einen unbekannten Anspruch am Verifier vorbei. |
| (Profil) | `profile` | Je Pflichtklasse, die der Bericht nicht nennt, ein Defekt. Ein Befund für den ganzen Bericht, nicht einer je Klasse. |
| `Files in scope: a, b` | `diff_scope` | Die genannte Liste wird gegen die Dateien gehalten, die der festgelegte Commit seit der Basisrevision geändert hat. |
| `Tests run: <befehl> -> exit <N>` | `tests_green`, `tests_exit` | Der Befehl läuft, wenn er auf der Allowlist steht, und die Ausgabe muss ein positives Summarium tragen. Ein Exit-Code allein zählt nicht. Ein ehrlich gemeldeter roter Lauf ist eine wahre Aussage und trotzdem kein Abschluss. |
| `[COMPUTE: <befehl> -> <sha256>]` | `compute` | Der Befehl läuft auf einem Wegwerf-Checkout des festgelegten Commits, und der Hash über seine Standardausgabe muss stimmen. |
| `[ARTIFACT: <pfad> -> <sha256>]` | `artifact` | Die Datei existiert, und ihr SHA-256 stimmt. |
| `[LINT: <befehl>]` | `lint` | Ein Lint-Befehl aus der Allowlist läuft sauber, mit derselben Engine wie die Tests. |
| `[EVIDENCE: cold-review=…, …]` | `evidence_ids` | Die `ses_`-IDs existieren, stammen von der Sitzung des Berichts ab und belegen je Slot einen erlaubten Agententyp. |
| `[PLAN: <projekt>]` | `plan_items` | Der genannte Plan hat keine offenen Schritte (`pending`, `in_progress`). |
| `[MUTATION: changed-lines survivors=<n>]` | `mutation` | Die geänderten Produktionszeilen reagieren auf Mutation, innerhalb der erklärten Überlebendenquote. |
| `[COVERAGE: changed-lines]` | `coverage_delta` | Jede geänderte Produktionszeile wurde ausgeführt, laut Coverage-Artefakt des Profils. |
| (Arbeitsbaum) | `worktree_clean` | `git status --porcelain` im genannten Repository muss leer sein. Der Verifier sieht Commits; uncommittete Reste stehen in keinem davon, und der Bericht kann sie nicht zeigen. Ignorierte Dateien zählen nicht (die Projektentscheidung, was nicht zur Historie gehört), und ein Wegwerf-Checkout oder ein Bare-Repository ist der echte No-op: dort ist der Status immer leer. |

Der Befund `worktree_clean` gehört dem Verifier, nicht dem Bericht: er erscheint in
jedem Lauf mit Repository, er zählt nicht als der eine bestätigte Anspruch, den ein
Bericht braucht, und er steht im Urteilsschlüssel des DONE-Gates. Ein Befund, der den
Arbeitsbaum nach der Vorab-Prüfung verschmutzt, ist damit ein anderer Zustand als der
geprüfte, und der Agent läuft die Prüfung erneut statt freigegeben zu werden.

## Urteilsklassen

Jeder Anspruch bekommt einen Status. Der Status sagt, ob die Behauptung stimmt; die
Klasse sagt, wem die Abweichung gehört.

| Status | Bedeutung |
|---|---|
| `confirmed` | Der Anspruch wurde aus der Welt hergeleitet. |
| `refuted` | Die Welt widerspricht dem Anspruch selbst. |
| `unverifiable` | Der Anspruch ließ sich nicht entscheiden: fehlende Basis, fehlende Angabe, kein Profil für die Datei. |

| Klasse | Wem gehört der Befund | Was der Agent tun kann |
|---|---|---|
| `defect` | Dem Bericht | Form korrigieren: fehlende Zeile, unbekannter Marker, unbrauchbarer Platzhalter, mehr Ansprüche als erlaubt. |
| `environment` | Der Maschine | Nichts. Fehlende Sandbox, fehlendes Werkzeug, kein Upstream, ein Coverage-Lauf ohne Exitzero: der Befund geht an den Orchestrator. |
| `limit` | Dem Budget | Nichts. Der Lauf hat sein Budget verbraucht, bevor er fertig war. |
| `unverifiable` | Dem Aufrufer | Nichts, solange die Policy die Klasse trägt. Das Gate trägt `deploy` und `merge`. |

Geblockt wird in dieser Reihenfolge: `refuted`, dann `defect`, dann `failed` (bestätigt
und trotzdem kein Abschluss, etwa ein roter Testlauf), dann `limit`, dann
`environment`, dann `unverifiable`. Ein Bericht, der nichts blockiert und nichts
bestätigt, ist ebenfalls kein Abschluss.

Das Kriterium ist `Report.Decision.Accept`, nie der Exit-Code. Die beiden sind
absichtlich entkoppelt: im strikten Modus liefert ein geduldeter Deploy den Exit-Code 4,
während die Policy den Bericht annimmt, und ein bestätigter Testlauf mit Exit ungleich
null liefert den Exit-Code 0, während die Policy ihn blockt. Eine Steuerung über den
Exit-Code würde in beide Richtungen falsch entscheiden.

## Aufruf

Als RPC auf dem Daemon-Socket:

```json
{"method": "verify_claims", "params": {
  "repo": "/pfad/zum/repo",
  "section": "yesloop-mein-task",
  "project": "/pfad/zum/projekt",
  "profile": "yesloop-done",
  "policy": "yesloop-done",
  "strict": true,
  "budget": "5m"
}}
```

Als MCP-Werkzeug `verify_claims` mit denselben Feldern. Statt `section` und `project`
kann `report` den Text direkt tragen; `base` setzt die Basisrevision einer
Dateiliste, `plan_thread` den Thread, gegen den ein `[PLAN:]`-Anspruch geprüft wird
(bei einem Bericht aus einer Section gehört der Thread dem Agenten: der Verifier
fragt alle Kennungen dieser Zeile, und ein fremder Thread wird nicht befragt).
`report_session` (Vorgabe: die aufrufende Sitzung) bestimmt, wovon die Evidenz-IDs
abstammen müssen.

Als Unterkommando für die Hand: `yesmem verify-claims --repo <pfad> --report <datei|->`
oder `--project <p> --section <s>`, dazu `--base`, `--head`, `--profile`, `--policy`,
`--budget`, `--strict` und `--json`. Der Exit-Code ist das Urteil: 0 angenommen, 1
geblockt, 2 Bedien- oder Transportfehler. Die drei Fälle stehen unten zum Nachlaufen.

Die Antwort:

```json
{
  "status": "blocked",
  "kind": "refuted",
  "reason": "1 claim(s) refuted: commit 0000… does not exist in /pfad/zum/repo",
  "findings": [{"kind": "commit_exists", "status": "refuted",
                "reason": "commit 0000… does not exist in /pfad/zum/repo",
                "command": "git -C /pfad/zum/repo rev-parse --verify --quiet 0000…^{commit}"}],
  "summary": {"confirmed": 0, "refuted": 1, "unverifiable": 0},
  "classes": {"none": 0, "defect": 0, "environment": 0, "limit": 0, "unverifiable": 0},
  "exit_code": 1,
  "duration_ms": 412,
  "repo": "/pfad/zum/repo",
  "report_from": "section"
}
```

`status` ist das einzige Feld, das ein Aufrufer lesen muss: `accepted` heißt, nichts hat
geblockt. `kind` und `reason` benennen den Blocker, `findings` trägt die Befunde mit
Befehl und Ausgabe, und `summary` sowie `classes` zählen alle Ansprüche, auch die
gekürzten.

Der `command`-Wert ist im Beispiel gekürzt. Der wirkliche Befehl trägt zusätzlich die
git-Optionen aus `engine.FormatCommand` (`-c core.hooksPath=/dev/null -c
core.fsmonitor=false -c core.commitGraph=false -C <repo>`) und quotet Argumente mit
Sonderzeichen: damit führt ein Repository beim Prüfen keine eigenen Programme aus, und
der Befehl bleibt trotzdem kopierbar.

Der Aufruf läuft synchron und nimmt den einen Prüfplatz des Daemons. Ein zweiter Aufruf
findet den Platz belegt und bekommt eine Fehlermeldung mit der Bitte, es kurz später
erneut zu versuchen, statt eine zweite Testsuite zu starten. Ein direkter Aufruf bekommt
dasselbe Budget wie der Lauf des Gates (5 Minuten); die Obergrenze des Verifiers selbst
liegt bei 10 Minuten.

## Ende zu Ende gegen den laufenden Daemon

Die drei Fälle (und die Widerlegungsform des dritten), die ein Mensch in wenigen Minuten
wiederholen kann. Sie brauchen weder
einen Agenten noch die Warteschlange: der Aufruf geht direkt an den Socket, der Daemon
schreibt dabei nichts außer dem Checkout unter `<dataDir>/tmp/check`.

Die Reporttexte als Dateien, damit der Aufruf kopierbar bleibt:

```bash
repo=/pfad/zum/repo
head=$(git -C "$repo" rev-parse HEAD)                 # ein Commit, den es wirklich gibt
printf '[COMMIT: %s]\n[BRANCH: main]\n' "$head" > honest.txt
printf '[COMMIT: %s]\n[BRANCH: main]\n' "$(printf '0%.0s' {1..40})" > faked.txt
printf '[COMMIT: %s]\n[BRANCH: lokal-nie-gepusht]\n' "$head" > unpushed.txt
```

Dann je Fall ein Aufruf. Der Exit-Code ist das Urteil, nicht der Transport:

```bash
yesmem verify-claims --repo "$repo" --report honest.txt     # 0: accepted
yesmem verify-claims --repo "$repo" --report faked.txt      # 1: blocked (refuted)
yesmem verify-claims --repo "$repo" --report unpushed.txt   # 1: blocked, Klasse je Fall
```

Ohne CLI geht derselbe Aufruf als JSON-Zeile an den Socket. Eine Zeile hin, eine
Antwortzeile zurück (`plugins/opencode-yesmem-v2/rpc.ts`, `internal/daemon/socket.go`):

```bash
python3 -c 'import socket,json,sys
s=socket.socket(socket.AF_UNIX); s.connect(sys.argv[1])
f=s.makefile("rwb")
f.write((json.dumps({"method":"verify_claims","params":{"repo":sys.argv[2],"report":open(sys.argv[3]).read()}})+"\n").encode())
f.flush(); print(f.readline().decode())' ~/.claude/yesmem/daemon.sock "$repo" honest.txt
```

Was am 02.11.2026 gegen den ausgerollten Stand herauskam:

| Fall | Bericht | Urteil | Laufzeit |
|---|---|---|---|
| A ehrlich | echter Commit, `[BRANCH: main]` (auf `origin` vorhanden) | `accepted`, 2 bestätigt, Exit 0 | rund 20 s |
| B gefälscht | erfundener Hash `000…0`, sonst gleich | `blocked`, `refuted`, Befund `commit_exists`, Exit 1 | 11 ms |
| C Branch nur lokal | echter Commit, `[BRANCH: yesloop/…]` (nur lokal) | `blocked`, `unverifiable`, Befund `branch_pushed`, Exit 1 | 1,4 s |
| C2 Commit nicht auf dem Branch | echter, lokal vorhandener Commit, `[BRANCH: main]` | `blocked`, `refuted`, „is not reachable from main on origin", Exit 1 | rund 20 s |

Zwei Dinge, die man dabei wissen muss:

- **Ein Branch, den es auf `origin` nicht gibt, ist `unverifiable`, nicht `refuted`.**
  Der Fetch scheitert („couldn't find remote ref"), und der Verifier kann einen
  fehlenden Branch nicht von einem kaputten Remote unterscheiden. Geblockt wird
  trotzdem, nur die Klasse heißt `unverifiable`. Die Widerlegung entsteht beim
  anderen Zuschnitt: der Branch existiert auf `origin` und enthält den Commit nicht
  (Fall C2).
- **Der Branch-Check zahlt die ganze Historie.** Jeder Lauf holt den Branch in ein
  frisches Bare-Repository, also ohne Cache. Bei diesem Repository (427 MB Pack)
  dauert das rund 20 s gegen ein Zeitlimit von 30 s; der erste Lauf im Daemon lief
  genau dort in den Timeout und meldete `environment`. Ein größeres Repository
  überschreitet das Limit dauerhaft.

`--head` ist eine Vorbedingung, keine zweite Revision: die Operation prüft immer den
HEAD des Checkouts. Wer eine andere Revision nennt, bekommt das gesagt (Exit 2),
statt ein Urteil über einen anderen Baum zu erhalten.

Die übrigen Flags reichen den Vertrag des RPC durch: `--report-session` und
`--plan-thread` für `[EVIDENCE:]` und `[PLAN:]`, `--profile yesloop-done` für die
Pflichtklassen eines Abschlussberichts, `--policy` für die geduldeten Klassen, `--budget`
für das Zeitbudget (muss positiv sein, Vorgabe 5 Minuten) und `--strict` für ein Urteil,
das nur vollständig bewiesene Berichte durchlässt. Exit 2 heißt: Aufruf, Transport oder
ein belegter Prüfplatz („retry shortly"), also nichts, was ein Bericht ändern könnte.

Die Policy ist der Hebel, den man beim Aufruf am leichtesten verwechselt. Mit
`--policy yesloop-done` werden genau die zwei Anspruchstypen geduldet, die der Verifier
strukturell nicht entscheiden kann: der Deploy und ein Merge, dessen Versuch offen
bleibt, etwa ein Statuswort wie `no` oder ein Branch, der sich nicht auflösen lässt. Die
leere Vorgabe `--policy ""` duldet in dieser Restklasse nichts und blockt deshalb jede
unbeweisbare Behauptung, also auch ein ehrliches `[DEPLOY: no]`; durchgehend strenger ist
sie aber nicht, denn erst `yesloop-done` blockt mit `DenyConfirmed` einen bestätigten
roten Testlauf als `failed`. Wer einen Bericht mit Deploy-Zeile prüft, gehört deshalb auf
`--policy yesloop-done`, und genau diese Policy fährt auch das DONE-Gate.

## Profilformat

Ein Profil ist zweierlei: die Anspruchsklassen, die ein Bericht dieser Art enthalten
muss, und die Sprachdaten, mit denen die Qualitätsansprüche arbeiten.

Das Profil `yesloop-done` verlangt `commit_exists`, `branch_pushed` und eines von
`tests_green` oder `tests_exit`. Fehlt eine dieser Klassen, ist das ein Defekt und keine
Auslassung.

Die Sprachdaten sind Konfiguration, kein Wissen. Kein Anspruchstyp verzweigt über einen
Sprachnamen: er schlägt die Endung der Datei in dieser Tabelle nach.

```go
type Language struct {
	Name               string   // nur für Meldungen, nie für eine Verzweigung
	Extensions         []string // Quelldateiendungen, Punkt inklusive
	TestFilePatterns   []string // treffen einen Basisnamen; eine Testdatei wird nie mutiert
	TestCommand        string   // Testbefehl der Qualitätsansprüche, Allowlist gilt weiter
	CoverageCommand    string   // schreibt CoverageArtifact im Format CoverageFormat; leer heißt: kein Coverage
	CoverageFormat     string   // "go-cover" oder "cobertura"
	CoverageArtifact   string   // Pfad, den CoverageCommand schreibt
	BuildErrorPatterns []string // erkennen einen gescheiterten Build in der Ausgabe
	SandboxReadOnly    []string // Pfade, die der Lauf lesen darf (Modulcache, Werkzeugverzeichnis)
	SandboxEnv         map[string]string // Zusatzumgebung des Laufs, ersetzt gleichnamige Einträge
	Requires           []string // Programme, die die Suite braucht; vor dem Lauf geprüft
	SandboxCache       string   // fester Build-Cache außerhalb des Checkouts, beschreibbar
}
```

Die drei Sandboxfelder sind der Werkzeugkasten des Projekts. `SandboxReadOnly` nennt
Host-Bäume, die der eingesperrte Lauf lesen muss, etwa den Modulcache. `Requires` nennt
die Programme, die die Suite aufruft; der Verifier löst jeden Namen auf und prüft ihn
gegen die Masken des Sandbox. Ein Werkzeug, das unter dem ausgeblendeten Home liegt und
nicht ausdrücklich gemountet ist, macht den Lauf zu einer Umgebungslücke mit dem
Werkzeugnamen und dem Pfad in der Meldung, nie zu einer Widerlegung: der Bericht ist
nicht falsch, die Einsperrung kann das Werkzeug nicht liefern. Ohne Deklaration bleibt
alles wie vorher. `SandboxCache` verlegt den Build-Cache aus dem Wegwerf-Checkout in ein
festes, beschreibbares Wurzelverzeichnis, damit der zweite Lauf warm kompiliert; der
Platzhalter `{cache}` zeigt auf dieses Verzeichnis, und ohne Deklaration bleibt der Cache
im Checkout.

Alle drei Felder werden streng geprüft, weil sie Host-Pfade öffnen. Ein Mount ist genau
ein absoluter Pfad. Unter dem Home ist nur erlaubt, was die Toolchain selbst meldet
(`{toolenv:NAME}`) oder was ein Werkzeug-Bin-Verzeichnis ist; Home selbst, seine
Elternverzeichnisse, Credential-Verzeichnisse, maskierte Bäume und Pfade innerhalb des
geprüften Repositories bleiben verboten. Ein gemountetes Bin-Verzeichnis wandert an das
Ende des PATH des Laufs, damit ein Werkzeug erreichbar wird, ohne ein Systemprogramm zu
verdecken. Der Cache ist der einzige beschreibbare Baum, und er liegt zwingend unter dem
Cache-Wurzelverzeichnis des Verifiers, das der Platzhalter `{base}` benennt: ein Profil aus
dem Repository darf benennen, welcher Unterbaum warm bleiben soll, aber nicht, welche
Stelle der Festplatte beschrieben wird. Ein Symlink wird vor der Prüfung aufgelöst, ein
fehlender Cache wird angelegt, und ein vorhandener, aber nicht beschreibbarer Cache bleibt
eine Umgebungslücke.

Die Vorgabe deckt Go (`go test ./...`, `go-cover`, `.yesmem-cover.out`), PHP (PHPUnit,
`cobertura`) und Python (pytest, `cobertura`) ab. Jeder Eintrag nennt Testdateimuster
und Build-Fehlermuster, weil ohne sie eine Testdatei mutiert und ein Build-Fehler als
getöteter Mutant gezählt würde, was der eine Fehler ist, den ein Qualitätsanspruch nie
machen darf.

### Der Testbefehl des Projekts

Ein Projekt, dessen Suite kein eingebauter Eintrag kennt, sagt es selbst: steht unter
`.yesmem/verify.yaml` ein `test_command`, ist genau dieser Befehl für `tests_green` und
`tests_exit` zugelassen, und genau der `coverage_command` für `coverage_delta`. Exakt
heißt Token für Token: `make integration` erlaubt nicht `make integration --all`, und
nichts wird gemustert. Alles andere bleibt so streng wie ohne Profil, und ein
Repository ohne Profil verhält sich unverändert.

Die Erlaubnis stammt aus den Bytes des Commits, nicht aus der Datei daneben. Ein Profil,
das nur im Arbeitsbaum liegt, vergibt kein Kommando: sonst könnte der, der beurteilt
wird, sein eigenes Ausführungsrecht schreiben. Ein Eintrag, den die Allowlist ohnehin
schon zulässt, braucht diese Herkunft nicht, weil er nichts weitet.

Ein Profil steht heute im Code (`profiles` in `internal/verify/verify.go`), nicht in
`config.yaml`. Ein Bericht ohne Profil wird trotzdem geprüft und bekommt die
Standardsprachdaten: ein Qualitätsmarker ist ein Anspruch über den Code, nicht über das
Profil.

## Die ehrliche Grenze

Der Verifier misst Behauptungen, nicht Absicht.

- Mutation und Coverage messen die Empfindlichkeit gegen Änderungen, nicht die
  Korrektheit der Anforderung. Eine testblinde Suite fällt auf, eine falsch
  spezifizierte nicht: decken die Tests eine falsche Regel ab, ist Coverage voll und
  jeder Mutant stirbt, und der Bericht ist trotzdem über die falsche Sache.
- Ein grüner Lauf mit vollem Coverage heißt: die Zeilen laufen und reagieren auf eine
  Änderung. Es heißt nicht, dass sie das Richtige tun.
- Die Außenwelt bleibt draußen. Ein `[DEPLOY:]` ist strukturell unbeweisbar, und eine
  Evidenz-ID belegt, dass ein Subagent gelaufen ist, nicht was er gefunden hat.
- Stil, Lesbarkeit, Architektur und Absicht sind kein Gegenstand. Wer das prüfen will,
  braucht den Cold Review, nicht den Verifier.
- Grenzen der Größe: höchstens 256 Ansprüche je Bericht, höchstens 4 MiB Berichtstext,
  ein Budget je Lauf (im Verifier 10 Minuten, im direkten Aufruf `done_verify.budget`,
  Vorgabe 5 Minuten). Ein überschrittenes Budget ist die Klasse `limit` und keine
  Widerlegung.
- Ein Befund ohne Befehl ist keine Anweisung. Wo ein Anspruchstyp keinen Befehl setzt,
  sagt die Meldung den Befund und sonst nichts.

## Wo das Gate ihn benutzt

Das DONE-Gate (`internal/daemon/done_gate_verify.go`) startet seit dem Umbau keine Prüfung
mehr. Jede `verify_claims`-Operation (RPC, MCP und CLI sind dieselbe Operation) legt ihr
Urteil unter einem Schlüssel ab, und das Gate liest diesen Schlüssel beim DONE:
Berichtstext, HEAD, Branch, der Stand des Arbeitsbaums (uncommittete Reste), Plan-Revision
und die Session, gegen die die Evidenz geprüft wurde. Was das Gate dabei tut:

- Ein frisches angenommenes Urteil gibt den Agenten frei (`DONE-VERIFIED`), ohne Lauf und
  ohne Checkout. Die Freigabe nennt die Herkunft (`Vorab-Prüfung`, `nichts neu
  gerechnet`), damit niemand einen zweiten Lauf annimmt.
- Ein frisches abgelehntes Urteil blockt mit den Befunden des Laufs. Die Befunde sind die
  des Laufs: das Gate rechnet nichts nach.
- Fehlt ein Urteil oder ist es veraltet, blockt das Gate sofort als Defekt und druckt den
  vollständigen `yesmem verify-claims`-Befehl. Der Grund der Veraltung steht dabei: Text
  geändert, HEAD gewandert, Branch gewechselt, uncommittete Reste aufgetaucht, Plan
  geändert, Session gewechselt.
- Ein Urteil, das unter einem schwächeren Regelsatz entstand (etwa ohne
  `--profile yesloop-done`), wird mit dem Grund abgelehnt: ein leeres Profil prüft
  weniger Klassen und beweist damit weniger.
- Läuft die Vorab-Prüfung noch, während der DONE eintrifft, hält das Gate den Agenten auf
  `DONE-PENDING-VERIFY` mit dem festen Wartetext und entscheidet beim nächsten Tick.

Ein Abschnitt, der fehlt oder keinen einzigen Claim hergibt, wird weiterhin vor jedem
Nachschlagen als Defekt geblockt: das Gate druckt das Sieben-Zeilen-Muster und den Befehl,
weil ein Lauf nur dasselbe Urteil kosten würde. Der Eskalationszähler bleibt: drei
Blockungen desselben Versuchs gehen an den Orchestrator, der Agent wartet.

Das Urteil liegt im Speicher des Daemons. Ein Neustart zwischen Prüfung und DONE verwirft
es, und der Agent muss die Vorab-Prüfung wiederholen. Das ist die sichere Richtung: ein
fehlendes Urteil blockt, es gibt nie versehentlich frei. Die Blockung zählt dabei wie jede
andere in den Eskalationszähler, weil der Zähler den Versuch sieht, nicht die Ursache; drei
Neustarts in einem Versuch bringen den Fall zum Orchestrator.

## Siehe auch

- [YesLoop](yesloop.md): das Gate, das diesen Verifier benutzt, und seine Meldungen
- [Developer Tools](developer-tools.md): der Code-Navigations- und Guard-Teil des Plugins
- [MCP Tools Reference](../mcp-tools-reference.md)
