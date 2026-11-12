package daemon

// Shared fixtures and platform guards for the daemon package.
//
// The daemon suite is the largest in the repo, and it grew one helper per
// ticket. Two things belong here rather than in the ticket file that first
// needed them: the store and handler openers (one per shape, so a fixture change
// is one edit) and the platform contract (which host can run which assertion).
// A guard that lives in one place cannot drift into six copies.

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/opencode"
	"github.com/carsteneu/yesmem/internal/storage"
)

func mustStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// neutralizeClaude points PATH at an empty tempdir so that any leftover exec of
// the `claude` CLI fails fast. The auto-correct pass must not use it any more;
// these tests keep the PATH clear so a regression to a CLI call fails loudly
// instead of passing on the dev machine's installation.
func neutralizeClaude(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// isolateHome gives the test its own HOME and returns it.
func isolateHome(t *testing.T) string {
	t.Helper()
	return isolateHomeAt(t, t.TempDir())
}

// isolateHomeAt points HOME and the XDG roots at home, for a test that brought
// its own home directory. os.UserConfigDir and os.UserCacheDir prefer XDG_* over
// HOME, and a CI runner exports XDG_CONFIG_HOME: without these a test that writes
// into its temp home would still read the operator's real config and find
// nothing there.
func isolateHomeAt(t *testing.T, home string) string {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

// stubV2Service points the capture seam at a stub service. HOME is left alone.
func stubV2Service(t *testing.T, srv *httptest.Server) {
	t.Helper()
	orig := opencodeV2Service
	opencodeV2Service = func() *opencode.Client {
		return opencode.NewClient(opencode.Service{ID: "stub", URL: srv.URL, Password: "secret"})
	}
	t.Cleanup(func() { opencodeV2Service = orig })
}

// stubOpencodeChildActive pins the opencode child-session lookup: while the
// stub is in place every agent counts as blocked inside a task() subagent
// (#89428).
func stubOpencodeChildActive(t *testing.T, active bool) {
	t.Helper()
	orig := opencodeChildActiveFn
	opencodeChildActiveFn = func(string) bool { return active }
	t.Cleanup(func() { opencodeChildActiveFn = orig })
}

// pinProcessTable forces the identity check on, so a test that depends on it does
// not pass vacuously (or fail) on a platform without a readable process table.
func pinProcessTable(t *testing.T) {
	t.Helper()
	prev := procTableReadable
	procTableReadable = true
	t.Cleanup(func() { procTableReadable = prev })
}

func doneGuardTracked(agentID string) bool {
	yesloopDoneGuardAgentsMu.Lock()
	defer yesloopDoneGuardAgentsMu.Unlock()
	_, ok := yesloopDoneGuardAgents[agentID]
	return ok
}

// insertCapRow seeds one cap with an explicit project column and scope, so a
// test can take the two halves of the binding apart. The save path applies the
// lock, so a test that needs a legacy shape has to write the row itself.
func insertCapRow(t *testing.T, h *Handler, name, project, body, sandbox, scope string) {
	t.Helper()
	meta := CapMeta{
		Name:        name,
		Description: "test cap",
		Scripts:     []ScriptMeta{{Name: "run", Kind: "handler", Runtime: "bash", Body: body, Sandbox: sandbox}},
		Version:     1,
		Tested:      true,
		Scope:       scope,
	}
	ctx, err := meta.ToJSON()
	if err != nil {
		t.Fatalf("cap meta json: %v", err)
	}
	if _, err := h.store.InsertLearning(&models.Learning{
		Category:    "cap",
		Content:     name + " — test cap",
		Context:     ctx,
		Confidence:  1,
		CreatedAt:   time.Now(),
		ModelUsed:   "test",
		TriggerRule: "cap:" + name,
		Project:     project,
	}); err != nil {
		t.Fatalf("insert cap: %v", err)
	}
}

// requireProcTable skips a test that supervises a process. Liveness, identity,
// start time and peer credentials all come from /proc, and darwin exposes no
// process table, so the supervision paths cannot answer there. The guard keeps
// the reason in the output instead of a failure that reads like a bug.
//
// A readable table is not enough. The image that replaced ubuntu-24.04 answers an
// empty cmdline for a child this process spawned (2026-11-12), so the identity
// check reads "gone, and not ours" for a process that is alive: the tests that act
// on that answer failed, and the ones that accept it passed for the wrong reason.
// The probe asks the host once and skips with what it saw.
func requireProcTable(t *testing.T) {
	t.Helper()
	if !procTableReadable {
		t.Skip("the process table (/proc) is not available on this platform")
	}
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary to probe the process table with: %v", err)
	}
	probe := exec.Command(sleepBin, "5")
	if err := probe.Start(); err != nil {
		t.Skipf("cannot start a probe process: %v", err)
	}
	defer func() {
		probe.Process.Kill()
		probe.Wait()
	}()
	// readProcCmdline, not the procCmdline seam: a test that pins the seam must not
	// decide whether the host can answer.
	deadline := time.Now().Add(2 * time.Second)
	for {
		cmdline := readProcCmdline(probe.Process.Pid)
		if strings.Contains(cmdline, "sleep") {
			return
		}
		if !time.Now().Before(deadline) {
			t.Skipf("this host does not expose a child's identity in /proc: /proc/%d/cmdline = %q after 2s",
				probe.Process.Pid, cmdline)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// requireBubblewrap skips when ai-jail cannot run at all: it refuses to start any
// profile without bubblewrap, and a runner image does not carry it. A runner can
// also carry bubblewrap and still be unable to create the namespaces it needs
// (Ubuntu 24.04 restricts unprivileged user namespaces through AppArmor), which
// surfaces as "bwrap: loopback: Failed RTM_NEWADDR". Both cases are skipped here
// instead of reported as a broken sandbox further down.
func requireBubblewrap(t *testing.T) {
	t.Helper()
	bin := os.Getenv("BWRAP_BIN")
	if bin == "" {
		found, err := exec.LookPath("bwrap")
		if err != nil {
			t.Skip("bubblewrap is not installed; ai-jail cannot run a profile without it")
		}
		bin = found
	}
	if out, err := exec.Command(bin, "--unshare-net", "--ro-bind", "/", "/", "true").CombinedOutput(); err != nil {
		t.Skipf("bubblewrap cannot create a sandbox here: %v (%s)", err, strings.TrimSpace(string(out)))
	}
}

// requireAiJailPlatform skips a real ai-jail run on a platform the pinned
// release has no build for. The daemon host is Linux; darwin is not a target.
func requireAiJailPlatform(t *testing.T) {
	t.Helper()
	asset, err := aiJailAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("ai-jail has no build for this platform: %v", err)
	}
	// The release names a darwin asset but pins no checksum for it, and the
	// installer refuses an unverified build.
	if _, ok := aiJailAssetDigests[asset]; !ok {
		t.Skipf("ai-jail v%s ships no pinned build for %s", aiJailPinnedVersion, asset)
	}
}

// requireLinuxSandbox skips an assertion about the sandbox as the daemon host
// runs it. The profiles are a Linux contract (systemd unit, ai-jail under
// bubblewrap); the same binary on darwin enforces different rules, so these
// assertions would pin the wrong behaviour there. A runner without a working
// bubblewrap cannot run a profile at all, so it gets the same treatment as
// darwin: the requirement is the environment's, not the test's.
func requireLinuxSandbox(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("the sandbox contract is asserted for its Linux host, not %s", runtime.GOOS)
	}
	requireBubblewrap(t)
}

// realPathOrSelf resolves symlinks and falls back to p for a path that does not
// exist. Mask entries are built from what the OS reports, and macOS reaches its
// temp root through /tmp -> /private/tmp, so a plain string compare misses.
func realPathOrSelf(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	// A path that does not exist yet (a sibling file the mask covers, a database
	// that was never created) is resolved through its nearest existing ancestor.
	dir := filepath.Dir(p)
	if dir == p {
		return p
	}
	return filepath.Join(realPathOrSelf(dir), filepath.Base(p))
}

// stubHelpExec replaces the `--help` runner for the duration of a test and
// clears the capability cache on both ends so tests never observe each other.
// It also installs a stub identity, so a test can exercise the help parsing
// without a real binary on disk; call stubBinaryIdentity afterwards to drive
// the cache-key path instead.
func stubHelpExec(t *testing.T, fn func(bin string) (string, error)) {
	t.Helper()
	originalHelp := runHelpOutput
	originalIdentity := binaryIdentity
	runHelpOutput = fn
	binaryIdentity = func(string) (string, bool) { return "stub-identity", true }
	resetThemeFlagCache()
	t.Cleanup(func() {
		runHelpOutput = originalHelp
		binaryIdentity = originalIdentity
		resetThemeFlagCache()
	})
}

// stubUserHome points userHomeDir at a temporary home and installs the given
// yesmem theme names under ~/.config/opencode/themes (empty = nothing
// installed).
func stubUserHome(t *testing.T, themes ...string) {
	t.Helper()
	home := t.TempDir()
	for _, name := range themes {
		dst := filepath.Join(home, ".config", "opencode", "themes", name+".json")
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	original := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = original })
}

// stubBinaryIdentity replaces the cache-key builder (mtime+size) for the
// duration of a test, so the binary-replacement path is testable without
// touching the filesystem.
func stubBinaryIdentity(t *testing.T, fn func(path string) (string, bool)) {
	t.Helper()
	original := binaryIdentity
	binaryIdentity = fn
	resetThemeFlagCache()
	t.Cleanup(func() {
		binaryIdentity = original
		resetThemeFlagCache()
	})
}

// mustHandler is the default handler fixture: in-memory store, its own data dir,
// and agent process spawning disabled so a test can never launch a real agent.
func mustHandler(t *testing.T) (*Handler, *storage.Store) {
	t.Helper()
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	h := NewHandler(s, nil)
	h.dataDir = t.TempDir()
	h.disableAgentProcesses = true
	return h, s
}

// mustFileHandler opens a handler on a file-backed store: session pins live in
// runtime.db, permanent pins in yesmem.db — separate tables with independent ID
// sequences, as in production. Reach for it whenever a test reads a row back that
// another connection just wrote: a :memory: store opens a fresh image per
// connection, so the reader pool would not see the writer's rows.
func mustFileHandler(t *testing.T) (*Handler, *storage.Store) {
	t.Helper()
	dir := t.TempDir()
	s, err := storage.Open(filepath.Join(dir, "yesmem.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	h := NewHandler(s, nil)
	h.dataDir = dir
	h.disableAgentProcesses = true
	return h, s
}

// mustHandlerWithEmbedding creates a test handler with embedding_vector column
// (missing in initial schema, added only via migration v0.26).
func mustHandlerWithEmbedding(t *testing.T) (*Handler, *storage.Store) {
	t.Helper()
	h, s := mustHandler(t)
	s.DB().Exec(`ALTER TABLE learnings ADD COLUMN embedding_vector BLOB`)
	return h, s
}

func mustHandlerWithCapStore(t *testing.T) *Handler {
	t.Helper()
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.OpenCapsDB(t.TempDir()); err != nil {
		t.Fatalf("open cap_store: %v", err)
	}
	t.Cleanup(func() { s.CloseCapsDB(); s.Close() })
	return NewHandler(s, nil)
}

// mustHandlerWithAmbiguousProbeProjects seeds two sessions whose projects share
// the basename "probe" — a short name that cannot be resolved without knowing
// which session asks — and registers the shared service PID for the first.
func mustHandlerWithAmbiguousProbeProjects(t *testing.T) *Handler {
	t.Helper()
	h, s := mustHandler(t)
	isolateHome(t)
	for _, sess := range []struct{ id, project string }{
		{"opencode:ses_LASTREG", "/elsewhere/probe"},
		{"opencode:ses_OTHER", "/other/probe"},
	} {
		if err := s.UpsertSession(&models.Session{ID: sess.id, Project: sess.project, StartedAt: time.Now()}); err != nil {
			t.Fatalf("seed session %s: %v", sess.id, err)
		}
	}
	if resp := h.handleRegisterPID(map[string]any{
		"session_id": "opencode:ses_LASTREG", "pid": float64(opencodeSharedServicePID), "source_agent": "opencode",
	}); resp.Error != "" {
		t.Fatalf("register pid: %s", resp.Error)
	}
	return h
}
