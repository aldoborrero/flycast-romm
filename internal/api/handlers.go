package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/aldoborrero/flycast-romm/internal/config"
	"github.com/aldoborrero/flycast-romm/internal/flycast"
)

// backgroundTimeout bounds work that outlives the request that started it:
// an async /save-state, or the idle relaunch after /save-and-exit.
const backgroundTimeout = 2 * time.Minute

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	p := s.ctl.Probe()
	snap := s.sess.Snapshot()

	writeJSON(w, http.StatusOK, map[string]any{
		"status":            "ok",
		"flycast_installed": p.BinaryPresent,
		"flycast_path":      p.BinaryPath,
		"display_up":        p.DisplayUp,
		"display":           p.Display,
		"process_alive":     p.ProcessAlive,
		"lua_ready":         p.LuaReady,
		"lua_state":         p.LuaState,
		"session_active":    snap.Active,
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	snap := s.sess.Snapshot()
	p := s.ctl.Probe()

	// `active` means a game is loaded, not merely that a process exists: the
	// idle game list is a healthy Flycast with no game. Callers that want the
	// process answer read `process_alive`.
	body := map[string]any{
		"active":           snap.Active,
		"rom_path":         nil,
		"rom_name":         nil,
		"started_at":       nil,
		"save_slot":        s.cfg.SaveSlot,
		"autosave_slot":    s.cfg.SaveSlot,
		"max_slot":         config.MaxSlot,
		"save_in_progress": s.sess.Saving(),
		"process_alive":    p.ProcessAlive,
		"lua_ready":        p.LuaReady,
	}
	if snap.Active {
		body["rom_path"] = snap.ROMPath
		body["rom_name"] = snap.ROMName
		body["started_at"] = rfc3339(snap.StartedAt)
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleLaunch(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readBody(w, r)
	if !ok {
		return
	}

	raw, err := b.str("rom_path")
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, err.Error(), err.Error())
		return
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		s.refuse(w, r, http.StatusBadRequest, "rom_path is required", "empty or missing rom_path")
		return
	}
	// RomM sends rom_name too. It is ignored on purpose, as in every sibling
	// broker: the display name is derived from the file actually resolved,
	// which for a folder-organised game is not what RomM guessed.

	romPath, err := flycast.ResolveROM(s.cfg.ROMRoot, raw)
	if err != nil {
		s.refuseResolve(w, r, raw, err)
		return
	}

	if err := s.sess.BeginLaunch(); s.sessionRefusal(w, r, err) {
		return
	}

	res, err := s.ctl.Launch(r.Context(), romPath)
	if err != nil {
		s.sess.AbortLaunch()
		s.refuse(w, r, http.StatusInternalServerError, "could not launch flycast", err.Error())
		return
	}

	name := strings.TrimSuffix(filepath.Base(romPath), filepath.Ext(romPath))
	s.sess.EndLaunch(romPath, name, time.Now())
	if romPath != raw {
		s.log.Info("resolved rom folder", "requested", raw, "booted", romPath)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "launching",
		"rom_path": romPath,
		"rom_name": name,
		// False means the emulator had not reported itself running before
		// LAUNCH_WAIT. It is still booting; the launch has not failed.
		"ready": res.Ready,
	})
}

func (s *Server) refuseResolve(w http.ResponseWriter, r *http.Request, raw string, err error) {
	switch {
	case errors.Is(err, flycast.ErrOutsideRoot):
		s.refuse(w, r, http.StatusBadRequest, "rom_path must be within ROM_ROOT", err.Error(),
			map[string]any{"rom_root": s.cfg.ROMRoot})
	case errors.Is(err, flycast.ErrNotFound):
		// 422, not 404: the path is well-formed but points at nothing. The
		// most common cause is the ROM volume being mounted at a different
		// path here than in the RomM container.
		s.refuse(w, r, http.StatusUnprocessableEntity, "rom_path does not exist", err.Error(),
			map[string]any{"path": raw})
	case errors.Is(err, flycast.ErrNoBootable):
		s.refuse(w, r, http.StatusUnprocessableEntity, "no bootable ROM file found under rom_path", err.Error(),
			map[string]any{"path": raw, "extensions": flycast.Extensions})
	default:
		s.refuse(w, r, http.StatusInternalServerError, "could not resolve rom_path", err.Error())
	}
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if err := s.ctl.Kill(r.Context()); err != nil {
		s.refuse(w, r, http.StatusInternalServerError, "could not stop flycast", err.Error())
		return
	}
	s.sess.Clear()

	// Back to the game list in the background so the stream is never a black
	// screen. RomM gives this call five seconds and ignores the body.
	s.background(func(ctx context.Context) {
		if _, err := s.ctl.Launch(ctx, ""); err != nil {
			s.log.Error("could not return to the game list", "err", err)
		}
	})
	writeJSON(w, http.StatusOK, map[string]any{"status": "resetting"})
}

