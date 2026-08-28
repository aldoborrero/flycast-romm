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
	"strconv"
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

	// RelaunchAbandoned is set once the crash-loop limiter gave up: nothing is
	// running and nothing will restart it without an explicit POST /launch.
	// /status exposes it so an operator can tell "idle, waiting for a user"
	// from "broker surrendered", the same field the pcsx2 broker reports.
	RelaunchAbandoned bool `json:"relaunch_abandoned"`
}

// Controller is the surface the HTTP layer drives. The API package depends on
// this interface, not on Runner, so its tests need no emulator.
type Controller interface {
	// Launch replaces whatever is running. An empty romPath boots the idle
	// game list, so the stream is never a black screen.
	Launch(ctx context.Context, romPath string) (LaunchResult, error)
	// LaunchIdle boots the idle game list only if the session is still idle,
	// so a background relaunch cannot replace a game that launched meanwhile.
	LaunchIdle(ctx context.Context) error
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

	// rapidExits counts consecutive unexpected exits within rapidExitWindow.
	// Reaching crashLoopLimit means the limiter has given up ("abandoned"):
	// nothing relaunches until an explicit Launch resets the counter.
	rapidExits int

	// onExit is called when the emulator dies, so the session can be cleared.
	onExit func()

	// idleOK is consulted, under launchMu, before an idle relaunch. It answers
	// whether the session is still idle; a launch that raced the relaunch owns
	// the screen and must not be replaced by the game list.
	idleOK func() bool
}

func NewRunner(cfg config.Config, log *slog.Logger, onExit func(), idleOK func() bool) *Runner {
	if onExit == nil {
		onExit = func() {}
	}
	if idleOK == nil {
		idleOK = func() bool { return true }
	}
	return &Runner{
		cfg:    cfg,
		log:    log,
		ch:     newChannel(cfg.ChannelDir()),
		onExit: onExit,
		idleOK: idleOK,
	}
}

var _ Controller = (*Runner)(nil)

// ── Launching ────────────────────────────────────────────────────────────────

// crashLoopLimit is how many consecutive rapid exits the monitor tolerates
// before it stops relaunching, matching the pcsx2 broker. One crash can be a
// fluke; three in a row within the window is a broken setup, and respawning it
// forever buries the reason under identical log lines.
const crashLoopLimit = 3

// Crash-loop pacing. Variables rather than constants so tests can shrink them.
var (
	// rapidExitWindow is how quickly an exit has to follow the launch to count
	// toward the crash loop.
	rapidExitWindow = 5 * time.Second
	// relaunchDelayRapid spaces relaunches after a rapid death so a fast crash
	// cannot become a tight loop; relaunchDelaySlow is the pause after a
	// process that ran for a while.
	relaunchDelayRapid = 5 * time.Second
	relaunchDelaySlow  = 1 * time.Second
)

func (r *Runner) Launch(ctx context.Context, romPath string) (LaunchResult, error) {
	r.launchMu.Lock()
	defer r.launchMu.Unlock()
	// An explicit launch is the recovery path out of an abandoned crash loop:
	// the operator fixed the underlying failure and asked for a boot.
	r.recoverCrashLoop()
	return r.launchLocked(ctx, romPath)
}

func (r *Runner) recoverCrashLoop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rapidExits = 0
}

// gaveUp reports whether the crash-loop limiter surrendered. Derived from the
// counter rather than stored, so the two cannot drift.
func (r *Runner) gaveUp() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rapidExits >= crashLoopLimit
}

