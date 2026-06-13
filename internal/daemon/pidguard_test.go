package daemon

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// killShell on a restored session (cmd == nil) must refuse to signal a PID whose
// recorded start-time no longer matches — i.e. a recycled PID now belonging to
// an unrelated process. This is the F3 PID-reuse guard.
func TestKillShellRefusesRecycledPID(t *testing.T) {
	canary := exec.Command("/bin/sleep", "60")
	if err := canary.Start(); err != nil {
		t.Fatalf("start canary: %v", err)
	}
	defer canary.Process.Kill()
	pid := canary.Process.Pid

	realStart, ok := procStartTicks(pid)
	if !ok {
		t.Fatalf("could not read start-time for pid %d", pid)
	}

	// A restored session that thinks `pid` is its shell, but with a stale
	// start-time (simulating the original shell having exited and `pid` being
	// recycled by this unrelated canary).
	sess := &Session{shellPid: pid, shellStart: realStart + 1}

	if sess.IsAlive() {
		t.Fatal("IsAlive returned true for a recycled PID (start-time mismatch)")
	}
	sess.killShell()
	time.Sleep(150 * time.Millisecond)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("canary was killed despite start-time mismatch: %v", err)
	}
}

// The positive case: when the recorded start-time matches, the session is alive
// and killShell signals the PID.
func TestKillShellKillsMatchingPID(t *testing.T) {
	canary := exec.Command("/bin/sleep", "60")
	if err := canary.Start(); err != nil {
		t.Fatalf("start canary: %v", err)
	}
	defer canary.Process.Kill()
	pid := canary.Process.Pid

	start, ok := procStartTicks(pid)
	if !ok {
		t.Fatalf("could not read start-time for pid %d", pid)
	}
	sess := &Session{shellPid: pid, shellStart: start}

	if !sess.IsAlive() {
		t.Fatal("IsAlive returned false for a matching live PID")
	}
	sess.killShell()

	done := make(chan struct{})
	go func() { canary.Process.Wait(); close(done) }()
	select {
	case <-done:
		// killed as expected
	case <-time.After(time.Second):
		t.Fatal("killShell did not kill a PID whose start-time matched")
	}
}
