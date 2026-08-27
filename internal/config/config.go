// Package config reads the broker's configuration from the environment.
//
// Every default is chosen to match the sibling RomM brokers (pcsx2, dolphin,
// xemu) where a corresponding knob exists, so an operator who has deployed one
// of those recognises the names. Where Flycast differs, the difference is
// commented.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MaxSlot is the highest save slot RomM may ask for. Flycast has exactly ten
// savestate slots, indexed 0-9 internally (core/ui/gui.cpp cycles them with
// `(slot + 10 + step) % 10`), which map onto RomM's 1-10 with none left over.
const MaxSlot = 10

// LuaScriptName is the file the mod installs into Flycast's config directory
// and selects with `-config config:LuaFileName=...`. It is deliberately not
// `flycast.lua`: that is the name Flycast loads by default, and taking it would
// silently disable a script the user wrote themselves.
const LuaScriptName = "romm-broker.lua"

// ChannelDirName holds the command/ack files the Lua script and the broker
// exchange. It lives under Flycast's config directory so it shares the /config
// volume's lifetime and permissions.
const ChannelDirName = "romm-broker"

type Config struct {
	Port   int
	Secret string

	// ROMRoot bounds every rom_path RomM sends. RomM's paths are
	// <LIBRARY_BASE_PATH>/roms/<platform>/…, so this is the roms directory, and
	// the broker must see it at the same path RomM does.
	ROMRoot string

	FlycastBin string
	ConfigDir  string
	DataDir    string
	StateDir   string

	// PIDFile records the spawned emulator's process group leader. The
	// emulator is started with setsid and survives an unclean broker death, so
	// this is how the next broker finds — and stops — what its predecessor
	// left running before booting a second one on top. It lives under /run so
	// a fresh container never inherits it.
	PIDFile string

	// SaveSlot is the slot /save-and-exit uses when RomM sends slot 0. Ten is
	// the autosave slot, leaving 1-9 for the player, matching pcsx2 and xemu.
	SaveSlot int

	LaunchWait  time.Duration
	SaveWait    time.Duration
	LuaWait     time.Duration
	QuitWait    time.Duration
	DisplayWait time.Duration

	// UID and GID own the files the broker creates for Flycast, which runs as
	// abc and has to be able to write into the command channel.
	UID int
	GID int

	// Emulator environment.
	User             string
	Home             string
	Display          string
	WaylandDisplay   string
	XDGRuntimeDir    string
	PulseRuntimePath string

	LogLevel slog.Level

	// Warnings collects environment values Load had to ignore and fall back
	// to a default for. Load runs before the logger exists, so main logs
	// these once it does: a typo'd BROKER_PORT silently becoming 8000 would
	// otherwise cost an afternoon of debugging.
	Warnings []string
}

