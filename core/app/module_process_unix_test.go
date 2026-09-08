//go:build darwin || linux

package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Run the actual shell/pipe/descendant path. A bounded Wait on the shell alone
// cannot prove that its children stopped touching the verification checkout.
func TestDeclaredTimeoutStopsStubbornDescendants(t *testing.T) {
	dir := t.TempDir()
	worker := "trap '' TERM\necho $$ > child.pid\nwhile :; do echo tick >> heartbeat; sleep 0.03; done\n"
	if err := os.WriteFile(filepath.Join(dir, "worker.sh"), []byte(worker), 0o600); err != nil {
		t.Fatal(err)
	}
	report := runDeclaredCommand(context.Background(), dir, "bash worker.sh & wait", 350*time.Millisecond)
	pid := readOwnedTestChild(t, dir)
	defer stopOwnedTestChild(pid)
	if report.Status != "timeout" {
		t.Fatalf("report=%#v", report)
	}
	assertTestChildStopped(t, pid)
	before, err := os.ReadFile(filepath.Join(dir, "heartbeat"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	after, err := os.ReadFile(filepath.Join(dir, "heartbeat"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("descendant wrote after command completion")
	}
}

func TestDeclaredCancellationStopsPipelineChildren(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan CommandReport, 1)
	go func() {
		done <- runDeclaredCommand(ctx, dir, "sleep 20 & echo $! > child.pid; wait | cat", 30*time.Second)
	}()
	pid := readOwnedTestChild(t, dir)
	defer stopOwnedTestChild(pid)
	cancel()
	select {
	case report := <-done:
		if report.Status == "passed" {
			t.Fatalf("cancellation passed: %#v", report)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("cancellation did not settle")
	}
	assertTestChildStopped(t, pid)
}

func TestDeclaredSuccessCannotLeaveBackgroundWriter(t *testing.T) {
	dir := t.TempDir()
	report := runDeclaredCommand(context.Background(), dir,
		"sleep 20 </dev/null >/dev/null 2>&1 & echo $! > child.pid", time.Second)
	pid := readOwnedTestChild(t, dir)
	defer stopOwnedTestChild(pid)
	if report.Status == "passed" {
		t.Error("unjoined background process was reported as completed work")
	}
	assertTestChildStopped(t, pid)
}

func TestDeclaredTimeoutDoesNotClaimSetsidChildren(t *testing.T) {
	// Process-group cleanup is not ownership of setsid/Docker-daemon children.
	// Darwin images have no util-linux `setsid(1)`; python3.os.setsid is the
	// same syscall on linux and darwin.
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required to create a new session without util-linux")
	}
	dir := t.TempDir()
	child := "import os, signal, time\n" +
		"os.setsid()\n" +
		"open('child.pid', 'w', encoding='ascii').write(str(os.getpid()))\n" +
		"signal.signal(signal.SIGTERM, signal.SIG_IGN)\n" +
		"while True:\n" +
		"    time.sleep(0.05)\n"
	if err := os.WriteFile(filepath.Join(dir, "setsid_child.py"), []byte(child), 0o600); err != nil {
		t.Fatal(err)
	}
	report := runDeclaredCommand(
		context.Background(),
		dir,
		"python3 setsid_child.py & wait",
		350*time.Millisecond,
	)
	pid := readOwnedTestChild(t, dir)
	defer stopOwnedTestChild(pid)
	if report.Status == "passed" {
		t.Fatalf("setsid child made the parent look finished: %#v", report)
	}
	b, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil || strings.TrimSpace(string(b)) == "" || strings.HasPrefix(strings.TrimSpace(string(b)), "Z") {
		t.Fatalf("setsid child %d did not remain outside the module process group: err=%v stat=%q", pid, err, b)
	}
}

func readOwnedTestChild(t *testing.T, dir string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(filepath.Join(dir, "child.pid"))
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err == nil && pid > 1 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("test child did not start")
	return 0
}

func stopOwnedTestChild(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

func assertTestChildStopped(t *testing.T, pid int) {
	t.Helper()
	b, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	// An unreaped zombie has already stopped executing; reaping belongs to its
	// parent/init. It cannot continue writing into the verification workspace.
	if err == nil && strings.TrimSpace(string(b)) != "" && !strings.HasPrefix(strings.TrimSpace(string(b)), "Z") {
		t.Fatalf("child %d still executes after command returned: %s", pid, b)
	}
}
