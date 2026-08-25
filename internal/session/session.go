// Package session tracks the one game session a Flycast container can have.
//
// RomM owns session ownership and single-use enforcement in Redis; this is the
// container-side backstop that keeps two concurrent requests from interleaving
// a launch with a save, and that tells /status what is loaded.
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
}

func New() *Manager { return &Manager{} }

// BeginLaunch claims the launch slot. A launch is refused while a save is in
// flight: killing the emulator mid-write truncates the state.
func (m *Manager) BeginLaunch() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saving {
		return ErrSaveInProgress
	}
	if m.launching {
		return ErrLaunchInProgress
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

// BeginSave claims the save slot, refusing when no game is loaded or a save is
// already running. Read and set happen under one lock so two concurrent
// /save-state requests cannot both win.
func (m *Manager) BeginSave() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.romPath == "" {
		return ErrNoGame
	}
	if m.saving {
		return ErrSaveInProgress
	}
	m.saving = true
	return nil
}

func (m *Manager) EndSave() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saving = false
}

// RequireGame reports whether a game is loaded and no save is in flight, which
// is the precondition for /load-state.
func (m *Manager) RequireGame() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.romPath == "" {
		return ErrNoGame
	}
	if m.saving {
		return ErrSaveInProgress
	}
	return nil
}

// Clear forgets the loaded game. Called when the emulator exits, expectedly or
// not, so /status stops reporting a game that is gone.
func (m *Manager) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.romPath, m.romName = "", ""
	m.startedAt = time.Time{}
	m.saving = false
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
