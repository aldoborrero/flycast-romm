package flycast

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeScript stands in for romm-broker.lua: it polls the command file and acks,
// which is the whole protocol the broker depends on.
type fakeScript struct {
	ch     *channel
	reply  func(seq, verb string, arg int) (status, reason string)
	stop   chan struct{}
	closed sync.Once
}

func startFakeScript(t *testing.T, ch *channel, reply func(seq, verb string, arg int) (string, string)) *fakeScript {
	t.Helper()
	f := &fakeScript{ch: ch, reply: reply, stop: make(chan struct{})}
	go f.run()
	t.Cleanup(f.Stop)
	return f
}

func (f *fakeScript) Stop() { f.closed.Do(func() { close(f.stop) }) }

func (f *fakeScript) run() {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-f.stop:
			return
		case <-tick.C:
		}
		raw, err := os.ReadFile(f.ch.commandPath())
		if err != nil {
			continue
		}
		_ = os.Remove(f.ch.commandPath())

		fields := strings.Fields(strings.TrimSpace(string(raw)))
		if len(fields) < 2 {
			continue
		}
		arg := 0
		if len(fields) > 2 {
			for _, c := range fields[2] {
				if c >= '0' && c <= '9' {
					arg = arg*10 + int(c-'0')
				}
			}
		}
		status, reason := f.reply(fields[0], fields[1], arg)
		line := fields[0] + " " + status
		if reason != "" {
			line += " " + reason
		}
		_ = os.WriteFile(f.ch.ackPath(), []byte(line+"\n"), 0o644)
	}
}

func newTestChannel(t *testing.T) *channel {
	t.Helper()
	ch := newChannel(t.TempDir())
	if err := ch.Reset(); err != nil {
		t.Fatal(err)
	}
	return ch
}

func markReady(t *testing.T, ch *channel) {
	t.Helper()
	if err := os.WriteFile(ch.readyPath(), []byte(ProtocolVersion+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestChannelRoundTrip(t *testing.T) {
	ch := newTestChannel(t)
	markReady(t, ch)

	var gotVerb string
	var gotArg int
	startFakeScript(t, ch, func(_, verb string, arg int) (string, string) {
		gotVerb, gotArg = verb, arg
		return "ok", ""
	})

	if err := ch.Do(context.Background(), 3*time.Second, "save", 7); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if gotVerb != "save" || gotArg != 7 {
		t.Fatalf("script saw %q %d, want save 7", gotVerb, gotArg)
	}
}

// Two commands in flight at once used to overwrite each other's command file
// before the script read it: the loser's caller then timed out for no reason.
// Do serialises, so every command reaches the script and gets its own ack.
func TestChannelSerialisesConcurrentCommands(t *testing.T) {
	ch := newTestChannel(t)
	markReady(t, ch)

	var mu sync.Mutex
	seen := map[string]bool{}
	startFakeScript(t, ch, func(seq, _ string, _ int) (string, string) {
		mu.Lock()
		seen[seq] = true
		mu.Unlock()
		return "ok", ""
	})

	const callers = 8
	errs := make(chan error, callers)
	for i := range callers {
		go func(slot int) {
			errs <- ch.Do(context.Background(), 5*time.Second, "load", slot)
		}(i)
	}
	for range callers {
		if err := <-errs; err != nil {
			t.Fatalf("a concurrent Do failed: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != callers {
		t.Fatalf("the script saw %d distinct commands, want %d", len(seen), callers)
	}
}

func TestChannelSurfacesRefusals(t *testing.T) {
	ch := newTestChannel(t)
	markReady(t, ch)
	startFakeScript(t, ch, func(string, string, int) (string, string) {
		return "err", "no game loaded"
	})

	err := ch.Do(context.Background(), 3*time.Second, "load", 2)
	if err == nil || !strings.Contains(err.Error(), "no game loaded") {
		t.Fatalf("Do = %v, want the script's reason", err)
	}
}

// An ack left over from before a broker restart carries sequence 1, which is
// also the first sequence a fresh broker sends. It must not satisfy that
// command.
func TestChannelIgnoresStaleAcks(t *testing.T) {
	ch := newTestChannel(t)
	markReady(t, ch)
	if err := os.WriteFile(ch.ackPath(), []byte("1 ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := ch.Do(context.Background(), 300*time.Millisecond, "save", 1)
	if !errors.Is(err, ErrLuaTimeout) {
		t.Fatalf("Do with only a stale ack = %v, want ErrLuaTimeout", err)
	}
}

func TestChannelRefusesWhenTheScriptNeverLoaded(t *testing.T) {
	ch := newTestChannel(t)
	if err := ch.Do(context.Background(), time.Second, "save", 1); !errors.Is(err, ErrLuaUnavailable) {
		t.Fatalf("Do without a ready file = %v, want ErrLuaUnavailable", err)
	}
}

// A script announcing a version this broker does not speak is not ready: the
// mod ships both halves, so a mismatch means /config was edited by hand.
func TestChannelRejectsAForeignProtocolVersion(t *testing.T) {
	ch := newTestChannel(t)
	if err := os.WriteFile(ch.readyPath(), []byte("99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ch.Ready() {
		t.Fatal("Ready accepted a protocol version it does not speak")
	}
}

// Reset runs before every launch so the previous process's files cannot be
// mistaken for the new one's.
func TestResetClearsTheChannel(t *testing.T) {
	ch := newTestChannel(t)
	markReady(t, ch)
	for _, p := range []string{ch.ackPath(), ch.statePath(), ch.commandPath()} {
		if err := os.WriteFile(p, []byte("stale\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := ch.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	entries, err := os.ReadDir(ch.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Fatalf("Reset left %s behind", filepath.Join(ch.dir, e.Name()))
	}
}

func TestWaitStateReportsRunning(t *testing.T) {
	ch := newTestChannel(t)
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(ch.statePath(), []byte("running\n"), 0o644)
	}()

	if err := ch.WaitState(context.Background(), "running", 3*time.Second); err != nil {
		t.Fatalf("WaitState: %v", err)
	}
}

func TestWaitStateTimesOut(t *testing.T) {
	ch := newTestChannel(t)
	if err := ch.WaitState(context.Background(), "running", 200*time.Millisecond); !errors.Is(err, ErrLuaTimeout) {
		t.Fatalf("WaitState = %v, want ErrLuaTimeout", err)
	}
}