// LaunchIdle boots the idle game list unless the session has moved on. The
// callers that want the menu back — a stop, a save-and-exit, the monitor after
// a crash — all schedule it after releasing their claim, so by the time it
// runs a new game may have launched or be launching, and replacing that with a
// menu would kill a session this call does not own. The check happens under
// launchMu so it cannot interleave with a launch that is mid-flight.
func (r *Runner) LaunchIdle(ctx context.Context) error {
	r.launchMu.Lock()
	defer r.launchMu.Unlock()
	// Once the limiter surrendered, nothing relaunches until an explicit
	// Launch — otherwise a stop would boot the same broken game list again
	// while /status keeps claiming the broker gave up.
	if r.gaveUp() {
		r.log.Warn("not relaunching the game list, the crash-loop limiter gave up; POST /launch to recover")
		return nil
	}
	if !r.idleOK() {
		r.log.Info("skipping the idle game list, a newer session owns the emulator")
		return nil
	}
	_, err := r.launchLocked(ctx, "")
	return err
}

func (r *Runner) launchLocked(ctx context.Context, romPath string) (LaunchResult, error) {
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
	r.writePIDFileLocked(cmd.Process.Pid)
	r.mu.Unlock()

	r.log.Info("flycast started", "pid", cmd.Process.Pid)
	go r.monitor(cmd)
	return nil
}

// flycastArgs builds the command line. Flycast only understands `-config` and
// a positional ROM path (core/cfg/cl.cpp); every other flag is warned about
// and ignored, and when several positional arguments are given the last one
// wins, so the ROM must come last.
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
		// Under the same lock as the current-check: a newer spawn has already
		// overwritten the file with its own PID, and must not lose it here.
		r.removePIDFileLocked()
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

	// Only unexpected exits reach this point, so only they move the counter: a
	// run that lasted a while resets it, a rapid death advances it.
	rapid := alive < rapidExitWindow
	r.mu.Lock()
	if rapid {
		r.rapidExits++
	} else {
		r.rapidExits = 0
	}
	exits := r.rapidExits
	r.mu.Unlock()

	if exits >= crashLoopLimit {
		r.log.Error("flycast died rapidly several times in a row, giving up on the game list; "+
			"fix the underlying failure, then POST /launch to recover",
			"consecutive_rapid_exits", exits, "window", rapidExitWindow)
		return
	}

	// Pause before relaunching so a fast crash cannot become a tight loop.
	delay := relaunchDelaySlow
	if rapid {
		delay = relaunchDelayRapid
	}
	time.Sleep(delay)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.LaunchIdle(ctx); err != nil {
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
	return r.terminate(ctx, proc.Pid)
}

// terminate takes a process group down: SIGTERM, a QUIT_WAIT grace, then
// SIGKILL. It works on a bare PID because it also stops emulators this broker
// did not spawn — the leftovers of a predecessor that died uncleanly.
func (r *Runner) terminate(ctx context.Context, pid int) error {
	// SIGTERM to the whole group: AppRun execs the real binary, so the group
	// is just Flycast and whatever it spawned.
	_ = syscall.Kill(-pid, syscall.SIGTERM)

	deadline := time.Now().Add(r.cfg.QuitWait)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}

	r.log.Warn("flycast ignored SIGTERM, sending SIGKILL", "pid", pid, "after", r.cfg.QuitWait)
	_ = syscall.Kill(-pid, syscall.SIGKILL)

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("flycast (pid %d) survived SIGKILL", pid)
}

func alive(pid int) bool {
	// Signal 0 only checks permission and existence. The child has been
	// reaped by monitor() if it is gone, so there is no zombie to confuse us.
	return syscall.Kill(pid, 0) == nil
}

// ── Leftover emulators ───────────────────────────────────────────────────────
//
// The emulator is spawned with setsid and no death signal, so it survives the
// broker. When the broker dies without its shutdown path — an OOM kill, a
// panic off the recovered HTTP goroutines — s6 restarts only the broker, and
// a fresh broker that knows nothing of the orphan would boot a second
// emulator on top of it. The pidfile is how a broker finds what its
// predecessor left running.

