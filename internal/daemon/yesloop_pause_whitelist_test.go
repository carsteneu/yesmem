package daemon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// pauseSite names one place that stops an agent: the file it lives in and the
// function that encloses it.
type pauseSite struct {
	file string
	fn   string
}

func (p pauseSite) String() string { return p.file + "#" + p.fn }

// yesloopPauseWhitelist is the closed list of places that may stop an agent.
//
// A pause means "process alive, supervisor needs orchestrator input": the agent
// waits until someone relays to it. Exactly one situation earns that: the DONE
// verdict. There the agent must not deliver while the pre-check runs, must not
// deliver again after a blocked verdict, and must wait once the gate hands the
// decision to the orchestrator. Every other guard reports and lets the agent run
// (see nudgeAgent in guard_nudge.go).
//
// The value is the justification the failure message prints, so an entry cannot
// be added without writing down why.
var yesloopPauseWhitelist = map[pauseSite]string{
	{"done_delivery.go", "announceWithheldDONE"}:        "the DONE payload is withheld pending a verdict; the agent waits for it",
	{"done_gate_verify.go", "escalateBlockedReport"}:    "third blocked round on the same DONE attempt; the gate cannot answer, the orchestrator decides",
	{"done_gate_verify.go", "holdOnRunningPrecheck"}:    "the pre-check for this report is still running; the verdict decides",
	{"done_gate_verify.go", "announceBlockedReport"}:    "a blocked verdict demands a correction; nothing may be delivered before it",
	{"done_hold_watchdog.go", "escalateUndecidedHold"}:  "the DONE verdict stayed undecided past the window; the orchestrator decides",
	{"yesloop_done_guard.go", "checkOneDoneGuardAgent"}: "the seven-phase contract is the DONE gate; a non-compliant report must not be delivered",
}

// yesloopPausePending is the one pause that is neither a DONE path nor a nudge
// yet. It is a bookkeeping pause by content, and the file belongs to a parallel
// loop that converts it; this list exists so the test can land first. Unlike the
// whitelist it is not a justification: an entry here is a debt, and the test says
// so on every run.
var yesloopPausePending = map[pauseSite]string{
	{"yesloop_skillcheck.go", "checkOneSkillCheck"}: "parallel loop yesloop/skillcheck-nudge converts it to a nudge",
}

// yesloopPauseStatusWriters is the only place allowed to write status=paused: the
// helper itself. Everything else goes through pauseAgent, which is what the
// whitelist names.
var yesloopPauseStatusWriters = map[pauseSite]string{
	{"heartbeat.go", "pauseAgent"}: "the helper the whitelist is about",
}

// TestPauseAgentOnlyFromDonePaths is the invariant this loop exists for: a guard
// nudges, it does not stop the agent. The check is mechanical, so a new pause
// outside the DONE paths cannot slip in unnoticed.
func TestPauseAgentOnlyFromDonePaths(t *testing.T) {
	sites, err := collectPauseSites(".")
	if err != nil {
		t.Fatalf("collecting pauseAgent call sites: %v", err)
	}
	if len(sites) == 0 {
		t.Fatal("no pauseAgent call site found at all: the collector lost its target")
	}

	for _, site := range sites {
		if _, ok := yesloopPauseWhitelist[site]; ok {
			continue
		}
		if _, ok := yesloopPausePending[site]; ok {
			t.Logf("DEBT: %s still stops an agent (%s). Only the DONE verdict paths may pause; everything else nudges (nudgeAgent in guard_nudge.go).", site, yesloopPausePending[site])
			continue
		}
		t.Errorf("%s stops an agent without a whitelist entry. Only the DONE verdict paths may pause; every other guard nudges (nudgeAgent in guard_nudge.go). If this site is a DONE path, add it to yesloopPauseWhitelist with its justification.", site)
	}

	used := make(map[pauseSite]bool, len(sites))
	for _, site := range sites {
		used[site] = true
	}
	// Unused entries are not a failure: the check runs from calls to list, so a
	// whitelist that outlives a code change is not a hole. They are logged so the
	// list does not rot.
	extra := make([]pauseSite, 0, len(yesloopPauseWhitelist)+len(yesloopPausePending))
	for site := range yesloopPauseWhitelist {
		if !used[site] {
			extra = append(extra, site)
		}
	}
	for site := range yesloopPausePending {
		if !used[site] {
			extra = append(extra, site)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].String() < extra[j].String() })
	for _, site := range extra {
		t.Logf("entry no longer has a pauseAgent call, remove it: %s", site)
	}
}

