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
	if _, err := m.BeginSave(); !errors.Is(err, ErrNoGame) {
		t.Fatalf("BeginSave on an idle session = %v, want ErrNoGame", err)
	}
	if err := m.RequireGame(); !errors.Is(err, ErrNoGame) {
		t.Fatalf("RequireGame on an idle session = %v, want ErrNoGame", err)
	}
}

func TestSecondSaveIsRefused(t *testing.T) {
	m := loaded(t)
	tok, err := m.BeginSave()
	if err != nil {
		t.Fatalf("first BeginSave: %v", err)
	}
	if _, err := m.BeginSave(); !errors.Is(err, ErrSaveInProgress) {
		t.Fatalf("second BeginSave = %v, want ErrSaveInProgress", err)
	}
	m.EndSave(tok)
	if _, err := m.BeginSave(); err != nil {
		t.Fatalf("BeginSave after EndSave: %v", err)
	}
}

// A save abandoned to a crash releases its claim through Clear, and a newer
// save may hold the claim by the time the abandoned save's deferred EndSave
// fires — up to SAVE_WAIT later. That stale release must be a no-op.
func TestStaleEndSaveDoesNotReleaseANewerClaim(t *testing.T) {
	m := loaded(t)
	old, err := m.BeginSave()
	if err != nil {
		t.Fatalf("BeginSave: %v", err)
	}
	m.Clear() // the emulator died mid-save

	if err := m.BeginLaunch(); err != nil {
		t.Fatalf("BeginLaunch: %v", err)
	}
	m.EndLaunch("/romm/library/dc/other.chd", "other", time.Now())
	cur, err := m.BeginSave()
	if err != nil {
		t.Fatalf("BeginSave for the new game: %v", err)
	}

	m.EndSave(old) // the abandoned save's deferred release finally fires
	if !m.Saving() {
		t.Fatal("a stale EndSave released the newer save's claim")
	}
	if m.SaveHeld(old) {
		t.Fatal("SaveHeld recognised a token from a released claim")
	}
	if !m.SaveHeld(cur) {
		t.Fatal("SaveHeld does not recognise the live claim")
	}

	m.EndSave(cur)
	if m.Saving() {
		t.Fatal("the live token did not release its own claim")
	}
}

// A load while a save is running would roll the player back onto the state the
// save is still writing.
func TestLoadIsRefusedDuringASave(t *testing.T) {
	m := loaded(t)
	if _, err := m.BeginSave(); err != nil {
		t.Fatalf("BeginSave: %v", err)
	}
	if err := m.RequireGame(); !errors.Is(err, ErrSaveInProgress) {
		t.Fatalf("RequireGame during a save = %v, want ErrSaveInProgress", err)
	}
}

// Killing the emulator mid-write truncates the state, so a launch has to wait.
func TestLaunchIsRefusedDuringASave(t *testing.T) {
	m := loaded(t)
	if _, err := m.BeginSave(); err != nil {
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

// Killing the emulator mid-write truncates the state, and the truncated file
// then reads back as stable — so a stop waits behind a save exactly as a
// launch does.
func TestStopIsRefusedDuringASaveClaim(t *testing.T) {
	m := loaded(t)
	tok, err := m.BeginSave()
	if err != nil {
		t.Fatalf("BeginSave: %v", err)
	}
	if err := m.BeginStop(); !errors.Is(err, ErrSaveInProgress) {
		t.Fatalf("BeginStop during a save = %v, want ErrSaveInProgress", err)
	}
	m.EndSave(tok)
	if err := m.BeginStop(); err != nil {
		t.Fatalf("BeginStop after EndSave: %v", err)
	}
}

// A stop interleaved with a launch would kill the game the launch just booted,
// and the launch's session record would outlive its process.
func TestStopAndLaunchExcludeEachOther(t *testing.T) {
	m := loaded(t)
	if err := m.BeginStop(); err != nil {
		t.Fatalf("BeginStop: %v", err)
	}
	if err := m.BeginLaunch(); !errors.Is(err, ErrStopInProgress) {
		t.Fatalf("BeginLaunch during a stop = %v, want ErrStopInProgress", err)
	}
	if _, err := m.BeginSave(); !errors.Is(err, ErrStopInProgress) {
		t.Fatalf("BeginSave during a stop = %v, want ErrStopInProgress", err)
	}
	if err := m.RequireGame(); !errors.Is(err, ErrStopInProgress) {
		t.Fatalf("RequireGame during a stop = %v, want ErrStopInProgress", err)
	}
	m.EndStop()

	if m.Snapshot().Active {
		t.Fatal("EndStop left the session active")
	}
	if err := m.BeginLaunch(); err != nil {
		t.Fatalf("BeginLaunch after EndStop: %v", err)
	}
	if err := m.BeginStop(); !errors.Is(err, ErrLaunchInProgress) {
		t.Fatalf("BeginStop during a launch = %v, want ErrLaunchInProgress", err)
	}
}

// A kill that failed leaves the emulator running: the claim is released but
// the loaded game must stay recorded.
func TestAbortStopKeepsTheGame(t *testing.T) {
	m := loaded(t)
	if err := m.BeginStop(); err != nil {
		t.Fatalf("BeginStop: %v", err)
	}
	m.AbortStop()

	if !m.Snapshot().Active {
		t.Fatal("AbortStop forgot the game that is still running")
	}
	if err := m.BeginStop(); err != nil {
		t.Fatalf("BeginStop after AbortStop: %v", err)
	}
}

// The background relaunch of the idle game list must yield to any session that
// arrived after the stop or crash that scheduled it.
func TestIdleRelaunchYieldsToANewSession(t *testing.T) {
	m := New()
	if !m.IdleRelaunchOK() {
		t.Fatal("an idle session should allow the game list")
	}

	if err := m.BeginLaunch(); err != nil {
		t.Fatalf("BeginLaunch: %v", err)
	}
	if m.IdleRelaunchOK() {
		t.Fatal("a launch in flight should block the game list")
	}
	m.EndLaunch("/romm/library/dc/game.chd", "game", time.Now())
	if m.IdleRelaunchOK() {
		t.Fatal("a loaded game should block the game list")
	}

	if err := m.BeginStop(); err != nil {
		t.Fatalf("BeginStop: %v", err)
	}
	if m.IdleRelaunchOK() {
		t.Fatal("a stop in flight should block the game list")
	}
	m.EndStop()
	if !m.IdleRelaunchOK() {
		t.Fatal("after EndStop the game list should be allowed again")
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
	if _, err := m.BeginSave(); err != nil {
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
			if _, err := m.BeginSave(); err == nil {
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