func Load() (Config, error) {
	e := &envs{}
	c := Config{
		Port: e.integer("BROKER_PORT", 8000),
		// RomM sends the value of its own STREAMING_BROKER_SECRET. The sibling
		// brokers name the container-side variable BROKER_SECRET; both are
		// accepted so an operator can use whichever name they already have in
		// their compose file.
		Secret:     firstNonEmpty(os.Getenv("BROKER_SECRET"), os.Getenv("STREAMING_BROKER_SECRET")),
		ROMRoot:    envStr("ROM_ROOT", "/romm/library/roms"),
		FlycastBin: envStr("FLYCAST_BIN", "/opt/flycast/AppRun"),
		ConfigDir:  envStr("FLYCAST_CONFIG_DIR", "/config/.config/flycast"),
		DataDir:    envStr("FLYCAST_DATA_DIR", "/config/.local/share/flycast"),
		PIDFile:    envStr("FLYCAST_PIDFILE", "/run/flycast-romm/flycast.pid"),
		SaveSlot:   e.integer("SAVE_SLOT", MaxSlot),
		// Comfortably inside RomM's fixed 10s /launch timeout: on expiry the
		// broker answers 200 with ready=false rather than letting RomM time out
		// and release a claim on an emulator that is still coming up.
		LaunchWait: e.duration("LAUNCH_WAIT", 8*time.Second),
		// Only the failure path pays this: the wait returns as soon as the
		// state file stops growing.
		SaveWait: e.duration("SAVE_WAIT", 20*time.Second),
		LuaWait:  e.duration("LUA_WAIT", 10*time.Second),
		QuitWait: e.duration("QUIT_WAIT", 6*time.Second),
		// A cold container often has no compositor five seconds in, which is
		// why this is not a flat sleep: it returns the moment the socket
		// appears and only costs its full length when something is wrong.
		DisplayWait: e.duration("DISPLAY_WAIT", 30*time.Second),

		User:             envStr("FLYCAST_USER", "abc"),
		Home:             envStr("FLYCAST_HOME", "/config"),
		Display:          envStr("DISPLAY", ":0"),
		WaylandDisplay:   envStr("WAYLAND_DISPLAY", "wayland-0"),
		XDGRuntimeDir:    envStr("XDG_RUNTIME_DIR", "/config/.XDG"),
		PulseRuntimePath: envStr("PULSE_RUNTIME_PATH", "/defaults"),

		LogLevel: e.level("BROKER_LOG_LEVEL", slog.LevelInfo),
	}

	// PUID/PGID default to the emulator user's real ids, not a flat 1000: a
	// linuxserver container that was never given PUID keeps abc at its
	// baked-in uid (911), and files chowned to the wrong id wedge the lua
	// channel with no visible error.
	uidDef, gidDef := userIDs(c.User)
	c.UID = e.integer("PUID", uidDef)
	c.GID = e.integer("PGID", gidDef)

	// Flycast writes savestates through get_writable_data_path unless
	// Dreamcast.SavestatePath is set, so the data directory is the default.
	c.StateDir = envStr("SSTATE_DIR", c.DataDir)

	if c.Port < 1 || c.Port > 65535 {
		return c, fmt.Errorf("BROKER_PORT must be 1-65535, got %d", c.Port)
	}
	if c.SaveSlot < 1 || c.SaveSlot > MaxSlot {
		return c, fmt.Errorf("SAVE_SLOT must be 1-%d, got %d", MaxSlot, c.SaveSlot)
	}

	root, err := filepath.Abs(c.ROMRoot)
	if err != nil {
		return c, fmt.Errorf("ROM_ROOT %q is not a usable path: %w", c.ROMRoot, err)
	}
	root = filepath.Clean(root)
	// The containment check in flycast.ResolveROM compares symlink-resolved
	// candidate paths against this root, so the root has to be resolved too: a
	// ROM_ROOT with a symlinked component would otherwise refuse every ROM
	// under it. Best effort — a root that does not exist yet stays as given.
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	c.ROMRoot = root

	c.Warnings = e.warnings
	return c, nil
}

// ChannelDir is where the broker and the Lua script exchange commands.
func (c Config) ChannelDir() string { return filepath.Join(c.ConfigDir, ChannelDirName) }

// LuaScriptPath is where the mod installs the command-channel script.
func (c Config) LuaScriptPath() string { return filepath.Join(c.ConfigDir, LuaScriptName) }

// FlycastSlot converts a RomM slot (1-10, or 0 meaning "the autosave slot")
// into the index Flycast's Dreamcast.SavestateSlot uses (0-9).
func (c Config) FlycastSlot(rommSlot int) int {
	if rommSlot == 0 {
		rommSlot = c.SaveSlot
	}
	return rommSlot - 1
}

// userIDs resolves the emulator user's uid and gid. Outside the container the
// lookup fails and the sibling brokers' documented default of 1000 applies.
func userIDs(name string) (int, int) {
	u, err := user.Lookup(name)
	if err != nil {
		return 1000, 1000
	}
	uid, uidErr := strconv.Atoi(u.Uid)
	gid, gidErr := strconv.Atoi(u.Gid)
	if uidErr != nil || gidErr != nil {
		return 1000, 1000
	}
	return uid, gid
}

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// envs reads environment values that can fail to parse, and remembers every
// value it had to ignore, so a fallback to a default is never silent.
type envs struct {
	warnings []string
}

func (e *envs) warnf(format string, args ...any) {
	e.warnings = append(e.warnings, fmt.Sprintf(format, args...))
}

func (e *envs) integer(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		e.warnf("%s=%q is not an integer, using %d", key, v, def)
		return def
	}
	return n
}

// duration accepts both a bare number of seconds ("20", as the Python brokers
// document) and a Go duration ("20s", "1m30s").
func (e *envs) duration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	v = strings.TrimSpace(v)
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs <= 0 {
			e.warnf("%s=%q is not a positive duration, using %s", key, v, def)
			return def
		}
		return time.Duration(secs * float64(time.Second))
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		e.warnf("%s=%q is not a duration in seconds or Go syntax, using %s", key, v, def)
		return def
	}
	return d
}

func (e *envs) level(key string, def slog.Level) slog.Level {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "DEBUG":
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	}
	e.warnf("%s=%q is not a log level (DEBUG, INFO, WARN, ERROR), using %s", key, v, def)
	return def
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
