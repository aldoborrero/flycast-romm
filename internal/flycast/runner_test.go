package flycast

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/aldoborrero/flycast-romm/internal/config"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// sleepBin resolves the sleep binary the process-lifecycle tests stand in for
// the emulator, skipping the test where none exists.
func sleepBin(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	return bin
}

// leftover spawns a process the way a dead broker would have left one behind:
// its own session and group, reparented to nobody the runner knows. The
// returned channel closes once the process has been reaped, which is when
// alive() stops seeing it.
func leftover(t *testing.T, bin string, args ...string) (*os.Process, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(reaped)
	}()
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	return cmd.Process, reaped
}

func writePIDFile(t *testing.T, pid int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flycast.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// shrinkCrashLoopPacing makes the monitor's relaunch pauses negligible for the
// duration of a test, restoring all the pacing variables afterwards — a test
// may also override rapidExitWindow without registering its own cleanup.
func shrinkCrashLoopPacing(t *testing.T) {
	t.Helper()
	window, rapid, slow := rapidExitWindow, relaunchDelayRapid, relaunchDelaySlow
	relaunchDelayRapid, relaunchDelaySlow = time.Millisecond, time.Millisecond
	t.Cleanup(func() {
		rapidExitWindow, relaunchDelayRapid, relaunchDelaySlow = window, rapid, slow
	})
}

// crashOnce hands the monitor an already-dead process, as if the emulator
// crashed right after spawning.
func crashOnce(t *testing.T, r *Runner, bin string, args ...string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.proc = cmd.Process
	r.intentional = false
	r.mu.Unlock()
	r.monitor(cmd)
}

func TestMonitorGivesUpAfterConsecutiveRapidCrashes(t *testing.T) {
	bin := sleepBin(t)
	shrinkCrashLoopPacing(t)

	cleared := 0
	r := NewRunner(config.Config{FlycastBin: bin}, discardLogger(),
		func() { cleared++ },
		func() bool { return false }) // gate closed: no real relaunch in a test

	for i := range crashLoopLimit {
		crashOnce(t, r, bin, "0")
		want := i+1 == crashLoopLimit
		if got := r.Probe().RelaunchAbandoned; got != want {
			t.Fatalf("after %d rapid crashes RelaunchAbandoned = %v, want %v", i+1, got, want)
		}
	}
	if cleared != crashLoopLimit {
		t.Errorf("onExit ran %d times, want once per crash (%d)", cleared, crashLoopLimit)
	}

	// Launch calls this on every explicit boot: the documented recovery path.
	r.recoverCrashLoop()
	if r.Probe().RelaunchAbandoned {
		t.Error("recoverCrashLoop left the limiter tripped")
	}
}

// Only consecutive rapid deaths trip the limiter: a process that ran for a
// while resets the counter, so a game that crashes once a night never
// accumulates toward giving up.
func TestMonitorSlowExitResetsTheCrashCounter(t *testing.T) {
	bin := sleepBin(t)
	shrinkCrashLoopPacing(t)
	rapidExitWindow = 50 * time.Millisecond

	r := NewRunner(config.Config{FlycastBin: bin}, discardLogger(),
		nil, func() bool { return false })

	crashOnce(t, r, bin, "0")
	crashOnce(t, r, bin, "0")
	crashOnce(t, r, bin, "0.2") // outlives the window: resets the counter
	crashOnce(t, r, bin, "0")
	crashOnce(t, r, bin, "0")

	if r.Probe().RelaunchAbandoned {
		t.Fatal("the limiter tripped although no three rapid crashes were consecutive")
	}
}

func TestKillLeftoverStopsAPredecessorsEmulator(t *testing.T) {
	bin := sleepBin(t)
	proc, reaped := leftover(t, bin, "300")

	cfg := config.Config{
		FlycastBin: bin, // what the pidfile check matches against cmdline
		PIDFile:    writePIDFile(t, proc.Pid),
		QuitWait:   2 * time.Second,
	}
	r := NewRunner(cfg, discardLogger(), nil, nil)

	if err := r.KillLeftover(context.Background()); err != nil {
		t.Fatalf("KillLeftover: %v", err)
	}
	select {
	case <-reaped:
	case <-time.After(3 * time.Second):
		t.Fatal("the leftover process is still running")
	}
	if _, err := os.Stat(cfg.PIDFile); !os.IsNotExist(err) {
		t.Error("the pidfile survived the kill")
	}
}

// PIDs recycle and the file can outlive the process it named, so a PID whose
// command line is not flycast's must be left alone — killing it would take
// down an innocent process every time the number happened to be reused.
func TestKillLeftoverLeavesARecycledPIDAlone(t *testing.T) {
	bin := sleepBin(t)
	proc, _ := leftover(t, bin, "300")

	cfg := config.Config{
		FlycastBin: "/opt/flycast/AppRun", // does not match the sleep's cmdline
		PIDFile:    writePIDFile(t, proc.Pid),
		QuitWait:   time.Second,
	}
	r := NewRunner(cfg, discardLogger(), nil, nil)

	if err := r.KillLeftover(context.Background()); err != nil {
		t.Fatalf("KillLeftover: %v", err)
	}
	if !alive(proc.Pid) {
		t.Error("KillLeftover killed a process that is not flycast")
	}
	if _, err := os.Stat(cfg.PIDFile); !os.IsNotExist(err) {
		t.Error("the stale pidfile was not discarded")
	}
}

func TestKillLeftoverDiscardsDeadAndMalformedPIDFiles(t *testing.T) {
	bin := sleepBin(t)
	// A PID that is already gone.
	dead := exec.Command(bin, "0")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}

	for name, content := range map[string]string{
		"dead pid":  strconv.Itoa(dead.Process.Pid) + "\n",
		"malformed": "bogus\n",
	} {
		path := filepath.Join(t.TempDir(), "flycast.pid")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		r := NewRunner(config.Config{FlycastBin: bin, PIDFile: path, QuitWait: time.Second},
			discardLogger(), nil, nil)

		if err := r.KillLeftover(context.Background()); err != nil {
			t.Fatalf("%s: KillLeftover: %v", name, err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s: the pidfile was not discarded", name)
		}
	}
}

// A gated LaunchIdle must be a complete no-op: no kill, no channel reset, no
// spawn. The channel files are the observable side effect a launch always has
// (launchLocked resets them before spawning), so an untouched `ready` file
// proves the gate returned before any of that ran.
func TestLaunchIdleRespectsTheGate(t *testing.T) {
	cfg := config.Config{ConfigDir: t.TempDir()}
	r := NewRunner(cfg, discardLogger(), nil, func() bool { return false })

	marker := filepath.Join(cfg.ChannelDir(), "ready")
	if err := os.MkdirAll(cfg.ChannelDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(ProtocolVersion+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := r.LaunchIdle(context.Background()); err != nil {
		t.Fatalf("a gated LaunchIdle should do nothing, got %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("a gated LaunchIdle reset the lua channel, so it went past the gate")
	}
}