// The whitelist names pauseAgent, so a pause written around it would be invisible.
// The status column is the behaviour: this test finds every direct status=paused
// write in the package and allows only the helper.
func TestPauseStatusIsOnlyWrittenByTheHelper(t *testing.T) {
	writes, err := collectPauseStatusWrites(".")
	if err != nil {
		t.Fatalf("collecting status=paused writes: %v", err)
	}
	if len(writes) == 0 {
		t.Fatal("no status=paused write found at all: the helper itself must be visible to this test")
	}
	for _, site := range writes {
		if _, ok := yesloopPauseStatusWriters[site]; !ok {
			t.Errorf("%s writes status=paused directly. A guard must not stop an agent; use pauseAgent only on a DONE verdict path, and nudge it otherwise (nudgeAgent in guard_nudge.go).", site)
		}
	}
}

// The collectors are the guard's eyes, so they get their own witness: a call in a
// function that is not on the list has to be found and has to stand out.
func TestPauseSiteCollectorSeesAnUnlistedCall(t *testing.T) {
	dir := t.TempDir()
	src := `package daemon

func (h *Handler) aBookkeepingGuard() {
	h.pauseAgent("x", "because")
	f := h.pauseAgent
	f("y", "method value")
}

func (h *Handler) aDonePath() {
	h.pauseAgent("z", "verdict")
}

func (h *Handler) aBypass() {
	h.store.AgentUpdate("id", map[string]any{"status": "paused", "progress": "paused: sneak"})
}
`
	if err := os.WriteFile(filepath.Join(dir, "synthetic.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	sites, err := collectPauseSites(dir)
	if err != nil {
		t.Fatalf("collectPauseSites: %v", err)
	}
	got := make([]string, 0, len(sites))
	for _, site := range sites {
		got = append(got, site.String())
	}
	sort.Strings(got)
	want := []string{"synthetic.go#aBookkeepingGuard", "synthetic.go#aBookkeepingGuard", "synthetic.go#aDonePath"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("collected sites = %v, want %v (the method value counts like the call)", got, want)
	}
	for _, site := range sites {
		if _, ok := yesloopPauseWhitelist[site]; ok {
			t.Errorf("the synthetic guard %s must not be whitelisted by the real list", site)
		}
	}

	writes, err := collectPauseStatusWrites(dir)
	if err != nil {
		t.Fatalf("collectPauseStatusWrites: %v", err)
	}
	if len(writes) != 1 || writes[0].String() != "synthetic.go#aBypass" {
		t.Fatalf("status writes = %v, want the direct write in aBypass", writes)
	}
}

// collectPauseSites reads every non-test Go file in dir and reports each use of
// pauseAgent with the function that encloses it. Parsing, not grep: a comment or a
// string that mentions the name must not count, and a method value must count like
// a call.
func collectPauseSites(dir string) ([]pauseSite, error) {
	var sites []pauseSite
	err := walkFuncs(dir, func(file, fn string, node ast.Node) {
		sel, ok := node.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "pauseAgent" {
			return
		}
		sites = append(sites, pauseSite{file: file, fn: fn})
	})
	sort.Slice(sites, func(i, j int) bool { return sites[i].String() < sites[j].String() })
	return sites, err
}

// collectPauseStatusWrites reports every call that writes status=paused through a
// literal field map, with the function that encloses it. It is a best-effort
// net, not a proof: a status that arrives in a variable, in a map built earlier or
// through AgentCreate is invisible to it. What it does catch is the shape a new
// pause takes when someone reaches past pauseAgent.
func collectPauseStatusWrites(dir string) ([]pauseSite, error) {
	var sites []pauseSite
	err := walkFuncs(dir, func(file, fn string, node ast.Node) {
		call, ok := node.(*ast.CallExpr)
		if !ok || !isAgentUpdate(call) {
			return
		}
		for _, arg := range call.Args {
			lit, ok := arg.(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if stringLit(kv.Key) == "status" && stringLit(kv.Value) == "paused" {
					sites = append(sites, pauseSite{file: file, fn: fn})
				}
			}
		}
	})
	sort.Slice(sites, func(i, j int) bool { return sites[i].String() < sites[j].String() })
	return sites, err
}

// walkFuncs parses every non-test Go file in dir and calls visit for each node
// inside a function body, with the file name and the enclosing function name.
func walkFuncs(dir string, visit func(file, fn string, node ast.Node)) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				visit(name, fn.Name.Name, node)
				return true
			})
		}
	}
	return nil
}

func isAgentUpdate(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel.Name == "AgentUpdate" || sel.Sel.Name == "AgentUpdateIfMatches"
}

func stringLit(expr ast.Expr) string {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	return strings.Trim(lit.Value, `"`)
}
