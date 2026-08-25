// Package flycast drives the Flycast emulator inside the linuxserver
// container: it owns the process, the Lua command channel, savestate write
// confirmation and PulseAudio volume.
package flycast

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aldoborrero/flycast-romm/internal/config"
)

// LaunchResult reports what a launch actually did.
type LaunchResult struct {
	// ROMPath is the file Flycast was pointed at, which for a folder-organised
	// game is not the path RomM sent.
	ROMPath string
	// Ready is false when the emulator had not reported itself running before
	// LAUNCH_WAIT expired. The launch is still in progress; it is not an error.
	Ready bool
}

// Probe is the health picture /health reports.
type Probe struct {
	BinaryPresent bool   `json:"flycast_installed"`
	BinaryPath    string `json:"flycast_path"`
	DisplayUp     bool   `json:"display_up"`
	Display       string `json:"display"`
	ProcessAlive  bool   `json:"process_alive"`
	LuaReady      bool   `json:"lua_ready"`
	LuaState      string `json:"lua_state,omitempty"`
}

// Controller is the surface the HTTP layer drives. The API package depends on
// this interface, not on Runner, so its tests need no emulator.
type Controller interface {
	// Launch replaces whatever is running. An empty romPath boots the idle
	// game list, so the stream is never a black screen.
	Launch(ctx context.Context, romPath string) (LaunchResult, error)
	// SaveState blocks until the state for the slot is on disk.
	SaveState(ctx context.Context, romPath string, flycastSlot int) error
	// LoadState blocks until Flycast acknowledges the load.
	LoadState(ctx context.Context, flycastSlot int) error
	// Kill stops the emulator without relaunching anything.
	Kill(ctx context.Context) error
	SetVolume(ctx context.Context, level int) error
	SetMute(ctx context.Context, mute *bool) (bool, error)
	Probe() Probe
}

// Runner is the real Controller.
type Runner struct {
	cfg config.Config
	log *slog.Logger
	ch  *channel

	// launchMu serialises whole launch sequences. Guarding only the fields
	// would let two launches interleave their kill and spawn steps.
	launchMu sync.Mutex

	mu          sync.Mutex
	proc        *os.Process
	intentional bool // a kill we asked for, so the monitor must not relaunch

	// onExit is called when the emulator dies, so the session can be cleared.
	onExit func()
}

func NewRunner(cfg config.Config, log *slog.Logger, onExit func()) *Runner {
	if onExit == nil {
		onExit = func() {}
	}
	return &Runner{
		cfg:    cfg,
		log:    log,
		ch:     newChannel(cfg.ChannelDir()),
		onExit: onExit,
	}
}

var _ Controller = (*Runner)(nil)

// ── Launching ────────────────────────────────────────────────────────────────

func (r *Runner) Launch(ctx context.Context, romPath string) (LaunchResult, error) {
	r.launchMu.Lock()
	defer r.launchMu.Unlock()

	if err := r.killLocked(ctx); err != nil {
		return LaunchResult{}, err
	}
	if err := r.ch.Reset(); err != nil {
		return LaunchResult{}, fmt.Errorf("resetting the lua channel: %w", err)
	}
	if err := r.spawn(romPath); err != nil {
		return LaunchResult{}, err
	}

	res := LaunchResult{ROMPath: romPath}
	if romPath == "" {
		// The idle game list has no game to report running; the script being
		// loaded is all the readiness there is.
		res.Ready = r.ch.WaitReady(ctx, r.cfg.LaunchWait) == nil
		return res, nil
	}

	// Waiting is capped well inside RomM's fixed 10s /launch timeout. On
	// expiry the caller answers 200 with ready=false: the emulator is still
	// coming up, and a 502 would make RomM release a claim on a boot that is
	// going to succeed.
	if err := r.ch.WaitState(ctx, "running", r.cfg.LaunchWait); err != nil {
		r.log.Warn("emulator did not report running before the launch deadline",
			"rom", romPath, "wait", r.cfg.LaunchWait, "err", err)
		return res, nil
	}
	res.Ready = true
	return res, nil
}

func (r *Runner) spawn(romPath string) error {
	argv := r.userEnvPrefix()
	argv = append(argv, r.cfg.FlycastBin)
	argv = append(argv, r.flycastArgs(romPath)...)

	r.log.Info("launching flycast", "rom", orDefault(romPath, "game list"))
	r.log.Debug("launch argv", "argv", strings.Join(argv, " "))

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// A new session, not a preexec hook: this process runs an HTTP server on
	// several goroutines, and forking with a hook can deadlock on a lock some
	// other thread held. setsid gives the same killable process group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", r.cfg.FlycastBin, err)
	}

	r.mu.Lock()
	r.proc = cmd.Process
	r.intentional = false
	r.mu.Unlock()

	r.log.Info("flycast started", "pid", cmd.Process.Pid)
	go r.monitor(cmd)
	return nil
}