// writePIDFileLocked records the spawned group leader. Callers hold mu, so
// this cannot interleave with the monitor removing an older process's file.
func (r *Runner) writePIDFileLocked(pid int) {
	if r.cfg.PIDFile == "" {
		return
	}
	err := os.MkdirAll(filepath.Dir(r.cfg.PIDFile), 0o755)
	if err == nil {
		err = os.WriteFile(r.cfg.PIDFile, []byte(strconv.Itoa(pid)+"\n"), 0o644)
	}
	if err != nil {
		r.log.Warn("could not write the flycast pidfile; an unclean broker restart would leak this emulator",
			"file", r.cfg.PIDFile, "err", err)
	}
}

func (r *Runner) removePIDFileLocked() {
	if r.cfg.PIDFile == "" {
		return
	}
	if err := os.Remove(r.cfg.PIDFile); err != nil && !os.IsNotExist(err) {
		r.log.Warn("could not remove the flycast pidfile", "file", r.cfg.PIDFile, "err", err)
	}
}

// KillLeftover stops an emulator a previous broker left behind, so startup
// never ends with two emulators fighting over the one display. Called once,
// before the boot launch. An error is not fatal to the caller: a leftover
// that cannot be stopped is logged and lived with, exactly like a display
// that never comes up.
func (r *Runner) KillLeftover(ctx context.Context) error {
	if r.cfg.PIDFile == "" {
		return nil
	}
	r.launchMu.Lock()
	defer r.launchMu.Unlock()

	raw, err := os.ReadFile(r.cfg.PIDFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", r.cfg.PIDFile, err)
	}
	discard := func() {
		r.mu.Lock()
		r.removePIDFileLocked()
		r.mu.Unlock()
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		r.log.Warn("discarding a malformed flycast pidfile",
			"file", r.cfg.PIDFile, "content", strings.TrimSpace(string(raw)))
		discard()
		return nil
	}
	if !alive(pid) {
		discard()
		return nil
	}
	// PIDs recycle, and an alive PID alone is not proof: it has to look like
	// the process this broker spawns, which always carries the flycast binary
	// path on its command line. Anything else is an innocent bystander.
	if !cmdlineContains(pid, r.cfg.FlycastBin) {
		r.log.Warn("the flycast pidfile points at an unrelated process, leaving it alone",
			"pid", pid, "file", r.cfg.PIDFile)
		discard()
		return nil
	}

	r.log.Warn("stopping an emulator a previous broker left running", "pid", pid)
	if err := r.terminate(ctx, pid); err != nil {
		return fmt.Errorf("stopping leftover flycast: %w", err)
	}
	discard()
	return nil
}

func cmdlineContains(pid int, needle string) bool {
	// /proc cmdline is NUL-separated; a path needle never contains NUL, so a
	// plain substring check is exact enough.
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false
	}
	return needle != "" && strings.Contains(string(raw), needle)
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
	abandoned := r.rapidExits >= crashLoopLimit
	r.mu.Unlock()

	p := Probe{
		BinaryPath:        r.cfg.FlycastBin,
		Display:           r.cfg.WaylandDisplay,
		LuaReady:          r.ch.Ready(),
		LuaState:          r.ch.State(),
		RelaunchAbandoned: abandoned,
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
		// Default SDL's audio backend to PulseAudio. The broker is built around
		// Pulse (pactl, PULSE_RUNTIME_PATH, the null sink); left unset, SDL picks
		// ALSA — which has no device in the container — and Flycast boots
		// "running without audio". An operator can still override this through
		// the SDL_AUDIODRIVER passthrough below.
		"SDL_AUDIODRIVER": "pulseaudio",
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
		// PATH so the `sudo -u abc env … pactl` prefix resolves pactl from the
		// broker's own PATH (e.g. the nix wrapper's), not sudo's secure_path
		// which hides a store-only pactl.
		"PATH",
	}
	passthroughPrefixes = []string{
		"NVIDIA_", "__GL", "__NV", "__EGL", "LIBVA_", "MESA_", "VK_",
		"GALLIUM_", "LIBGL_", "DRI_",
		// GBM_BACKENDS_PATH points Mesa at its GBM backend; without it the
		// compositor and Flycast fall back to software rendering.
		"GBM_",
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
