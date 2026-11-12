package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ym297ProcFixture is a /proc-like tree plus a signal recorder: the stop path
// reads every process identity through procRoot, so the fixture is the whole
// process table for the duration of one test.
type ym297ProcFixture struct {
	root      string
	signalled []string
	sigkill   map[int]bool
}

func ym297NewProcFixture(t *testing.T) *ym297ProcFixture {
	t.Helper()
	oldRoot, oldExe := procRoot, selfBinaryPath
	oldSignal, oldGone, oldSleep, oldNow := daemonProcSignal, daemonProcGone, daemonProcGraceWait, daemonProcNow
	t.Cleanup(func() {
		procRoot, selfBinaryPath = oldRoot, oldExe
		daemonProcSignal, daemonProcGone, daemonProcGraceWait, daemonProcNow = oldSignal, oldGone, oldSleep, oldNow
	})

	f := &ym297ProcFixture{root: t.TempDir(), sigkill: map[int]bool{}}
	procRoot = f.root
	selfBinaryPath = "/opt/yesmem/yesmem"
	daemonProcSignal = func(pid int, sig syscall.Signal) error {
		f.signalled = append(f.signalled, fmt.Sprintf("%d:%s", pid, sig))
		if sig == syscall.SIGKILL {
			f.sigkill[pid] = true
		}
		return nil
	}
	// "Gone" mirrors the fixture table: a PID is gone iff its entry is missing or
	// it was SIGKILLed. SIGTERM alone does not end it, which is what proves the
	// SIGTERM→SIGKILL order. Lstat, not Stat: the fixture's exe symlink dangles.
	daemonProcGone = func(pid int) bool {
		_, err := os.Lstat(filepath.Join(f.root, strconv.Itoa(pid), "exe"))
		return err != nil || f.sigkill[pid]
	}
	daemonProcGraceWait = func(time.Duration) {}
	// A clock that advances one second per read: the exit poll terminates
	// immediately instead of waiting out the real grace.
	ticks := 0
	base := time.Now()
	daemonProcNow = func() time.Time {
		ticks++
		return base.Add(time.Duration(ticks) * time.Second)
	}
	return f
}

// addProc materialises one /proc entry: cmdline plus the exe symlink whose
// target is the process identity.
func (f *ym297ProcFixture) addProc(t *testing.T, pid int, exe, argv string) {
	t.Helper()
	dir := filepath.Join(f.root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(argv), 0644); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}
	if err := os.Symlink(exe, filepath.Join(dir, "exe")); err != nil {
		t.Fatalf("symlink exe: %v", err)
	}
}

func (f *ym297ProcFixture) writePidFile(t *testing.T, dataDir, content string) {
	t.Helper()
	if content == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dataDir, "daemon.pid"), []byte(content), 0644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
}

