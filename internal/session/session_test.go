package session

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func loaded(t *testing.T) *Manager {
	t.Helper()
	m := New()
	if err := m.BeginLaunch(); err != nil {
		t.Fatalf("BeginLaunch: %v", err)
	}
	m.EndLaunch("/romm/library/dc/game.chd", "game", time.Unix(1700000000, 0))
	return m
}

func TestSaveNeedsAGame(t *testing.T) {
	m := New()
	if err := m.BeginSave(); !errors.Is(err, ErrNoGame) {
		t.Fatalf("BeginSave on an idle session = %v, want ErrNoGame", err)
	}
	if err := m.RequireGame(); !errors.Is(err, ErrNoGame) {
		t.Fatalf("RequireGame on an idle session = %v, want ErrNoGame", err)
	}
}

func TestSecondSaveIsRefused(t *testing.T) {
	m := loaded(t)
	if err := m.BeginSave(); err != nil {
		t.Fatalf("first BeginSave: %v", err)
	}
	if err := m.BeginSave(); !errors.Is(err, ErrSaveInProgress) {
		t.Fatalf("second BeginSave = %v, want ErrSaveInProgress", err)
	}
	m.EndSave()
	if err := m.BeginSave(); err != nil {
		t.Fatalf("BeginSave after EndSave: %v", err)
	}
}

// A load while a save is running would roll the player back onto the state the
// save is still writing.
func TestLoadIsRefusedDuringASave(t *testing.T) {
	m := loaded(t)
	if err := m.BeginSave(); err != nil {
		t.Fatalf("BeginSave: %v", err)
	}
	if err := m.RequireGame(); !errors.Is(err, ErrSaveInProgress) {
		t.Fatalf("RequireGame during a save = %v, want ErrSaveInProgress", err)
	}
}

// Killing the emulator mid-write truncates the state, so a launch has to wait.
func TestLaunchIsRefusedDuringASave(t *testing.T) {
	m := loaded(t)
	if err := m.BeginSave(); err != nil {
		t.Fatalf("BeginSave: %v", err)
	}
	if err := m.BeginLaunch(); !errors.Is(err, ErrSaveInProgress) {
		t.Fatalf("BeginLaunch during a save = %v, want ErrSaveInProgress", err)
	}
}

func TestSecondLaunchIsRefused(t *testing.T) {
	m := New()
	if err := m.BeginLaunch(); err != nil {
		t.Fatalf("first BeginLaunch: %v", err)
	}
	if err := m.BeginLaunch(); !errors.Is(err, ErrLaunchInProgress) {
		t.Fatalf("second BeginLaunch = %v, want ErrLaunchInProgress", err)
	}
	m.AbortLaunch()
	if err := m.BeginLaunch(); err != nil {
		t.Fatalf("BeginLaunch after AbortLaunch: %v", err)
	}
}

// An empty rom path means the emulator went back to its idle game list, which
// is a live process with no session.
func TestIdleLaunchClearsTheSession(t *testing.T) {
	m := loaded(t)
	m.EndLaunch("", "", time.Now())

	snap := m.Snapshot()
	if snap.Active || snap.ROMPath != "" || snap.ROMName != "" || !snap.StartedAt.IsZero() {
		t.Fatalf("idle launch left a session behind: %+v", snap)
	}
}

func TestClearForgetsEverything(t *testing.T) {
	m := loaded(t)
	if err := m.BeginSave(); err != nil {
		t.Fatalf("BeginSave: %v", err)
	}
	m.Clear()

	if m.Snapshot().Active {
		t.Fatal("Clear left the session active")
	}
	// A save flag surviving the process it belonged to would wedge every
	// later save with a 409.
	if m.Saving() {
		t.Fatal("Clear left save_in_progress set")
	}
}

// Two concurrent /save-state requests must not both win the claim.
func TestOnlyOneConcurrentSaveWins(t *testing.T) {
	m := loaded(t)

	const racers = 32
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)
	start := make(chan struct{})
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := m.BeginSave(); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if wins != 1 {
		t.Fatalf("%d of %d concurrent BeginSave calls succeeded, want exactly 1", wins, racers)
	}
}
