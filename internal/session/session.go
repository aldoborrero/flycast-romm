// Package session tracks the one game session a Flycast container can have.
//
// RomM owns session ownership and single-use enforcement in Redis; this is the
// container-side backstop that keeps two concurrent requests from interleaving
// a launch, a stop and a save, and that tells /status what is loaded.
package session

import (
	"errors"
	"sync"
	"time"
)

var (
	// ErrNoGame is returned by operations that need a loaded game. Flycast is
	// kept alive on its own game list when idle, so "no process" and "no game"
	// are different states and only the latter is an error.
	ErrNoGame = errors.New("no game is running")

	// ErrSaveInProgress guards against a second save, and against a load
	// racing the write it would overwrite.
	ErrSaveInProgress = errors.New("save already in progress")

	// ErrLaunchInProgress guards against two launches interleaving their
	// kill/spawn steps and leaving two emulators running, or none.
	ErrLaunchInProgress = errors.New("launch in progress")

	// ErrStopInProgress guards against anything touching the emulator while a
	// stop is tearing it down.
	ErrStopInProgress = errors.New("stop in progress")
)

// Snapshot is a consistent read of the session, safe to serialise.
type Snapshot struct {
	Active    bool
	ROMPath   string
	ROMName   string
	StartedAt time.Time
}

type Manager struct {
	mu        sync.Mutex
	romPath   string
	romName   string
	startedAt time.Time
	saving    bool
	launching bool
	stopping  bool

	// saveGen numbers save claims. EndSave and SaveHeld take the token
	// BeginSave issued, so a save abandoned to a crash — whose deferred
	// release can fire tens of seconds later — cannot free or observe the
	// claim a newer save holds by then.
	saveGen uint64
}

func New() *Manager { return &Manager{} }

// busyLocked reports the claim that blocks a new operation, in a fixed order
// so every entry point refuses the same state with the same error. Callers
// hold mu.
func (m *Manager) busyLocked() error {
	if m.saving {
		return ErrSaveInProgress
	}
	if m.launching {
		return ErrLaunchInProgress
	}
	if m.stopping {
		return ErrStopInProgress
	}
	return nil
}

// clearLocked forgets the loaded game and releases the save claim. Callers
// hold mu.
func (m *Manager) clearLocked() {
	m.romPath, m.romName = "", ""
	m.startedAt = time.Time{}
	m.saving = false
}

// BeginLaunch claims the launch slot. A launch is refused while a save is in
// flight: killing the emulator mid-write truncates the state.
func (m *Manager) BeginLaunch() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.busyLocked(); err != nil {
		return err
	}
	m.launching = true
	return nil
}

// EndLaunch records the game a completed launch loaded. An empty romPath means
// the emulator went back to its idle game list.
func (m *Manager) EndLaunch(romPath, romName string, startedAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.launching = false
	m.romPath, m.romName = romPath, romName
	if romPath == "" {
		m.startedAt = time.Time{}
		m.romName = ""
		return
	}
	m.startedAt = startedAt
}

// AbortLaunch releases the launch claim without changing what is loaded.
func (m *Manager) AbortLaunch() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.launching = false
}

// BeginStop claims the session for a stop. A stop is as destructive as a
// launch and takes the same exclusions: killing the emulator during an
// in-flight save truncates the state being written, and a stop interleaved
// with a launch kills the game the launch just booted.
func (m *Manager) BeginStop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.busyLocked(); err != nil {
		return err
	}
	m.stopping = true
	return nil
}

// AbortStop releases the stop claim without touching what is loaded, for a
// kill that failed and may have left the emulator running.
func (m *Manager) AbortStop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopping = false
}

// EndStop forgets the loaded game and releases the stop claim.
func (m *Manager) EndStop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopping = false
	m.clearLocked()
}

// BeginSave claims the save slot, refusing when no game is loaded, a save is
// already running, or the emulator is being replaced or torn down — a save
// dispatched into a launch or a stop lands on a dying process. Read and set
// happen under one lock so two concurrent /save-state requests cannot both
// win. The returned token names this claim and is what EndSave releases.
func (m *Manager) BeginSave() (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.romPath == "" {
		return 0, ErrNoGame
	}
	if err := m.busyLocked(); err != nil {
		return 0, err
	}
	m.saving = true
	m.saveGen++
	return m.saveGen, nil
}

// EndSave releases the save claim the token names. A token from an older
// claim is a no-op: the save it belonged to was abandoned when the emulator
// died and Clear released it, and the claim now belongs to someone else.
func (m *Manager) EndSave(token uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saving && token == m.saveGen {
		m.saving = false
	}
}

// SaveHeld reports whether the token still names the live save claim.
// save-and-exit checks it between the save and the kill: a claim lost
// mid-save means the emulator crashed, and whatever is running by now belongs
// to a newer session that must not be killed.
func (m *Manager) SaveHeld(token uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saving && token == m.saveGen
}

// RequireGame reports whether a game is loaded and nothing is mid-flight,
// which is the precondition for /load-state: a load during a save races the
// write it would overwrite, and a load during a launch or a stop talks to an
// emulator that is being replaced.
func (m *Manager) RequireGame() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.romPath == "" {
		return ErrNoGame
	}
	return m.busyLocked()
}

// Clear forgets the loaded game. Called when the emulator exits, expectedly or
// not, so /status stops reporting a game that is gone.
func (m *Manager) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clearLocked()
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Snapshot{
		Active:    m.romPath != "",
		ROMPath:   m.romPath,
		ROMName:   m.romName,
		StartedAt: m.startedAt,
	}
}

// Saving reports whether a save is in flight, for /status.
func (m *Manager) Saving() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saving
}

// IdleRelaunchOK reports whether the idle game list may take the screen: no
// game loaded and nothing mid-flight. The background relaunch after a stop, a
// save-and-exit or a crash consults this — under the runner's launch lock —
// so a menu never replaces a game that launched in the meantime.
func (m *Manager) IdleRelaunchOK() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.romPath == "" && m.busyLocked() == nil
}
