package flycast

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ProtocolVersion is written by the Lua script into `ready` at load. A
// mismatch means the script on disk is older or newer than this broker, which
// is only possible if someone edited /config by hand: the mod ships both.
const ProtocolVersion = "1"

var (
	// ErrLuaUnavailable means the command channel never came up: either
	// Flycast was built without Lua, or the script is not where
	// -config config:LuaFileName points.
	ErrLuaUnavailable = errors.New("flycast lua command channel is not responding")

	ErrLuaTimeout = errors.New("flycast did not acknowledge the command in time")
)

// channel is the broker's half of the file protocol described in
// docs/CONTRACT.md D2. Flycast has no IPC socket, but it does embed Lua with
// the full standard library, so the script polls these files from its
// `overlay` callback and calls flycast.emulator.saveState / loadState / exit
// directly. That addresses slots by index instead of cycling a selector, and
// works the same whether Flycast renders through Wayland or Xwayland.
type channel struct {
	dir string
	seq atomic.Uint64
}

func newChannel(dir string) *channel { return &channel{dir: dir} }

func (c *channel) readyPath() string   { return filepath.Join(c.dir, "ready") }
func (c *channel) commandPath() string { return filepath.Join(c.dir, "command") }
func (c *channel) ackPath() string     { return filepath.Join(c.dir, "ack") }
func (c *channel) statePath() string   { return filepath.Join(c.dir, "state") }

// Ready reports whether the script has loaded and announced a version this
// broker speaks.
func (c *channel) Ready() bool {
	b, err := os.ReadFile(c.readyPath())
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(b)) == ProtocolVersion
}

// State is what the script last published: "running", "stopped", or "" when it
// has published nothing yet.
func (c *channel) State() string {
	b, err := os.ReadFile(c.statePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Reset clears the channel before a launch so an ack from the previous process
// cannot be mistaken for one from the new one.
func (c *channel) Reset() error {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	for _, p := range []string{c.readyPath(), c.commandPath(), c.ackPath(), c.statePath()} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// WaitReady blocks until the script announces itself, or ctx/timeout expires.
func (c *channel) WaitReady(ctx context.Context, timeout time.Duration) error {
	return poll(ctx, timeout, func() (bool, error) {
		return c.Ready(), nil
	}, ErrLuaUnavailable)
}

// WaitState blocks until the script publishes the wanted state.
func (c *channel) WaitState(ctx context.Context, want string, timeout time.Duration) error {
	return poll(ctx, timeout, func() (bool, error) {
		return c.State() == want, nil
	}, ErrLuaTimeout)
}

// Do sends one command and waits for its acknowledgement.
//
// The command file is written to a temporary name and renamed into place, so
// the script can never read a half-written line. The sequence number is what
// makes the ack unambiguous: the script echoes it back, and an ack carrying an
// older sequence is a leftover from a previous command.
func (c *channel) Do(ctx context.Context, timeout time.Duration, verb string, arg int) error {
	if !c.Ready() {
		return ErrLuaUnavailable
	}

	// Drop any previous acknowledgement before publishing the command. The
	// sequence number alone is not enough: the broker restarts independently
	// of Flycast, so a `1 ok` left over from before a restart would satisfy
	// the first command after it.
	if err := os.Remove(c.ackPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clearing the previous ack: %w", err)
	}

	seq := c.seq.Add(1)
	line := fmt.Sprintf("%d %s %d\n", seq, verb, arg)

	tmp, err := os.CreateTemp(c.dir, ".command-*")
	if err != nil {
		return fmt.Errorf("staging command: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.WriteString(line); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing command: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("flushing command: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing command: %w", err)
	}
	// Flycast runs as abc and has to be able to remove the file it consumes.
	if err := os.Chmod(tmpName, 0o666); err != nil {
		return fmt.Errorf("relaxing command permissions: %w", err)
	}
	if err := os.Rename(tmpName, c.commandPath()); err != nil {
		return fmt.Errorf("publishing command: %w", err)
	}

	var ackErr error
	err = poll(ctx, timeout, func() (bool, error) {
		gotSeq, status, reason, ok := c.readAck()
		if !ok || gotSeq != seq {
			return false, nil
		}
		if status != "ok" {
			ackErr = fmt.Errorf("flycast refused %s: %s", verb, reason)
		}
		return true, nil
	}, ErrLuaTimeout)
	if err != nil {
		return err
	}
	return ackErr
}

func (c *channel) readAck() (seq uint64, status, reason string, ok bool) {
	b, err := os.ReadFile(c.ackPath())
	if err != nil {
		return 0, "", "", false
	}
	fields := strings.Fields(strings.TrimSpace(string(b)))
	if len(fields) < 2 {
		return 0, "", "", false
	}
	n, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, "", "", false
	}
	return n, fields[1], strings.Join(fields[2:], " "), true
}

// poll runs cond every 50ms until it is true, ctx is cancelled, or the timeout
// expires. 50ms is well under a frame at 60fps, which is how often the Lua
// overlay callback checks for work, so the round trip is bounded by Flycast's
// framerate rather than by this loop.
func poll(ctx context.Context, timeout time.Duration, cond func() (bool, error), onTimeout error) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		done, err := cond()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if time.Now().After(deadline) {
			return onTimeout
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