// TestYm297StopTouchesOnlyThePidFileProcess is the regression test for
// YM-297 (1). The unpatched sweep signalled every process whose argv contained
// "yesmem" and "daemon" — the systemd instance, another data dir's daemon, a
// *.test binary from a yesmem worktree. The stop may only touch the PID that
// this data dir's PID file names and that actually runs this binary.
func TestYm297StopTouchesOnlyThePidFileProcess(t *testing.T) {
	const (
		ourDaemon   = 100
		lookalike   = 200
		otherArgv   = 300
		foreignInst = 400
	)
	cases := []struct {
		name        string
		pidFile     string
		want        []string
		wantStopped bool
		wantErr     bool
		scenario    string
	}{
		{
			name:        "own daemon is stopped TERM first",
			pidFile:     strconv.Itoa(ourDaemon),
			want:        []string{"100:terminated", "100:killed"},
			wantStopped: true,
			scenario:    "own instance",
		},
		{
			name:     "lookalike binary is refused, not signalled",
			pidFile:  strconv.Itoa(lookalike),
			want:     nil,
			wantErr:  true,
			scenario: "PID file names a process from a different binary",
		},
		{
			name:     "same binary, other argument is refused",
			pidFile:  strconv.Itoa(otherArgv),
			want:     nil,
			wantErr:  true,
			scenario: "PID file names our binary running agent-tty",
		},
		{
			name:     "no PID file touches nothing",
			pidFile:  "",
			want:     nil,
			scenario: "socket unreachable, no PID file left",
		},
		{
			name:     "PID file naming a free PID touches nothing",
			pidFile:  "999999999",
			want:     nil,
			scenario: "stale PID file",
		},
		{
			name:        "foreign instance is never reached, only the named PID counts",
			pidFile:     strconv.Itoa(ourDaemon),
			want:        []string{"100:terminated", "100:killed"},
			wantStopped: true,
			scenario:    "a second, foreign yesmem daemon exists in the table",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ym297NewProcFixture(t)
			f.addProc(t, ourDaemon, "/opt/yesmem/yesmem", "yesmem\x00daemon")
			f.addProc(t, lookalike, "/home/u/yesmem-internal/daemon.test", "yesmem-internal/daemon.test\x00daemon")
			f.addProc(t, otherArgv, "/opt/yesmem/yesmem", "yesmem\x00agent-tty\x00--sock\x00/tmp/s")
			f.addProc(t, foreignInst, "/opt/yesmem/yesmem", "yesmem\x00daemon\x00--replace")

			dataDir := t.TempDir()
			f.writePidFile(t, dataDir, tc.pidFile)

			stopped, err := stopDaemonFromPidFile(dataDir)
			if (err != nil) != tc.wantErr {
				t.Fatalf("%s: err = %v, wantErr %v", tc.scenario, err, tc.wantErr)
			}
			if stopped != tc.wantStopped {
				t.Errorf("%s: stopped = %v, want %v", tc.scenario, stopped, tc.wantStopped)
			}
			if len(f.signalled) != len(tc.want) {
				t.Fatalf("%s: signals = %v, want exactly %v", tc.scenario, f.signalled, tc.want)
			}
			for i := range tc.want {
				if f.signalled[i] != tc.want[i] {
					t.Fatalf("%s: signals = %v, want %v (SIGTERM before SIGKILL, same PID)", tc.scenario, f.signalled, tc.want)
				}
			}
		})
	}
}

// TestYm297StopReportsSurvivor covers the branch where the daemon ignores both
// signals: the stop must report the failure instead of pretending success.
func TestYm297StopReportsSurvivor(t *testing.T) {
	f := ym297NewProcFixture(t)
	f.addProc(t, 100, "/opt/yesmem/yesmem", "yesmem\x00daemon")
	oldGone, oldNow := daemonProcGone, daemonProcNow
	t.Cleanup(func() { daemonProcGone, daemonProcNow = oldGone, oldNow })
	daemonProcGone = func(pid int) bool { return false } // never dies
	daemonProcNow = func() time.Time { return time.Now().Add(daemonTermGrace * 2) }
	dataDir := t.TempDir()
	f.writePidFile(t, dataDir, "100")

	stopped, err := stopDaemonFromPidFile(dataDir)
	if err == nil || stopped {
		t.Fatalf("stopped=%v err=%v, want stopped=false and an error for a daemon that survives SIGKILL", stopped, err)
	}
}