func (s *Server) handleSaveState(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readBody(w, r)
	if !ok {
		return
	}
	slot, ok := s.slotFrom(w, r, b, 1)
	if !ok {
		return
	}

	if err := s.sess.BeginSave(); s.sessionRefusal(w, r, err) {
		return
	}
	romPath := s.sess.Snapshot().ROMPath
	flySlot := s.cfg.FlycastSlot(slot)

	// RomM gives this route five seconds and reads `"status": "saving"`, so
	// the save is dispatched and confirmed on a background goroutine. A
	// caller that needs to know the write landed polls /status for
	// save_in_progress, or waits for the state file.
	s.background(func(ctx context.Context) {
		defer s.sess.EndSave()
		if err := s.ctl.SaveState(ctx, romPath, flySlot); err != nil {
			s.log.Error("save state failed", "slot", slot, "flycast_slot", flySlot, "err", err)
			return
		}
	})

	writeJSON(w, http.StatusOK, map[string]any{"status": "saving", "slot": slot})
}

func (s *Server) handleLoadState(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readBody(w, r)
	if !ok {
		return
	}
	slot, ok := s.slotFrom(w, r, b, 1)
	if !ok {
		return
	}
	// A load during a save would roll the player back onto the state the save
	// is still writing, so the same guard applies here.
	if err := s.sess.RequireGame(); s.sessionRefusal(w, r, err) {
		return
	}

	flySlot := s.cfg.FlycastSlot(slot)
	if err := s.ctl.LoadState(r.Context(), flySlot); err != nil {
		s.log.Error("load state failed", "slot", slot, "flycast_slot", flySlot, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status": "error", "loaded": false, "slot": slot, "error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "loaded": true, "slot": slot})
}

func (s *Server) handleSaveAndExit(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readBody(w, r)
	if !ok {
		return
	}
	slot, ok := s.slotFrom(w, r, b, 0)
	if !ok {
		return
	}
	wait, err := b.boolOr("wait", true)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, err.Error(), err.Error())
		return
	}

	if err := s.sess.BeginSave(); s.sessionRefusal(w, r, err) {
		return
	}
	romPath := s.sess.Snapshot().ROMPath
	flySlot := s.cfg.FlycastSlot(slot)

	if !wait {
		s.background(func(ctx context.Context) {
			s.saveAndExit(ctx, romPath, slot, flySlot)
		})
		writeJSON(w, http.StatusOK, map[string]any{"status": "queued", "slot": slot})
		return
	}

	// RomM allows STREAMING_SAVE_TIMEOUT (45s by default) here, which is the
	// budget the write confirmation and the kill share.
	saved := s.saveAndExit(r.Context(), romPath, slot, flySlot)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "saved": saved, "slot": slot})
}

// saveAndExit writes the state, then stops the emulator and brings the game
// list back. The emulator is killed even when the save fails: the user asked
// to leave, and refusing to exit would strand the session.
func (s *Server) saveAndExit(ctx context.Context, romPath string, slot, flySlot int) bool {
	saved := true
	if err := s.ctl.SaveState(ctx, romPath, flySlot); err != nil {
		s.log.Warn("save-and-exit could not save, exiting anyway",
			"slot", slot, "flycast_slot", flySlot, "err", err)
		saved = false
	}
	s.sess.EndSave()

	if err := s.ctl.Kill(ctx); err != nil {
		s.log.Error("save-and-exit could not stop flycast", "err", err)
	}
	s.sess.Clear()

	s.background(func(ctx context.Context) {
		if _, err := s.ctl.Launch(ctx, ""); err != nil {
			s.log.Error("could not return to the game list", "err", err)
		}
	})
	return saved
}

func (s *Server) handleVolume(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readBody(w, r)
	if !ok {
		return
	}
	if !b.has("level") {
		s.refuse(w, r, http.StatusBadRequest, "level is required", "missing level")
		return
	}
	level, err := b.intOr("level", 0)
	if err != nil {
		s.refuse(w, r, http.StatusBadRequest, err.Error(), err.Error())
		return
	}
	if level < 0 || level > 100 {
		s.refuse(w, r, http.StatusBadRequest, "level must be an integer 0-100",
			fmt.Sprintf("got level %d", level))
		return
	}
	if err := s.ctl.SetVolume(r.Context(), level); err != nil {
		s.refuse(w, r, http.StatusInternalServerError, "pactl failed", err.Error())
		return
	}
	// RomM checks this exact string, not the status code.
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "level": level})
}

func (s *Server) handleMute(w http.ResponseWriter, r *http.Request) {
	b, ok := s.readBody(w, r)
	if !ok {
		return
	}

	// An absent `mute` key means toggle; that is how RomM's UI asks for one.
	var want *bool
	if b.has("mute") {
		v, err := b.boolOr("mute", false)
		if err != nil {
			s.refuse(w, r, http.StatusBadRequest, err.Error(), err.Error())
			return
		}
		want = &v
	}

	// The state is read back from PulseAudio rather than echoed, so RomM's UI
	// shows what the sink actually did.
	state, err := s.ctl.SetMute(r.Context(), want)
	if err != nil {
		s.refuse(w, r, http.StatusInternalServerError, "pactl failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "mute": state})
}

// background runs work that must outlive the request. The request context is
// cancelled the moment the response is written, so it cannot be reused.
func (s *Server) background(fn func(context.Context)) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), backgroundTimeout)
		defer cancel()
		fn(ctx)
	}()
}
