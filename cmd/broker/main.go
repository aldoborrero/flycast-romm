// Command broker serves the RomM streaming contract for a Flycast container.
//
// It is started as an s6 longrun service inside lscr.io/linuxserver/flycast by
// the Docker mod in this repository. RomM talks to it over HTTP; it talks to
// Flycast through a Lua command channel and to PulseAudio through pactl.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/aldoborrero/flycast-romm/internal/api"
	"github.com/aldoborrero/flycast-romm/internal/config"
	"github.com/aldoborrero/flycast-romm/internal/flycast"
	"github.com/aldoborrero/flycast-romm/internal/session"
)

// version is stamped by the Nix build from the flake's revision. A binary that
// cannot say which build it is turns every "did the mod actually update?"
// question into guesswork.
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "broker: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)
	log.Info("flycast romm broker starting", "version", version)

	if cfg.Secret == "" {
		// Not fatal, because a local debugging container is a real use, but
		// the consequence is worth stating: the broker runs as root and an
		// open API means root-privileged launches within ROM_ROOT.
		log.Warn("no shared secret set, every request will be accepted",
			"fix", "set BROKER_SECRET (or STREAMING_BROKER_SECRET) to the same value as RomM's STREAMING_BROKER_SECRET")
	} else {
		log.Info("shared secret auth enabled")
	}

	sess := session.New()
	runner := flycast.NewRunner(cfg, log, sess.Clear)

	if err := runner.EnsureChannelDir(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if runner.WaitForDisplay(ctx) {
		log.Info("display is up", "display", runner.Probe().Display)
	} else {
		log.Warn("no display appeared within DISPLAY_WAIT, starting anyway",
			"wait", cfg.DisplayWait)
	}

	// Boot the idle game list before the listener opens, so a reachable
	// /health means the emulator is already running rather than about to be.
	if _, err := runner.Launch(ctx, ""); err != nil {
		log.Error("could not start flycast on the game list", "err", err)
	} else if !runner.Probe().LuaReady {
		log.Warn("flycast started but the lua command channel did not answer; "+
			"save and load states will not work",
			"script", cfg.LuaScriptPath(),
			"channel", cfg.ChannelDir())
	}

	srv := &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(cfg.Port)),
		Handler:           api.NewServer(cfg, log, runner, sess).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: /save-and-exit legitimately blocks for as long as
		// RomM's STREAMING_SAVE_TIMEOUT allows while a state is flushed.
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("broker listening", "port", cfg.Port, "rom_root", cfg.ROMRoot)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
			return
		}
		errc <- nil
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")

	// Stop accepting first, then let an in-flight save finish, then take the
	// emulator down. Killing first would truncate a state mid-write.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown was not clean", "err", err)
	}
	waitForSaves(shutdownCtx, sess, log)
	if err := runner.Kill(shutdownCtx); err != nil {
		log.Warn("could not stop flycast cleanly", "err", err)
	}
	return <-errc
}

func waitForSaves(ctx context.Context, sess *session.Manager, log *slog.Logger) {
	if !sess.Saving() {
		return
	}
	log.Info("waiting for an in-flight save before stopping flycast")
	for sess.Saving() {
		select {
		case <-ctx.Done():
			log.Warn("gave up waiting for the in-flight save")
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}