// TestYm297ReplaceContract is the regression test for the contract between the
// stop and its caller. ensureSingleInstance(replace=true) — what the systemd unit
// (ExecStart=… daemon --replace), the documented CLI flag and the Makefile
// fallback all use — must return nil after a verified stop, and must refuse when
// the PID file names a live process that is not ours (leaving its socket and PID
// file untouched).
func TestYm297ReplaceContract(t *testing.T) {
	requireProcTable(t)
	oldExe, oldGrace := selfBinaryPath, daemonProcGraceWait
	t.Cleanup(func() { selfBinaryPath, daemonProcGraceWait = oldExe, oldGrace })
	// A real wait, not a no-op: the fixture has to be able to die between two
	// polls. With a no-op the loop burns its iteration bound in microseconds,
	// before the signalled process is reapable, and reports a survivor.
	daemonProcGraceWait = time.Sleep

	t.Run("verified stop lets the replacement start", func(t *testing.T) {
		fixtureBin, fixture := ym297SpawnAliveDaemonArgv(t)
		selfBinaryPath = fixtureBin
		dataDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dataDir, "daemon.pid"), []byte(strconv.Itoa(fixture.Process.Pid)), 0644); err != nil {
			t.Fatalf("write pid file: %v", err)
		}
		if err := ensureSingleInstance(dataDir, true); err != nil {
			t.Fatalf("ensureSingleInstance(replace=true) after a verified stop = %v, want nil — the replacement must be allowed to start", err)
		}
		pid, _ := os.ReadFile(filepath.Join(dataDir, "daemon.pid"))
		if string(pid) != strconv.Itoa(os.Getpid()) {
			t.Errorf("PID file = %q, want this process (%d)", pid, os.Getpid())
		}
	})

	t.Run("foreign live process blocks the replacement", func(t *testing.T) {
		selfBinaryPath = "/opt/yesmem/yesmem" // not the fixture's binary
		_, fixture2 := ym297SpawnAliveDaemonArgv(t)
		dataDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dataDir, "daemon.pid"), []byte(strconv.Itoa(fixture2.Process.Pid)), 0644); err != nil {
			t.Fatalf("write pid file: %v", err)
		}
		err := ensureSingleInstance(dataDir, true)
		if err == nil {
			t.Fatalf("ensureSingleInstance(replace=true) with a foreign live PID = nil, want an error — a second daemon must not take over a live data dir (%s)",
				describeProcessIdentity(fixture2.Process.Pid))
		}
		if !strings.Contains(err.Error(), "not this installation's daemon") {
			t.Errorf("error = %v, want it to name the identity mismatch", err)
		}
		if sigErr := fixture2.Process.Signal(syscall.Signal(0)); sigErr != nil {
			t.Errorf("the foreign process was signalled: %v", sigErr)
		}
		if _, statErr := os.Stat(filepath.Join(dataDir, "daemon.pid")); statErr != nil {
			t.Errorf("the PID file of the foreign process was removed: %v", statErr)
		}
	})
}

// describeProcessIdentity reports what the identity check saw. A failure that only
// says "not ours" cannot be told apart from a host that reports something else than
// this test expects, and the CI runner is exactly that case.
func describeProcessIdentity(pid int) string {
	exe, exeErr := os.Readlink(fmt.Sprintf("%s/%d/exe", procRoot, pid))
	return fmt.Sprintf("pid=%d selfBinaryPath=%q exe=%q exeErr=%v cmdline=%q gone=%v procRoot=%q",
		pid, selfBinaryPath, exe, exeErr, readProcCmdline(pid), daemonProcGone(pid), procRoot)
}

