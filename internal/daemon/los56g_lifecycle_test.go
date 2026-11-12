package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// YM-394 (OPS-97): after SIGTERM and the grace period, stopDaemonFromPidFile
// re-verifies the identity before it sends SIGKILL. The verification and the
// liveness answer are two reads of /proc, and the process can end between them —
// exactly what happens when a daemon exits cleanly but a step late. The identity
// read then fails, and the function reported it as an error ("did not exit ... and
// no longer identifies"), although the process it was asked to stop was gone.
//
// The fixture is a real child of this process, so the exit is real; the poll seam
// holds "not yet gone" for the grace window to place the exit in the reported
// window instead of leaving it to timing. No daemon, socket or data dir of the
// host is touched.
func TestLos56gStopAcceptsAnExitDuringTheIdentityCheck(t *testing.T) {
	requireProcTable(t)
	oldSignal, oldGone, oldWait, oldNow, oldExe := daemonProcSignal, daemonProcGone, daemonProcGraceWait, daemonProcNow, selfBinaryPath
	t.Cleanup(func() {
		daemonProcSignal, daemonProcGone, daemonProcGraceWait, daemonProcNow, selfBinaryPath = oldSignal, oldGone, oldWait, oldNow, oldExe
	})

	fixtureBin, fixture := ym297SpawnAliveDaemonArgv(t)
	selfBinaryPath = fixtureBin
	daemonProcGraceWait = func(time.Duration) {}
	daemonProcNow = func() time.Time { return time.Unix(0, 0) }

	// The signal ends the fixture and reaps it, so the identity read afterwards
	// cannot find it. Reaping here, not in the cleanup, is what makes the exit
	// complete rather than a zombie the kernel has not finished with.
	daemonProcSignal = func(pid int, sig syscall.Signal) error {
		if sig != syscall.SIGTERM {
			return nil
		}
		if err := fixture.Process.Signal(syscall.SIGTERM); err != nil {
			return err
		}
		_, _ = fixture.Process.Wait()
		return nil
	}

	// "Still alive" for every poll of the grace window, "gone" from the
	// re-verification on: the window the report measured.
	gracePolls := int(daemonTermGrace/daemonTermPoll) + 3
	polls := 0
	daemonProcGone = func(pid int) bool {
		polls++
		return polls > gracePolls
	}

	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "daemon.pid"), []byte(strconv.Itoa(fixture.Process.Pid)), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	stopped, err := stopDaemonFromPidFile(dataDir)

	if err != nil {
		t.Fatalf("stopDaemonFromPidFile = %v, want nil — the process it was asked to stop did exit (%s)",
			err, describeProcessIdentity(fixture.Process.Pid))
	}
	if !stopped {
		t.Fatalf("stopped = false, want true — a daemon that exited during the grace period is stopped")
	}
}

// The safety half stays: a live process that is not ours is never signalled, and
// the caller still gets the identity error.
func TestLos56gLiveForeignProcessIsStillAnError(t *testing.T) {
	oldSignal, oldGone, oldWait, oldNow, oldExe := daemonProcSignal, daemonProcGone, daemonProcGraceWait, daemonProcNow, selfBinaryPath
	t.Cleanup(func() {
		daemonProcSignal, daemonProcGone, daemonProcGraceWait, daemonProcNow, selfBinaryPath = oldSignal, oldGone, oldWait, oldNow, oldExe
	})

	_, fixture := ym297SpawnAliveDaemonArgv(t)
	selfBinaryPath = "/opt/los56g/not-the-fixture"
	daemonProcGraceWait = func(time.Duration) {}
	daemonProcNow = func() time.Time { return time.Unix(0, 0) }
	signalled := false
	daemonProcSignal = func(pid int, sig syscall.Signal) error {
		signalled = true
		return nil
	}
	daemonProcGone = func(pid int) bool { return false }

	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "daemon.pid"), []byte(strconv.Itoa(fixture.Process.Pid)), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	stopped, err := stopDaemonFromPidFile(dataDir)

	if err == nil {
		t.Fatalf("stopped=%v, err=nil for a live foreign process, want the identity error", stopped)
	}
	if signalled {
		t.Errorf("the foreign process was signalled")
	}
}