// flycastArgs builds the command line. Flycast only understands `-config` and
// a single trailing ROM path (core/cfg/cl.cpp); every other flag is warned
// about and ignored, and everything after the first positional argument is
// discarded, so the ROM must come last.
//
// Each comma-separated `-config` entry has to repeat its section: the parser
// resets to "expecting a section" after every comma.
func (r *Runner) flycastArgs(romPath string) []string {
	settings := []string{
		// Point Flycast at the mod's command-channel script instead of the
		// default flycast.lua, so a script the user wrote is left alone.
		"config:LuaFileName=" + config.LuaScriptName,
		// Slot semantics stay explicit: RomM decides when a state is written.
		// Auto-save would silently overwrite whatever slot happens to be
		// current every time a game unloads.
		"config:Dreamcast.AutoSaveState=no",
		"config:Dreamcast.AutoLoadState=no",
	}
	args := []string{"-config", strings.Join(settings, ",")}

	if romPath != "" {
		// Fullscreen only with a game: the idle game list is easier to use
		// windowed if an operator ever opens the desktop directly.
		args = append(args, "-config", "window:fullscreen=yes")
		args = append(args, romPath)
	}
	return args
}

// monitor reaps the emulator and, when it dies unasked, brings the idle game
// list back so the stream does not go black.
func (r *Runner) monitor(cmd *exec.Cmd) {
	started := time.Now()
	err := cmd.Wait()
	alive := time.Since(started)

	r.mu.Lock()
	current := r.proc != nil && cmd.Process != nil && r.proc.Pid == cmd.Process.Pid
	intentional := r.intentional
	if current {
		r.proc = nil
	}
	r.mu.Unlock()

	if !current {
		// A newer launch already replaced this process; nothing to do.
		return
	}
	r.onExit()

	if intentional {
		r.log.Info("flycast exited", "pid", cmd.Process.Pid, "alive", alive.Round(time.Millisecond))
		return
	}
	r.log.Warn("flycast exited unexpectedly, returning to the game list",
		"pid", cmd.Process.Pid, "alive", alive.Round(time.Millisecond), "err", err)

	// Best effort, and only once: an emulator that dies immediately will die
	// again, and respawning a broken renderer forever buries the reason under
	// thousands of identical log lines. /launch clears the state either way.
	if alive < 5*time.Second {
		r.log.Error("flycast died within five seconds, not relaunching the game list")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := r.Launch(ctx, ""); err != nil {
		r.log.Error("could not relaunch the game list", "err", err)
	}
}

// ── Stopping ─────────────────────────────────────────────────────────────────

func (r *Runner) Kill(ctx context.Context) error {
	r.launchMu.Lock()
	defer r.launchMu.Unlock()
	return r.killLocked(ctx)
}

func (r *Runner) killLocked(ctx context.Context) error {
	r.mu.Lock()
	proc := r.proc
	if proc != nil {
		r.intentional = true
	}
	r.mu.Unlock()

	if proc == nil {
		return nil
	}

	// SIGTERM to the whole group: AppRun execs the real binary, so the group
	// is just Flycast and whatever it spawned.
	_ = syscall.Kill(-proc.Pid, syscall.SIGTERM)

	deadline := time.Now().Add(r.cfg.QuitWait)
	for time.Now().Before(deadline) {
		if !alive(proc.Pid) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}

	r.log.Warn("flycast ignored SIGTERM, sending SIGKILL", "pid", proc.Pid, "after", r.cfg.QuitWait)
	_ = syscall.Kill(-proc.Pid, syscall.SIGKILL)

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(proc.Pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("flycast (pid %d) survived SIGKILL", proc.Pid)
}

func alive(pid int) bool {
	// Signal 0 only checks permission and existence. The child has been
	// reaped by monitor() if it is gone, so there is no zombie to confuse us.
	return syscall.Kill(pid, 0) == nil
}

// ── Save states ──────────────────────────────────────────────────────────────

func (r *Runner) SaveState(ctx context.Context, romPath string, flycastSlot int) error {
	target := filepath.Join(r.cfg.StateDir, StateFileName(romPath, flycastSlot))
	before := stamp(target)

	if err := r.ch.Do(ctx, r.cfg.LuaWait, "save", flycastSlot); err != nil {
		return err
	}

	// The Lua ack says dc_savestate returned, which is not the same as the
	// bytes being on disk. /save-and-exit kills the process straight after
	// this returns, so the write has to be confirmed independently.
	if !waitForStateWrite(ctx, target, before, r.cfg.SaveWait) {
		return fmt.Errorf("save to slot %d was acknowledged but %s did not settle within %s",
			flycastSlot, target, r.cfg.SaveWait)
	}
	r.log.Info("save state written", "slot", flycastSlot, "file", target)
	return nil
}

func (r *Runner) LoadState(ctx context.Context, flycastSlot int) error {
	return r.ch.Do(ctx, r.cfg.LuaWait, "load", flycastSlot)
}

// ── Health ───────────────────────────────────────────────────────────────────

func (r *Runner) Probe() Probe {
	r.mu.Lock()
	proc := r.proc
	r.mu.Unlock()

	p := Probe{
		BinaryPath: r.cfg.FlycastBin,
		Display:    r.cfg.WaylandDisplay,
		LuaReady:   r.ch.Ready(),
		LuaState:   r.ch.State(),
	}
	if st, err := os.Stat(r.cfg.FlycastBin); err == nil && !st.IsDir() {
		p.BinaryPresent = true
	}
	p.ProcessAlive = proc != nil && alive(proc.Pid)

	// The flycast image sets PIXELFLUX_WAYLAND=true, which parks Xvfb on
	// `sleep infinity` and runs labwc instead — so the display to check is the
	// compositor's socket, not /tmp/.X11-unix. The X socket is still probed as
	// a fallback for an image that runs the Xorg path.
	if _, err := os.Stat(filepath.Join(r.cfg.XDGRuntimeDir, r.cfg.WaylandDisplay)); err == nil {
		p.DisplayUp = true
	} else if n := strings.TrimPrefix(r.cfg.Display, ":"); n != "" {
		if i := strings.IndexByte(n, '.'); i >= 0 {
			n = n[:i]
		}
		if _, err := os.Stat("/tmp/.X11-unix/X" + n); err == nil {
			p.DisplayUp = true
			p.Display = r.cfg.Display
		}
	}
	return p
}

// ── Process environment ──────────────────────────────────────────────────────

// userEnvPrefix builds the `sudo -u abc env K=V ...` prefix every child needs.
// The broker runs as root (s6 starts it that way) but Flycast, pactl and the
// PulseAudio socket all belong to abc.
func (r *Runner) userEnvPrefix() []string {
	env := map[string]string{
		"DISPLAY":            r.cfg.Display,
		"WAYLAND_DISPLAY":    r.cfg.WaylandDisplay,
		"XDG_RUNTIME_DIR":    r.cfg.XDGRuntimeDir,
		"HOME":               r.cfg.Home,
		"USER":               r.cfg.User,
		"PULSE_RUNTIME_PATH": r.cfg.PulseRuntimePath,
		// Tells the Lua script where the command channel lives, so the two
		// halves cannot disagree if FLYCAST_CONFIG_DIR is overridden.
		"ROMM_BROKER_CHANNEL": r.cfg.ChannelDir(),
	}
	// Forward the GPU knobs an operator set on the container, plus the
	// joystick interposer the base image relies on for controller input.
	for _, name := range passthroughEnv {
		if v, ok := os.LookupEnv(name); ok && v != "" {
			env[name] = v
		}
	}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || v == "" {
			continue
		}
		for _, prefix := range passthroughPrefixes {
			if strings.HasPrefix(k, prefix) {
				env[k] = v
				break
			}
		}
	}

	argv := []string{"sudo", "-u", r.cfg.User, "env"}
	for _, k := range sortedKeys(env) {
		argv = append(argv, k+"="+env[k])
	}
	return argv
}

var (
	passthroughEnv = []string{
		"LD_PRELOAD", "LD_LIBRARY_PATH", "DRINODE", "XDG_DATA_DIRS",
		"SDL_VIDEODRIVER", "SDL_AUDIODRIVER",
	}
	passthroughPrefixes = []string{
		"NVIDIA_", "__GL", "__NV", "__EGL", "LIBVA_", "MESA_", "VK_",
		"GALLIUM_", "LIBGL_", "DRI_",
	}
)

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// EnsureChannelDir creates the command-channel directory and hands it to the
// emulator user. Flycast runs as abc and has to remove the command file it
// consumes and create its own ack, so a root-owned directory would wedge the
// channel with no visible error.
func (r *Runner) EnsureChannelDir() error {
	dir := r.cfg.ChannelDir()
	if err := os.MkdirAll(dir, 0o775); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := os.Chown(dir, r.cfg.UID, r.cfg.GID); err != nil && !os.IsPermission(err) {
		r.log.Warn("could not chown the lua channel directory", "dir", dir, "err", err)
	}
	return nil
}

// WaitForDisplay blocks until the compositor (or X server) is accepting
// clients, so the first launch does not fail silently on a cold container.
// It reports whether a display appeared; a false is logged, not fatal.
func (r *Runner) WaitForDisplay(ctx context.Context) bool {
	deadline := time.Now().Add(r.cfg.DisplayWait)
	for {
		if r.Probe().DisplayUp {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(250 * time.Millisecond):
		}
	}
}