// ym297SpawnAliveDaemonArgv starts a real process whose argv is
// "<path> daemon" and which stays alive and dies on SIGTERM. Returns its binary
// path (the identity the stop path compares against) and the command. Used to
// drive ensureSingleInstance, whose liveness check reads the real process table.
// The name deliberately carries no "yesmem": an unpatched sweep in a parallel
// worktree would otherwise kill this fixture mid-test.
func ym297SpawnAliveDaemonArgv(t *testing.T) (string, *exec.Cmd) {
	t.Helper()
	src, err := exec.LookPath("yes")
	if err != nil {
		t.Skip("yes binary not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "daemon-argv-fixture")
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open %s: %v", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(bin, os.O_CREATE|os.O_WRONLY, 0755)
	if err != nil {
		t.Fatalf("create %s: %v", bin, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatalf("copy %s: %v", bin, err)
	}
	out.Close()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { devNull.Close() })

	cmd := exec.Command(bin, "daemon")
	cmd.Stdout = devNull
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	time.Sleep(100 * time.Millisecond)
	// Resolve the path before returning it: the identity check compares it against /proc/<pid>/exe,
	// and the kernel reports that path with symlinks resolved. A t.TempDir() behind a symlink (a
	// runner whose TMPDIR points into one) would otherwise pin an unresolved path, and the fixture
	// would read as another installation's daemon.
	resolved, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatalf("resolve the fixture path: %v", err)
	}
	return resolved, cmd
}

// TestYm297NoBlindProcessSweep is the structural half: the daemon start must not
// enumerate the process table at all, because every process it would find there
// belongs to somebody else. The identity question is answered from the PID file.
func TestYm297NoBlindProcessSweep(t *testing.T) {
	src, err := os.ReadFile("lifecycle.go")
	if err != nil {
		t.Fatalf("read lifecycle.go: %v", err)
	}
	for _, forbidden := range []string{`"/proc"`, "ReadDir(", "filepath.Walk"} {
		if strings.Contains(string(src), forbidden) {
			t.Fatalf("lifecycle.go contains %q — the daemon start must not enumerate the process table; the PID file is the only identity it may act on (YM-297)", forbidden)
		}
	}
}

// ym297ServeFakeDaemon binds this data dir's socket and answers a ping, so the
// socket-reachable branch of ensureSingleInstance is exercised (the branch where
// the inverted stop contract did its damage). It returns only once the socket
// accepts: a caller asking "does a daemon answer here" must not race the accept
// loop, or the same test takes the socket branch on one machine and the PID branch
// on another — two branches, two messages.
func ym297ServeFakeDaemon(t *testing.T, dataDir string) {
	t.Helper()
	sock := SocketPath(dataDir)
	// The kernel keeps a unix socket path in 108 bytes; past that, bind fails with EINVAL, which
	// reads like a bug instead of the environment limit it is.
	if len(sock) > 100 {
		t.Skipf("the socket path would exceed the kernel's limit: %d bytes, %s", len(sock), sock)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen %s: %v", sock, err)
	}
	t.Cleanup(func() {
		ln.Close()
		os.Remove(sock)
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var req Request
			if err := json.NewDecoder(conn).Decode(&req); err == nil {
				json.NewEncoder(conn).Encode(Response{Result: json.RawMessage(`"pong"`)})
			}
			conn.Close()
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, dialErr := net.Dial("unix", sock)
		if dialErr == nil {
			conn.Close()
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the fake daemon never answered on %s: %v", sock, dialErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestYm297SocketBranchRefusesUnidentifiedDaemon covers the branch that a
// regression is most likely to break: a daemon answers on the socket, so a
// replacement must not remove its socket or take over its data dir unless the
// PID file names a process that is verifiably ours.
func TestYm297SocketBranchRefusesUnidentifiedDaemon(t *testing.T) {
	requireProcTable(t)
	oldExe := selfBinaryPath
	t.Cleanup(func() { selfBinaryPath = oldExe })

	t.Run("running daemon without replace is reported", func(t *testing.T) {
		dataDir := t.TempDir()
		ym297ServeFakeDaemon(t, dataDir)
		err := ensureSingleInstance(dataDir, false)
		if err == nil || !strings.Contains(err.Error(), "already running") {
			t.Fatalf("ensureSingleInstance(replace=false) = %v, want 'daemon already running'", err)
		}
	})

	t.Run("foreign PID file blocks the replace and keeps the socket", func(t *testing.T) {
		selfBinaryPath = "/opt/yesmem/yesmem" // not the fixture's binary
		_, fixture := ym297SpawnAliveDaemonArgv(t)
		dataDir := t.TempDir()
		ym297ServeFakeDaemon(t, dataDir)
		pidFile := filepath.Join(dataDir, "daemon.pid")
		if err := os.WriteFile(pidFile, []byte(strconv.Itoa(fixture.Process.Pid)), 0644); err != nil {
			t.Fatalf("write pid file: %v", err)
		}

		err := ensureSingleInstance(dataDir, true)
		if err == nil {
			t.Fatal("ensureSingleInstance(replace=true) with a foreign live PID = nil, want an error")
		}
		// Either wording: the socket branch says "names no process that could be verified as this
		// installation's daemon", the PID branch says "is alive but is not this installation's
		// daemon". Both refuse for the same reason, and the branch depends on whether the dial
		// reaches the listener before the check.
		if !strings.Contains(err.Error(), "this installation's daemon") {
			t.Errorf("error = %v, want the identity mismatch", err)
		}
		if _, statErr := os.Stat(SocketPath(dataDir)); statErr != nil {
			t.Errorf("the live daemon's socket was removed: %v", statErr)
		}
		if data, _ := os.ReadFile(pidFile); string(data) != strconv.Itoa(fixture.Process.Pid) {
			t.Errorf("the PID file was rewritten to %q while its process lives on", data)
		}
	})

	t.Run("no PID file means no takeover", func(t *testing.T) {
		dataDir := t.TempDir()
		ym297ServeFakeDaemon(t, dataDir)

		err := ensureSingleInstance(dataDir, true)
		if err == nil || !strings.Contains(err.Error(), "names no process that could be verified") || !strings.Contains(err.Error(), "remove that file if it is stale") {
			t.Fatalf("ensureSingleInstance(replace=true) without a PID file = %v, want a refusal naming the missing identity", err)
		}
		if _, statErr := os.Stat(SocketPath(dataDir)); statErr != nil {
			t.Errorf("the answering daemon's socket was removed: %v", statErr)
		}
	})
}
