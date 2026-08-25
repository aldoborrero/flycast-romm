package flycast

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// pactl runs a PulseAudio client command as the emulator user.
//
// It must never run as root. The client library calls pa_make_secure_dir() on
// PULSE_RUNTIME_PATH and takes ownership of it, so a single root pactl chowns
// /defaults to root:root 0700 and locks Selkies, pcmflux and this broker out
// of the socket for the life of the container — audio dies quietly while the
// daemon looks perfectly healthy.
func (r *Runner) pactl(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	argv := append(r.userEnvPrefix(), "pactl")
	argv = append(argv, args...)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("pactl %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// SetVolume sets the stream sink's volume. RomM sends 0-100 and PulseAudio
// takes a percentage, so the values map one to one.
func (r *Runner) SetVolume(ctx context.Context, level int) error {
	if level < 0 || level > 100 {
		return fmt.Errorf("level must be 0-100, got %d", level)
	}
	_, err := r.pactl(ctx, "set-sink-volume", "@DEFAULT_SINK@", strconv.Itoa(level)+"%")
	return err
}

// SetMute sets or toggles mute and reports the state read back from
// PulseAudio, which is what RomM forwards to the UI. A nil argument toggles.
func (r *Runner) SetMute(ctx context.Context, mute *bool) (bool, error) {
	arg := "toggle"
	if mute != nil {
		if *mute {
			arg = "1"
		} else {
			arg = "0"
		}
	}
	if _, err := r.pactl(ctx, "set-sink-mute", "@DEFAULT_SINK@", arg); err != nil {
		return false, err
	}

	out, err := r.pactl(ctx, "get-sink-mute", "@DEFAULT_SINK@")
	if err != nil {
		return false, err
	}
	return strings.HasSuffix(strings.TrimSpace(out), "yes"), nil
}
